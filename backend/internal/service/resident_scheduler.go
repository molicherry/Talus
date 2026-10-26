package service

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

const residentMaxInstallJobs = 16
const residentTransferRetryDelay = 30 * time.Second
const residentBusyShortRetries = 5

type residentRetry struct {
	key      string
	failures int
	busy     int
	due      time.Time
}

// Pending jobs are bounded independently of the two resource pools. Admission
// retains per-node ownership until all SSH workers have joined, including when
// removal or a configuration change cancels a queued/staging/activating job.
func (m *ResidentMonitor) scheduleInstall(ctx context.Context, n model.AgentNode, epoch uint64, key string, wg *sync.WaitGroup, run func(context.Context) error) bool {
	m.installJobsMu.Lock()
	defer m.installJobsMu.Unlock()
	if _, busy := m.installJobs[n.ServerID]; busy || m.removing[n.ServerID] != nil || epoch != m.installEpoch || len(m.installJobs) >= residentMaxInstallJobs || ctx.Err() != nil {
		return false
	}
	retry := m.installRetries[n.ServerID]
	if retry.key == key {
		if m.now().Before(retry.due) {
			return false
		}
	} else if retry.key == "" && n.LastAttempt != nil && m.now().Sub(*n.LastAttempt) < time.Minute {
		// After a restart there is no in-memory outcome; retain the persisted
		// minimum. A known progressing/busy pull can retry after 30s instead.
		return false
	}
	jobCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	m.installJobs[n.ServerID] = residentJob{cancel: stop, hash: n.TokenHash, done: done}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stop()
		err := run(jobCtx)
		m.installJobsMu.Lock()
		if err == nil || errors.Is(err, context.Canceled) || epoch != m.installEpoch || m.removing[n.ServerID] != nil {
			delete(m.installRetries, n.ServerID)
		} else {
			if retry.key != key {
				retry = residentRetry{key: key}
			}
			delay := residentTransferRetryDelay
			switch {
			case errors.Is(err, errArtifactPullProgress):
				// New bytes break the capacity-only streak, even on a slow link.
				retry.busy = 0
			case errors.Is(err, errArtifactPullBusy):
				retry.busy++
				if retry.busy > residentBusyShortRetries {
					delay = residentRetryDelay(retry.busy - residentBusyShortRetries)
				}
			default:
				retry.busy = 0
				retry.failures++
				delay = residentRetryDelay(retry.failures)
			}
			retry.due = m.now().Add(delay)
			m.installRetries[n.ServerID] = retry
			slog.Info("resident agent retry scheduled", "server_id", n.ServerID, "failures", retry.failures, "busy_attempts", retry.busy, "retry_after_ms", retry.due.Sub(m.now()).Milliseconds())
		}
		delete(m.installJobs, n.ServerID)
		close(done)
		m.installJobsMu.Unlock()
		m.NotifyTopologyChange()
	}()
	return true
}

func residentRetryDelay(failures int) time.Duration {
	base := min(time.Minute*time.Duration(1<<min(max(failures-1, 0), 4)), 10*time.Minute)
	return base + time.Duration(rand.Int64N(int64(base/2)+1))
}

type residentStagesKey struct{}
type residentStages struct {
	monitor              *ResidentMonitor
	lease                *sshLease
	transfer, activation bool
}

func (m *ResidentMonitor) beginInstall(ctx context.Context) (context.Context, *residentStages, error) {
	select {
	case m.transferSlots <- struct{}{}:
		s := &residentStages{monitor: m, transfer: true}
		return context.WithValue(ctx, residentStagesKey{}, s), s, nil
	case <-ctx.Done():
		return ctx, nil, ctx.Err()
	}
}

func (s *residentStages) finish() {
	if s.lease != nil {
		s.lease.Release()
		<-s.lease.Done()
	}
	if s.transfer {
		<-s.monitor.transferSlots
		s.transfer = false
	}
	if s.activation {
		<-s.monitor.activationSlots
		s.activation = false
	}
}

func (m *ResidentMonitor) beginActivation(ctx context.Context) error {
	s, _ := ctx.Value(residentStagesKey{}).(*residentStages)
	if s == nil {
		return errors.New("installation stage ownership missing")
	}
	if s.transfer {
		<-m.transferSlots
		s.transfer = false
	}
	select {
	case m.activationSlots <- struct{}{}:
		s.activation = true
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
