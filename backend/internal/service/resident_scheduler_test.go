package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

func TestResidentStagesSeparateTransferAndActivationCapacity(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	begin := func() (context.Context, *residentStages) {
		t.Helper()
		ctx, s, err := m.beginInstall(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.finish)
		return ctx, s
	}
	activations := []*residentStages{}
	for i := 0; i < 4; i++ {
		ctx, s := begin()
		if err := m.beginActivation(ctx); err != nil {
			t.Fatal(err)
		}
		activations = append(activations, s)
	}
	contexts := []context.Context{}
	for i := 0; i < 4; i++ {
		ctx, _ := begin()
		contexts = append(contexts, ctx)
	}
	if len(m.transferSlots) != 4 || len(m.activationSlots) != 4 {
		t.Fatal("activation waits prevented independent transfers")
	}
	transitioned := make(chan error, 1)
	go func() { transitioned <- m.beginActivation(contexts[0]) }()
	waitFor(t, "release transfer slot before activation wait", func() bool { return len(m.transferSlots) == 3 })
	_, extra := begin()
	select {
	case <-transitioned:
		t.Fatal("activation exceeded its concurrency limit")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		_, s, err := m.beginInstall(ctx)
		if s != nil {
			s.finish()
		}
		waiting <- err
	}()
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued transfer cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued transfer ignored cancellation")
	}
	extra.finish()
	activations[0].finish()
	select {
	case err := <-transitioned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("activation did not take the released slot")
	}
	if len(m.activationSlots) != 4 {
		t.Fatal("stage transition lost activation ownership")
	}
}

func TestResidentSchedulerBoundsJobsBacksOffAndResetsDesiredState(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := uint(1); i <= residentMaxInstallJobs; i++ {
		if !m.scheduleInstall(ctx, model.AgentNode{ServerID: i}, 0, "v1", &wg, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }) {
			t.Fatalf("job %d was not admitted", i)
		}
	}
	if m.scheduleInstall(ctx, model.AgentNode{ServerID: 1}, 0, "v1", &wg, func(context.Context) error { return nil }) {
		t.Fatal("admitted two installers for one node")
	}
	if m.scheduleInstall(ctx, model.AgentNode{ServerID: 99}, 0, "v1", &wg, func(context.Context) error { return nil }) {
		t.Fatal("pending installation queue grew beyond its bound")
	}
	cancel()
	wg.Wait()
	if len(m.installJobs) != 0 || len(m.installRetries) != 0 {
		t.Fatal("canceled jobs retained admission/backoff")
	}
	n := model.AgentNode{ServerID: 1}
	if !m.scheduleInstall(context.Background(), n, 0, "v1", &wg, func(context.Context) error { return errors.New("slow node") }) {
		t.Fatal("failed job not admitted")
	}
	wg.Wait()
	r := m.installRetries[1]
	if delay := time.Until(r.due); delay < 59*time.Second || delay > 90*time.Second {
		t.Fatalf("first failure has no bounded jittered retry: %v", delay)
	}
	if m.scheduleInstall(context.Background(), n, 0, "v1", &wg, func(context.Context) error { return nil }) {
		t.Fatal("ignored retry backoff")
	}
	if !m.scheduleInstall(context.Background(), n, 0, "v2-route", &wg, func(context.Context) error { return nil }) {
		t.Fatal("changed desired version/route retained obsolete backoff")
	}
	wg.Wait()
	if len(m.installRetries) != 0 {
		t.Fatal("successful install retained failure backoff")
	}
	previous := time.Duration(0)
	for failures := 1; failures <= 8; failures++ {
		delay := residentRetryDelay(failures)
		if delay < time.Minute || delay > 15*time.Minute {
			t.Fatal("retry escaped its bounds")
		}
		// Once the base reaches its cap, adjacent jitter intervals overlap;
		// an individual capped delay need not exceed the previous sample.
		if failures <= 4 && delay <= previous {
			t.Fatal(fmt.Sprintf("retry did not back off after %d failures", failures))
		}
		previous = delay
	}
}

func TestResidentSchedulerRetriesTransferProgressAndCapacityAfterThirtySeconds(t *testing.T) {
	for _, outcome := range []error{errArtifactPullProgress, errArtifactPullBusy} {
		t.Run(outcome.Error(), func(t *testing.T) {
			m := NewResidentMonitor(nil, nil, nil, "", false, 1)
			now := time.Now()
			m.now = func() time.Time { return now }
			n := model.AgentNode{ServerID: 1}
			var wg sync.WaitGroup
			attempts := 4
			if errors.Is(outcome, errArtifactPullProgress) {
				attempts = 12
			}
			for attempt := 0; attempt < attempts; attempt++ {
				if !m.scheduleInstall(context.Background(), n, 0, "v1", &wg, func(context.Context) error { return fmt.Errorf("download: %w", outcome) }) {
					t.Fatal("transfer did not resume at short retry deadline")
				}
				wg.Wait()
				retry := m.installRetries[1]
				if retry.failures != 0 || retry.due.Sub(now) != 30*time.Second {
					t.Fatalf("progress/capacity counted as node failure: %+v", retry)
				}
				lastAttempt := now
				n.LastAttempt = &lastAttempt
				now = now.Add(29 * time.Second)
				if m.scheduleInstall(context.Background(), n, 0, "v1", &wg, func(context.Context) error { return nil }) {
					t.Fatal("ignored short retry deadline")
				}
				now = now.Add(time.Second)
			}
			// A restarted Hub has no transient outcome and retains the persisted
			// one-minute minimum instead of retrying in a tight loop.
			m.installRetries = make(map[uint]residentRetry)
			if m.scheduleInstall(context.Background(), n, 0, "v1", &wg, func(context.Context) error { return nil }) {
				t.Fatal("restart bypassed persisted last-attempt protection")
			}
			now = now.Add(30 * time.Second)
			if !m.scheduleInstall(context.Background(), n, 0, "v1", &wg, func(context.Context) error { return nil }) {
				t.Fatal("persisted protection exceeded one minute")
			}
			wg.Wait()
		})
	}
}

func TestResidentSchedulerBacksOffRepeatedBusyAndResetsOnProgress(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	now := time.Now()
	m.now = func() time.Time { return now }
	n := model.AgentNode{ServerID: 1}
	var wg sync.WaitGroup
	attempt := func(key string, outcome error) (residentRetry, time.Duration) {
		t.Helper()
		if !m.scheduleInstall(context.Background(), n, 0, key, &wg, func(context.Context) error { return fmt.Errorf("transfer: %w", outcome) }) {
			t.Fatal("eligible transfer was not admitted")
		}
		wg.Wait()
		r := m.installRetries[n.ServerID]
		delay := r.due.Sub(now)
		now = r.due
		return r, delay
	}
	for i := 1; i <= residentBusyShortRetries+8; i++ {
		r, delay := attempt("v1", errArtifactPullBusy)
		if r.failures != 0 || r.busy != i {
			t.Fatalf("capacity contention counted as a node failure or lost its streak: %+v", r)
		}
		if i <= residentBusyShortRetries {
			if delay != 30*time.Second {
				t.Fatalf("early busy retry=%v, want 30s", delay)
			}
		} else {
			base := min(time.Minute*time.Duration(1<<min(i-residentBusyShortRetries-1, 4)), 10*time.Minute)
			if delay < base || delay > base+base/2 {
				t.Fatalf("busy attempt %d escaped exponential jitter bounds: %v", i, delay)
			}
		}
	}
	if r, delay := attempt("v1", errArtifactPullProgress); r.busy != 0 || r.failures != 0 || delay != 30*time.Second {
		t.Fatalf("new bytes did not reset the capacity backoff: %+v %v", r, delay)
	}
	if r, delay := attempt("v1", errArtifactPullBusy); r.busy != 1 || delay != 30*time.Second {
		t.Fatalf("busy retry retained the old streak after progress: %+v %v", r, delay)
	}
	if r, delay := attempt("v1", errors.New("node failure")); r.busy != 0 || r.failures != 1 || delay < time.Minute || delay > 90*time.Second {
		t.Fatalf("ordinary failures did not retain their independent backoff: %+v %v", r, delay)
	}
	if r, _ := attempt("v1", errArtifactPullBusy); r.busy != 1 || r.failures != 1 {
		t.Fatalf("capacity outcome changed the node failure count: %+v", r)
	}
	if r, delay := attempt("v2-config-route", errArtifactPullBusy); r.busy != 1 || r.failures != 0 || delay != 30*time.Second {
		t.Fatalf("desired-state change retained obsolete counters: %+v %v", r, delay)
	}
	if !m.scheduleInstall(context.Background(), n, 0, "v2-config-route", &wg, func(context.Context) error { return nil }) {
		t.Fatal("successful retry was not admitted")
	}
	wg.Wait()
	if len(m.installRetries) != 0 {
		t.Fatal("success retained busy backoff")
	}
}
