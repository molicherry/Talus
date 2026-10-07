package handler

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

type usageReaderStub struct {
	rows   []model.UsageLog
	bound  uint64
	lastAt time.Time
	lastID uint64
	called int
}

func (s *usageReaderStub) FirstPage(_ context.Context, _ repository.UsageLogFilter, _ int) ([]model.UsageLog, uint64, error) {
	s.called++
	return s.rows, s.bound, nil
}
func (s *usageReaderStub) List(_ context.Context, _ repository.UsageLogFilter, b uint64, at *time.Time, id uint64, _ int) ([]model.UsageLog, error) {
	s.called++
	s.lastAt = *at
	s.lastID = id
	s.bound = b
	return s.rows, nil
}
func (s *usageReaderStub) Get(_ context.Context, id uint64) (*model.UsageLog, error) {
	s.called++
	s.lastID = id
	return &s.rows[0], nil
}

func TestUsageDetailPreservesFullWidthIDs(t *testing.T) {
	const id uint64 = 9007199254742000
	s := &usageReaderStub{rows: []model.UsageLog{{ID: id}}}
	h := NewUsageLogHandler(s, "secret")
	r := usageTestRequest(url.Values{}, 1, "admin")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", strconv.FormatUint(id, 10))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	h.Get(w, r)
	if w.Code != http.StatusOK || s.lastID != id {
		t.Fatalf("detail truncated log ID: status=%d id=%d", w.Code, s.lastID)
	}
}

func TestResourceIDParsingRejectsNativeOverflow(t *testing.T) {
	maxID := ^uint(0)
	maxValid := uint64(maxID)
	if maxValid > math.MaxInt64 {
		maxValid = math.MaxInt64
	}
	if got, err := parsePositiveID(strconv.FormatUint(maxValid, 10)); err != nil || uint64(got) != maxValid {
		t.Fatalf("maximum resource ID rejected: got=%d err=%v", got, err)
	}
	overflow := "18446744073709551616"
	if strconv.IntSize == 32 {
		overflow = "4294967296"
	}
	for _, input := range []string{overflow, "9223372036854775808", "18446744073709551615", "0", "-1", "+1", "1.0"} {
		if _, err := parsePositiveID(input); err == nil {
			t.Fatalf("invalid resource ID accepted: %s", input)
		}
	}
}

func TestUsageResourceFiltersRejectDatabaseIDOverflowBeforeLookup(t *testing.T) {
	maxValid := uint64(math.MaxInt64)
	if strconv.IntSize == 32 {
		maxValid = uint64(^uint(0))
	}
	for _, field := range []string{"user_id", "api_key_id", "resource_id", "server_id"} {
		t.Run(field, func(t *testing.T) {
			s := &usageReaderStub{}
			h := NewUsageLogHandler(s, "secret")
			for _, input := range []string{"9223372036854775808", "18446744073709551615", "18446744073709551616"} {
				w := httptest.NewRecorder()
				h.List(w, usageTestRequest(url.Values{field: {input}}, 1, "admin"))
				if w.Code != http.StatusBadRequest || s.called != 0 {
					t.Fatalf("database ID overflow reached lookup: %s=%s status=%d calls=%d", field, input, w.Code, s.called)
				}
			}
			w := httptest.NewRecorder()
			h.List(w, usageTestRequest(url.Values{field: {strconv.FormatUint(maxValid, 10)}}, 1, "admin"))
			if w.Code != http.StatusOK || s.called != 1 {
				t.Fatalf("maximum valid entity ID rejected: %s status=%d calls=%d body=%s", field, w.Code, s.called, w.Body)
			}
		})
	}
}

func usageTestRequest(q url.Values, user uint, role string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/usage-logs?"+q.Encode(), nil)
	return r.WithContext(mw.WithUserClaims(r.Context(), &token.Claims{UserID: user, Username: "admin", Role: role}))
}

func TestUsageCursorContinuationPreservesPrecisionAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s := &usageReaderStub{bound: 9007199254742000}
	for i := 0; i < 26; i++ {
		s.rows = append(s.rows, model.UsageLog{ID: s.bound - uint64(i), StartedAt: now.Add(-time.Hour).Add(-time.Duration(i) * time.Microsecond), Action: "server.exec", Outcome: "succeeded", Phase: "closed", AuthType: "jwt", Source: "operation", Metadata: json.RawMessage(`{}`)})
	}
	h := NewUsageLogHandler(s, "testsecret")
	h.now = func() time.Time { return now }
	w := httptest.NewRecorder()
	h.List(w, usageTestRequest(url.Values{}, 1, "admin"))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var response struct {
		Data struct {
			Items []model.UsageLog `json:"items"`
			Query UsageLogQuery    `json:"query"`
			Bound string           `json:"id_upper_bound"`
			Next  string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Bound != strconv.FormatUint(s.bound, 10) || len(response.Data.Items) != 25 {
		t.Fatal("unsafe bound or incorrect page")
	}
	c, reason := h.decodeCursor(response.Data.Next, 1, now)
	if reason != "" {
		t.Fatal(reason)
	}
	last := s.rows[24]
	if c.LastID != last.ID || c.LastAt != last.StartedAt.Format(time.RFC3339Nano) {
		t.Fatal("cursor must use last displayed row and microsecond precision")
	}
	q := url.Values{"from": {response.Data.Query.From}, "to": {response.Data.Query.To}, "page_size": {"25"}, "cursor": {response.Data.Next}}
	h.now = func() time.Time { return now.Add(10 * time.Minute) }
	w = httptest.NewRecorder()
	h.List(w, usageTestRequest(q, 1, "admin"))
	if w.Code != 200 || !s.lastAt.Equal(last.StartedAt) || s.lastID != last.ID {
		t.Fatalf("continuation failed: %s", w.Body)
	}
	var continuation struct {
		Data struct {
			Next string `json:"next_cursor"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &continuation)
	next, r := h.decodeCursor(continuation.Data.Next, 1, now.Add(10*time.Minute))
	if r != "" || next.Expires != c.Expires {
		t.Fatal("continuation extended cursor expiration")
	}
	h.now = func() time.Time { return now.Add(30 * time.Minute) }
	w = httptest.NewRecorder()
	h.List(w, usageTestRequest(q, 1, "admin"))
	if w.Code != 400 {
		t.Fatal("expired cursor accepted")
	}
}

func TestUsageCursorRejectsTamperingSubjectAndFilterChanges(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s := &usageReaderStub{}
	h := NewUsageLogHandler(s, "secret")
	h.now = func() time.Time { return now }
	q, _, reason := parseUsageQuery(url.Values{}, now, false)
	if reason != "" {
		t.Fatal(reason)
	}
	c := usageCursor{Version: 1, Subject: 1, Hash: usageQueryHash(q), From: q.From, To: q.To, Size: 25, Bound: 100, LastAt: now.Add(-time.Hour).Format(time.RFC3339Nano), LastID: 50, Issued: now.Unix(), Expires: now.Add(30 * time.Minute).Unix()}
	token := h.encodeCursor(c)
	for _, tc := range []struct {
		name     string
		query    url.Values
		user     uint
		expected string
	}{
		{"signature", url.Values{"cursor": {token + "x"}}, 1, "invalid_cursor"},
		{"subject", url.Values{"cursor": {token}}, 2, "invalid_cursor"},
		{"missing canonical filters", url.Values{"cursor": {token}}, 1, "cursor_filter_mismatch"},
		{"changed filter", url.Values{"cursor": {token}, "from": {q.From}, "to": {q.To}, "page_size": {"25"}, "outcome": {"failed"}}, 1, "cursor_filter_mismatch"},
		{"untrusted bound", url.Values{"id_upper_bound": {"100"}}, 1, "invalid_query"},
		{"duplicate filter", url.Values{"outcome": {"failed", "succeeded"}}, 1, "invalid_query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.List(w, usageTestRequest(tc.query, tc.user, "admin"))
			var b struct {
				Error struct {
					Reason string `json:"reason"`
				} `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &b)
			if w.Code != 400 || b.Error.Reason != tc.expected {
				t.Fatalf("status=%d reason=%s", w.Code, b.Error.Reason)
			}
		})
	}
	if s.called != 0 {
		t.Fatal("invalid cursors reached database")
	}
}

func TestUsageLogsRequireAdminAndReturnEmptyArray(t *testing.T) {
	s := &usageReaderStub{}
	h := NewUsageLogHandler(s, "secret")
	for _, role := range []string{"user", ""} {
		w := httptest.NewRecorder()
		h.List(w, usageTestRequest(url.Values{}, 1, role))
		if w.Code != 403 {
			t.Fatal("non-admin accepted")
		}
	}
	w := httptest.NewRecorder()
	h.List(w, usageTestRequest(url.Values{}, 1, "admin"))
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
		t.Fatal("empty list failed")
	}
	var b struct {
		Data struct {
			Items []model.UsageLog `json:"items"`
			Bound string           `json:"id_upper_bound"`
			Next  *string          `json:"next_cursor"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if b.Data.Items == nil || len(b.Data.Items) != 0 || b.Data.Bound != "0" || b.Data.Next != nil {
		t.Fatal("empty list must be [] and bound 0")
	}
}
