package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

// manualClock is a controllable monitorClock: After registers a timer that the
// test fires by advancing time, so scheduling assertions need no sleeping.
type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*manualTimer
}

type manualTimer struct {
	at    time.Time
	ch    chan time.Time
	fired bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, &manualTimer{at: c.now.Add(d), ch: ch})
	return ch
}

// AdvanceTo moves the clock and fires every timer that is now due.
func (c *manualClock) AdvanceTo(t time.Time) {
	c.mu.Lock()
	if !t.After(c.now) {
		t = c.now
	}
	c.now = t
	var due []*manualTimer
	for _, w := range c.waiters {
		if !w.fired && !w.at.After(c.now) {
			w.fired = true
			due = append(due, w)
		}
	}
	c.mu.Unlock()
	for _, w := range due {
		w.ch <- c.now
	}
}

func (c *manualClock) Advance(d time.Duration) { c.AdvanceTo(c.Now().Add(d)) }

// fakeServerSource serves a mutable node list.
type fakeServerSource struct {
	mu  sync.Mutex
	ids []uint
}

func (f *fakeServerSource) set(ids ...uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = ids
}

func (f *fakeServerSource) FindAll(context.Context) ([]model.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	servers := make([]model.Server, 0, len(f.ids))
	for _, id := range f.ids {
		servers = append(servers, model.Server{BaseModel: model.BaseModel{ID: id}})
	}
	return servers, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newLoopTestMonitor(t *testing.T, src monitorServerSource, task func(context.Context, uint) error, taskBudget time.Duration) (*MonitorService, *manualClock) {
	t.Helper()
	clock := newManualClock()
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.serverRepo = src
	svc.clock = clock
	svc.rng = fixedMonitorRand{0.5}
	svc.taskBudget = taskBudget
	svc.taskFn = task
	return svc, clock
}

// TestMonitorLoopRespectsGlobalCapAndJoinsOnShutdown pins min(N,16) admission in
// the running loop and that shutdown cancels in-flight work and joins it.
func TestMonitorLoopRespectsGlobalCapAndJoinsOnShutdown(t *testing.T) {
	src := &fakeServerSource{}
	ids := make([]uint, 0, 17)
	for id := uint(1); id <= 17; id++ {
		ids = append(ids, id)
	}
	src.set(ids...)

	var inFlight, maxInFlight, started int32
	release := make(chan struct{})
	svc, _ := newLoopTestMonitor(t, src, func(ctx context.Context, _ uint) error {
		atomic.AddInt32(&started, 1)
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		defer atomic.AddInt32(&inFlight, -1)
		<-ctx.Done() // hold the slot until cancelled
		return ctx.Err()
	}, time.Minute)

	appCtx, stopApp := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Start(appCtx)
	}()

	waitFor(t, "16 admitted nodes", func() bool { return atomic.LoadInt32(&inFlight) == 16 })
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&maxInFlight); got > 16 {
		t.Fatalf("max in flight = %d, want at most 16", got)
	}
	if got := atomic.LoadInt32(&started); got != 16 {
		t.Fatalf("started = %d tasks, want 16 (the 17th stays queued)", got)
	}

	// Shutdown must join every in-flight task before Start returns.
	stopApp()
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after cancellation; workers are still parked")
	}
	if got := atomic.LoadInt32(&inFlight); got != 0 {
		t.Fatalf("in flight after shutdown = %d, want 0", got)
	}
}

// TestMonitorLoopDeadlineCountsAsFailureAndBacksOff pins that the admission
// deadline is enforced by the task context and that the following attempt waits
// at least one interval (the k=1 backoff).
func TestMonitorLoopDeadlineCountsAsFailureAndBacksOff(t *testing.T) {
	src := &fakeServerSource{}
	src.set(1)

	var calls int32
	deadlineSeen := make(chan time.Duration, 4)
	svc, clock := newLoopTestMonitor(t, src, func(ctx context.Context, _ uint) error {
		atomic.AddInt32(&calls, 1)
		if deadline, ok := ctx.Deadline(); ok {
			deadlineSeen <- time.Until(deadline)
		}
		<-ctx.Done()
		return ctx.Err()
	}, 50*time.Millisecond)

	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	go svc.Start(appCtx)

	// The first collection starts immediately and is cut by its own deadline.
	select {
	case budget := <-deadlineSeen:
		if budget <= 0 || budget > 50*time.Millisecond {
			t.Fatalf("task budget = %v, want the 50ms admission deadline", budget)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first collection never started")
	}
	waitFor(t, "the deadline to fail the task", func() bool { return atomic.LoadInt32(&calls) == 1 })
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want exactly 1 before the backoff elapses", got)
	}

	// The node is now due only after the backoff, so no further attempt happens
	// until the controllable clock advances.
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want no retry before the backoff window", got)
	}
	clock.Advance(time.Minute)
	waitFor(t, "the retry after the backoff", func() bool { return atomic.LoadInt32(&calls) >= 2 })
}

// TestMonitorLoopDeletionCancelsWithoutFailures pins that removing a node cancels
// its in-flight task promptly and keeps the scheduler serving the rest.
func TestMonitorLoopDeletionCancelsWithoutFailures(t *testing.T) {
	src := &fakeServerSource{}
	src.set(1, 2)

	cancelled := make(chan uint, 4)
	var calls int32
	svc, _ := newLoopTestMonitor(t, src, func(ctx context.Context, id uint) error {
		atomic.AddInt32(&calls, 1)
		<-ctx.Done()
		cancelled <- id
		return ctx.Err()
	}, time.Minute)

	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	go svc.Start(appCtx)

	waitFor(t, "both nodes to start", func() bool { return atomic.LoadInt32(&calls) == 2 })

	// Deleting node 2 cancels it; node 1 keeps running. The delete path in
	// ServerService signals the same change.
	src.set(1)
	svc.NotifyTopologyChange()
	select {
	case id := <-cancelled:
		if id != 2 {
			t.Fatalf("cancelled node %d, want the deleted node 2", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the deleted node's task was not cancelled")
	}

	// A newly managed node is admitted immediately: the loop is still healthy.
	src.set(1, 3)
	svc.NotifyTopologyChange()
	waitFor(t, "the new node to be collected", func() bool { return atomic.LoadInt32(&calls) == 3 })
}
