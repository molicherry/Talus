package gate

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmitSerializesPerUser(t *testing.T) {
	g := New(func(context.Context, uint) (int64, error) { return 1, nil })

	var inFlight int32
	var maxInFlight int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.Admit(context.Background(), 7, func(int64) error {
				n := atomic.AddInt32(&inFlight, 1)
				for {
					m := atomic.LoadInt32(&maxInFlight)
					if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&inFlight, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("admissions overlapped: max in flight = %d", maxInFlight)
	}
}

func TestAdmitPassesFreshVersion(t *testing.T) {
	var calls int32
	g := New(func(context.Context, uint) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 42, nil
	})
	var seen int64
	if err := g.Admit(context.Background(), 1, func(v int64) error { seen = v; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 42 {
		t.Fatalf("version = %d, want 42", seen)
	}
	if calls != 1 {
		t.Fatalf("version source called %d times, want 1 per admission", calls)
	}
}

func TestAdmitUnavailableOnVersionError(t *testing.T) {
	g := New(func(context.Context, uint) (int64, error) { return 0, errors.New("db down") })
	err := g.Admit(context.Background(), 1, func(int64) error { t.Fatal("fn must not run"); return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestAdmitRespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	g := New(func(context.Context, uint) (int64, error) { return 1, nil })

	// Hold the gate.
	held := make(chan struct{})
	go func() {
		_ = g.Admit(context.Background(), 1, func(int64) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Admit(ctx, 1, func(int64) error { t.Fatal("fn must not run"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(release)
}

// TestRecoveryFenceNeverUsesMVCCFallback verifies uncertainty remains fenced on
// failed locking reads, while successful recovery restores fresh normal reads.
func TestRecoveryFenceNeverUsesMVCCFallback(t *testing.T) {
	var normal, locking int
	blocked := false
	g := New(func(context.Context, uint) (int64, error) {
		normal++
		return 0, nil
	}, func(context.Context, uint) (int64, error) {
		locking++
		if blocked {
			return 0, errors.New("row lock unavailable")
		}
		return 1, nil
	})
	unknown := errors.New("commit outcome unknown")
	err := g.Update(context.Background(), 1, func(context.Context, int64) (bool, error) {
		blocked = true
		return true, unknown
	})
	if !errors.Is(err, unknown) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		err := g.Admit(context.Background(), 1, func(int64) error { t.Fatal("unresolved gate admitted"); return nil })
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("error = %v, want unavailable", err)
		}
	}
	if normal != 0 || locking != 3 {
		t.Fatalf("reads normal/locking = %d/%d, want 0/3", normal, locking)
	}
	blocked = false
	if err := g.Recover(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := g.Admit(context.Background(), 1, func(int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if normal != 1 || locking != 4 {
		t.Fatalf("recovery reads normal/locking = %d/%d, want 1/4", normal, locking)
	}
}

// TestGateBudgetIncludesLockWaitAndLookup pins one deadline across both stages.
func TestGateBudgetIncludesLockWaitAndLookup(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	g := New(func(ctx context.Context, _ uint) (int64, error) {
		if calls.Add(1) == 1 {
			return 0, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		_ = g.Admit(context.Background(), 1, func(int64) error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- g.Admit(ctx, 1, func(int64) error { t.Error("expired lookup admitted"); return nil }) }()
	time.Sleep(40 * time.Millisecond)
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected budget exhaustion")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lookup restarted the admission budget")
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("admission took %v, want one shared 80ms budget", elapsed)
	}
	<-first
}

func TestUpdateWithBudgetKeepsAdmissionBoundAndLock(t *testing.T) {
	var admissionCtx context.Context
	g := New(func(ctx context.Context, _ uint) (int64, error) {
		admissionCtx = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > DefaultBudget {
			t.Error("write budget leaked into the version lookup")
		}
		return 1, nil
	})
	err := g.UpdateWithBudget(context.Background(), 1, 4*time.Second, func(writeCtx context.Context, _ int64) (bool, error) {
		// Pass the original admission deadline while the write is still live.
		<-admissionCtx.Done()
		if err := writeCtx.Err(); err != nil {
			t.Fatalf("admission timeout cancelled the write: %v", err)
		}
		short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if err := g.Admit(short, 1, func(int64) error { t.Error("gate unlocked before write finished"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("contending admission = %v", err)
		}
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpdateWithBudgetHonorsWriteAndCallerCancellation(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "write budget", true: "caller deadline"}[callerDeadline], func(t *testing.T) {
			g := New(func(context.Context, uint) (int64, error) { return 1, nil })
			ctx := context.Background()
			budget := 30 * time.Millisecond
			if callerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
				budget = 10 * time.Second
			}
			started := time.Now()
			err := g.UpdateWithBudget(ctx, 1, budget, func(writeCtx context.Context, _ int64) (bool, error) {
				<-writeCtx.Done()
				return true, writeCtx.Err()
			})
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
				t.Fatalf("write did not respect its deadline: %v", err)
			}
			if !g.stateFor(1).pending {
				t.Fatal("timed out uncertain write lost its fence")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := New(func(context.Context, uint) (int64, error) { t.Error("cancelled caller reached lookup"); return 1, nil })
	if err := g.UpdateWithBudget(ctx, 1, time.Second, func(context.Context, int64) (bool, error) { t.Error("cancelled write started"); return false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestUpdateWithBudgetDoesNotExtendVersionLookup(t *testing.T) {
	g := New(func(ctx context.Context, _ uint) (int64, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > DefaultBudget {
			t.Error("lookup deadline enlarged")
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	err := g.UpdateWithBudget(context.Background(), 1, 10*time.Second, func(context.Context, int64) (bool, error) { t.Error("expired lookup reached write"); return false, nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lookup error = %v", err)
	}
}
