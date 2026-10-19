package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"gorm.io/gorm"
)

type residentFakeStore struct {
	sync.Mutex
	fail bool
	seen map[string]bool
	node model.AgentNode
}

func (f *residentFakeStore) Auth(ctx context.Context, token string) (model.AgentNode, error) {
	if repository.TokenHash(token) != f.node.TokenHash {
		return model.AgentNode{}, gorm.ErrRecordNotFound
	}
	return f.node, nil
}
func (f *residentFakeStore) Contact(context.Context, uint, time.Time, string, *time.Time, *time.Time) error {
	return nil
}
func (f *residentFakeStore) Ingest(ctx context.Context, n model.AgentNode, s []agentwire.Sample) ([]string, error) {
	f.Lock()
	defer f.Unlock()
	if f.fail {
		return nil, errors.New("database unavailable")
	}
	ack := []string{}
	for _, s := range s {
		f.seen[s.ID] = true
		ack = append(ack, s.ID)
	}
	return ack, nil
}
func monitorFixture() (*ResidentMonitor, *residentFakeStore) {
	token := strings.Repeat("a", 64)
	f := &residentFakeStore{seen: map[string]bool{}, node: model.AgentNode{ServerID: 1, TokenHash: repository.TokenHash(token), Deployment: "ready"}}
	m := NewResidentMonitor(nil, nil, nil, "https://hub", false, 5)
	m.store = f
	return m, f
}
func monitorSample() agentwire.Sample {
	id, _ := agentwire.NewID()
	cpu := 12.0
	return agentwire.Sample{ID: id, BootID: "boot", Time: time.Now().UTC(), CPU: &cpu, Errors: map[string]string{"disk": "timeout"}}
}

func TestResidentPresenceSurvivesDatabaseFailureAndHistoryDoesNotReplaceLatest(t *testing.T) {
	m, f := monitorFixture()
	now := time.Now().UTC()
	m.now = func() time.Time { return now }
	s := monitorSample()
	req := agentwire.Request{Protocol: 2, Version: "test", Kind: "samples", Latest: &s, Samples: []agentwire.Sample{s}}
	f.fail = true
	reply := m.receive(context.Background(), f.node, req, 0)
	if len(reply.Ack) > 0 || !reply.Retry {
		t.Fatal("acknowledged failed write")
	}
	status := m.Statuses(context.Background(), []uint{1})[1]
	if status.Connection != "online" || status.Collection != "partial" || status.Data != "stale" || status.Errors["storage"] != "write_failed" {
		t.Fatalf("conflated presence/storage: %+v", status)
	}
	f.fail = false
	reply = m.receive(context.Background(), f.node, req, 0)
	if len(reply.Ack) != 1 {
		t.Fatal("retry failed")
	}
	old := s
	old.ID, _ = agentwire.NewID()
	old.Time = s.Time.Add(-time.Hour)
	req.Latest = &old
	req.Samples = []agentwire.Sample{old}
	m.receive(context.Background(), f.node, req, 0)
	status = m.Statuses(context.Background(), []uint{1})[1]
	if !status.LastSample.Equal(s.Time) || status.Data != "fresh" {
		t.Fatalf("history replaced current state: %+v", status)
	}
	now = now.Add(time.Minute)
	status = m.Statuses(context.Background(), []uint{1})[1]
	if status.Connection != "unreachable" || status.Data != "stale" {
		t.Fatalf("stale agent still online: %+v", status)
	}
}

func TestResidentWSSReplacementAndCredentialIsolation(t *testing.T) {
	m, f := monitorFixture()
	srv := httptest.NewServer(http.HandlerFunc(m.AgentWebSocket))
	defer srv.Close()
	defer m.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	headers := http.Header{"Authorization": []string{"Bearer " + strings.Repeat("a", 64)}}
	a, _, e := websocket.DefaultDialer.Dial(url, headers)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, _, e := websocket.DefaultDialer.Dial(url, headers)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	s := monitorSample()
	msg := agentwire.Request{Protocol: 2, Version: "test", Kind: "heartbeat", Latest: &s}
	if e = b.WriteJSON(msg); e != nil {
		t.Fatal(e)
	}
	var response agentwire.Response
	if e = b.ReadJSON(&response); e != nil {
		t.Fatal(e)
	}
	_ = a.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = a.ReadMessage(); e == nil {
		t.Fatal("superseded connection was not closed")
	}
	if m.Statuses(context.Background(), []uint{f.node.ServerID})[1].Connection != "online" {
		t.Fatal("old connection cleanup cleared replacement")
	}
	bad := http.Header{"Authorization": []string{"Bearer wrong"}}
	if c, r, e := websocket.DefaultDialer.Dial(url, bad); e == nil {
		_ = c.Close()
		t.Fatal("invalid token accepted")
	} else if r.StatusCode != 401 {
		t.Fatalf("wrong auth status: %d", r.StatusCode)
	}
}

func TestResidentHTTPValidatesBeforeUpdatingPresence(t *testing.T) {
	m, _ := monitorFixture()
	s := monitorSample()
	p := 101.0
	s.CPU = &p
	b, _ := json.Marshal(agentwire.Request{Protocol: 2, Version: "test", Kind: "heartbeat", Latest: &s})
	r := httptest.NewRequest("POST", "/api/v1/agent/report", strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	m.AgentHTTP(w, r)
	if w.Code != 400 || m.Statuses(context.Background(), []uint{1})[1].Connection != "unknown" {
		t.Fatal("invalid report updated presence")
	}
}

func TestResidentClockSkewDoesNotBlockValidBacklogOrRecovery(t *testing.T) {
	m, f := monitorFixture()
	now := time.Now().UTC()
	m.now = func() time.Time { return now }
	good := monitorSample()
	bad := monitorSample()
	bad.Time = now.Add(time.Hour)
	req := agentwire.Request{Protocol: 2, Version: "test", Kind: "samples", Latest: &bad, Samples: []agentwire.Sample{bad, good}}
	if err := req.Validate(now); err != nil {
		t.Fatal(err)
	}
	reply := m.receive(context.Background(), f.node, req, 0)
	if len(reply.Ack) != 1 || reply.Ack[0] != good.ID || len(reply.Rejected) != 1 || reply.Rejected[0] != bad.ID || f.seen[bad.ID] {
		t.Fatalf("clock-skewed sample blocked valid data or was committed: %+v", reply)
	}
	s := m.Statuses(context.Background(), []uint{1})[1]
	if s.Connection != "online" || s.Data != "stale" || s.Errors["clock"] != "clock_skew" {
		t.Fatalf("clock skew hid presence or data failure: %+v", s)
	}
	// Correcting the node clock must recover even though capture time moves back.
	req.Latest = &good
	req.Samples = []agentwire.Sample{good}
	m.receive(context.Background(), f.node, req, 0)
	s = m.Statuses(context.Background(), []uint{1})[1]
	if s.Data != "fresh" || s.Errors["clock"] != "" || !s.LastSample.Equal(good.Time) {
		t.Fatalf("clock correction failed to recover: %+v", s)
	}
}

func TestResidentDeletionAndShutdownRejectAlreadyAuthenticatedRequests(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		m, f := monitorFixture()
		if shutdown {
			m.Close()
		} else {
			m.Disconnect(f.node.ServerID)
		}
		s := monitorSample()
		reply := m.receive(context.Background(), f.node, agentwire.Request{Protocol: 2, Version: "test", Kind: "samples", Latest: &s, Samples: []agentwire.Sample{s}}, 0)
		if !reply.Retry || len(reply.Ack) != 0 || len(f.seen) != 0 || m.Statuses(context.Background(), []uint{1})[1].Connection != "unknown" {
			t.Fatal("late request recreated deleted or stopped monitor")
		}
	}
}
