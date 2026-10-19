package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/agentwire"
)

func TestHTTPFallbackLostACKReplayAndWSSRecovery(t *testing.T) {
	q, e := OpenSpool(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	s := sampleAt(t, time.Now())
	if e = q.Add(s); e != nil {
		t.Fatal(e)
	}
	var latest currentSample
	latest.set(s)
	var mu sync.Mutex
	seen := map[string]int{}
	dropACK := true
	allowWS := false
	wsSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.URL.RawQuery != "" {
			t.Error("credential missing or leaked into URL")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		wsAllowed := allowWS
		mu.Unlock()
		if r.URL.Path == "/api/v1/agent/connect" {
			if !wsAllowed {
				http.NotFound(w, r)
				return
			}
			u := websocket.Upgrader{}
			c, e := u.Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer c.Close()
			mu.Lock()
			wsSeen = true
			mu.Unlock()
			for {
				var req agentwire.Request
				if c.ReadJSON(&req) != nil {
					return
				}
				_ = c.WriteJSON(agentwire.Response{ServerTime: time.Now()})
			}
		}
		var req agentwire.Request
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad body", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		reply := agentwire.Response{ServerTime: time.Now()}
		for _, s := range req.Samples {
			seen[s.ID]++
			reply.Ack = append(reply.Ack, s.ID)
		}
		if len(req.Samples) > 0 && dropACK {
			dropACK = false
			http.Error(w, "simulated lost ACK", http.StatusServiceUnavailable)
			return
		}
		if len(reply.Ack) > 0 {
			allowWS = true
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTransport(ctx, agentwire.Config{HubURL: server.URL, Token: strings.Repeat("a", 64), StateDir: q.dir, IntervalSeconds: 5, AllowHTTP: true}, "test", q, &latest)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ws := wsSeen
		attempts := seen[s.ID]
		mu.Unlock()
		count, _ := q.Stats()
		if ws && count == 0 {
			if attempts < 2 {
				t.Fatal("sample not replayed after lost ACK")
			}
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("HTTP fallback did not replay and recover WebSocket")
}
