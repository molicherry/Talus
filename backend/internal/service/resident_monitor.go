package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/telemetry"
	"gorm.io/gorm"
)

type residentStore interface {
	Auth(context.Context, string) (model.AgentNode, error)
	Ingest(context.Context, model.AgentNode, []agentwire.Sample) ([]string, error)
	Contact(context.Context, uint, time.Time, string, *time.Time, *time.Time) error
}

type residentRuntime struct {
	node          model.AgentNode
	lastContact   time.Time
	latest        *agentwire.Sample
	lastPersisted *time.Time
	storedSample  *time.Time
	version       string
	pending       int
	dropped       uint64
	connected     bool
	close         func()
	generation    uint64
	writeFailed   bool
}

// ResidentMonitor owns authenticated presence independently of metric writes.
// Its bounded writer pool prevents a reconnect/backfill burst exhausting SQL.
type ResidentMonitor struct {
	store      residentStore
	repo       *repository.AgentNodeRepo
	servers    *repository.ServerRepo
	deployer   *AgentDeploymentService
	hubURL     string
	allowHTTP  bool
	interval   int
	mu         sync.Mutex
	nodes      map[uint]*residentRuntime
	writeSlots chan struct{}
	wake       chan struct{}
	now        func() time.Time
	telemetry  *monitorTelemetry
	closed     bool
	revoked    map[uint]time.Time
	// installJobs tracks in-flight resident installations so deletion can
	// cancel a job before it races an uninstall.
	installJobs   map[uint]residentJob
	installJobsMu sync.Mutex
	removing      map[uint]chan struct{}
	installEpoch  uint64
}

func NewResidentMonitor(repo *repository.AgentNodeRepo, servers *repository.ServerRepo, deployer *AgentDeploymentService, url string, allowHTTP bool, interval int) *ResidentMonitor {
	return &ResidentMonitor{store: repo, repo: repo, servers: servers, deployer: deployer, hubURL: url, allowHTTP: allowHTTP, interval: interval, nodes: map[uint]*residentRuntime{}, writeSlots: make(chan struct{}, 8), wake: make(chan struct{}, 1), now: time.Now, telemetry: newMonitorTelemetry(nil, nil), revoked: map[uint]time.Time{}, installJobs: map[uint]residentJob{}, removing: map[uint]chan struct{}{}}
}

func (m *ResidentMonitor) SetTelemetry(r *telemetry.Registry) {
	m.telemetry = newMonitorTelemetry(r, nil)
	if m.deployer != nil {
		m.deployer.SetTelemetry(r, slog.Default())
	}
}

func (m *ResidentMonitor) NotifyTopologyChange() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *ResidentMonitor) authenticate(ctx context.Context, r *http.Request) (model.AgentNode, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return model.AgentNode{}, errors.New("monitor is stopping")
	}
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return model.AgentNode{}, gorm.ErrRecordNotFound
	}
	return m.store.Auth(ctx, strings.TrimPrefix(value, "Bearer "))
}

func (m *ResidentMonitor) AgentHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	n, err := m.authenticate(ctx, r)
	if err != nil {
		agentAuthError(w, err)
		return
	}
	var req agentwire.Request
	r.Body = http.MaxBytesReader(w, r.Body, agentwire.MaxFrame)
	dec := json.NewDecoder(r.Body)
	if err = dec.Decode(&req); err != nil || req.Validate(m.now()) != nil {
		http.Error(w, "invalid agent message", http.StatusBadRequest)
		return
	}
	reply := m.receive(ctx, n, req, 0)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func agentAuthError(w http.ResponseWriter, err error) {
	code := http.StatusServiceUnavailable
	if errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "invalid credential") {
		code = http.StatusUnauthorized
	}
	http.Error(w, http.StatusText(code), code)
}

func (m *ResidentMonitor) AgentWebSocket(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	n, err := m.authenticate(ctx, r)
	cancel()
	if err != nil {
		agentAuthError(w, err)
		return
	}
	// Agent-only endpoint: browser origins are never needed, and credentials are
	// supplied in an Authorization header rather than URLs or subprotocols.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	u := websocket.Upgrader{HandshakeTimeout: 5 * time.Second}
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Time{})
	conn.SetReadLimit(agentwire.MaxFrame)
	m.mu.Lock()
	if m.closed || m.now().Before(m.revoked[n.ServerID]) {
		m.mu.Unlock()
		return
	}
	v := m.runtimeLocked(n)
	oldClose := v.close
	v.generation++
	generation := v.generation
	v.close = func() { _ = conn.Close() }
	v.connected = true
	m.mu.Unlock()
	if oldClose != nil {
		oldClose()
	}
	defer func() {
		m.mu.Lock()
		if v := m.nodes[n.ServerID]; v != nil && v.generation == generation {
			v.connected = false
			v.close = nil
		}
		m.mu.Unlock()
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		var req agentwire.Request
		if err = conn.ReadJSON(&req); err != nil {
			return
		}
		if req.Validate(m.now()) != nil {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid agent message"), time.Now().Add(time.Second))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		reply := m.receive(ctx, n, req, generation)
		cancel()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err = conn.WriteJSON(reply); err != nil {
			return
		}
	}
}

func (m *ResidentMonitor) runtimeLocked(n model.AgentNode) *residentRuntime {
	v := m.nodes[n.ServerID]
	if v == nil {
		v = &residentRuntime{node: n, lastPersisted: n.LastPersisted}
		m.nodes[n.ServerID] = v
	}
	return v
}

func (m *ResidentMonitor) receive(ctx context.Context, n model.AgentNode, req agentwire.Request, generation uint64) agentwire.Response {
	now := m.now().UTC()
	reply := agentwire.Response{ServerTime: now}
	m.mu.Lock()
	if m.closed || m.now().Before(m.revoked[n.ServerID]) {
		m.mu.Unlock()
		reply.Retry = true
		return reply
	}
	v := m.runtimeLocked(n)
	if v.node.TokenHash != n.TokenHash || (generation != 0 && v.generation != generation) {
		m.mu.Unlock()
		reply.Retry = true
		return reply
	}
	v.lastContact = now
	v.version = req.Version
	v.pending = req.Pending
	v.dropped = req.Dropped
	if generation == 0 && v.close == nil {
		v.connected = true
	}
	if req.Latest != nil && (v.latest == nil || errors.Is(v.latest.Validate(now), agentwire.ErrSampleTime) || req.Latest.Time.After(v.latest.Time)) {
		copy := *req.Latest
		if errors.Is(copy.Validate(now), agentwire.ErrSampleTime) {
			copy.Errors = map[string]string{}
			for k, e := range req.Latest.Errors {
				copy.Errors[k] = e
			}
			copy.Errors["clock"] = "clock_skew"
		}
		v.latest = &copy
	}
	m.mu.Unlock()
	if req.Kind == "heartbeat" {
		return reply
	}
	valid := make([]agentwire.Sample, 0, len(req.Samples))
	for _, s := range req.Samples {
		if errors.Is(s.Validate(now), agentwire.ErrSampleTime) {
			reply.Rejected = append(reply.Rejected, s.ID)
		} else {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return reply
	}
	started := time.Now()
	outcome, reason := telOutcomeSuccess, telReasonNone
	defer func() {
		m.telemetry.collection(collectionEvent{CollectionID: m.telemetry.nextCollectionID(n.ServerID), ServerID: n.ServerID, Outcome: outcome, ReasonClass: reason, Duration: time.Since(started)})
	}()
	select {
	case m.writeSlots <- struct{}{}:
		defer func() { <-m.writeSlots }()
	case <-ctx.Done():
		outcome, reason = telOutcomeTimeout, telReasonTimeout
		reply.Retry = true
		return reply
	default:
		outcome, reason = telOutcomeSkipped, telReasonQuota
		reply.Retry = true
		return reply
	}
	ack, err := m.store.Ingest(ctx, n, valid)
	m.mu.Lock()
	defer m.mu.Unlock()
	v = m.nodes[n.ServerID]
	if v == nil || v.node.TokenHash != n.TokenHash {
		return reply
	}
	if err != nil {
		outcome, reason = telOutcomeFailure, telReasonDB
		v.writeFailed = true
		reply.Retry = true
		slog.Warn("resident metrics write deferred", "server_id", n.ServerID)
		return reply
	}
	reply.Ack = ack
	v.writeFailed = false
	v.lastPersisted = &now
	for _, s := range valid {
		if v.storedSample == nil || s.Time.After(*v.storedSample) {
			at := s.Time
			v.storedSample = &at
		}
	}
	return reply
}

func (m *ResidentMonitor) Statuses(ctx context.Context, ids []uint) map[uint]*model.MonitoringStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make(map[uint]*model.MonitoringStatus, len(ids))
	for _, id := range ids {
		s := &model.MonitoringStatus{Connection: "unknown", Collection: "unknown", Data: "stale", Deployment: "pending"}
		out[id] = s
		if m.hubURL == "" {
			s.Deployment = "setup_required"
			s.DeploymentError = "hub_url_required"
		}
		v := m.nodes[id]
		if v == nil {
			continue
		}
		if m.hubURL != "" {
			s.Deployment = v.node.Deployment
			s.DeploymentError = v.node.DeployError
		}
		s.Version = v.version
		s.Pending = v.pending
		s.Dropped = v.dropped
		s.LastPersisted = v.lastPersisted
		if !v.lastContact.IsZero() {
			at := v.lastContact
			s.LastContact = &at
			s.Connection = "unreachable"
			if now.Sub(v.lastContact) <= 45*time.Second {
				s.Connection = "reconnecting"
				if v.connected {
					s.Connection = "online"
				}
			}
		} else if v.node.LastContact != nil {
			s.LastContact = v.node.LastContact
			s.Connection = "reconnecting"
			if now.Sub(*v.node.LastContact) > 45*time.Second {
				s.Connection = "unreachable"
			}
		}
		if v.latest != nil {
			at := v.latest.Time
			s.LastSample = &at
			s.Errors = v.latest.Errors
			s.Collection = "healthy"
			if len(s.Errors) > 0 {
				s.Collection = "partial"
			}
			if v.latest.CPU == nil && v.latest.MemoryUsed == nil && v.latest.DiskUsed == nil && v.latest.NetRecv == nil && v.latest.Load1 == nil {
				s.Collection = "failed"
			}
			freshFor := time.Duration(max(3*m.interval, 15)) * time.Second
			if v.latest.Validate(now) == nil && v.storedSample != nil && now.Sub(*v.storedSample) <= freshFor && !v.writeFailed {
				s.Data = "fresh"
			}
		}
		if s.Pending > 1 && s.Data == "fresh" {
			s.Data = "backfilling"
		}
		if v.writeFailed {
			if s.Errors == nil {
				s.Errors = map[string]string{}
			} else {
				copy := map[string]string{}
				for k, e := range s.Errors {
					copy[k] = e
				}
				s.Errors = copy
			}
			s.Errors["storage"] = "write_failed"
		}
	}
	return out
}

func (m *ResidentMonitor) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, v := range m.nodes {
		if v.close != nil {
			v.close()
			v.close = nil
		}
		v.connected = false
	}
}

func (m *ResidentMonitor) Disconnect(id uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, until := range m.revoked {
		if !m.now().Before(until) {
			delete(m.revoked, id)
		}
	}
	m.revoked[id] = m.now().Add(time.Minute)
	if v := m.nodes[id]; v != nil {
		if v.close != nil {
			v.close()
		}
		delete(m.nodes, id)
	}
}
