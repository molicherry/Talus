package agentclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/agentwire"
)

type queueEntry struct {
	sample agentwire.Sample
	size   int64
}

// Spool is a bounded, crash-persistent outbox. Only ACKed or explicitly expired
// samples are removed. The process lock also excludes two installed agents.
type Spool struct {
	mu      sync.Mutex
	dir     string
	lock    *os.File
	entries []queueEntry
	bytes   int64
	dropped uint64
	limit   int64
	age     time.Duration
	now     func() time.Time
}

func OpenSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockStateFile(f); err != nil {
		_ = f.Close()
		return nil, errors.New("another agent owns this state directory")
	}
	q := &Spool{dir: dir, lock: f, limit: agentwire.MaxQueueBytes, age: agentwire.MaxAge, now: time.Now}
	fail := func(err error) (*Spool, error) { _ = q.Close(); return nil, err }
	if b, e := os.ReadFile(filepath.Join(dir, "dropped.json")); e == nil {
		if err = json.Unmarshal(b, &q.dropped); err != nil {
			return fail(fmt.Errorf("read dropped count: %w", err))
		}
	} else if !os.IsNotExist(e) {
		return fail(e)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return fail(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".pending-") {
			if err := os.Remove(filepath.Join(dir, f.Name())); err != nil {
				return fail(err)
			}
			continue
		}
		if !strings.HasSuffix(f.Name(), ".sample") {
			continue
		}
		if f.Type()&os.ModeSymlink != 0 {
			return fail(errors.New("symlink in agent outbox"))
		}
		path := filepath.Join(dir, f.Name())
		file, e := os.Open(path)
		if e != nil {
			return fail(e)
		}
		b, e := readLimited(file, 32<<10)
		_ = file.Close()
		if e != nil {
			return fail(e)
		}
		var s agentwire.Sample
		if len(b) > 32<<10 || json.Unmarshal(b, &s) != nil || len(s.ID) != 32 || s.ID+".sample" != f.Name() {
			return fail(fmt.Errorf("corrupt outbox sample %s", f.Name()))
		}
		q.entries = append(q.entries, queueEntry{s, int64(len(b))})
		q.bytes += int64(len(b))
	}
	sort.Slice(q.entries, func(i, j int) bool { return q.entries[i].sample.Time.Before(q.entries[j].sample.Time) })
	if err = q.pruneLocked(); err != nil {
		return fail(err)
	}
	return q, nil
}

func (q *Spool) Close() error {
	if q.lock == nil {
		return nil
	}
	err := q.lock.Close()
	q.lock = nil
	return err
}

func writeAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = replaceStateFile(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (q *Spool) Add(s agentwire.Sample) error {
	if err := s.Validate(q.now()); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) > 32<<10 {
		return errors.New("sample exceeds outbox limit")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.entries {
		if e.sample.ID == s.ID {
			return errors.New("duplicate local sample")
		}
	}
	if err = writeAtomic(filepath.Join(q.dir, s.ID+".sample"), b); err != nil {
		return err
	}
	if s.Errors != nil {
		copy := make(map[string]string, len(s.Errors))
		for k, v := range s.Errors {
			copy[k] = v
		}
		s.Errors = copy
	}
	q.entries = append(q.entries, queueEntry{s, int64(len(b))})
	q.bytes += int64(len(b))
	sort.Slice(q.entries, func(i, j int) bool { return q.entries[i].sample.Time.Before(q.entries[j].sample.Time) })
	return q.pruneLocked()
}

func (q *Spool) pruneLocked() error {
	cutoff := q.now().Add(-q.age)
	for len(q.entries) > 0 && (q.bytes > q.limit || q.entries[0].sample.Time.Before(cutoff)) {
		// Persist the loss counter first: a crash may over-count a loss, but may
		// never silently erase an unacknowledged sample.
		count := q.dropped + 1
		b, _ := json.Marshal(count)
		if err := writeAtomic(filepath.Join(q.dir, "dropped.json"), b); err != nil {
			return err
		}
		q.dropped = count
		e := q.entries[0]
		if err := os.Remove(filepath.Join(q.dir, e.sample.ID+".sample")); err != nil && !os.IsNotExist(err) {
			return err
		}
		q.bytes -= e.size
		q.entries = q.entries[1:]
	}
	return syncDirectory(q.dir)
}

// Batch sends the newest sample first, then drains the oldest backlog. Recovery
// immediately refreshes the live dashboard while historical gaps are filled.
func (q *Spool) Batch() ([]agentwire.Sample, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.pruneLocked(); err != nil {
		return nil, err
	}
	n := len(q.entries)
	if n == 0 {
		return nil, nil
	}
	out := []agentwire.Sample{q.entries[n-1].sample}
	for i := 0; i < n-1 && len(out) < agentwire.MaxBatch; i++ {
		out = append(out, q.entries[i].sample)
	}
	return out, nil
}

func (q *Spool) Ack(ids []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.ackLocked(ids)
}

// Reject records permanently invalid capture times separately from committed
// ACKs. They must not block fresh observations behind a clock-skewed sample.
func (q *Spool) Reject(ids []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	var count uint64
	for _, e := range q.entries {
		if set[e.sample.ID] {
			count++
		}
	}
	if count > 0 {
		value := q.dropped + count
		b, _ := json.Marshal(value)
		if err := writeAtomic(filepath.Join(q.dir, "dropped.json"), b); err != nil {
			return err
		}
		q.dropped = value
	}
	return q.ackLocked(ids)
}

func (q *Spool) ackLocked(ids []string) error {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	kept := make([]queueEntry, 0, len(q.entries))
	var firstErr error
	for _, e := range q.entries {
		if !set[e.sample.ID] {
			kept = append(kept, e)
			continue
		}
		if err := os.Remove(filepath.Join(q.dir, e.sample.ID+".sample")); err != nil && !os.IsNotExist(err) {
			kept = append(kept, e)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		q.bytes -= e.size
	}
	q.entries = kept
	if err := syncDirectory(q.dir); firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (q *Spool) Stats() (int, uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries), q.dropped
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("message too large")
	}
	return b, err
}
