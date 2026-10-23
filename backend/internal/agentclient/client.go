package agentclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/agentwire"
)

type healthRecord struct {
	Version string    `json:"version"`
	Config  string    `json:"config"`
	At      time.Time `json:"at"`
}

func configHash(c agentwire.Config) string {
	b, _ := json.Marshal(c)
	v := sha256.Sum256(b)
	return hex.EncodeToString(v[:])
}

// Command handles resident-agent modes while preserving the original one-shot
// command as the build/protocol compatibility probe.
func Command(args []string, version string) (bool, error) {
	if len(args) == 2 && args[0] == "--agent-collect" {
		r, e := CollectSection(args[1])
		if e != nil {
			return true, e
		}
		return true, json.NewEncoder(os.Stdout).Encode(r)
	}
	if len(args) == 0 || (args[0] != "--daemon" && args[0] != "--health") {
		return false, nil
	}
	f := flag.NewFlagSet("resident-agent", flag.ContinueOnError)
	daemon := f.Bool("daemon", false, "")
	health := f.Bool("health", false, "")
	configPath := f.String("config", "", "")
	serviceName := f.String("service-name", "", "Windows SCM service name")
	if e := f.Parse(args); e != nil {
		return true, e
	}
	b, e := os.ReadFile(*configPath)
	if e != nil {
		return true, e
	}
	var c agentwire.Config
	if e = json.Unmarshal(b, &c); e != nil {
		return true, e
	}
	if e = c.Validate(); e != nil {
		return true, e
	}
	if *health {
		var h healthRecord
		b, e = os.ReadFile(filepath.Join(c.StateDir, "health.json"))
		if e != nil {
			return true, e
		}
		if e = json.Unmarshal(b, &h); e != nil {
			return true, e
		}
		if h.Version != version || h.Config != configHash(c) || time.Since(h.At) > 30*time.Second || h.At.After(time.Now().Add(time.Minute)) {
			return true, errors.New("agent has not contacted Hub with this version and configuration")
		}
		return true, nil
	}
	if !*daemon {
		return true, errors.New("daemon mode required")
	}
	return true, runDaemon(c, version, *serviceName)
}

type currentSample struct {
	sync.Mutex
	sample *agentwire.Sample
}

func (s *currentSample) set(v agentwire.Sample) { s.Lock(); defer s.Unlock(); s.sample = &v }
func (s *currentSample) get() *agentwire.Sample { s.Lock(); defer s.Unlock(); return s.sample }

func Run(ctx context.Context, c agentwire.Config, version string) error {
	if e := c.Validate(); e != nil {
		return e
	}
	q, e := OpenSpool(c.StateDir)
	if e != nil {
		return e
	}
	defer q.Close()
	collector, e := NewCollector()
	if e != nil {
		return e
	}
	var latest currentSample
	samplingDone := make(chan struct{})
	defer func() { <-samplingDone }()
	go func() {
		defer close(samplingDone)
		ticker := time.NewTicker(time.Duration(c.IntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			s, e := collector.Collect(ctx)
			if ctx.Err() != nil {
				return
			}
			if e == nil {
				if e = q.Add(s); e != nil {
					if s.Errors == nil {
						s.Errors = map[string]string{}
					}
					s.Errors["spool"] = "write_failed"
					slog.Error("agent outbox write failed")
				}
				latest.set(s)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	runTransport(ctx, c, version, q, &latest)
	return nil
}

func runTransport(ctx context.Context, c agentwire.Config, version string, q *Spool, latest *currentSample) {
	base := strings.TrimRight(c.HubURL, "/") + "/api/v1/agent"
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/connect"
	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 5 * time.Second}
	var conn *websocket.Conn
	var stopConn func() bool
	closeConn := func() {
		if stopConn != nil {
			stopConn()
			stopConn = nil
		}
		if conn != nil {
			_ = conn.Close()
			conn = nil
		}
	}
	defer closeConn()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var nextDial, lastHeartbeat time.Time
	backoff := time.Second
	for ctx.Err() == nil {
		now := time.Now()
		if conn == nil && !now.Before(nextDial) {
			headers := http.Header{"Authorization": []string{"Bearer " + c.Token}}
			v, resp, err := dialer.DialContext(ctx, wsURL, headers)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if err == nil {
				conn = v
				conn.SetReadLimit(agentwire.MaxFrame)
				captured := conn
				stopConn = context.AfterFunc(ctx, func() { _ = captured.Close() })
				backoff = time.Second
				lastHeartbeat = time.Time{}
			} else {
				nextDial = now.Add(time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64())))
				backoff = min(30*time.Second, backoff*2)
			}
		}
		request := func(msg agentwire.Request) (agentwire.Response, error) {
			if conn != nil {
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if e := conn.WriteJSON(msg); e != nil {
					return agentwire.Response{}, e
				}
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				var reply agentwire.Response
				e := conn.ReadJSON(&reply)
				return reply, e
			}
			return post(ctx, httpClient, base+"/report", c.Token, msg)
		}
		pending, dropped := q.Stats()
		msg := agentwire.Request{Protocol: agentwire.Protocol, Version: version, Kind: "heartbeat", Latest: latest.get(), Pending: pending, Dropped: dropped}
		if time.Since(lastHeartbeat) >= 15*time.Second {
			reply, e := request(msg)
			if e != nil {
				wasConnected := conn != nil
				closeConn()
				if wasConnected {
					nextDial = time.Now().Add(time.Second)
				}
			} else if !reply.ServerTime.IsZero() {
				lastHeartbeat = time.Now()
				_ = writeAtomic(filepath.Join(c.StateDir, "health.json"), mustJSON(healthRecord{version, configHash(c), time.Now().UTC()}))
			}
		}
		if samples, e := q.Batch(); e == nil && len(samples) > 0 {
			msg.Kind = "samples"
			msg.Samples = samples
			reply, e := request(msg)
			if e != nil {
				wasConnected := conn != nil
				closeConn()
				if wasConnected {
					nextDial = time.Now().Add(time.Second)
				}
			} else if e = q.Ack(reply.Ack); e != nil {
				slog.Error("agent outbox acknowledgement failed")
			}
			if e == nil && len(reply.Rejected) > 0 {
				if e = q.Reject(reply.Rejected); e != nil {
					slog.Error("agent rejected-sample cleanup failed")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func post(ctx context.Context, client *http.Client, url, token string, msg agentwire.Request) (agentwire.Response, error) {
	b, e := json.Marshal(msg)
	if e != nil {
		return agentwire.Response{}, e
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(b)))
	if e != nil {
		return agentwire.Response{}, e
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(r)
	if e != nil {
		return agentwire.Response{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentwire.Response{}, fmt.Errorf("agent upload returned %d", resp.StatusCode)
	}
	b, e = readLimited(resp.Body, agentwire.MaxFrame)
	if e != nil {
		return agentwire.Response{}, e
	}
	var reply agentwire.Response
	e = json.Unmarshal(b, &reply)
	return reply, e
}
