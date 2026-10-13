package service

import "testing"

// TestReviewRetentionRolloutDecision pins the documented rollout: retention is
// off by default, a dry run reports without deleting, and only an explicitly
// enabled non-dry-run deletes anything.
func TestReviewRetentionRolloutDecision(t *testing.T) {
	for _, tc := range []struct {
		enabled, dryRun bool
		want            retentionDecisionKind
	}{
		{false, false, retentionDisabled},
		{false, true, retentionDisabled},
		{true, true, retentionDryRunOnly},
		{true, false, retentionApply},
	} {
		if got := retentionDecision(tc.enabled, tc.dryRun); got != tc.want {
			t.Fatalf("retentionDecision(%v, %v) = %v, want %v", tc.enabled, tc.dryRun, got, tc.want)
		}
	}
}
