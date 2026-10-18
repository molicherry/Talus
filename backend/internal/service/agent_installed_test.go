package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"gorm.io/gorm"
)

type artifactHistory map[string]agentproto.Artifact

func (h artifactHistory) Find(_ context.Context, platform, digest string) (agentproto.Artifact, error) {
	if artifact, ok := h[digest]; ok && artifact.Platform == platform {
		return artifact, nil
	}
	return agentproto.Artifact{}, gorm.ErrRecordNotFound
}

type memoryMetricSink struct {
	mu   sync.Mutex
	rows []*repository.MetricFull
}

func (s *memoryMetricSink) InsertFull(ctx context.Context, row *repository.MetricFull) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

func TestAgentInstalledRequiresTrustedHashAndCompatibleProtocol(t *testing.T) {
	for _, tc := range []struct {
		name     string
		trusted  bool
		protocol int
	}{
		{"unknown_hash", false, 1},
		{"compatible_old_build", true, 1},
		{"incompatible_protocol", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployer, remote, svc, _ := delayedAgentDeployment(t)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("old agent")))
			remote.agentPayload = `{"agent_version":"v0.10.0","protocol_version":1}`
			if tc.trusted {
				deployer.SetTrustedArtifacts(artifactHistory{digest: {Platform: "linux/amd64", SHA256: digest,
					BuildVersion: "v0.10.0", ProtocolVersion: tc.protocol}})
			}
			lease, err := svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			out, upgrade, err := deployer.CollectInstalled(context.Background(), lease)
			if !upgrade {
				t.Fatal("old agent must request an upgrade")
			}
			if tc.trusted && tc.protocol == 1 {
				if err != nil || out == nil || out.AgentVersion != "v0.10.0" {
					t.Fatalf("compatible agent: out=%+v err=%v", out, err)
				}
			} else if !errors.Is(err, errAgentDeploymentPending) || remote.ran(agentPath+" --format json") {
				t.Fatalf("unknown/incompatible artifact was executed: %v", err)
			}
			if remote.ran("sftp.upload") {
				t.Fatal("collection performed an upload")
			}
		})
	}
}

func TestAgentSlowDeploymentOutlivesCollectionAndOldAgentKeepsCollecting(t *testing.T) {
	deployer, remote, svc, _ := delayedAgentDeployment(t)
	// Production has three SSH slots per node; deployment must own its own one.
	svc.pool.Close()
	svc.pool = sshpool.NewPool(time.Minute, 3, time.Second)
	t.Cleanup(svc.pool.Close)
	svc.execDefaultTimeout = 250 * time.Millisecond
	oldDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("old agent")))
	deployer.SetTrustedArtifacts(artifactHistory{oldDigest: {Platform: "linux/amd64", SHA256: oldDigest,
		BuildVersion: "v0.10.0", ProtocolVersion: 1}})
	manifest, err := deployer.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	remote.agentPayloads = map[string]string{
		oldDigest:                 `{"agent_version":"v0.10.0","protocol_version":1,"cpu":{"model":"old"}}`,
		manifest.Agents[0].SHA256: `{"agent_version":"v0.11.0","protocol_version":1,"cpu":{"model":"new"}}`,
	}
	uploadStarted, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(resume) })
	remote.afterUpload = func() { close(uploadStarted); <-resume }
	monitor := NewMonitorService(svc, nil, nil, time.Second)
	var logs bytes.Buffer
	monitor.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	sink := &memoryMetricSink{}
	monitor.metricRepo = sink
	monitor.SetAgentDeployer(deployer)
	appCtx, stop := context.WithCancel(context.Background())
	done := monitor.deployments.start(appCtx)
	defer func() { stop(); <-done }()
	monitor.deployments.sync([]uint{1})
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 2*time.Second)
	firstCtx = WithCollectionID(firstCtx, "mc-1-trigger")
	if err := monitor.collectOne(firstCtx, 1); err != nil {
		t.Fatal(err)
	}
	cancelFirst() // must not cancel deployment admitted by this collection
	select {
	case <-uploadStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("background upload did not start")
	}
	// This exceeds the cancelled collection and the default 250ms SSH budget.
	time.Sleep(500 * time.Millisecond)
	nextCtx, cancelNext := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelNext()
	if err := monitor.collectOne(nextCtx, 1); err != nil {
		t.Fatalf("old agent during upload: %v", err)
	}
	release.Do(func() { close(resume) })
	waitFor(t, "new agent activation", func() bool { return remote.count("chmod 0755") == 1 })
	waitFor(t, "deployment completion", func() bool {
		monitor.deployments.mu.Lock()
		defer monitor.deployments.mu.Unlock()
		return monitor.deployments.active == 0
	})
	if err := monitor.collectOne(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if remote.count("sftp.upload ") != 1 {
		t.Fatal("collections duplicated the long upload")
	}
	record := safeRemoteLogRecord(t, logs.String(), "agent deployment attempt")
	if record["collection_id"] != "mc-1-trigger" {
		t.Fatal("background deployment lost its requesting collection ID")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.rows) != 3 || *sink.rows[0].CPUModel != "old" || *sink.rows[1].CPUModel != "old" || *sink.rows[2].CPUModel != "new" {
		t.Fatalf("upgrade interrupted metric storage: %+v", sink.rows)
	}
}

func TestAgentPendingCollectionIsSkippedWithoutFailureBackoff(t *testing.T) {
	monitor := NewMonitorService(nil, nil, nil, time.Second)
	monitor.taskFn = func(context.Context, uint) error { return errAgentDeploymentPending }
	outcome, err := monitor.runTask(context.Background(), 1)
	label, reason := collectionOutcome(outcome, err)
	if outcome != outcomeSkipped || label != telOutcomeSkipped || reason != telReasonArtifact {
		t.Fatalf("pending deployment classified as %v/%s/%s", outcome, label, reason)
	}
}

type activationRaceLease struct {
	Lease
	beforeRun func()
}

func (l *activationRaceLease) Exec(ctx context.Context, command string, budget time.Duration) (*ExecResult, error) {
	if command == agentPath+" --format json" {
		l.beforeRun()
	}
	return l.Lease.Exec(ctx, command, budget)
}

func TestAgentInstalledRechecksHashWhenActivationRacesCollection(t *testing.T) {
	for _, activate := range []bool{false, true} {
		name := "forged_report"
		if activate {
			name = "atomic_activation"
		}
		t.Run(name, func(t *testing.T) {
			deployer, remote, svc, _ := delayedAgentDeployment(t)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("old agent")))
			deployer.SetTrustedArtifacts(artifactHistory{digest: {Platform: "linux/amd64", SHA256: digest,
				BuildVersion: "v0.10.0", ProtocolVersion: 1}})
			manifest, err := deployer.Manifest()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(deployer.artifactsDir, manifest.Agents[0].File))
			if err != nil {
				t.Fatal(err)
			}
			lease, err := svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			wrapped := &activationRaceLease{Lease: lease, beforeRun: func() {
				if activate {
					remote.mu.Lock()
					remote.files[agentPath] = data
					remote.mu.Unlock()
				}
			}}
			out, upgrade, err := deployer.CollectInstalled(context.Background(), wrapped)
			if activate {
				if err != nil || out == nil || out.AgentVersion != "v0.11.0" || upgrade {
					t.Fatalf("activation race: out=%+v upgrade=%t err=%v", out, upgrade, err)
				}
			} else if ReasonFromError(err) != ReasonAgentProtocolMismatch || out != nil {
				t.Fatal("a new-version report bypassed the artifact hash")
			}
		})
	}
}
