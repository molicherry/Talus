package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/telemetry"
)

// monitorTelemetry records the bounded log and metric vocabulary for the
// background paths. Every method is nil-safe: without a registry the counter
// calls are skipped and only the structured log line is written.
//
// The values are never free-form. `talus_monitor_collections_total` and
// `talus_operations_total` labels come from the closed enums, and the mapping
// below is asserted by tests through the registry's own validation, so a new
// reason class cannot reach the metrics without being added to the vocabulary.
type monitorTelemetry struct {
	registry *telemetry.Registry
	logger   *slog.Logger
	seq      atomic.Uint64
}

func newMonitorTelemetry(registry *telemetry.Registry, logger *slog.Logger) *monitorTelemetry {
	if logger == nil {
		logger = slog.Default()
	}
	return &monitorTelemetry{registry: registry, logger: logger}
}

// nextCollectionID is stable for one collection attempt and lets the collection
// event, its stage events and its deployment attempt be correlated.
func (t *monitorTelemetry) nextCollectionID(serverID uint) string {
	return fmt.Sprintf("mc-%d-%d", serverID, t.seq.Add(1))
}

// collectionEvent is one finished collection attempt.
type collectionEvent struct {
	CollectionID string
	ServerID     uint
	Outcome      string
	ReasonClass  string
	Duration     time.Duration
	ScheduleLag  time.Duration
	QueueWait    time.Duration
}

// collection records the final event of one attempt: the log line is written for
// every outcome (success included) and the counters are incremented exactly once.
func (t *monitorTelemetry) collection(event collectionEvent) {
	if t == nil {
		return
	}
	t.logger.Info("monitor collection finished",
		"action", "monitor.collect",
		"collection_id", event.CollectionID,
		"server_id", event.ServerID,
		"outcome", event.Outcome,
		"reason_class", event.ReasonClass,
		"duration_ms", event.Duration.Milliseconds(),
		"schedule_lag_ms", event.ScheduleLag.Milliseconds(),
		"queue_wait_ms", event.QueueWait.Milliseconds(),
	)
	if t.registry == nil {
		return
	}
	labels := map[string]string{"action": "monitor.collect", "outcome": event.Outcome, "reason_class": event.ReasonClass}
	_ = t.registry.IncCounter("talus_operations_total", labels)
	_ = t.registry.IncCounter("talus_monitor_collections_total", map[string]string{
		"outcome": event.Outcome, "reason_class": event.ReasonClass,
	})
	_ = t.registry.ObserveHistogram("talus_monitor_schedule_lag_seconds", nil, event.ScheduleLag.Seconds())
}

// stage records one stage duration of an action.
func (t *monitorTelemetry) stage(action, stage string, duration time.Duration) {
	if t == nil || t.registry == nil {
		return
	}
	_ = t.registry.ObserveHistogram("talus_stage_duration_seconds",
		map[string]string{"action": action, "stage": stage}, duration.Seconds())
}

// skippedCollection counts a missed admission slot in both counters. The generic
// operations counter and the dedicated collections counter describe the same
// event, so they must never disagree: the collection series covers *collection
// attempts and skips*, whether or not a collection id was produced (N05).
func (t *monitorTelemetry) skippedCollection(reasonClass string) {
	if t == nil || t.registry == nil {
		return
	}
	labels := map[string]string{"action": "monitor.collect", "outcome": telOutcomeSkipped, "reason_class": reasonClass}
	_ = t.registry.IncCounter("talus_operations_total", labels)
	_ = t.registry.IncCounter("talus_monitor_collections_total", map[string]string{
		"outcome": telOutcomeSkipped, "reason_class": reasonClass,
	})
}

// workers records the in-flight worker gauge for a component.
func (t *monitorTelemetry) workers(component string, delta float64) {
	if t == nil || t.registry == nil {
		return
	}
	_ = t.registry.AddGauge("talus_workers_in_flight", map[string]string{"component": component}, delta)
}

// agentUpdate records one deployment attempt outcome.
func (t *monitorTelemetry) agentUpdate(arch, outcome, reasonClass string) {
	if t == nil {
		return
	}
	if t.registry == nil {
		return
	}
	_ = t.registry.IncCounter("talus_agent_updates_total", map[string]string{
		"arch": arch, "outcome": outcome, "reason_class": reasonClass,
	})
	_ = t.registry.IncCounter("talus_operations_total", map[string]string{
		"action": "agent.deploy", "outcome": outcome, "reason_class": reasonClass,
	})
}

// operation records one maintenance cycle.
func (t *monitorTelemetry) operation(action, outcome, reasonClass string) {
	if t == nil || t.registry == nil {
		return
	}
	_ = t.registry.IncCounter("talus_operations_total", map[string]string{
		"action": action, "outcome": outcome, "reason_class": reasonClass,
	})
}

// Fixed outcome/reason vocabulary used by the mappings below. Keeping them as
// constants documents the only legal values.
const (
	telOutcomeSuccess  = "success"
	telOutcomeFailure  = "failure"
	telOutcomeTimeout  = "timeout"
	telOutcomeCanceled = "canceled"
	telOutcomeSkipped  = "skipped"
	telOutcomeRejected = "rejected"
	telOutcomeUnknown  = "unknown"

	telReasonNone       = "none"
	telReasonAuth       = "auth"
	telReasonPlatform   = "platform"
	telReasonProbe      = "probe"
	telReasonArtifact   = "artifact"
	telReasonIntegrity  = "integrity"
	telReasonPermission = "permission"
	telReasonProtocol   = "protocol"
	telReasonDial       = "dial"
	telReasonQuota      = "quota"
	telReasonTimeout    = "timeout"
	telReasonCanceled   = "canceled"
	telReasonRevoked    = "revoked"
	telReasonWrite      = "write"
	telReasonDB         = "db"
	telReasonOther      = "other"
)

// collectionOutcome maps an internal task result onto the frozen outcome and
// reason class. It never returns an unlisted value.
func collectionOutcome(outcome monitorOutcome, err error) (string, string) {
	switch outcome {
	case outcomeSuccess:
		return telOutcomeSuccess, telReasonNone
	case outcomeCancelled:
		if errors.Is(err, sshpool.ErrStaleGeneration) {
			return telOutcomeCanceled, telReasonRevoked
		}
		return telOutcomeCanceled, telReasonCanceled
	case outcomeSkipped:
		if errors.Is(err, errAgentDeploymentPending) {
			return telOutcomeSkipped, telReasonArtifact
		}
		// Admission was deferred for capacity: skipped, and the reason names the
		// resource that was exhausted.
		return telOutcomeSkipped, telReasonQuota
	default:
		if err == nil {
			return telOutcomeFailure, telReasonOther
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return telOutcomeTimeout, telReasonTimeout
		}
		return telOutcomeFailure, deploymentReasonClass(err)
	}
}

// deploymentReasonClass translates a deployment failure into the frozen reason
// vocabulary.
func deploymentReasonClass(err error) string {
	reason := ReasonFromError(err)
	switch reason {
	case ReasonAgentPlatformUnsupported:
		return telReasonPlatform
	case ReasonAgentToolMissing:
		return telReasonProbe
	case ReasonAgentReadOnly, ReasonAgentNoExec:
		return telReasonPermission
	case ReasonAgentHashMismatch:
		return telReasonIntegrity
	case ReasonAgentProtocolMismatch, ReasonAgentLegacyProtocol:
		return telReasonProtocol
	case ReasonAgentActivationUnknown:
		return telReasonArtifact
	}
	if isConnectionError(err) {
		return telReasonDial
	}
	return telReasonOther
}

// agentArch maps a probed architecture onto the frozen arch enum.
func agentArch(arch string) string {
	switch arch {
	case "amd64", "arm64":
		return arch
	default:
		return "unsupported"
	}
}

// collectionIDKey carries one attempt's correlation id down to the deployment and
// stage logs, so the whole attempt shares the id emitted with its final event.
type collectionIDKey struct{}

// WithCollectionID attaches a collection id to a task context.
func WithCollectionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, collectionIDKey{}, id)
}

// CollectionIDFromContext returns the collection id of the attempt, if any.
func CollectionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(collectionIDKey{}).(string)
	return id
}
