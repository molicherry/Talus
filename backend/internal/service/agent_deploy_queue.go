package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
)

const agentDeploymentConcurrency = 4

type agentDeploymentJob struct {
	generation   Generation
	due          time.Time
	failures     int
	running      bool
	retired      bool
	cancel       context.CancelFunc
	collectionID string
}

// The queue belongs to Monitor.Start's application context, not to any one
// collection. At most one job per node and four foreground deployments exist.
type agentDeploymentQueue struct {
	mu      sync.Mutex
	jobs    map[uint]*agentDeploymentJob
	live    map[uint]bool
	wake    chan struct{}
	ctx     context.Context
	active  int
	wg      sync.WaitGroup
	budget  time.Duration
	limit   int
	retry   time.Duration
	execute func(context.Context, uint, Generation) error
	current func(uint, Generation) bool
	logger  *slog.Logger
}

func newAgentDeploymentQueue(s *AgentDeploymentService) *agentDeploymentQueue {
	return &agentDeploymentQueue{
		jobs: make(map[uint]*agentDeploymentJob), live: make(map[uint]bool), wake: make(chan struct{}, 1),
		budget: agentInstallBudget, limit: agentDeploymentConcurrency, retry: 5 * time.Second,
		execute: s.ensureDeployed, logger: s.logger,
		current: func(id uint, generation Generation) bool { return s.ssh.pool.IsCurrent(id, uint64(generation)) },
	}
}

func (s *AgentDeploymentService) ensureDeployed(ctx context.Context, id uint, generation Generation) error {
	lease, err := s.ssh.AcquireLease(ctx, id, LeaseConsumerAgent)
	if err != nil {
		return err
	}
	defer lease.Release()
	if lease.Generation() != generation {
		return errors.Join(sshpool.ErrStaleGeneration, lease.releaseAndWait())
	}
	_, err = s.Collect(ctx, lease)
	return errors.Join(err, lease.releaseAndWait())
}

func (q *agentDeploymentQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *agentDeploymentQueue) start(ctx context.Context) <-chan struct{} {
	q.mu.Lock()
	q.ctx = ctx
	q.mu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); q.run(ctx) }()
	return done
}

func (q *agentDeploymentQueue) sync(ids []uint) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.live = make(map[uint]bool, len(ids))
	for _, id := range ids {
		q.live[id] = true
	}
	for id, job := range q.jobs {
		if !q.live[id] || !q.current(id, job.generation) {
			job.retired = true
			if job.cancel != nil {
				job.cancel()
			}
			if !job.running {
				delete(q.jobs, id)
			}
		}
	}
	q.signal()
}

func (q *agentDeploymentQueue) request(id uint, generation Generation, collectionID ...string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx == nil || q.ctx.Err() != nil || !q.live[id] || !q.current(id, generation) {
		return
	}
	job := q.jobs[id]
	if job != nil && job.generation == generation && !job.retired {
		return
	}
	if job == nil {
		job = &agentDeploymentJob{}
		q.jobs[id] = job
	} else if job.cancel != nil {
		job.cancel()
	}
	job.generation, job.due, job.failures, job.retired = generation, time.Now(), 0, false
	if len(collectionID) > 0 {
		job.collectionID = collectionID[0]
	}
	q.signal()
}

func (q *agentDeploymentQueue) run(ctx context.Context) {
	defer func() {
		q.mu.Lock()
		for _, job := range q.jobs {
			if job.cancel != nil {
				job.cancel()
			}
		}
		q.mu.Unlock()
		q.wg.Wait()
	}()
	for ctx.Err() == nil {
		q.mu.Lock()
		now := time.Now()
		delay := time.Minute
		for id, job := range q.jobs {
			if job.running || job.retired {
				continue
			}
			if !q.current(id, job.generation) {
				delete(q.jobs, id)
				continue
			}
			if q.active >= q.limit {
				continue // worker completion wakes admission; never busy-poll
			}
			if wait := job.due.Sub(now); wait > 0 {
				delay = min(delay, wait)
				continue
			}
			operation, cancel := context.WithTimeout(ctx, q.budget)
			operation = WithCollectionID(operation, job.collectionID)
			job.running, job.cancel = true, cancel
			q.active++
			q.wg.Add(1)
			go q.work(operation, id, job, job.generation)
		}
		q.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case <-q.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (q *agentDeploymentQueue) work(ctx context.Context, id uint, job *agentDeploymentJob, generation Generation) {
	defer q.wg.Done()
	err := q.executeSafely(ctx, id, generation)
	waitAgentDeploymentCleanup(q.ctx, err)
	q.mu.Lock()
	defer q.mu.Unlock()
	job.cancel()
	job.cancel, job.running = nil, false
	q.active--
	if job.retired || !q.live[id] || q.ctx.Err() != nil {
		delete(q.jobs, id)
	} else if job.generation != generation {
		job.due = time.Now() // replacement waits until the previous owner joins
	} else if err == nil || errors.Is(err, sshpool.ErrStaleGeneration) {
		delete(q.jobs, id)
	} else {
		job.failures++
		backoff := min(q.retry*time.Duration(1<<min(job.failures-1, 6)), 5*time.Minute)
		job.due = time.Now().Add(backoff)
		attrs := []slog.Attr{slog.Uint64("server_id", uint64(id)), slog.String("action", "agent.deploy"),
			slog.String("collection_id", CollectionIDFromContext(ctx)),
			slog.Int64("retry_after_ms", backoff.Milliseconds())}
		q.logger.LogAttrs(q.ctx, slog.LevelWarn, "agent deployment retry scheduled", append(attrs, remoteFailureLogAttrs(err)...)...)
	}
	q.signal()
}

// A failed primitive and its lease settlement can each contribute pending
// cleanup to a joined error. Retain admission until every owner has joined;
// errors.As would find only the primitive, before the lease returns its slot.
func waitAgentDeploymentCleanup(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	switch err := err.(type) {
	case *sshCleanupPending:
		select {
		case <-err.done:
		case <-ctx.Done():
		}
	case interface{ Unwrap() []error }:
		for _, child := range err.Unwrap() {
			waitAgentDeploymentCleanup(ctx, child)
		}
	case interface{ Unwrap() error }:
		waitAgentDeploymentCleanup(ctx, err.Unwrap())
	}
}

func (q *agentDeploymentQueue) executeSafely(ctx context.Context, id uint, generation Generation) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("agent deployment panicked")
		}
	}()
	return q.execute(ctx, id, generation)
}
