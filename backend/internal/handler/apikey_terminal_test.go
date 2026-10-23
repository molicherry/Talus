package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
)

// Exercise the real PATCH, primary-database gate, WS handler and SSH peer;
// revocation must win both during shell setup and after input becomes possible.
func TestAPIKeyPermissionsTerminalRevocation(t *testing.T) {
	for _, phase := range []string{"preparing", "ready", "delete", "rotate_preparing", "rotate_ready"} {
		t.Run(phase, func(t *testing.T) {
			db := usageFlowDB(t)
			f := newTerminalE2EFixture(t)
			resume := sync.OnceFunc(func() { close(f.shellResume) })
			keyRepo := repository.NewAPIKeyRepo(db)
			master, err := crypto.NewMasterKey(strings.Repeat("ab", 32))
			if err != nil {
				t.Fatal(err)
			}
			keys := service.NewAPIKeyService(keyRepo, repository.NewServerRepo(db), master)
			raw := "synthetic-terminal-key"
			hash := sha256.Sum256([]byte(raw))
			key := model.APIKey{Name: "terminal", KeyHash: hex.EncodeToString(hash[:]), KeyPrefix: "test", Scopes: []string{"servers:terminal", "servers:read"}}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			f.svc.SetAPIKeyPermissions(keys.PermissionsGate(), keys.TerminalRegistry())
			// A JWT session with the same numeric ID must remain untouched.
			other := f.registry.Register(key.ID, 0, nil, nil)
			defer f.registry.Done(other.ID)
			otherKey := keys.TerminalRegistry().Register(key.ID+1, 1, nil, nil)
			defer keys.TerminalRegistry().Done(otherKey.ID)
			validator := mw.APIKeyIdentityValidatorFunc(func(ctx context.Context, raw string) (usage.Principal, error) {
				k, err := keys.Validate(ctx, raw)
				if err != nil {
					return usage.Principal{}, err
				}
				return usage.Principal{AuthType: "api_key", APIKeyID: &k.ID, Scopes: k.Scopes, ServerIDs: k.ServerIDs, PermissionsRevision: k.PermissionsRevision}, nil
			})
			handler := NewAPIKeyHandler(keys, nil)
			terminal := NewTerminalHandler(f.svc, f.jwt)
			done := make(chan struct{})
			router := chi.NewRouter()
			router.Use(mw.Auth(f.jwt, validator, nil))
			router.Patch("/api/v1/api-keys/{id}", handler.UpdatePermissions)
			router.Post("/api/v1/api-keys/{id}/rotate", handler.Rotate)
			router.Delete("/api/v1/api-keys/{id}", handler.Delete)
			router.Get("/api/v1/servers/{id}/terminal", func(w http.ResponseWriter, r *http.Request) { defer close(done); terminal.Handle(w, r) })
			srv := httptest.NewServer(router)
			defer func() { resume(); srv.Close() }()
			ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/servers/1/terminal", http.Header{"X-Api-Key": []string{raw}})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
			terminalE2EWait(t, f.shellEntered)
			preparing := strings.HasSuffix(phase, "preparing")
			if !preparing {
				resume()
				terminalE2EReady(t, ws)
				if err := ws.WriteJSON(wsMessage{Type: "input", Data: "before\n"}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-f.input:
				case <-time.After(time.Second):
					t.Fatal("live terminal did not forward input")
				}
			}
			token, err := f.jwt.GenerateToken(7, "admin", "admin", 0)
			if err != nil {
				t.Fatal(err)
			}
			method, status := http.MethodPatch, http.StatusOK
			if phase == "delete" {
				method, status = http.MethodDelete, http.StatusNoContent
			}
			path, body := "/api/v1/api-keys/1", `{"expected_revision":"1","scopes":["servers:read"]}`
			if strings.HasPrefix(phase, "rotate_") {
				method, path, body = http.MethodPost, path+"/rotate", `{"expected_revision":"1"}`
			}
			req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			response := make(chan *http.Response, 1)
			requestErr := make(chan error, 1)
			go func() {
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					requestErr <- err
				} else {
					response <- resp
				}
			}()
			if preparing {
				// Wait until the production gate has marked the preparing session.
				deadline := time.Now().Add(3 * time.Second)
				for {
					revision, err := keyRepo.PermissionsRevision(context.Background(), key.ID)
					if err != nil {
						t.Fatal(err)
					}
					if revision == 2 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("PATCH did not commit")
					}
					time.Sleep(5 * time.Millisecond)
				}
				resume()
			}
			select {
			case resp := <-response:
				resp.Body.Close()
				if resp.StatusCode != status {
					t.Fatalf("update status %d", resp.StatusCode)
				}
			case err := <-requestErr:
				t.Fatal(err)
			case <-time.After(5 * time.Second):
				t.Fatal("permission mutation hung")
			}
			_ = ws.WriteJSON(wsMessage{Type: "input", Data: "after\n"})
			_ = ws.WriteJSON(map[string]any{"type": "resize", "rows": 70, "cols": 150})
			terminalE2ENoReady(t, ws)
			terminalE2EWait(t, done)
			if keys.TerminalRegistry().LiveCount() != 1 || f.registry.LiveCount() != 1 {
				t.Fatal("wrong subject revoked or resources leaked")
			}
			select {
			case got := <-f.input:
				t.Fatalf("input after revoke: %q", got)
			default:
			}
			select {
			case <-f.resizes:
				t.Fatal("resize after revoke")
			default:
			}
		})
	}
}
