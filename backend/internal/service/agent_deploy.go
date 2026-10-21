package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/telemetry"
)

// agentPath is where the deployed agent lives on a managed node. Activation
// renames a verified upload over it atomically.
const agentPath = "/tmp/vpsmanager-agent"

// probeBudget bounds the single composite probe command; the caller's admission
// deadline still caps the whole collection.
const probeBudget = 5 * time.Second

// Failure reason classes for agent deployment. They
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
	ReasonAgentHealthCheckFailed   = "agent_health_check_failed"
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

// ReasonFromError reports the stable failure reason class of an error, or an
// empty string when the error carries none.
func ReasonFromError(err error) string {
	var deployErr *AgentDeployError
	if errors.As(err, &deployErr) {
		return deployErr.Reason
	}
	return ""
}

// AgentPlatform is the probed target platform.
type AgentPlatform struct {
	OS   string
	Arch string
}

func (p AgentPlatform) String() string { return p.OS + "/" + p.Arch }

// AgentDeploymentService owns everything the Hub does to get a matching,
// verified agent running on a managed node: probe, idempotent upload, atomic
// activation and protocol verification. The monitor only
// schedules, validates and stores what this service returns.
type AgentDeploymentService struct {
	ssh              *SSHService
	artifactsDir     string
	probeBudget      time.Duration
	execBudget       time.Duration
	uploadBudget     time.Duration
	trustedArtifacts trustedAgentArtifacts
	// logger emits the agent.deploy correlation events: shared
	// server_id with monitor.collect, never a metric label.
	logger *slog.Logger
	// telemetry records the frozen arch/outcome/reason vocabulary.
	telemetry *monitorTelemetry
}

// NewAgentDeploymentService creates the deployment service over an artifacts
// directory holding the manifest and both architecture-specific binaries.
func NewAgentDeploymentService(ssh *SSHService, artifactsDir string) *AgentDeploymentService {
	return &AgentDeploymentService{
		ssh:          ssh,
		artifactsDir: artifactsDir,
		probeBudget:  probeBudget,
		uploadBudget: agentDeploymentBudget,
		logger:       slog.Default().With("component", "agent-deploy"),
		telemetry:    newMonitorTelemetry(nil, nil),
	}
}

// SetTelemetry wires the shared registry so deployment attempts and stage
// durations are recorded with the frozen vocabulary.
func (s *AgentDeploymentService) SetTelemetry(registry *telemetry.Registry, logger *slog.Logger) {
	s.telemetry = newMonitorTelemetry(registry, logger)
	if logger != nil {
		s.logger = logger.With("component", "agent-deploy")
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
	s.telemetry.agentUpdate(agentArch(platform.Arch), telOutcomeRejected, telReasonPlatform)
	return agentproto.Artifact{}, deployError(ReasonAgentPlatformUnsupported,
		fmt.Errorf("no bundled agent for %s", platform))
}

// Probe reads the target operating system and architecture with one composite
// command. An unsupported platform is rejected before anything is uploaded.
func (s *AgentDeploymentService) Probe(ctx context.Context, lease Lease) (AgentPlatform, error) {
	started := time.Now()
	defer func() { s.telemetry.stage("monitor.collect", "arch_probe", time.Since(started)) }()
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

	// The deployed artifact is verified by hash before anything runs: a matching
	// self-reported version is not evidence (a local rebuild, a reused version
	// string or a foreign binary can all claim it), so only a missing or
	// different artifact triggers a deployment (R10).
	deployedHash, err := s.remoteHash(ctx, lease, agentPath)
	if err != nil {
		return nil, err
	}
	if deployedHash != artifact.SHA256 {
		// Deployments are reported as their own action so failures and skips stay
		// countable next to the collection events.
		started := time.Now()
		s.logger.Info("agent deployment attempt",
			"action", "agent.deploy", "collection_id", CollectionIDFromContext(ctx),
			"server_id", lease.ServerID(), "platform", platform.String(),
			"build_version", artifact.BuildVersion, "protocol_version", manifest.ProtocolVersion)
		if err := s.deploy(ctx, lease, artifact); err != nil {
			s.telemetry.agentUpdate(agentArch(platform.Arch), telOutcomeFailure, deploymentReasonClass(err))
			attrs := []slog.Attr{
				slog.String("action", "agent.deploy"), slog.String("collection_id", CollectionIDFromContext(ctx)),
				slog.String("outcome", "failure"), slog.Uint64("server_id", uint64(lease.ServerID())),
				slog.String("platform", platform.String()), slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			}
			diagnostics := remoteFailureLogAttrs(err)
			if reason := ReasonFromError(err); reason != "" {
				// Preserve existing deployment-specific classes; unclassified
				// transport failures now receive the shared non-empty fallback.
				diagnostics[0] = slog.String("reason_class", reason)
			}
			attrs = append(attrs, diagnostics...)
			s.logger.LogAttrs(ctx, slog.LevelWarn, "agent deployment failed", attrs...)
			return nil, err
		}
		s.telemetry.agentUpdate(agentArch(platform.Arch), telOutcomeSuccess, telReasonNone)
		s.logger.Info("agent deployment succeeded",
			"action", "agent.deploy", "collection_id", CollectionIDFromContext(ctx),
			"outcome", "success", "reason_class", "",
			"server_id", lease.ServerID(), "platform", platform.String(),
			"duration_ms", time.Since(started).Milliseconds())
	}

	// Execution is verified independently of what the artifact claimed.
	out, err := s.runAgent(ctx, lease)
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
	started := time.Now()
	defer func() { s.telemetry.stage("monitor.collect", "run", time.Since(started)) }()
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
// Hub was built with.
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
	// A failed upload can still have written the file, and every retry uses a new
	// name, so each failure path removes the temporary artifact. A cleanup that
	// cannot complete is reported instead of being silently dropped (R11).
	activated := false
	defer func() {
		if activated {
			return
		}
		// Exec can report a clean transport with a non-zero remote exit: the file
		// would stay and the failure would be invisible. Only exit 0 confirms the
		// cleanup (F04).
		result, err := s.cleanupUpload(ctx, lease, tmpPath)
		if err != nil || result == nil || result.ExitCode != 0 {
			exitCode := -1
			if result != nil {
				exitCode = result.ExitCode
			}
			attrs := []slog.Attr{
				slog.String("action", "agent.deploy"), slog.String("outcome", "failure"),
				slog.Uint64("server_id", uint64(lease.ServerID())), slog.String("path", tmpPath),
				slog.Int("exit_code", exitCode),
			}
			if err == nil {
				attrs = append(attrs, slog.String("reason_class", telReasonPermission))
			} else {
				attrs = append(attrs, remoteFailureLogAttrs(err)...)
			}
			s.logger.LogAttrs(ctx, slog.LevelWarn, "agent upload cleanup failed", attrs...)
		}
	}()

	uploadStarted := time.Now()
	if err := lease.Upload(ctx, localPath, tmpPath, s.uploadBudget); err != nil {
		s.telemetry.stage("agent.deploy", "upload", time.Since(uploadStarted))
		if reason := classifyRemoteStderr(err.Error()); reason != "" {
			return deployError(reason, err)
		}
		return fmt.Errorf("upload agent: %w", err)
	}

	s.telemetry.stage("agent.deploy", "upload", time.Since(uploadStarted))
	hashStarted := time.Now()
	uploaded, err := s.remoteHash(ctx, lease, tmpPath)
	s.telemetry.stage("agent.deploy", "hash_verify", time.Since(hashStarted))
	if err != nil {
		return err
	}
	if uploaded != artifact.SHA256 {
		return deployError(ReasonAgentHashMismatch,
			fmt.Errorf("uploaded agent hash %s, want %s", uploaded, artifact.SHA256))
	}

	// Activate with a same-filesystem rename so a partial upload can never be
	// executed, and verify by hash when the result is unknown.
	activateStarted := time.Now()
	activate := fmt.Sprintf("chmod 0755 %s && mv -f %s %s", tmpPath, tmpPath, agentPath)
	result, activateErr := lease.Exec(ctx, activate, s.execBudget)
	// A non-zero exit or a lost reply both leave the activation outcome unknown;
	// only the remote hash decides, and a failed confirmation cleans up the
	// temporary upload so nothing half-activated is ever executed.
	if activateErr != nil || result == nil || result.ExitCode != 0 {
		confirmed, verifyErr := s.remoteHash(ctx, lease, agentPath)
		if verifyErr != nil || confirmed != artifact.SHA256 {
			stderr := ""
			if result != nil {
				stderr = strings.TrimSpace(result.Stderr)
			}
			return deployError(ReasonAgentActivationUnknown,
				fmt.Errorf("activate agent: exit=%v err=%v stderr=%q remote_hash=%q", exitCodeOf(result), activateErr, stderr, confirmed))
		}
	}
	s.telemetry.stage("agent.deploy", "activate", time.Since(activateStarted))
	activated = true
	return nil
}

// cleanupUpload survives task cancellation but shares the failed primitive's
// absolute cleanup deadline. A dead lease is replaced only after it settles,
// and only the same target generation may remove the generated temporary file.
func (s *AgentDeploymentService) cleanupUpload(ctx context.Context, lease Lease, path string) (*ExecResult, error) {
	deadline := lease.SettlementDeadline()
	cleanupCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	clock := &sshCleanupClock{}
	clock.once.Do(func() {
		grace := time.Now().Add(execTeardownGrace)
		if deadline.Before(grace) {
			grace = deadline
		}
		clock.value = sshCleanupDeadlines{grace: grace, total: deadline}
	})
	if original, ok := lease.(*sshLease); ok {
		original.mu.Lock()
		clock = original.cleanup
		original.mu.Unlock()
	}
	cleanupCtx = context.WithValue(cleanupCtx, sshCleanupClockKey{}, clock)
	result, err := lease.Exec(cleanupCtx, "rm -f "+path, s.execBudget)
	if !errors.Is(err, errSSHLeaseUnusable) || s.ssh == nil {
		return result, err
	}
	lease.Discard()
	select {
	case <-lease.Done():
	case <-cleanupCtx.Done():
		return nil, context.Cause(cleanupCtx)
	}
	replacement, err := s.ssh.AcquireLease(cleanupCtx, lease.ServerID(), LeaseConsumerAgent)
	if err != nil {
		return nil, err
	}
	defer replacement.Release()
	replacement.mu.Lock()
	replacement.cleanup = clock
	replacement.mu.Unlock()
	if replacement.Generation() != lease.Generation() {
		replacement.Release()
		return nil, errors.Join(sshpool.ErrStaleGeneration, replacement.releaseAndWait())
	}
	result, err = replacement.Exec(cleanupCtx, "rm -f "+path, s.execBudget)
	return result, errors.Join(err, replacement.releaseAndWait())
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
