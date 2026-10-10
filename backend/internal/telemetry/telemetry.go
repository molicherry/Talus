// Package telemetry freezes the fixed log and metric vocabulary from
// REQUIREMENTS §12. Phase 2+ wires these into the running services; this package
// only defines the enums, the metric registry and the bounded label contract so
// call sites can compile against a single source of truth.
package telemetry

import (
	"fmt"
	"sort"
	"sync"
)

// Component is the REQUIRED §12.1 component vocabulary.
type Component string

const (
	ComponentAuth     Component = "auth"
	ComponentSSH      Component = "ssh"
	ComponentTerminal Component = "terminal"
	ComponentAgent    Component = "agent"
	ComponentMonitor  Component = "monitor"
	ComponentMetrics  Component = "metrics"
	ComponentRelay    Component = "relay"
)

// Stage is the REQUIRED §12.1 stage vocabulary.
type Stage string

const (
	StageVersionCheck   Stage = "version_check"
	StageRegister       Stage = "register"
	StageReady          Stage = "ready"
	StageRevoke         Stage = "revoke"
	StageQuotaWait      Stage = "quota_wait"
	StageDial           Stage = "dial"
	StageArchProbe      Stage = "arch_probe"
	StageArtifactSelect Stage = "artifact_select"
	StageRemoteInspect  Stage = "remote_inspect"
	StageUpload         Stage = "upload"
	StageHashVerify     Stage = "hash_verify"
	StageActivate       Stage = "activate"
	StageRun            Stage = "run"
	StageWrite          Stage = "write"
	StageStore          Stage = "store"
	StageCleanup        Stage = "cleanup"
	StageAggregate      Stage = "aggregate"
	StageRetention      Stage = "retention"
	StageFinalize       Stage = "finalize"
)

// Outcome is the REQUIRED §12.1 outcome vocabulary.
type Outcome string

const (
	OutcomeSuccess  Outcome = "success"
	OutcomeFailure  Outcome = "failure"
	OutcomeTimeout  Outcome = "timeout"
	OutcomeCanceled Outcome = "canceled"
	OutcomeSkipped  Outcome = "skipped"
	OutcomeRejected Outcome = "rejected"
	OutcomeUnknown  Outcome = "unknown"
)

// ReasonClass is the REQUIRED §12.1 reason vocabulary.
type ReasonClass string

const (
	ReasonNone         ReasonClass = "none"
	ReasonAuth         ReasonClass = "auth"
	ReasonHostKey      ReasonClass = "host_key"
	ReasonQuota        ReasonClass = "quota"
	ReasonDial         ReasonClass = "dial"
	ReasonProbe        ReasonClass = "probe"
	ReasonPlatform     ReasonClass = "platform"
	ReasonArchitecture ReasonClass = "architecture"
	ReasonArtifact     ReasonClass = "artifact"
	ReasonIntegrity    ReasonClass = "integrity"
	ReasonPermission   ReasonClass = "permission"
	ReasonProtocol     ReasonClass = "protocol"
	ReasonDB           ReasonClass = "db"
	ReasonWrite        ReasonClass = "write"
	ReasonRemoteExit   ReasonClass = "remote_exit"
	ReasonTimeout      ReasonClass = "timeout"
	ReasonCanceled     ReasonClass = "canceled"
	ReasonRevoked      ReasonClass = "revoked"
	ReasonRetention    ReasonClass = "retention"
	ReasonOther        ReasonClass = "other"
)

// Kind is a metric type.
type Kind string

const (
	KindCounter   Kind = "counter"
	KindHistogram Kind = "histogram"
	KindGauge     Kind = "gauge"
)

// MetricDef is a metric with its bounded label key set (REQUIREMENTS §12.2).
// No ID, host, path, command, version/hash or budget value may be a label.
type MetricDef struct {
	Name   string
	Kind   Kind
	Labels []string
}

// Metrics is the closed metric catalog. Lease gauges have exactly one owner:
// the SSH resource layer, with consumer in {exec, terminal, agent}.
var Metrics = []MetricDef{
	{Name: "talus_operations_total", Kind: KindCounter, Labels: []string{"action", "outcome", "reason_class"}},
	{Name: "talus_stage_duration_seconds", Kind: KindHistogram, Labels: []string{"action", "stage"}},
	{Name: "talus_auth_checks_total", Kind: KindCounter, Labels: []string{"channel", "outcome", "reason_class"}},
	{Name: "talus_session_revocations_total", Kind: KindCounter, Labels: []string{"outcome"}},
	{Name: "talus_ssh_leases_in_use", Kind: KindGauge, Labels: []string{"consumer"}},
	{Name: "talus_workers_in_flight", Kind: KindGauge, Labels: []string{"component"}},
	{Name: "talus_teardowns_total", Kind: KindCounter, Labels: []string{"action", "result"}},
	{Name: "talus_monitor_collections_total", Kind: KindCounter, Labels: []string{"outcome", "reason_class"}},
	{Name: "talus_monitor_schedule_lag_seconds", Kind: KindHistogram, Labels: nil},
	{Name: "talus_agent_updates_total", Kind: KindCounter, Labels: []string{"arch", "outcome", "reason_class"}},
}

func lookup(name string) (MetricDef, bool) {
	for _, d := range Metrics {
		if d.Name == name {
			return d, true
		}
	}
	return MetricDef{}, false
}

// Registry is a minimal in-process metric registry. It is deliberately small:
// it exists so call sites and tests share one bounded vocabulary, not to
// replace an external monitoring platform.
type Registry struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{counters: map[string]float64{}, gauges: map[string]float64{}}
}

func (r *Registry) key(name string, labels map[string]string) (string, error) {
	def, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown metric %q", name)
	}
	allowed := map[string]bool{}
	for _, l := range def.Labels {
		allowed[l] = true
	}
	if len(labels) != len(def.Labels) {
		return "", fmt.Errorf("metric %q expects labels %v, got %v", name, def.Labels, labelKeys(labels))
	}
	for k := range labels {
		if !allowed[k] {
			return "", fmt.Errorf("metric %q does not allow label %q", name, k)
		}
	}
	parts := make([]string, 0, len(def.Labels))
	for _, l := range def.Labels {
		parts = append(parts, l+"="+labels[l])
	}
	return name + "{" + join(parts) + "}", nil
}

func labelKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// IncCounter increments a counter by 1 after validating the metric and labels.
func (r *Registry) IncCounter(name string, labels map[string]string) error {
	k, err := r.key(name, labels)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[k]++
	return nil
}

// AddGauge adds delta to a gauge after validating the metric and labels. The
// caller owns exactly-once accounting for the underlying resource.
func (r *Registry) AddGauge(name string, labels map[string]string, delta float64) error {
	k, err := r.key(name, labels)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[k] += delta
	return nil
}

// Snapshot returns the current counter and gauge values, for test assertions.
func (r *Registry) Snapshot() map[string]float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]float64, len(r.counters)+len(r.gauges))
	for k, v := range r.counters {
		out[k] = v
	}
	for k, v := range r.gauges {
		out[k] = v
	}
	return out
}
