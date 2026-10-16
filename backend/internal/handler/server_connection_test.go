package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
)

type connectionTesterStub struct {
	id    uint
	err   error
	calls int
}

func (s *connectionTesterStub) TestConnection(_ context.Context, id uint) (*service.ConnectionTestResult, error) {
	s.id = id
	s.calls++
	return &service.ConnectionTestResult{Mode: "socks5", Success: true, LatencyMS: 12}, s.err
}

func connectionTestRequest(id string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/"+id+"/connection/test", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	return r.WithContext(mw.WithUserClaims(ctx, &token.Claims{UserID: 1, Role: "admin"}))
}

func TestConnectionTestOnlyAllowsJWTAdmin(t *testing.T) {
	for _, principal := range []usage.Principal{
		{AuthType: "api_key", Role: "admin"},
		{AuthType: "jwt", Role: "viewer"},
		{AuthType: "unauthenticated"},
	} {
		t.Run(principal.AuthType+principal.Role, func(t *testing.T) {
			stub := &connectionTesterStub{}
			r := connectionTestRequest("42")
			uid := uint(1)
			principal.UserID = &uid
			r = r.WithContext(mw.WithPrincipal(r.Context(), principal))
			w := httptest.NewRecorder()
			NewServerConnectionHandler(stub).Test(w, r)
			if w.Code != http.StatusForbidden || stub.calls != 0 {
				t.Fatalf("status=%d calls=%d: %s", w.Code, stub.calls, w.Body)
			}
		})
	}
}

func TestConnectionTestUsesSavedServerIDAndReportsResult(t *testing.T) {
	stub := &connectionTesterStub{}
	w := httptest.NewRecorder()
	NewServerConnectionHandler(stub).Test(w, connectionTestRequest("42"))
	var body struct {
		Data service.ConnectionTestResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || stub.id != 42 || !body.Data.Success || body.Data.Mode != "socks5" || body.Data.LatencyMS != 12 {
		t.Fatalf("unexpected response status=%d id=%d body=%s", w.Code, stub.id, w.Body)
	}
}

func TestConnectionTestPreservesSafeFailureStage(t *testing.T) {
	stub := &connectionTesterStub{err: server.NewAppErrorParams(http.StatusBadGateway, server.ReasonProxyAuth, map[string]any{"stage": "proxy_auth", "role": "proxy"})}
	w := httptest.NewRecorder()
	NewServerConnectionHandler(stub).Test(w, connectionTestRequest("42"))
	var body struct {
		Error struct {
			Reason string         `json:"reason"`
			Params map[string]any `json:"params"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusBadGateway || body.Error.Reason != server.ReasonProxyAuth || body.Error.Params["stage"] != "proxy_auth" {
		t.Fatalf("failure lost stage: status=%d body=%s", w.Code, w.Body)
	}
}

func TestConnectionTestRejectsMalformedIDBeforeDial(t *testing.T) {
	stub := &connectionTesterStub{}
	w := httptest.NewRecorder()
	NewServerConnectionHandler(stub).Test(w, connectionTestRequest("invalid"))
	if w.Code != http.StatusBadRequest || stub.calls != 0 {
		t.Fatalf("status=%d calls=%d", w.Code, stub.calls)
	}
}
