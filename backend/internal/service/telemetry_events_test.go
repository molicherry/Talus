package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/telemetry"
)

// TestReviewTelemetryVocabularyIsClosed pins gap C's fail-closed property: every
// outcome/reason pair the emitter can produce is accepted by the frozen registry
// vocabulary, and a success is reported rather than only failures.
func TestReviewTelemetryVocabularyIsClosed(t *testing.T) {
	registry := telemetry.NewRegistry()
	pairs := []struct {
		outcome monitorOutcome
		err     error
	}{
		{outcomeSuccess, nil},
		{outcomeFailure, context.DeadlineExceeded},
		{outcomeFailure, errors.New("boom")},
		{outcomeFailure, deployError(ReasonAgentPlatformUnsupported, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentToolMissing, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentReadOnly, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentNoExec, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentHashMismatch, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentProtocolMismatch, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentLegacyProtocol, errors.New("x"))},
		{outcomeFailure, deployError(ReasonAgentActivationUnknown, errors.New("x"))},
		{outcomeCancelled, sshpool.ErrStaleGeneration},
		{outcomeCancelled, context.Canceled},
		{outcomeSkipped, nil},
	}
	seen := map[string]bool{}
	for _, tc := range pairs {
		label, reason := collectionOutcome(tc.outcome, tc.err)
		if err := registry.IncCounter("talus_monitor_collections_total",
			map[string]string{"outcome": label, "reason_class": reason}); err != nil {
			t.Fatalf("collectionOutcome(%v, %v) = %q/%q is outside the frozen vocabulary: %v",
				tc.outcome, tc.err, label, reason, err)
		}
		if err := registry.IncCounter("talus_operations_total",
			map[string]string{"action": "monitor.collect", "outcome": label, "reason_class": reason}); err != nil {
			t.Fatalf("operations labels rejected for %q/%q: %v", label, reason, err)
		}
		seen[label] = true
	}
	for _, want := range []string{telOutcomeSuccess, telOutcomeFailure, telOutcomeTimeout, telOutcomeCanceled, telOutcomeSkipped} {
		if !seen[want] {
			t.Fatalf("the mapping never produces the %q outcome", want)
		}
	}
}

// TestReviewCollectionEventCountsExactlyOnce pins the counter contract: one
// collection attempt is counted once, and the schedule lag is observed.
func TestReviewCollectionEventCountsExactlyOnce(t *testing.T) {
	registry := telemetry.NewRegistry()
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.SetTelemetry(registry)

	finished := time.Now()
	for i := 0; i < 3; i++ {
		svc.emitCollection("mc-7-1", 7, finished.Add(-2*time.Second), time.Time{}, finished.Add(-time.Second), finished, outcomeSuccess, nil)
	}
	snapshot := registry.Snapshot()
	key := `talus_monitor_collections_total{outcome=success,reason_class=none}`
	if snapshot[key] != 3 {
		t.Fatalf("%s = %v, want 3", key, snapshot[key])
	}
	operations := snapshot[`talus_operations_total{action=monitor.collect,outcome=success,reason_class=none}`]
	if operations != 3 {
		t.Fatalf("operations counter = %v, want 3", operations)
	}
	lag, ok := registry.Observations()["talus_monitor_schedule_lag_seconds"]
	if !ok || lag.Count != 3 {
		t.Fatalf("schedule lag histogram = %+v, want 3 observations", lag)
	}
	// The attempt started one second after its slot became due.
	if lag.Max < 0.9 || lag.Max > 1.1 {
		t.Fatalf("schedule lag max = %v, want about 1s", lag.Max)
	}
}

// TestReviewAgentUpdateLabelsStayBounded pins the arch/outcome vocabulary of the
// deployment counter: a raw uname value never becomes a label.
func TestReviewAgentUpdateLabelsStayBounded(t *testing.T) {
	registry := telemetry.NewRegistry()
	emitter := newMonitorTelemetry(registry, nil)

	// agentArch is a guard, not a normaliser: the caller already mapped uname
	// output, and anything else must not become a label.
	if got := agentArch("x86_64"); got != "unsupported" {
		t.Fatalf("agentArch(x86_64) = %q, want unsupported (raw values are not labels)", got)
	}
	emitter.agentUpdate(agentArch("amd64"), telOutcomeSuccess, telReasonNone)
	emitter.agentUpdate(agentArch("arm64"), telOutcomeFailure, telReasonPermission)
	emitter.agentUpdate(agentArch("riscv64"), telOutcomeRejected, telReasonPlatform)

	snapshot := registry.Snapshot()
	if snapshot[`talus_agent_updates_total{arch=amd64,outcome=success,reason_class=none}`] != 1 {
		t.Fatalf("amd64 success not counted: %v", snapshot)
	}
	if snapshot[`talus_agent_updates_total{arch=arm64,outcome=failure,reason_class=permission}`] != 1 {
		t.Fatalf("arm64 failure not counted: %v", snapshot)
	}
	if snapshot[`talus_agent_updates_total{arch=unsupported,outcome=rejected,reason_class=platform}`] != 1 {
		t.Fatalf("an unsupported architecture must be reported as unsupported: %v", snapshot)
	}
	if err := registry.IncCounter("talus_agent_updates_total",
		map[string]string{"arch": "x86_64", "outcome": telOutcomeSuccess, "reason_class": telReasonNone}); err == nil {
		t.Fatal("a raw uname value must not be accepted as an arch label")
	}
}

// TestReviewDeploymentStageDurationsAreRecorded pins the stage histogram.
func TestReviewDeploymentStageDurationsAreRecorded(t *testing.T) {
	registry := telemetry.NewRegistry()
	emitter := newMonitorTelemetry(registry, nil)
	for _, stage := range []string{"upload", "hash_verify", "activate"} {
		emitter.stage("agent.deploy", stage, 25*time.Millisecond)
	}
	observations := registry.Observations()
	for _, stage := range []string{"upload", "hash_verify", "activate"} {
		key := `talus_stage_duration_seconds{action=agent.deploy,stage=` + stage + `}`
		observation, ok := observations[key]
		if !ok || observation.Count != 1 {
			t.Fatalf("stage %s not recorded: %+v", stage, observations)
		}
	}
}

// TestRereviewCollectionReasonBelongsToItsAttempt pins F08: concurrent attempts
// must not borrow each other's reason class. The old shared error slot let a
// finished attempt pick up whatever another attempt had just returned.
func TestRereviewCollectionReasonBelongsToItsAttempt(t *testing.T) {
	registry := telemetry.NewRegistry()
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.SetTelemetry(registry)

	now := time.Now()
	// Attempt A failed with permission; attempt B failed with protocol. Both
	// finished before A's final event was written, as concurrency allows.
	svc.emitCollection("mc-1-1", 1, time.Time{}, time.Time{}, now.Add(-10*time.Millisecond), now, outcomeFailure,
		deployError(ReasonAgentReadOnly, errors.New("read-only")))
	svc.emitCollection("mc-2-1", 2, time.Time{}, time.Time{}, now.Add(-10*time.Millisecond), now, outcomeFailure,
		deployError(ReasonAgentProtocolMismatch, errors.New("protocol")))

	snapshot := registry.Snapshot()
	if got := snapshot[`talus_monitor_collections_total{outcome=failure,reason_class=permission}`]; got != 1 {
		t.Fatalf("permission count = %v, want 1 (%v)", got, snapshot)
	}
	if got := snapshot[`talus_monitor_collections_total{outcome=failure,reason_class=protocol}`]; got != 1 {
		t.Fatalf("protocol count = %v, want 1 (%v)", got, snapshot)
	}
}

// TestRereviewTimeoutReasonIsNotBorrowed pins the deadline case: a timeout attempt
// keeps its own reason even if another attempt failed differently.
func TestRereviewTimeoutReasonIsNotBorrowed(t *testing.T) {
	registry := telemetry.NewRegistry()
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.SetTelemetry(registry)
	now := time.Now()

	svc.emitCollection("mc-1-2", 1, time.Time{}, time.Time{}, now, now, outcomeFailure, context.DeadlineExceeded)
	svc.emitCollection("mc-2-2", 2, time.Time{}, time.Time{}, now, now, outcomeFailure, deployError(ReasonAgentHashMismatch, errors.New("hash")))

	snapshot := registry.Snapshot()
	if got := snapshot[`talus_monitor_collections_total{outcome=timeout,reason_class=timeout}`]; got != 1 {
		t.Fatalf("timeout count = %v, want 1 (%v)", got, snapshot)
	}
	if got := snapshot[`talus_monitor_collections_total{outcome=failure,reason_class=integrity}`]; got != 1 {
		t.Fatalf("integrity count = %v, want 1 (%v)", got, snapshot)
	}
}

// TestRereviewSkippedAdmissionsAreCounted pins the production deferral path: a
// missed slot is counted once with the frozen skipped/quota pair, and the count
// follows the slots that were really missed rather than the scheduler passes.
func TestRereviewSkippedAdmissionsAreCounted(t *testing.T) {
	registry := telemetry.NewRegistry()
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.SetTelemetry(registry)

	// Two nodes missed one slot each in one pass.
	svc.reportTick(monitorTick{Queued: []uint{1, 2}, Deferred: 2})
	snapshot := registry.Snapshot()
	key := `talus_monitor_collections_total{outcome=skipped,reason_class=quota}`
	if got := snapshot[key]; got != 0 {
		t.Fatalf("skipped count = %v before any deferral: %v", got, snapshot)
	}
	if got := snapshot[`talus_operations_total{action=monitor.collect,outcome=skipped,reason_class=quota}`]; got != 2 {
		t.Fatalf("skipped operations = %v, want 2", got)
	}

	// A pass that only skipped an in-flight node without a missed slot counts
	// nothing new.
	svc.reportTick(monitorTick{Skipped: []uint{1}})
	if got := registry.Snapshot()[`talus_operations_total{action=monitor.collect,outcome=skipped,reason_class=quota}`]; got != 2 {
		t.Fatalf("skipped operations = %v, want the count unchanged", got)
	}
}

// TestRereviewCollectionIDReachesTheDeployment pins the correlation requirement:
// the id generated for an attempt is available to the deployment and its stage
// logs, instead of being created only when the final event is written.
func TestRereviewCollectionIDReachesTheDeployment(t *testing.T) {
	registry := telemetry.NewRegistry()
	emitter := newMonitorTelemetry(registry, nil)
	id := emitter.nextCollectionID(7)
	ctx := WithCollectionID(context.Background(), id)
	if got := CollectionIDFromContext(ctx); got != id {
		t.Fatalf("collection id = %q, want %q", got, id)
	}
	if got := CollectionIDFromContext(context.Background()); got != "" {
		t.Fatalf("a context without an attempt must have no collection id, got %q", got)
	}
}

// TestRereviewLeaseGaugeIsShared pins the SSH lease gauge for the agent consumer.
func TestRereviewLeaseGaugeIsShared(t *testing.T) {
	registry := telemetry.NewRegistry()
	emitter := newMonitorTelemetry(registry, nil)
	emitter.leaseGauge("agent", 1)
	emitter.leaseGauge("agent", -1)
	key := `talus_ssh_leases_in_use{consumer=agent}`
	if got := registry.Snapshot()[key]; got != 0 {
		t.Fatalf("%s = %v, want 0 after acquire and release", key, got)
	}
	// The consumer label stays inside the frozen enum.
	if err := registry.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "probe"}, 1); err == nil {
		t.Fatal("an unknown consumer must be rejected")
	}
}
