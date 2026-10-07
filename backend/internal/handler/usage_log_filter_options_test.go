package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

type usageFilterOptionsStub struct {
	items      []repository.UsageLogFilterOption
	hasMore    bool
	err        error
	called     int
	kind, text string
	deadline   time.Time
	ctxErr     error
}

func (s *usageFilterOptionsStub) FilterOptions(ctx context.Context, kind, text string) ([]repository.UsageLogFilterOption, bool, error) {
	s.called++
	s.kind, s.text = kind, text
	s.deadline, _ = ctx.Deadline()
	s.ctxErr = ctx.Err()
	return s.items, s.hasMore, s.err
}

func usageFilterOptionsRequest(q url.Values, user uint, role string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/usage-logs/filter-options?"+q.Encode(), nil)
	return r.WithContext(mw.WithUserClaims(r.Context(), &token.Claims{UserID: user, Role: role}))
}

func TestUsageFilterOptionsTrimsSearchAndLimitsLookupDuration(t *testing.T) {
	s := &usageFilterOptionsStub{}
	h := NewUsageLogFilterOptionsHandler(s)
	q := url.Values{"kind": {"user"}, "q": {"  \t 中文筛选 \n "}}
	w := httptest.NewRecorder()
	before := time.Now()
	h.List(w, usageFilterOptionsRequest(q, 1, "admin"))
	if w.Code != http.StatusOK || s.called != 1 || s.kind != "user" || s.text != "中文筛选" {
		t.Fatalf("trimmed lookup failed: status=%d stub=%+v body=%s", w.Code, s, w.Body)
	}
	if s.deadline.IsZero() || s.deadline.Before(before.Add(2*time.Second)) || s.deadline.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("lookup did not get the two-second deadline: %v", s.deadline)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("entity labels must not be cached")
	}
	var b struct {
		Data struct {
			Items   []repository.UsageLogFilterOption `json:"items"`
			HasMore bool                              `json:"has_more"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Data.Items == nil || len(b.Data.Items) != 0 || b.Data.HasMore {
		t.Fatal("an empty lookup must return items: [] and has_more: false")
	}

	// The child timeout must preserve a request canceled by its caller.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := usageFilterOptionsRequest(q, 1, "admin")
	r = r.WithContext(mw.WithUserClaims(ctx, &token.Claims{UserID: 1, Role: "admin"}))
	h.List(httptest.NewRecorder(), r)
	if !errors.Is(s.ctxErr, context.Canceled) {
		t.Fatalf("request cancellation was lost: %v", s.ctxErr)
	}
}

func TestUsageFilterOptionsQueryValidation(t *testing.T) {
	valid := []url.Values{
		{"kind": {"server"}},
		{"kind": {"user"}, "q": {""}},
		{"kind": {"api_key"}, "q": {strings.Repeat("中", 128)}},
		{"kind": {"server"}, "q": {" \n" + strings.Repeat("中", 128) + "\t "}},
	}
	for _, query := range valid {
		s := &usageFilterOptionsStub{}
		w := httptest.NewRecorder()
		NewUsageLogFilterOptionsHandler(s).List(w, usageFilterOptionsRequest(query, 1, "admin"))
		if w.Code != http.StatusOK || s.called != 1 {
			t.Fatalf("valid query rejected: %q status=%d body=%s", query, w.Code, w.Body)
		}
	}
	invalid := []url.Values{
		{},
		{"kind": {"credential"}},
		{"kind": {" server"}},
		{"kind": {"server", "user"}},
		{"kind": {"server"}, "q": {"a", "b"}},
		{"kind": {"server"}, "limit": {"1000"}},
		{"kind": {"server"}, "q": {strings.Repeat("中", 129)}},
		{"kind": {"server"}, "q": {"a\x00b"}},
		{"kind": {"server"}, "q": {"\xff"}},
	}
	for _, query := range invalid {
		s := &usageFilterOptionsStub{}
		w := httptest.NewRecorder()
		NewUsageLogFilterOptionsHandler(s).List(w, usageFilterOptionsRequest(query, 1, "admin"))
		if w.Code != http.StatusBadRequest || s.called != 0 || !strings.Contains(w.Body.String(), `"reason":"invalid_query"`) {
			t.Fatalf("invalid query reached lookup: %q status=%d calls=%d body=%s", query, w.Code, s.called, w.Body)
		}
	}
	for _, rawQuery := range []string{"kind=server&q=%ZZ", "kind=server&q=a;b"} {
		s := &usageFilterOptionsStub{}
		r := usageFilterOptionsRequest(url.Values{}, 1, "admin")
		r.URL.RawQuery = rawQuery
		w := httptest.NewRecorder()
		NewUsageLogFilterOptionsHandler(s).List(w, r)
		if w.Code != http.StatusBadRequest || s.called != 0 {
			t.Fatalf("malformed encoding silently changed search: %s status=%d calls=%d", rawQuery, w.Code, s.called)
		}
	}
}

func TestUsageFilterOptionsRequireJWTAdminBeforeLookup(t *testing.T) {
	owner, key := uint(1), uint(2)
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/usage-logs/filter-options?kind=user", nil),
		usageFilterOptionsRequest(url.Values{"kind": {"user"}}, 1, "user"),
		usageFilterOptionsRequest(url.Values{"kind": {"user"}}, 0, "admin"),
	}
	keyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/usage-logs/filter-options?kind=user", nil)
	keyRequest = keyRequest.WithContext(mw.WithPrincipal(keyRequest.Context(), usage.Principal{
		AuthType: "api_key", UserID: &owner, APIKeyID: &key, Role: "admin", Scopes: []string{"*"},
	}))
	requests = append(requests, keyRequest)
	for _, r := range requests {
		s := &usageFilterOptionsStub{}
		w := httptest.NewRecorder()
		NewUsageLogFilterOptionsHandler(s).List(w, r)
		if w.Code != http.StatusForbidden || s.called != 0 {
			t.Fatalf("non-JWT-admin reached entity lookup: status=%d calls=%d", w.Code, s.called)
		}
	}
}

func TestUsageFilterOptionsJSONPreservesLabelsIDsAndBounds(t *testing.T) {
	s := &usageFilterOptionsStub{items: []repository.UsageLogFilterOption{
		{ID: "9007199254742000", Name: "automation", Prefix: "tk_short", Deleted: false},
		{ID: "41", Name: "removed server", Deleted: true},
	}}
	for len(s.items) < 51 {
		s.items = append(s.items, repository.UsageLogFilterOption{ID: strconv.Itoa(len(s.items) + 100), Name: "option"})
	}
	w := httptest.NewRecorder()
	NewUsageLogFilterOptionsHandler(s).List(w, usageFilterOptionsRequest(url.Values{"kind": {"api_key"}}, 1, "admin"))
	var b struct {
		Data struct {
			Items   []map[string]any `json:"items"`
			HasMore bool             `json:"has_more"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(b.Data.Items) != 50 || !b.Data.HasMore {
		t.Fatalf("unbounded options: status=%d len=%d more=%v", w.Code, len(b.Data.Items), b.Data.HasMore)
	}
	first, second := b.Data.Items[0], b.Data.Items[1]
	if first["id"] != "9007199254742000" || first["name"] != "automation" || first["prefix"] != "tk_short" || first["deleted"] != false || second["deleted"] != true {
		t.Fatalf("labels or large IDs changed: %#v %#v", first, second)
	}
	if _, present := second["prefix"]; present {
		t.Fatal("non-key label should omit prefix")
	}
}

func TestUsageFilterOptionsHideRepositoryErrors(t *testing.T) {
	s := &usageFilterOptionsStub{err: errors.New("SELECT credentials; private-key-material")}
	w := httptest.NewRecorder()
	NewUsageLogFilterOptionsHandler(s).List(w, usageFilterOptionsRequest(url.Values{"kind": {"server"}}, 1, "admin"))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private-key-material") || strings.Contains(w.Body.String(), "SELECT") {
		t.Fatalf("database error leaked: status=%d body=%s", w.Code, w.Body)
	}
}

func TestUsageFilterOptionsRouterEnforcesAuthAndStaticRoute(t *testing.T) {
	s := &usageFilterOptionsStub{}
	h := NewUsageLogFilterOptionsHandler(s)
	jwt := token.NewJWTService("filter-options-test-secret", time.Hour)
	adminToken, err := jwt.GenerateToken(1, "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	userToken, err := jwt.GenerateToken(2, "user", "user")
	if err != nil {
		t.Fatal(err)
	}
	validator := mw.APIKeyIdentityValidatorFunc(func(context.Context, string) (usage.Principal, error) {
		owner, key := uint(1), uint(2)
		return usage.Principal{UserID: &owner, APIKeyID: &key, Role: "admin", Scopes: []string{"*"}}, nil
	})
	router := server.NewRouter(server.RouteConfig{
		JWTService:                   jwt,
		APIKeyAuth:                   validator,
		RevealLimiter:                mw.NewRateLimiter(time.Minute, 5),
		FilterUsageLogOptionsHandler: h.List,
		GetUsageLogHandler: func(http.ResponseWriter, *http.Request) {
			t.Fatal("filter-options matched the log-ID route")
		},
	})
	for _, tc := range []struct {
		name, token, key string
		status           int
		lookups          int
	}{
		{name: "anonymous", status: http.StatusUnauthorized},
		{name: "invalid JWT", token: "bad-token", status: http.StatusUnauthorized},
		{name: "JWT user", token: userToken, status: http.StatusForbidden},
		{name: "API key admin wildcard", key: "valid-key", status: http.StatusForbidden},
		{name: "API key preferred over JWT", token: adminToken, key: "valid-key", status: http.StatusForbidden},
		{name: "JWT admin", token: adminToken, status: http.StatusOK, lookups: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := s.called
			r := httptest.NewRequest(http.MethodGet, "/api/v1/usage-logs/filter-options?kind=server", nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.key != "" {
				r.Header.Set("X-API-Key", tc.key)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status || s.called-before != tc.lookups {
				t.Fatalf("auth boundary failed: status=%d calls=%d body=%s", w.Code, s.called-before, w.Body)
			}
		})
	}
}
