package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
)

// fakeRemote is a controlled SSH peer with an in-memory filesystem. It answers
// the exact command shapes the deployment service issues, so probe, hash check,
// upload, activation and execution can be asserted end to end.
type fakeRemote struct {
	mu       sync.Mutex
	files    map[string][]byte
	commands []string

	unameOS, unameArch string
	noShaTool          bool
	uploadError        string
	// uploadWriteThenFail stores the upload and then reports failure, as a chmod
	// or a partially applied command would.
	uploadWriteThenFail bool
	// removeExit makes the remote rm answer with a non-zero exit status while the
	// transport stays healthy.
	removeExit        int
	activateUnknown   bool
	activateFailsSoft bool
	corruptUpload     bool

	agentPayload  string
	agentPayloads map[string]string
	agentExit     int
	agentStderr   string
	// afterUpload runs once the upload has been stored remotely, so a test can
	// invalidate the target generation between the upload and the activation.
	afterUpload func()
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{
		files:     map[string][]byte{},
		unameOS:   "Linux",
		unameArch: "x86_64",
		agentExit: 0,
	}
}

func (f *fakeRemote) record(command string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
}

func (f *fakeRemote) ran(command string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range f.commands {
		if strings.Contains(entry, command) {
			return true
		}
	}
	return false
}

func (f *fakeRemote) count(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, entry := range f.commands {
		if strings.Contains(entry, substr) {
			n++
		}
	}
	return n
}

func (f *fakeRemote) fileKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.files))
	for key := range f.files {
		keys = append(keys, key)
	}
	return keys
}

func (f *fakeRemote) file(path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[path]
	return data, ok
}

func exitStatus(ch ssh.Channel, code uint32) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
	_ = ch.Close()
}

// handler implements the peer protocol. Command shapes are matched with
// anchored regexes so path extraction cannot drift.
func (f *fakeRemote) handler() func(string, ssh.Channel, <-chan struct{}) {
	activateRe := regexp.MustCompile(`^chmod 0755 (\S+) && mv -f (\S+) (\S+)$`)
	hashRe := regexp.MustCompile(`then (?:sha256sum|shasum -a 256) (\S+) `)
	removeRe := regexp.MustCompile(`^rm -f (\S+)$`)

	return func(command string, ch ssh.Channel, _ <-chan struct{}) {
		f.record(command)
		switch {
		case strings.HasPrefix(command, "uname -s; uname -m"):
			fmt.Fprintf(ch, "%s\n%s\n", f.unameOS, f.unameArch)
			exitStatus(ch, 0)
		case strings.HasPrefix(command, "if command -v sha256sum"):
			if f.noShaTool {
				fmt.Fprint(ch, "NO_SHA_TOOL\n")
				exitStatus(ch, 0)
				return
			}
			match := hashRe.FindStringSubmatch(command)
			if match == nil {
				fmt.Fprintf(ch.Stderr(), "unparsable hash command\n")
				exitStatus(ch, 1)
				return
			}
			path := match[1]
			f.mu.Lock()
			data, ok := f.files[path]
			f.mu.Unlock()
			if !ok {
				fmt.Fprintf(ch.Stderr(), "sha256sum: %s: No such file or directory\n", path)
				exitStatus(ch, 1)
				return
			}
			digest := sha256.Sum256(data)
			fmt.Fprintf(ch, "%s  %s\n", hex.EncodeToString(digest[:]), path)
			exitStatus(ch, 0)
		case command == "subsystem:sftp":
			serveUploadPeer(ch, &uploadPeerFS{
				open: func(path string) error {
					f.record("sftp.upload " + path)
					if f.uploadError != "" {
						return errors.New(f.uploadError)
					}
					return nil
				},
				finish: func(path string, data []byte) error {
					if f.corruptUpload {
						data = append(data, []byte("corrupted")...)
					}
					f.mu.Lock()
					f.files[path] = data
					hook := f.afterUpload
					f.mu.Unlock()
					if f.uploadWriteThenFail {
						return errors.New("Permission denied during SFTP close")
					}
					if hook != nil {
						hook()
					}
					return nil
				},
			})
		case activateRe.MatchString(command):
			match := activateRe.FindStringSubmatch(command)
			f.mu.Lock()
			data, ok := f.files[match[1]]
			if ok {
				f.files[match[3]] = data
				delete(f.files, match[1])
			}
			f.mu.Unlock()
			if f.activateUnknown {
				// The rename happened but the reply is lost with the connection.
				_ = ch.Close()
				return
			}
			if f.activateFailsSoft {
				// The rename happened, but the command still reported failure.
				fmt.Fprintf(ch.Stderr(), "mv: cannot stat temporary file: interrupted by signal\n")
				exitStatus(ch, 1)
				return
			}
			exitStatus(ch, 0)
		case removeRe.MatchString(command):
			match := removeRe.FindStringSubmatch(command)
			if f.removeExit != 0 {
				// A rejected rm: the file stays and the transport is fine.
				fmt.Fprintf(ch.Stderr(), "rm: cannot remove '%s': Permission denied\n", match[1])
				exitStatus(ch, uint32(f.removeExit))
				return
			}
			f.mu.Lock()
			delete(f.files, match[1])
			f.mu.Unlock()
			exitStatus(ch, 0)
		case strings.HasPrefix(command, agentPath+" --format json"):
			f.mu.Lock()
			_, deployed := f.files[agentPath]
			f.mu.Unlock()
			if !deployed {
				fmt.Fprintf(ch.Stderr(), "sh: %s: not found\n", agentPath)
				exitStatus(ch, 127)
				return
			}
			if f.agentStderr != "" {
				fmt.Fprintf(ch.Stderr(), "%s\n", f.agentStderr)
			}
			f.mu.Lock()
			payload := f.agentPayload
			if candidate, ok := f.agentPayloads[fmt.Sprintf("%x", sha256.Sum256(f.files[agentPath]))]; ok {
				payload = candidate
			}
			f.mu.Unlock()
			fmt.Fprint(ch, payload)
			exitStatus(ch, uint32(f.agentExit))
		default:
			fmt.Fprintf(ch.Stderr(), "unexpected command: %s\n", command)
			exitStatus(ch, 1)
		}
	}
}

type agentDeployHarness struct {
	svc     *SSHService
	remote  *fakeRemote
	dir     string
	pool    *sshpool.Pool
	address string
	hostKey ssh.PublicKey
}

// newAgentDeployHarness wires a deployment service over a fake peer and a real
// artifact directory built by the shipping build script path.
func newAgentDeployHarness(t *testing.T, version string, sftpEnabled ...bool) (*AgentDeploymentService, agentDeployHarness) {
	t.Helper()
	remote := newFakeRemote()
	address, hostKey := startExecSSHServer(t, remote.handler(), sftpEnabled...)
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)

	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, version)

	return NewAgentDeploymentService(sshSvc, dir), agentDeployHarness{
		svc: sshSvc, remote: remote, dir: dir, pool: pool, address: address, hostKey: hostKey,
	}
}

func (h agentDeployHarness) localArtifact(t *testing.T, arch string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.dir, "vpsmanager-agent-linux-"+arch))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestAgentDeployProbesMapsAndSkipsUploadWhenActive pins TC-08-02's idempotent
// path: a node already running the matching artifact needs no upload, and the
// probe/run share one lease.
func TestAgentDeployProbesMapsAndSkipsUploadWhenActive(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1,"timestamp":"2026-01-01T00:00:00Z"}`
	h.remote.files[agentPath] = h.localArtifact(t, "amd64")

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	out, err := deployer.Collect(context.Background(), lease)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if out.AgentVersion != "v1.2.3" || out.ProtocolVersion != 1 {
		t.Fatalf("payload = %+v", out)
	}
	if h.remote.count("sftp.upload ") != 0 {
		t.Fatalf("an active matching agent must not be uploaded again: %v", h.remote.commands)
	}
	if h.remote.count("chmod 0755") != 0 {
		t.Fatalf("no activation should happen: %v", h.remote.commands)
	}
}

// TestAgentDeployUploadsVerifiesAndActivates pins the deployment path: upload to
// a temporary sibling, verify the hash, then activate atomically.
func TestAgentDeployUploadsVerifiesAndActivates(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if _, err := deployer.Collect(context.Background(), lease); err != nil {
		t.Fatalf("Collect: %v\ncommands: %v\nfiles: %v", err, h.remote.commands, h.remote.fileKeys())
	}
	if h.remote.count("sftp.upload ") != 1 {
		t.Fatalf("uploads = %d, want exactly 1 (%v)", h.remote.count("sftp.upload "), h.remote.commands)
	}
	if h.remote.count("chmod 0755") != 1 {
		t.Fatalf("activations = %d, want exactly 1", h.remote.count("chmod 0755"))
	}
	deployed, ok := h.remote.file(agentPath)
	if !ok {
		t.Fatal("the agent was not activated at the expected path")
	}
	if string(deployed) != string(h.localArtifact(t, "amd64")) {
		t.Fatal("the activated binary does not match the bundled amd64 artifact")
	}
	for _, command := range append([]string{}, h.remote.commands...) {
		if strings.HasPrefix(command, "rm -f ") {
			t.Fatalf("the temporary upload should have been renamed, not removed: %v", command)
		}
	}
}

// TestAgentDeployRejectsUnsupportedPlatformWithoutUploading pins TC-08-04: a
// target the Hub has no artifact for is rejected with zero uploads.
func TestAgentDeployRejectsUnsupportedPlatformWithoutUploading(t *testing.T) {
	for _, tc := range []struct{ name, os, arch string }{
		{"unknown architecture", "Linux", "riscv64"},
		{"non-linux target", "Darwin", "x86_64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployer, h := newAgentDeployHarness(t, "v1.2.3")
			h.remote.unameOS, h.remote.unameArch = tc.os, tc.arch

			lease, err := h.svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()

			_, err = deployer.Collect(context.Background(), lease)
			if err == nil {
				t.Fatal("an unsupported platform must be rejected")
			}
			var deployErr *AgentDeployError
			if !asDeployError(err, &deployErr) || deployErr.Reason != ReasonAgentPlatformUnsupported {
				t.Fatalf("error = %v, want %s", err, ReasonAgentPlatformUnsupported)
			}
			if h.remote.count("sftp.upload ") != 0 {
				t.Fatalf("nothing may be uploaded for an unsupported platform: %v", h.remote.commands)
			}
		})
	}
}

// TestAgentDeployRejectsLegacyAndMismatchedProtocols verifies that an agent
// that does not speak the exact protocol and build version is refused.
func TestAgentDeployRejectsLegacyAndMismatchedProtocols(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		reason  string
	}{
		{"legacy agent without protocol fields", `{"timestamp":"2026-01-01T00:00:00Z"}`, ReasonAgentLegacyProtocol},
		{"future protocol", `{"agent_version":"v1.2.3","protocol_version":2}`, ReasonAgentProtocolMismatch},
		{"stale build version", `{"agent_version":"v0.0.1","protocol_version":1}`, ReasonAgentProtocolMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployer, h := newAgentDeployHarness(t, "v1.2.3")
			h.remote.agentPayload = tc.payload
			h.remote.files[agentPath] = h.localArtifact(t, "amd64")

			lease, err := h.svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()

			if out, err := deployer.Collect(context.Background(), lease); err == nil || out != nil {
				t.Fatalf("Collect = (%+v, %v), want a refusal and no payload", out, err)
			} else {
				var deployErr *AgentDeployError
				if !asDeployError(err, &deployErr) || deployErr.Reason != tc.reason {
					t.Fatalf("error = %v, want reason %s", err, tc.reason)
				}
			}
		})
	}
}

// TestAgentDeployClassifiesRemoteFailures pins TC-08-04's failure classes.
func TestAgentDeployClassifiesRemoteFailures(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fakeRemote)
		reason string
	}{
		{"missing hash tool", func(f *fakeRemote) { f.noShaTool = true }, ReasonAgentToolMissing},
		{"read-only target", func(f *fakeRemote) { f.uploadError = "cat: /tmp/vpsmanager-agent.upload-1: Read-only file system" }, ReasonAgentReadOnly},
		{"corrupt upload", func(f *fakeRemote) { f.corruptUpload = true }, ReasonAgentHashMismatch},
		{"noexec target", func(f *fakeRemote) {
			f.agentStderr = "sh: /tmp/vpsmanager-agent: Permission denied"
			f.agentExit = 126
		}, ReasonAgentNoExec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployer, h := newAgentDeployHarness(t, "v1.2.3")
			h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
			tc.setup(h.remote)

			lease, err := h.svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()

			_, err = deployer.Collect(context.Background(), lease)
			if err == nil {
				t.Fatal("expected a classified failure")
			}
			var deployErr *AgentDeployError
			if !asDeployError(err, &deployErr) || deployErr.Reason != tc.reason {
				t.Fatalf("error = %v, want reason %s", err, tc.reason)
			}
			if tc.reason == ReasonAgentHashMismatch && h.remote.ran("rm -f ") == false {
				t.Fatalf("a corrupt upload must be cleaned up: %v", h.remote.commands)
			}
		})
	}
}

// TestAgentDeployConfirmsAmbiguousActivationByRemoteHash covers an unknown
// activation result: when the command reports failure but the rename
// actually happened, the remote hash decides and the collection proceeds.
func TestAgentDeployConfirmsAmbiguousActivationByRemoteHash(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
	h.remote.activateFailsSoft = true

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	out, err := deployer.Collect(context.Background(), lease)
	if err != nil {
		t.Fatalf("Collect must confirm the activation by hash: %v", err)
	}
	if out.ProtocolVersion != 1 {
		t.Fatalf("payload = %+v", out)
	}
	if _, ok := h.remote.file(agentPath); !ok {
		t.Fatal("the agent should be active after the confirmed rename")
	}
}

// TestAgentDeployReportsLostActivationAndRecoversIdempotently pins the honest
// failure when the activation reply is lost with the connection: the collection
// fails as unknown, and the next collection on a fresh lease skips the upload
// because the remote hash already matches.
func TestAgentDeployReportsLostActivationAndRecoversIdempotently(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
	h.remote.activateUnknown = true

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = deployer.Collect(context.Background(), lease)
	lease.Release()
	if err == nil {
		t.Fatal("a lost activation reply must be reported as unknown")
	}
	var deployErr *AgentDeployError
	if !asDeployError(err, &deployErr) || deployErr.Reason != ReasonAgentActivationUnknown {
		t.Fatalf("error = %v, want %s", err, ReasonAgentActivationUnknown)
	}
	uploads := h.remote.count("sftp.upload ")

	// The rename left the verified artifact in place, so a retry with a fresh
	// lease must skip the upload entirely.
	seedPool(t, h.pool, 1, dialTestSSH(t, h.address, h.hostKey))
	retry, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Release()
	out, err := deployer.Collect(context.Background(), retry)
	if err != nil {
		t.Fatalf("retry after an unknown activation: %v", err)
	}
	if out.ProtocolVersion != 1 {
		t.Fatalf("payload = %+v", out)
	}
	if got := h.remote.count("sftp.upload "); got != uploads {
		t.Fatalf("uploads after retry = %d, want no additional upload (was %d)", got, uploads)
	}
}

// TestAgentArchNameMapping pins the uname → platform mapping.
func TestAgentArchNameMapping(t *testing.T) {
	cases := map[string]string{
		"x86_64": "amd64", "amd64": "amd64",
		"aarch64": "arm64", "arm64": "arm64",
		"riscv64": "", "": "",
	}
	for machine, want := range cases {
		if got := archName(machine); got != want {
			t.Fatalf("archName(%q) = %q, want %q", machine, got, want)
		}
	}
}

// asDeployError is a tiny errors.As wrapper kept local to avoid importing errors
// in every assertion.
func asDeployError(err error, target **AgentDeployError) bool {
	for err != nil {
		if typed, ok := err.(*AgentDeployError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

var _ = os.Stat
var _ = time.Second

// TestAgentDeployEventsCorrelateWithServerID pins TC-08-07's correlation
// contract: a deployment attempt and its failure are reported as their own
// action with the node id, outcome and reason class, so scheduled collections
// and deployment attempts can be counted together without node identity ever
// becoming a metric label.
func TestAgentDeployEventsCorrelateWithServerID(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	var logs bytes.Buffer
	deployer.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	// A read-only target fails the upload, which is a real deployment attempt (a
	// missing hash tool would fail before any deployment is attempted).
	h.remote.uploadError = "cat: /tmp/vpsmanager-agent.upload-1: Read-only file system"

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := deployer.Collect(context.Background(), lease); err == nil {
		t.Fatal("expected the deployment to fail")
	}

	output := logs.String()
	for _, want := range []string{
		`"action":"agent.deploy"`,
		`"outcome":"failure"`,
		`"reason_class":"agent_target_read_only"`,
		`"server_id":1`,
		`"platform":"linux/amd64"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deployment log is missing %s:\n%s", want, output)
		}
	}
	if strings.Contains(output, "talus_") {
		t.Fatalf("node identity must not appear as a metric label:\n%s", output)
	}
}

func TestAgentDeployRequiresSFTPForUploadAndKeepsVerifiedArtifactReuse(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_installed_%v", installed), func(t *testing.T) {
			deployer, h := newAgentDeployHarness(t, "v1.2.3", false)
			h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
			if installed {
				h.remote.files[agentPath] = h.localArtifact(t, "amd64")
			}
			lease, err := h.svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			_, err = deployer.Collect(context.Background(), lease)
			if installed {
				if err != nil {
					t.Fatal("verified installed artifact should not need an upload", err)
				}
			} else {
				fileReason(t, err, "sftp_unavailable")
				if _, ok := h.remote.file(agentPath); ok {
					t.Fatal("failed SFTP upload activated an agent")
				}
			}
			for _, command := range h.remote.commands {
				if strings.HasPrefix(command, "cat >") {
					t.Fatal("shell upload fallback was attempted")
				}
			}
		})
	}
}
