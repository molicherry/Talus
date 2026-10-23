package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
	"github.com/vpsmanager/backend/internal/agentwindows"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
)

type windowsProbeLease struct {
	Lease
	exec func(string) *ExecResult
}

func (l windowsProbeLease) Exec(_ context.Context, command string, _ time.Duration) (*ExecResult, error) {
	return l.exec(command), nil
}

func TestWindowsAgentProbe(t *testing.T) {
	for _, arch := range []string{"AMD64", "ARM64", "x86"} {
		t.Run(arch, func(t *testing.T) {
			calls := 0
			lease := windowsProbeLease{exec: func(command string) *ExecResult {
				calls++
				if command == "uname -s; uname -m" {
					return &ExecResult{ExitCode: 127}
				}
				if command != agentwindows.Command(agentwindows.Probe) {
					t.Fatal("unexpected probe")
				}
				return &ExecResult{Stdout: "TALUS_WINDOWS_V1\r\n" + arch + "\r\n"}
			}}
			p, err := NewAgentDeploymentService(nil, "").Probe(context.Background(), lease)
			if calls != 2 {
				t.Fatalf("probe calls: %d", calls)
			}
			if arch == "AMD64" {
				if err != nil || p.String() != "windows/amd64" {
					t.Fatalf("probe %v %v", p, err)
				}
			} else if ReasonFromError(err) != ReasonAgentPlatformUnsupported {
				t.Fatalf("accepted unsupported Windows arch: %v", err)
			}
		})
	}
}

func decodeWindowsCommand(t *testing.T, command string) string {
	t.Helper()
	parts := strings.Fields(command)
	b, err := base64.StdEncoding.DecodeString(parts[len(parts)-1])
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	s := string(utf16.Decode(u))
	if strings.Contains(s, "$m=New-Object IO.MemoryStream") {
		m := regexp.MustCompile(`FromBase64String\('([^']+)'\)`).FindStringSubmatch(s)
		if len(m) != 2 {
			t.Fatal("invalid compressed command")
		}
		b, err = base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			t.Fatal(err)
		}
		z, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		b, err = io.ReadAll(z)
		_ = z.Close()
		if err != nil {
			t.Fatal(err)
		}
		s = string(b)
	}
	return s
}

func TestWindowsAgentDeploymentPersistsLayoutAndVerifiesBeforeActivation(t *testing.T) {
	for _, scenario := range []string{"success", "hash-mismatch", "health-failure", "no-admin"} {
		t.Run(scenario, func(t *testing.T) {
			svc, servers, db, ctx, _ := connectionServiceFixture(t)
			srv, err := svc.Create(ctx, &model.Server{Name: "windows", Host: "windows.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
			if err != nil {
				t.Fatal(err)
			}
			agents, node := serverDeleteAgent(t, db, ctx, srv)
			remote := newFakeRemote()
			var launched, prepared, removed atomic.Bool
			inherited := remote.handler()
			handler := func(command string, ch ssh.Channel, closed <-chan struct{}) {
				if command == "uname -s; uname -m" {
					exitStatus(ch, 127)
					return
				}
				if !strings.HasPrefix(command, "powershell.exe ") {
					inherited(command, ch, closed)
					return
				}
				script := decodeWindowsCommand(t, command)
				switch {
				case command == agentwindows.Command(agentwindows.Probe):
					fmt.Fprint(ch, "TALUS_WINDOWS_V1\r\nAMD64\r\n")
				case command == agentwindows.Command(agentwindows.Context):
					fmt.Fprintf(ch, `{"home":"C:\\ProgramData","admin":%t}`, scenario != "no-admin")
				case strings.Contains(script, "# Abort before uploads"):
					prepared.Store(true)
				case strings.Contains(script, "$ErrorActionPreference='Stop'; (Get-FileHash"):
					m := regexp.MustCompile(`-LiteralPath '([^']+)'`).FindStringSubmatch(script)
					if len(m) != 2 {
						t.Error("missing hash path")
						exitStatus(ch, 1)
						return
					}
					data, ok := remote.file("/" + m[1])
					if !ok || !prepared.Load() {
						t.Errorf("hash before protected upload: path=%q keys=%v prepared=%v", m[1], remote.fileKeys(), prepared.Load())
						exitStatus(ch, 1)
						return
					}
					hash := repository.TokenHash(string(data))
					if scenario == "hash-mismatch" {
						hash = strings.Repeat("0", 64)
					}
					fmt.Fprintln(ch, hash)
				case strings.Contains(script, "Start-ScheduledTask -TaskName $task") && !strings.Contains(script, ".remove.ps1"):
					if remote.count("sftp.upload ") != 3 {
						t.Error("activation before complete uploads")
					}
					launched.Store(true)
				case strings.Contains(script, "TALUS_READY"):
					if !launched.Load() {
						t.Error("readiness before activation")
					}
					if scenario == "health-failure" {
						exitStatus(ch, 1)
						return
					}
					fmt.Fprintln(ch, "TALUS_READY")
				case strings.Contains(script, "# Pending tasks cannot"):
					removed.Store(true)
				default:
					t.Error("unexpected Windows command")
					exitStatus(ch, 1)
					return
				}
				exitStatus(ch, 0)
			}
			address, key := startExecSSHServer(t, handler)
			pool := newTestPool(t)
			seedPool(t, pool, srv.ID, dialTestSSH(t, address, key))
			sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
			dir := t.TempDir()
			agentprototest.WriteArtifacts(t, dir, "windows-test")
			monitor := NewResidentMonitor(agents, servers, NewAgentDeploymentService(sshSvc, dir), "https://hub.example", false, 5)
			monitor.installJob(ctx, node, "desired", "config")
			after, err := agents.Get(ctx, srv.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "success":
				if after.Deployment != "ready" || after.InstallOS != "windows" || after.InstallHome == nil || *after.InstallHome != "C:/ProgramData" || len(after.PendingInstallLayouts) != 0 {
					t.Fatalf("installation not persisted: %+v", after)
				}
				if residentInstallNeeded(after, "desired", "config", time.Now(), time.Now()) {
					t.Fatal("healthy Windows service would reinstall")
				}
				lease, err := sshSvc.AcquireLease(ctx, srv.ID, LeaseConsumerAgent)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
				if err = monitor.uninstall(ctx, after, lease); err != nil || !removed.Load() {
					t.Fatalf("Windows removal: %v", err)
				}
			case "hash-mismatch":
				if after.DeployError != ReasonAgentHashMismatch || launched.Load() {
					t.Fatal("unverified executable activated")
				}
			case "health-failure":
				if after.DeployError != ReasonAgentHealthCheckFailed || len(after.PendingInstallLayouts) != 1 || after.PendingInstallLayouts[0].OS != "windows" {
					t.Fatal("failed install lost cleanup layout")
				}
			case "no-admin":
				if after.DeployError != reasonWindowsService || prepared.Load() || remote.count("sftp.upload ") != 0 {
					t.Fatal("unprivileged account modified Windows host")
				}
			}
		})
	}
}
