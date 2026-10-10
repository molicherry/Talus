package telemetry

import "testing"

func TestRegistryRejectsUnknownMetricAndLabels(t *testing.T) {
	r := NewRegistry()
	if err := r.IncCounter("talus_nope_total", nil); err == nil {
		t.Fatal("unknown metric must be rejected")
	}
	if err := r.IncCounter("talus_operations_total", map[string]string{"action": "server.exec"}); err == nil {
		t.Fatal("missing declared labels must be rejected")
	}
	if err := r.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "agent", "server_id": "1"}, 1); err == nil {
		t.Fatal("undeclared label must be rejected")
	}
	if err := r.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "agent"}, 1); err != nil {
		t.Fatalf("declared label should be accepted: %v", err)
	}
}

func TestRegistryCountsExactlyOnce(t *testing.T) {
	r := NewRegistry()
	labels := map[string]string{"action": "ssh.upload", "outcome": "success", "reason_class": "none"}
	for i := 0; i < 3; i++ {
		if err := r.IncCounter("talus_operations_total", labels); err != nil {
			t.Fatalf("inc: %v", err)
		}
	}
	if err := r.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "agent"}, 1); err != nil {
		t.Fatalf("gauge +1: %v", err)
	}
	if err := r.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "agent"}, -1); err != nil {
		t.Fatalf("gauge -1: %v", err)
	}
	snap := r.Snapshot()
	if got := snap["talus_operations_total{action=ssh.upload,outcome=success,reason_class=none}"]; got != 3 {
		t.Fatalf("counter = %v, want 3", got)
	}
	if got := snap["talus_ssh_leases_in_use{consumer=agent}"]; got != 0 {
		t.Fatalf("gauge = %v, want 0 after release", got)
	}
}

func TestMetricsCatalogIsClosed(t *testing.T) {
	want := map[string]bool{
		"talus_operations_total":             true,
		"talus_stage_duration_seconds":       true,
		"talus_auth_checks_total":            true,
		"talus_session_revocations_total":    true,
		"talus_ssh_leases_in_use":            true,
		"talus_workers_in_flight":            true,
		"talus_teardowns_total":              true,
		"talus_monitor_collections_total":    true,
		"talus_monitor_schedule_lag_seconds": true,
		"talus_agent_updates_total":          true,
	}
	for _, d := range Metrics {
		delete(want, d.Name)
	}
	if len(want) != 0 {
		t.Fatalf("metric catalog missing: %v", want)
	}
	// Lease gauge label set is bounded to a single owner dimension.
	if def, ok := lookup("talus_ssh_leases_in_use"); !ok || len(def.Labels) != 1 || def.Labels[0] != "consumer" {
		t.Fatalf("lease gauge must have exactly the consumer label, got %+v", def)
	}
}

func TestRegistryRejectsInvalidLabelValues(t *testing.T) {
	r := NewRegistry()
	// consumer is a closed enum: the Monitor role must not appear.
	if err := r.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "monitor"}, 1); err == nil {
		t.Fatal("consumer=monitor must be rejected")
	}
	if err := r.AddGauge("talus_ssh_leases_in_use", map[string]string{"consumer": "agent"}, 1); err != nil {
		t.Fatalf("consumer=agent should be accepted: %v", err)
	}
	// outcome/reason_class/action are closed enums too.
	if err := r.IncCounter("talus_operations_total", map[string]string{"action": "server.exec", "outcome": "nope", "reason_class": "none"}); err == nil {
		t.Fatal("an unknown outcome value must be rejected")
	}
	if err := r.IncCounter("talus_operations_total", map[string]string{"action": "curl http://x", "outcome": "success", "reason_class": "none"}); err == nil {
		t.Fatal("a free-form action must be rejected")
	}
}

func TestRegistryEnforcesMetricKind(t *testing.T) {
	r := NewRegistry()
	if err := r.IncCounter("talus_ssh_leases_in_use", map[string]string{"consumer": "agent"}); err == nil {
		t.Fatal("IncCounter on a gauge must be rejected")
	}
	if err := r.AddGauge("talus_operations_total", map[string]string{"action": "server.exec", "outcome": "success", "reason_class": "none"}, 1); err == nil {
		t.Fatal("AddGauge on a counter must be rejected")
	}
}
