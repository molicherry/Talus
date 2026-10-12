package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
)

// agentPath is where the deployed agent lives on a managed node. Activation
// renames a verified upload over it atomically.
const agentPath = "/tmp/vpsmanager-agent"

// probeBudget bounds the single composite probe command; the caller's admission
// deadline still caps the whole collection.
const probeBudget = 5 * time.Second

// Failure reason classes for agent deployment (REQUIREMENTS §8.2 step 2). They
// are distinct so a read-only or noexec target is not confused with a corrupt
// upload.
const (
	ReasonAgentPlatformUnsupported = "agent_platform_unsupported"
	ReasonAgentToolMissing         = "agent_remote_tool_missing"
	ReasonAgentReadOnly            = "agent_target_read_only"
	ReasonAgentNoExec              = "agent_target_noexec"
	ReasonAgentHashMismatch        = "agent_hash_mismatch"
	ReasonAgentProtocolMismatch    = "agent_protocol_mismatch"
	ReasonAgentLegacyProtocol      = "agent_legacy_protocol"
	ReasonAgentActivationUnknown   = "agent_activation_unknown"
)

// AgentDeployError carries a stable reason class with the underlying cause.
type AgentDeployError struct {
	Reason string
	Err    error
}

func (e *AgentDeployError) Error() string {
	if e.Err == nil {
		return e.Reason
	}
	return e.Reason + ": " + e.Err.Error()
}

func (e *AgentDeployError) Unwrap() error { return e.Err }

func deployError(reason string, err error) error {
	return &AgentDeployError{Reason: reason, Err: err}
}

// AgentPlatform is the probed target platform.
type AgentPlatform struct {
	OS   string
	Arch string
}

func (p AgentPlatform) String() string { return p.OS + "/" + p.Arch }

// AgentDeploymentService owns everything the Hub does to get a matching,
// verified agent running on a managed node: probe, idempotent upload, atomic
// activation and protocol verification (REQ-08 §8.2). The monitor only
// schedules, validates and stores what this service returns.
type AgentDeploymentService struct {
	ssh          *SSHService
	artifactsDir string
	probeBudget  time.Duration
	execBudget   time.Duration
}

// NewAgentDeploymentService creates the deployment service over an artifacts
// directory holding the manifest and both architecture-specific binaries.
func NewAgentDeploymentService(ssh *SSHService, artifactsDir string) *AgentDeploymentService {
	return &AgentDeploymentService{
		ssh:          ssh,
		artifactsDir: artifactsDir,
		probeBudget:  probeBudget,
	}
}

// Manifest loads and validates the bundled artifact manifest.
func (s *AgentDeploymentService) Manifest() (*agentproto.Manifest, error) {
	return agentproto.LoadManifest(s.artifactsDir)
}

func (s *AgentDeploymentService) artifactFor(platform AgentPlatform) (agentproto.Artifact, error) {
	manifest, err := s.Manifest()
	if err != nil {
		return agentproto.Artifact{}, err
	}
	for _, artifact := range manifest.Agents {
		if artifact.Platform == platform.String() {
			return artifact, nil
		}
	}
	return agentproto.Artifact{}, deployError(ReasonAgentPlatformUnsupported,
		fmt.Errorf("no bundled agent for %s", platform))
}

// Probe reads the target operating system and architecture with one composite
// command. An unsupported platform is rejected before anything is uploaded.
func (s *AgentDeploymentService) Probe(ctx context.Context, lease Lease) (AgentPlatform, error) {
	result, err := lease.Exec(ctx, "uname -s; uname -m", s.probeBudget)
	if err != nil {
		return AgentPlatform{}, fmt.Errorf("probe target: %w", err)
	}
	if result == nil || result.ExitCode != 0 {
		code := -1
		if result != nil {
			code = result.ExitCode
		}
		return AgentPlatform{}, fmt.Errorf("probe target: uname exited %d", code)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) < 2 {
		return AgentPlatform{}, fmt.Errorf("probe target: unexpected uname output %q", strings.TrimSpace(result.Stdout))
	}
	osName := strings.ToLower(strings.TrimSpace(lines[0]))
	arch := strings.TrimSpace(lines[1])
	platform := AgentPlatform{OS: "linux", Arch: archName(arch)}
	if osName != "linux" || platform.Arch == "" {
		return AgentPlatform{}, deployError(ReasonAgentPlatformUnsupported,
			fmt.Errorf("unsupported target %s/%s", osName, arch))
	}
	return platform, nil
}

// archName maps uname -m output onto the bundled platform names.
func archName(machine string) string {
	switch strings.ToLower(strings.TrimSpace(machine)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
}

// Collect returns the parsed agent payload for one node, deploying the matching
// artifact when the running agent is missing, stale or speaks a different
// protocol. It never writes partial metrics: a protocol mismatch is an error.
func (s *AgentDeploymentService) Collect(ctx context.Context, lease Lease) (*agentOutput, error) {
	platform, err := s.Probe(ctx, lease)
	if err != nil {
		return nil, err
	}
	artifact, err := s.artifactFor(platform)
	if err != nil {
		return nil, err
	}
	manifest, err := s.Manifest()
	if err != nil {
		return nil, err
	}

	// Fast path: an already matching agent needs no upload.
	out, err := s.runAgent(ctx, lease)
	if err == nil {
		if err := verifyProtocol(out, artifact, manifest.ProtocolVersion); err == nil {
			return out, nil
		}
	}

	if err := s.deploy(ctx, lease, artifact); err != nil {
		return nil, err
	}
	out, err = s.runAgent(ctx, lease)
	if err != nil {
		return nil, err
	}
	if err := verifyProtocol(out, artifact, manifest.ProtocolVersion); err != nil {
		return nil, err
	}
	return out, nil
}

// runAgent executes the deployed agent and parses its JSON payload.
func (s *AgentDeploymentService) runAgent(ctx context.Context, lease Lease) (*agentOutput, error) {
	result, err := lease.Exec(ctx, agentPath+" --format json", s.execBudget)
	if err != nil {
		return nil, classifyExecFailure(err)
	}
	if result == nil || result.ExitCode != 0 {
		stderr := ""
		code := -1
		if result != nil {
			stderr, code = strings.TrimSpace(result.Stderr), result.ExitCode
		}
		if strings.Contains(stderr, "not found") || strings.Contains(stderr, "No such file") {
			return nil, fmt.Errorf("agent is not deployed yet: %s", stderr)
		}
		if reason := classifyRemoteStderr(stderr); reason != "" {
			return nil, deployError(reason, fmt.Errorf("agent exited %d: %s", code, stderr))
		}
		return nil, fmt.Errorf("agent exited %d: %s", code, stderr)
	}
	out, parseErr := parseAgentOutput(result.Stdout)
	if parseErr != nil {
		return nil, parseErr
	}
	return out, nil
}

// verifyProtocol accepts only the exact protocol version and build version the
// Hub was built with (§8.2 step 4).
func verifyProtocol(out *agentOutput, artifact agentproto.Artifact, protocol int) error {
	if out == nil {
		return deployError(ReasonAgentProtocolMismatch, errors.New("empty agent output"))
	}
	if out.AgentVersion == "" || out.ProtocolVersion == 0 {
		return deployError(ReasonAgentLegacyProtocol,
			fmt.Errorf("agent output lacks agent_version/protocol_version (agent_version=%q protocol_version=%d)", out.AgentVersion, out.ProtocolVersion))
	}
	if out.ProtocolVersion != protocol {
		return deployError(ReasonAgentProtocolMismatch,
			fmt.Errorf("agent protocol %d, want %d", out.ProtocolVersion, protocol))
	}
	if out.AgentVersion != artifact.BuildVersion {
		return deployError(ReasonAgentProtocolMismatch,
			fmt.Errorf("agent version %q, want %q", out.AgentVersion, artifact.BuildVersion))
	}
	return nil
}

// deploy makes the target run the manifest's artifact for its platform. It is
// idempotent: an upload is skipped when the remote hash already matches.
func (s *AgentDeploymentService) deploy(ctx context.Context, lease Lease, artifact agentproto.Artifact) error {
	localPath := filepath.Join(s.artifactsDir, artifact.File)
	if _, err := os.Stat(localPath); err != nil {
		return fmt.Errorf("bundled agent %s: %w", artifact.File, err)
	}

	remote, err := s.remoteHash(ctx, lease, agentPath)
	if err != nil {
		return err
	}
	if remote == artifact.SHA256 {
		return nil
	}

	tmpPath := fmt.Sprintf("%s.upload-%d", agentPath, time.Now().UnixNano())
	if err := lease.Upload(ctx, localPath, tmpPath, s.execBudget); err != nil {
		if reason := classifyRemoteStderr(err.Error()); reason != "" {
			return deployError(reason, err)
		}
		return fmt.Errorf("upload agent: %w", err)
	}

	uploaded, err := s.remoteHash(ctx, lease, tmpPath)
	if err != nil {
		return err
	}
	if uploaded != artifact.SHA256 {
		// Leave nothing half-activated behind.
		_, _ = lease.Exec(ctx, "rm -f "+tmpPath, s.execBudget)
		return deployError(ReasonAgentHashMismatch,
			fmt.Errorf("uploaded agent hash %s, want %s", uploaded, artifact.SHA256))
	}

	// Activate with a same-filesystem rename so a partial upload can never be
	// executed, and verify by hash when the result is unknown.
	activate := fmt.Sprintf("chmod 0755 %s && mv -f %s %s", tmpPath, tmpPath, agentPath)
	result, activateErr := lease.Exec(ctx, activate, s.execBudget)
	// A non-zero exit or a lost reply both leave the activation outcome unknown;
	// only the remote hash decides, and a failed confirmation cleans up the
	// temporary upload so nothing half-activated is ever executed.
	if activateErr != nil || result == nil || result.ExitCode != 0 {
		confirmed, verifyErr := s.remoteHash(ctx, lease, agentPath)
		if verifyErr != nil || confirmed != artifact.SHA256 {
			_, _ = lease.Exec(ctx, "rm -f "+tmpPath, s.execBudget)
			stderr := ""
			if result != nil {
				stderr = strings.TrimSpace(result.Stderr)
			}
			return deployError(ReasonAgentActivationUnknown,
				fmt.Errorf("activate agent: exit=%v err=%v stderr=%q remote_hash=%q", exitCodeOf(result), activateErr, stderr, confirmed))
		}
	}
	return nil
}

var shaLine = regexp.MustCompile(`(?m)^([0-9a-fA-F]{64})\s`)

// remoteHash reads the agent's SHA-256 using a tool the host already has. An
// absent file yields an empty hash; a host with neither tool is a distinct
// failure class.
func (s *AgentDeploymentService) remoteHash(ctx context.Context, lease Lease, path string) (string, error) {
	command := fmt.Sprintf(
		"if command -v sha256sum >/dev/null 2>&1; then sha256sum %[1]s 2>/dev/null; elif command -v shasum >/dev/null 2>&1; then shasum -a 256 %[1]s 2>/dev/null; else echo NO_SHA_TOOL; fi",
		path)
	result, err := lease.Exec(ctx, command, s.probeBudget)
	if err != nil {
		return "", fmt.Errorf("remote hash of %s: %w", path, err)
	}
	if result == nil {
		return "", fmt.Errorf("remote hash of %s: empty result", path)
	}
	output := strings.TrimSpace(result.Stdout)
	if strings.Contains(output, "NO_SHA_TOOL") {
		return "", deployError(ReasonAgentToolMissing, errors.New("target has neither sha256sum nor shasum"))
	}
	if match := shaLine.FindStringSubmatch(output); match != nil {
		return strings.ToLower(match[1]), nil
	}
	if reason := classifyRemoteStderr(result.Stderr); reason != "" {
		return "", deployError(reason, errors.New(strings.TrimSpace(result.Stderr)))
	}
	// No digest: the file is absent (or the tool refused), which the caller
	// treats as "needs deployment".
	return "", nil
}

// classifyExecFailure maps transport-level failures onto the read-only/noexec
// classes when the peer says so.
func classifyExecFailure(err error) error {
	if err == nil {
		return nil
	}
	if reason := classifyRemoteStderr(err.Error()); reason != "" {
		return deployError(reason, err)
	}
	return err
}

// classifyRemoteStderr recognises the failures that need their own reason class.
func classifyRemoteStderr(stderr string) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "read-only file system"), strings.Contains(lower, "readonly file system"):
		return ReasonAgentReadOnly
	case strings.Contains(lower, "no space left on device"):
		return ReasonAgentReadOnly
	case strings.Contains(lower, "exec format error"), strings.Contains(lower, "text file busy"),
		strings.Contains(lower, "permission denied"), strings.Contains(lower, "operation not permitted"):
		return ReasonAgentNoExec
	default:
		return ""
	}
}

// parseAgentOutput decodes the agent's JSON payload.
func parseAgentOutput(stdout string) (*agentOutput, error) {
	var out agentOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return nil, fmt.Errorf("parse agent JSON: %w", err)
	}
	return &out, nil
}

// exitCodeOf reports a command's exit code for diagnostics.
func exitCodeOf(result *ExecResult) int {
	if result == nil {
		return -1
	}
	return result.ExitCode
}
