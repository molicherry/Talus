package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

func TestWindowsAgentActivationReservesCompletionBudget(t *testing.T) {
	for _, scenario := range []string{"slow-staging", "canceled", "ready", "launch-failure"} {
		t.Run(scenario, func(t *testing.T) {
			remaining := 4 * time.Minute
			if scenario == "slow-staging" {
				remaining = 2 * time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), remaining)
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			layout := agentwindows.Layout{Home: "C:/ProgramData", InstallID: "test", Attempt: "attempt"}
			hash := strings.Repeat("a", 64)
			commands := []string{agentwindows.Command(layout.Launch(hash)), agentwindows.Command(layout.Wait(agentwindows.ActivationWaitSeconds))}
			calls := 0
			lease := windowsProbeLease{exec: func(command string) *ExecResult {
				if calls >= len(commands) || command != commands[calls] {
					t.Fatal("activation did not launch then wait for its own receipt")
				}
				calls++
				if scenario == "launch-failure" {
					return &ExecResult{ExitCode: 1}
				}
				return &ExecResult{}
			}}
			err := activateWindowsAgent(ctx, lease, layout, hash)
			switch scenario {
			case "slow-staging":
				if !errors.Is(err, context.DeadlineExceeded) || calls != 0 || deployFailureReason(err) != "deployment_timeout" {
					t.Fatalf("launched a detached task without time for completion: calls=%d err=%v", calls, err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) || calls != 0 {
					t.Fatalf("launched after cancellation: calls=%d err=%v", calls, err)
				}
			case "ready":
				if err != nil || calls != 2 {
					t.Fatalf("did not wait for the launched task: calls=%d err=%v", calls, err)
				}
			case "launch-failure":
				if ReasonFromError(err) != reasonWindowsService || calls != 1 {
					t.Fatalf("waited for an unlaunched task: calls=%d err=%v", calls, err)
				}
			}
		})
	}
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

func TestWindowsAgentProbeWithUnixTools(t *testing.T) {
	for _, osName := range []string{"MINGW64_NT-10.0", "MSYS_NT-10.0", "CYGWIN_NT-10.0"} {
		t.Run(osName, func(t *testing.T) {
			lease := windowsProbeLease{exec: func(command string) *ExecResult {
				if command == "uname -s; uname -m" {
					return &ExecResult{Stdout: osName + "\nx86_64\n"}
				}
				return &ExecResult{Stdout: "TALUS_WINDOWS_V1\r\nAMD64\r\n"}
			}}
			p, err := NewAgentDeploymentService(nil, "").Probe(context.Background(), lease)
			if err != nil || p.String() != "windows/amd64" {
				t.Fatalf("probe: %v %v", p, err)
			}
		})
	}
}

func TestWindowsAgentProbePreservesUnameFailure(t *testing.T) {
	lease := windowsProbeLease{exec: func(command string) *ExecResult {
		if command == "uname -s; uname -m" {
			return &ExecResult{ExitCode: 42}
		}
		return &ExecResult{ExitCode: 127}
	}}
	_, err := NewAgentDeploymentService(nil, "").Probe(context.Background(), lease)
	if err == nil || !strings.Contains(err.Error(), "uname exit=42") || ReasonFromError(err) != ReasonAgentPlatformUnsupported {
		t.Fatalf("lost probe failure: %v", err)
	}
}

func TestLinuxAgentProbeToleratesTrailingOutput(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64", "riscv64"} {
		t.Run(arch, func(t *testing.T) {
			lease := windowsProbeLease{exec: func(command string) *ExecResult {
				if command != "uname -s; uname -m" {
					t.Fatal("Linux probe fell back to PowerShell")
				}
				return &ExecResult{Stdout: "Linux\n" + arch + "\nshell logout message\n"}
			}}
			p, err := NewAgentDeploymentService(nil, "").Probe(context.Background(), lease)
			if arch == "riscv64" {
				if ReasonFromError(err) != ReasonAgentPlatformUnsupported {
					t.Fatalf("unsupported Linux architecture: %v", err)
				}
			} else if err != nil || p.OS != "linux" || p.Arch != archName(arch) {
				t.Fatalf("Linux probe: %+v %v", p, err)
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
	for _, scenario := range []string{"success", "hash-mismatch", "health-failure", "no-admin", "remove-pending", "remove-failure", "remove-invalid"} {
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
				case strings.Contains(script, "# Protect removal payload"):
				case strings.Contains(script, "Start-ScheduledTask -TaskName $task") && strings.Contains(script, ".remove.ps1"):
					removed.Store(true)
				case strings.Contains(script, "TALUS_REMOVE_OK"):
					status := "TALUS_REMOVE_OK"
					switch scenario {
					case "remove-pending":
						status = "TALUS_REMOVE_PENDING"
					case "remove-failure":
						status = "TALUS_REMOVE_FAILED"
					case "remove-invalid":
						status = "unknown"
					}
					fmt.Fprintln(ch, status)
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
			case "success", "remove-pending", "remove-failure", "remove-invalid":
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
				var logs bytes.Buffer
				previous := slog.Default()
				func() {
					slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
					defer slog.SetDefault(previous)
					err = monitor.uninstall(ctx, after, lease)
				}()
				switch scenario {
				case "remove-pending":
					if !strings.Contains(logs.String(), "level=INFO") || strings.Contains(logs.String(), "level=WARN") {
						t.Fatalf("normal async removal warned: %s", logs.String())
					}
				case "remove-failure", "remove-invalid":
					if !strings.Contains(logs.String(), "level=WARN") {
						t.Fatalf("removal failure did not warn: %s", logs.String())
					}
				}
				wantErr := scenario == "remove-failure" || scenario == "remove-invalid"
				if (err != nil) != wantErr || !removed.Load() {
					t.Fatalf("Windows removal: %v", err)
				}
				if wantErr && ReasonFromError(err) != reasonWindowsService {
					t.Fatalf("removal failure not propagated: %v", err)
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
