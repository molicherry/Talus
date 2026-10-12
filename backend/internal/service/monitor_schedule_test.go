package service

import (
	"testing"
	"time"
)

// fixedMonitorRand pins the jitter source: 0.5 yields exactly zero jitter.
type fixedMonitorRand struct{ v float64 }

func (r fixedMonitorRand) Float64() float64 { return r.v }

// frozenMonitorClock never advances on its own; tests pass explicit times.
type frozenMonitorClock struct{ now time.Time }

func (c *frozenMonitorClock) Now() time.Time { return c.now }
func (c *frozenMonitorClock) After(time.Duration) <-chan time.Time {
	return make(chan time.Time)
}

func newTestScheduler(t *testing.T, interval time.Duration, jitter float64) (*monitorScheduler, *frozenMonitorClock) {
	t.Helper()
	clock := &frozenMonitorClock{now: time.Unix(1_700_000_000, 0)}
	sched := newMonitorScheduler(interval, clock, fixedMonitorRand{jitter})
	return sched, clock
}

// TestMonitorBackoffSequenceMatchesContract pins TC-08-01's frozen delays: with
// zero jitter and I=60s the consecutive failures wait 60/120/240/300/300s.
func TestMonitorBackoffSequenceMatchesContract(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	sched.sync([]uint{1}, clock.Now())

	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 300 * time.Second, 300 * time.Second}
	now := clock.Now()
	for i, expected := range want {
		tick := sched.plan(now)
		if len(tick.Admitted) != 1 {
			t.Fatalf("round %d: admitted %v, want node 1", i, tick.Admitted)
		}
		// The task finishes immediately and fails: the next attempt is due after
		// the contracted backoff.
		sched.complete(1, outcomeFailure, now)
		due := sched.nodes[1].due
		if got := due.Sub(now); got != expected {
			t.Fatalf("failure %d backoff = %v, want %v", i+1, got, expected)
		}
		now = due
	}
}

// TestMonitorBackoffNeverBelowInterval pins that I=600s keeps 600s between
// attempts even though the cap is max(I,300s)=600s.
func TestMonitorBackoffNeverBelowInterval(t *testing.T) {
	sched, clock := newTestScheduler(t, 600*time.Second, 0.5)
	sched.sync([]uint{1}, clock.Now())
	now := clock.Now()
	for i := 0; i < 4; i++ {
		if tick := sched.plan(now); len(tick.Admitted) != 1 {
			t.Fatalf("round %d not admitted: %+v", i, tick)
		}
		sched.complete(1, outcomeFailure, now)
		delay := sched.nodes[1].due.Sub(now)
		if delay < 600*time.Second || delay > 600*time.Second {
			t.Fatalf("round %d delay = %v, want exactly 600s", i, delay)
		}
		now = sched.nodes[1].due
	}
}

// TestMonitorBackoffJitterStaysInRangeClamped pins the ±10% jitter window and the
// [I, cap] clamp.
func TestMonitorBackoffJitterStaysInRangeClamped(t *testing.T) {
	// k=1 with I=60s: base=60s, so -10% would be 54s and must clamp back to 60s.
	low, _ := newTestScheduler(t, 60*time.Second, 0)
	if got := low.backoffDelay(1); got != 60*time.Second {
		t.Fatalf("low jitter delay = %v, want the I floor 60s", got)
	}
	high, _ := newTestScheduler(t, 60*time.Second, 1)
	if got := high.backoffDelay(1); got != 66*time.Second {
		t.Fatalf("high jitter delay = %v, want 66s (60s +10%%)", got)
	}
	// k=3 with I=60s: base=240s, both jitter ends stay inside [60s,300s].
	if got := low.backoffDelay(3); got != 216*time.Second {
		t.Fatalf("k=3 low = %v, want 216s", got)
	}
	if got := high.backoffDelay(3); got != 264*time.Second {
		t.Fatalf("k=3 high = %v, want 264s", got)
	}
	// Large failure counts saturate at the cap instead of overflowing.
	for _, k := range []int{10, 40, 1000} {
		if got := high.backoffDelay(k); got != 300*time.Second {
			t.Fatalf("k=%d delay = %v, want the 300s cap", k, got)
		}
	}
}

// TestMonitorSuccessResetsFailuresAndSchedule pins that success clears the
// counter and resumes a future slot without replaying missed ticks.
func TestMonitorSuccessResetsFailuresAndSchedule(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	sched.sync([]uint{1}, clock.Now())
	now := clock.Now()
	for i := 0; i < 3; i++ {
		sched.plan(now)
		sched.complete(1, outcomeFailure, now)
		now = sched.nodes[1].due
	}
	if sched.failures(1) != 3 {
		t.Fatalf("failures = %d, want 3", sched.failures(1))
	}
	sched.plan(now)
	// The task finishes 2.5 intervals late: the next attempt must be in the
	// future and within one interval, not a catch-up burst.
	sched.complete(1, outcomeSuccess, now.Add(150*time.Second))
	if sched.failures(1) != 0 {
		t.Fatalf("failures = %d after success, want 0", sched.failures(1))
	}
	due := sched.nodes[1].due
	if !due.After(now.Add(150 * time.Second)) {
		t.Fatalf("due = %v, want a future slot", due)
	}
	if ahead := due.Sub(now.Add(150 * time.Second)); ahead > 60*time.Second {
		t.Fatalf("due is %v ahead, want at most one interval (no replay)", ahead)
	}
}

// TestMonitorCancelClassesDoNotCountAsFailures pins that shutdown, deletion and
// generation invalidation never inflate the consecutive-failure counter.
func TestMonitorCancelClassesDoNotCountAsFailures(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	sched.sync([]uint{1, 2}, clock.Now())
	now := clock.Now()

	sched.plan(now)
	sched.complete(1, outcomeCancelled, now)
	if sched.failures(1) != 0 {
		t.Fatalf("cancelled node failures = %d, want 0", sched.failures(1))
	}

	// A skipped node keeps exactly one queued slot and no failure.
	sched.complete(2, outcomeSkipped, now)
	if sched.failures(2) != 0 {
		t.Fatalf("skipped node failures = %d, want 0", sched.failures(2))
	}
	if sched.queuedCount() > len(sched.nodes) {
		t.Fatalf("queued = %d, want at most one record per node", sched.queuedCount())
	}

	// Removing a node drops its state instead of counting a failure.
	removed := sched.sync([]uint{1}, clock.Now())
	if len(removed) != 1 || removed[0] != 2 {
		t.Fatalf("removed = %v, want [2]", removed)
	}
	if sched.failures(2) != 0 || sched.inFlightCount() != 0 {
		t.Fatalf("deleted node left state behind: failures=%d inFlight=%d", sched.failures(2), sched.inFlightCount())
	}
}

// TestMonitorAdmissionRespectsGlobalCap pins min(N,16): with 17 nodes at most 16
// run at once, the 17th keeps one queued slot and is admitted when capacity frees.
func TestMonitorAdmissionRespectsGlobalCap(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	live := make([]uint, 0, 17)
	for id := uint(1); id <= 17; id++ {
		live = append(live, id)
	}
	sched.sync(live, clock.Now())
	now := clock.Now()

	tick := sched.plan(now)
	if len(tick.Admitted) != 16 {
		t.Fatalf("admitted %d nodes, want 16", len(tick.Admitted))
	}
	if len(tick.Queued) != 1 {
		t.Fatalf("queued %d nodes, want 1", len(tick.Queued))
	}
	if sched.inFlightCount() != 16 {
		t.Fatalf("in flight = %d, want 16", sched.inFlightCount())
	}

	// Still no capacity: the queued node must not be admitted twice over.
	tick = sched.plan(now)
	if len(tick.Admitted) != 0 {
		t.Fatalf("admitted %d nodes without capacity, want 0", len(tick.Admitted))
	}

	// Freeing one slot admits exactly the queued node, in the same pass. The
	// queued node is not in flight, so only the admitted node reports completion.
	sched.complete(sched.order[0], outcomeSuccess, now)
	tick = sched.plan(now)
	if len(tick.Admitted) != 1 {
		t.Fatalf("admitted %d nodes after freeing a slot, want 1", len(tick.Admitted))
	}
	if sched.inFlightCount() > 16 {
		t.Fatalf("in flight = %d, want at most 16", sched.inFlightCount())
	}
}

// TestMonitorNodeIsNotReentrant pins one in-flight task per node.
func TestMonitorNodeIsNotReentrant(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	sched.sync([]uint{1}, clock.Now())
	now := clock.Now()

	if tick := sched.plan(now); len(tick.Admitted) != 1 {
		t.Fatalf("first pass admitted %v", tick.Admitted)
	}
	tick := sched.plan(now.Add(5 * time.Minute))
	if len(tick.Admitted) != 0 {
		t.Fatalf("re-entrant admission: %v", tick.Admitted)
	}
	if len(tick.Skipped) != 1 {
		t.Fatalf("skipped = %v, want the in-flight node", tick.Skipped)
	}
	if sched.inFlightCount() != 1 {
		t.Fatalf("in flight = %d, want 1", sched.inFlightCount())
	}
}

// TestMonitorEarliestDueDrivesWakeUp pins the wake-up calculation used by the
// run loop: a due or queued node returns the zero time (run immediately).
func TestMonitorEarliestDueDrivesWakeUp(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	now := clock.Now()
	sched.sync([]uint{1, 2}, now)

	if due := sched.earliestDue(now); !due.IsZero() {
		t.Fatalf("earliestDue = %v, want the zero time for a fresh node", due)
	}
	sched.plan(now)
	sched.complete(1, outcomeFailure, now)
	sched.complete(2, outcomeFailure, now)
	due := sched.earliestDue(now)
	if got := due.Sub(now); got != time.Minute {
		t.Fatalf("earliestDue = %v, want now+60s", got)
	}
}
