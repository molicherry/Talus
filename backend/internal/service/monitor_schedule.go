package service

import (
	"math/rand"
	"sort"
	"sync"
	"time"
)

// The frozen scheduling numbers live here (REQUIREMENTS §8.1.1): a global
// in-flight cap of min(N,16), one in-flight task plus at most one queued record
// per node, a 30s deadline after admission, and an exponential backoff with
// ±10% jitter clamped to [I, max(I,300s)].
const (
	monitorMaxInFlight = 16
	monitorTaskBudget  = 30 * time.Second
	monitorBackoffCap  = 300 * time.Second
	monitorJitter      = 0.1
	// monitorWarnAfter is the consecutive count that escalates to a WARN
	// (REQUIREMENTS §12.3): repeated collection failures, or repeated deferred
	// admissions (capacity/re-entrancy skips).
	monitorWarnAfter = 3
)

// monitorClock abstracts time so the scheduler can be driven by a controllable
// clock in tests.
type monitorClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realMonitorClock struct{}

func (realMonitorClock) Now() time.Time                         { return time.Now() }
func (realMonitorClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// monitorRand yields a uniform value in [0,1). Tests inject a fixed source so
// backoff assertions are deterministic.
type monitorRand interface{ Float64() float64 }

type seededMonitorRand struct{ r *rand.Rand }

func newSeededMonitorRand(seed int64) *seededMonitorRand {
	return &seededMonitorRand{r: rand.New(rand.NewSource(seed))}
}

func (s *seededMonitorRand) Float64() float64 { return s.r.Float64() }

// monitorOutcome classifies a finished task. Only outcomeFailure advances the
// consecutive-failure counter: skips, shutdown, node deletion and generation
// invalidation never inflate it (§8.1.1).
type monitorOutcome int

const (
	outcomeSuccess monitorOutcome = iota
	outcomeFailure
	outcomeSkipped
	outcomeCancelled
)

// monitorNode is the per-server scheduling state.
type monitorNode struct {
	id       uint
	failures int
	due      time.Time
	inFlight bool
	// queued records one pending slot: a node whose tick was skipped for
	// capacity is admitted as soon as capacity allows, without waiting for the
	// next interval and without stacking further records.
	queued   bool
	queuedAt time.Time
	// defers counts consecutive deferred admissions; failureWarned/deferWarned
	// keep the escalation to exactly one WARN per streak (§12.3).
	defers        int
	failureWarned bool
	deferWarned   bool
}

// monitorCompletion reports the state transition of one finished task so the
// caller can emit exactly-once escalation and recovery events.
type monitorCompletion struct {
	Failures     int
	WarnDegraded bool
	Recovered    bool
}

// monitorTick is one scheduler pass, reported for telemetry.
type monitorTick struct {
	Admitted []uint
	Queued   []uint
	Skipped  []uint
	// WarnDeferred lists nodes that just reached the deferred-admission warning
	// threshold; RecoveredDefer lists nodes admitted again after such a streak.
	WarnDeferred   []uint
	RecoveredDefer []uint
}

// monitorScheduler owns the pure scheduling state. It never blocks and never
// spawns work, which keeps TC-08-01's assertions deterministic.
type monitorScheduler struct {
	// mu serializes every state transition: the run loop plans while worker
	// goroutines report completion, so the state machine must be shared safely.
	mu          sync.Mutex
	interval    time.Duration
	maxInFlight int
	clock       monitorClock
	rng         monitorRand
	nodes       map[uint]*monitorNode
	order       []uint
	cursor      int
}

func newMonitorScheduler(interval time.Duration, clock monitorClock, rng monitorRand) *monitorScheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if clock == nil {
		clock = realMonitorClock{}
	}
	if rng == nil {
		rng = newSeededMonitorRand(time.Now().UnixNano())
	}
	return &monitorScheduler{
		interval:    interval,
		maxInFlight: monitorMaxInFlight,
		clock:       clock,
		rng:         rng,
		nodes:       map[uint]*monitorNode{},
	}
}

// capacity is the global in-flight limit for the current node count: min(N,16),
// and zero when nothing is managed.
func (s *monitorScheduler) capacity() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capacityLocked()
}

// capacityLocked requires the caller to hold s.mu.
func (s *monitorScheduler) capacityLocked() int {
	n := len(s.nodes)
	if n > s.maxInFlight {
		n = s.maxInFlight
	}
	return n
}

// sync reconciles the scheduler with the live node list. New nodes become due
// immediately; removed nodes are returned so the caller can cancel their tasks
// and drop their state without counting a failure.
func (s *monitorScheduler) sync(live []uint, now time.Time) (removed []uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[uint]bool, len(live))
	for _, id := range live {
		seen[id] = true
		if _, ok := s.nodes[id]; ok {
			continue
		}
		s.nodes[id] = &monitorNode{id: id, due: now}
		s.order = append(s.order, id)
	}
	sort.Slice(s.order, func(i, j int) bool { return s.order[i] < s.order[j] })

	kept := s.order[:0]
	for _, id := range s.order {
		if seen[id] {
			kept = append(kept, id)
			continue
		}
		removed = append(removed, id)
		delete(s.nodes, id)
	}
	s.order = kept
	if s.cursor >= len(s.order) {
		s.cursor = 0
	}
	return removed
}

// plan admits eligible nodes in a fair rotation and marks the state in place.
func (s *monitorScheduler) plan(now time.Time) monitorTick {
	s.mu.Lock()
	defer s.mu.Unlock()
	tick := monitorTick{}
	if len(s.order) == 0 {
		return tick
	}
	inFlight := s.inFlightLocked()
	capacity := s.capacityLocked()
	checked := 0
	for checked < len(s.order) {
		id := s.order[s.cursor%len(s.order)]
		s.cursor = (s.cursor + 1) % len(s.order)
		checked++
		node := s.nodes[id]
		if node == nil {
			continue
		}
		if node.inFlight {
			tick.Skipped = append(tick.Skipped, id)
			s.noteDeferLocked(node, &tick, now)
			continue
		}
		if !s.dueLocked(node, now) {
			continue
		}
		if inFlight >= capacity {
			if !node.queued {
				node.queued, node.queuedAt = true, now
			}
			tick.Queued = append(tick.Queued, id)
			s.noteDeferLocked(node, &tick, now)
			continue
		}
		node.inFlight, node.queued = true, false
		if node.deferWarned {
			tick.RecoveredDefer = append(tick.RecoveredDefer, id)
		}
		node.defers, node.deferWarned = 0, false
		inFlight++
		tick.Admitted = append(tick.Admitted, id)
	}
	return tick
}

// noteDeferLocked counts one missed scheduling slot and escalates once per
// streak. A node whose slot has not been passed yet (for example an in-flight
// collection that is still inside its interval) is not a missed collection, and
// counting per scheduler pass would inflate the streak during a burst of wake-ups.
func (s *monitorScheduler) noteDeferLocked(node *monitorNode, tick *monitorTick, now time.Time) {
	if node.due.After(now) {
		return
	}
	node.defers++
	// Advance the missed slot so the same interval is counted exactly once; the
	// next attempt is the following slot.
	node.due = node.due.Add(s.interval)
	if node.defers >= monitorWarnAfter && !node.deferWarned {
		node.deferWarned = true
		tick.WarnDeferred = append(tick.WarnDeferred, node.id)
	}
}

// dueLocked requires the caller to hold s.mu.
func (s *monitorScheduler) dueLocked(node *monitorNode, now time.Time) bool {
	if node.queued {
		return true
	}
	return !node.due.After(now)
}

// complete records a finished task and returns the node's next due time.
func (s *monitorScheduler) complete(id uint, outcome monitorOutcome, now time.Time) monitorCompletion {
	s.mu.Lock()
	defer s.mu.Unlock()
	node := s.nodes[id]
	// Only the owner of an admitted task reports completion: anything else (a
	// queued node, a node whose task was already accounted for) must not have its
	// pending slot or schedule rewritten.
	if node == nil || !node.inFlight {
		return monitorCompletion{}
	}
	node.inFlight = false
	result := monitorCompletion{}
	switch outcome {
	case outcomeSuccess:
		// Success resets the counter and resumes the node's normal future
		// schedule; missed ticks are not replayed.
		if node.failureWarned {
			result.Recovered = true
			node.failureWarned = false
		}
		node.failures = 0
		node.due = s.nextAlignedLocked(node.due, now)
		node.queued = false
	case outcomeFailure:
		node.failures++
		if node.failures >= monitorWarnAfter && !node.failureWarned {
			result.WarnDegraded = true
			node.failureWarned = true
		}
		node.due = now.Add(s.backoffDelayLocked(node.failures))
		node.queued = false
	default:
		// Skipped/cancelled work never advances the failure counter. A skipped
		// node keeps its queued slot so it is admitted when capacity frees up.
		if !node.queued {
			node.due = s.nextAlignedLocked(node.due, now)
		}
	}
	result.Failures = node.failures
	return result
}

// nextAligned advances a node's phase to the first schedule slot after now so a
// slow or failed round never triggers catch-up bursts.
// nextAlignedLocked requires the caller to hold s.mu.
func (s *monitorScheduler) nextAlignedLocked(previous, now time.Time) time.Time {
	next := previous
	if next.IsZero() || !next.After(now) {
		steps := now.Sub(next)/s.interval + 1
		if next.IsZero() {
			steps = 1
		}
		next = next.Add(steps * s.interval)
		for !next.After(now) {
			next = next.Add(s.interval)
		}
	}
	return next
}

// backoffDelay implements base=min(I×2^(k−1), cap) with uniform ±10% jitter
// clamped to [I, cap], cap=max(I,300s). The exponential uses saturating steps so
// large failure counts cannot overflow.
func (s *monitorScheduler) backoffDelay(failures int) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backoffDelayLocked(failures)
}

// backoffDelayLocked requires the caller to hold s.mu.
func (s *monitorScheduler) backoffDelayLocked(failures int) time.Duration {
	cap := s.interval
	if cap < monitorBackoffCap {
		cap = monitorBackoffCap
	}
	base := s.interval
	for i := 1; i < failures; i++ {
		if base >= cap/2 {
			base = cap
			break
		}
		base *= 2
	}
	if base > cap {
		base = cap
	}
	jitter := (s.rng.Float64()*2 - 1) * monitorJitter
	delay := time.Duration(float64(base) * (1 + jitter))
	if delay < s.interval {
		delay = s.interval
	}
	if delay > cap {
		delay = cap
	}
	return delay
}

// hasAdmissible reports whether a pass right now could admit work: a due or
// already-queued node exists and capacity is free. In-flight nodes alone never
// make the caller wake immediately — they signal completion through the notify
// channel, and a zero-delay wake-up would spin (R01).
func (s *monitorScheduler) hasAdmissible(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlightLocked() >= s.capacityLocked() {
		return false
	}
	for _, id := range s.order {
		node := s.nodes[id]
		if node == nil || node.inFlight {
			continue
		}
		if node.queued || !node.due.After(now) {
			return true
		}
	}
	return false
}

// earliestDue is the next wake-up time, or zero when a node is already due or
// queued (the caller should run another pass immediately).
func (s *monitorScheduler) earliestDue(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A due node may only be admitted while capacity is free. Otherwise an
	// in-flight or queued node must not make the caller wake immediately: it is
	// already running and will signal completion through the notify channel.
	if s.inFlightLocked() < s.capacityLocked() {
		for _, id := range s.order {
			node := s.nodes[id]
			if node == nil || node.inFlight || node.queued {
				continue
			}
			if !node.due.After(now) {
				return time.Time{}
			}
		}
	}
	var earliest time.Time
	for _, id := range s.order {
		node := s.nodes[id]
		if node == nil || node.inFlight || node.queued {
			continue
		}
		if !node.due.After(now) {
			// Blocked by capacity: wait for a completion or for the discovery cap.
			continue
		}
		if earliest.IsZero() || node.due.Before(earliest) {
			earliest = node.due
		}
	}
	return earliest
}

func (s *monitorScheduler) inFlightCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlightLocked()
}

// inFlightLocked requires the caller to hold s.mu.
func (s *monitorScheduler) inFlightLocked() int {
	count := 0
	for _, node := range s.nodes {
		if node.inFlight {
			count++
		}
	}
	return count
}

// queuedCount reports node states for assertions and telemetry: pending records
// never exceed the node count.
func (s *monitorScheduler) queuedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, node := range s.nodes {
		if node.queued {
			count++
		}
	}
	return count
}

func (s *monitorScheduler) failures(id uint) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.nodes[id]; ok {
		return node.failures
	}
	return 0
}
