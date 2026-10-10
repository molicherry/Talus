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
