package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"gorm.io/gorm"
)

type usageLogReader interface {
	FirstPage(context.Context, repository.UsageLogFilter, int) ([]model.UsageLog, uint64, error)
	List(context.Context, repository.UsageLogFilter, uint64, *time.Time, uint64, int) ([]model.UsageLog, error)
	Get(context.Context, uint64) (*model.UsageLog, error)
}

// UsageLogQuery is the canonical query echoed to clients and bound to cursors.
type UsageLogQuery struct {
	From         string `json:"from"`
	To           string `json:"to"`
	PageSize     int    `json:"page_size"`
	Action       string `json:"action,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
	AuthType     string `json:"auth_type,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	APIKeyID     string `json:"api_key_id,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	ServerID     string `json:"server_id,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}

type usageCursor struct {
	Version int    `json:"v"`
	Subject uint   `json:"sub"`
	Hash    string `json:"q"`
	From    string `json:"from"`
	To      string `json:"to"`
	Size    int    `json:"size"`
	Bound   uint64 `json:"bound,string"`
	LastAt  string `json:"last_at"`
	LastID  uint64 `json:"last_id,string"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
}

type UsageLogHandler struct {
	repo usageLogReader
	key  []byte
	now  func() time.Time
}

func NewUsageLogHandler(repo usageLogReader, jwtSecret string) *UsageLogHandler {
	key := hmac.New(sha256.New, []byte(jwtSecret))
	_, _ = key.Write([]byte("talus/usage-log-cursor/v1"))
	return &UsageLogHandler{repo: repo, key: key.Sum(nil), now: time.Now}
}

func usageAdmin(r *http.Request) (uint, bool) {
	p := mw.GetPrincipal(r.Context())
	return valueUint(p.UserID), p.AuthType == "jwt" && p.Role == "admin" && p.UserID != nil && *p.UserID != 0
}

func valueUint(v *uint) uint {
	if v == nil {
		return 0
	}
	return *v
}

func (h *UsageLogHandler) List(w http.ResponseWriter, r *http.Request) {
	viewer, allowed := usageAdmin(r)
	if !allowed {
		server.WriteError(w, r, server.ErrForbidden)
		return
	}
	now := h.now().UTC()
	raw := r.URL.Query()
	var cursor *usageCursor
	if token := raw.Get("cursor"); token != "" {
		c, reason := h.decodeCursor(token, viewer, now)
		if reason != "" {
			server.WriteError(w, r, server.NewAppError(400, reason))
			return
		}
		cursor = &c
	}
	query, filter, reason := parseUsageQuery(raw, now, cursor != nil)
	if reason != "" {
		server.WriteError(w, r, server.NewAppError(400, reason))
		return
	}
	if cursor != nil && (cursor.Hash != usageQueryHash(query) || cursor.From != query.From || cursor.To != query.To || cursor.Size != query.PageSize) {
		server.WriteError(w, r, server.NewAppError(400, server.ReasonCursorFilterMismatch))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var items []model.UsageLog
	var bound uint64
	var err error
	if cursor == nil {
		items, bound, err = h.repo.FirstPage(ctx, filter, query.PageSize+1)
	} else {
		bound = cursor.Bound
		lastAt, _ := time.Parse(time.RFC3339Nano, cursor.LastAt)
		items, err = h.repo.List(ctx, filter, bound, &lastAt, cursor.LastID, query.PageSize+1)
	}
	if err != nil {
		server.WriteError(w, r, server.ErrInternal)
		return
	}
	if items == nil {
		items = []model.UsageLog{}
	}
	hasMore := len(items) > query.PageSize
	var next *string
	if hasMore {
		items = items[:query.PageSize]
		last := items[len(items)-1]
		issued, expires := now.Unix(), now.Add(30*time.Minute).Unix()
		if cursor != nil {
			issued, expires = cursor.Issued, cursor.Expires
		}
		n := h.encodeCursor(usageCursor{Version: 1, Subject: viewer, Hash: usageQueryHash(query), From: query.From, To: query.To, Size: query.PageSize, Bound: bound, LastAt: last.StartedAt.UTC().Format(time.RFC3339Nano), LastID: last.ID, Issued: issued, Expires: expires})
		next = &n
	}
	server.WriteJSON(w, 200, struct {
		Items       []model.UsageLog `json:"items"`
		Query       UsageLogQuery    `json:"query"`
		Bound       string           `json:"id_upper_bound"`
		Next        *string          `json:"next_cursor"`
		More        bool             `json:"has_more"`
		Consistency string           `json:"consistency"`
	}{items, query, strconv.FormatUint(bound, 10), next, hasMore, "bounded_keyset"})
}

func (h *UsageLogHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, allowed := usageAdmin(r); !allowed {
		server.WriteError(w, r, server.ErrForbidden)
		return
	}
	id, err := parseUsageLogID(chi.URLParam(r, "id"))
	if err != nil {
		server.WriteError(w, r, server.NewAppError(400, server.ReasonInvalidQuery))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	item, err := h.repo.Get(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		server.WriteError(w, r, server.ErrNotFound)
		return
	}
	if err != nil {
		server.WriteError(w, r, server.ErrInternal)
		return
	}
	server.WriteJSON(w, 200, item)
}

var usageActions = strings.Fields("credential.create credential.update credential.delete credential.reveal api_key.create api_key.delete api_key.reveal server.create server.update server.delete server.host_key.trust server.exec server.terminal service.create service.update service.delete service.credentials service.relay")

func oneOf(v string, choices []string) bool {
	if v == "" {
		return true
	}
	for _, c := range choices {
		if c == v {
			return true
		}
	}
	return false
}

func parsePositiveID(s string) (uint, error) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("invalid id")
	}
	n, err := strconv.ParseUint(s, 10, strconv.IntSize)
	if err != nil || n == 0 || n > math.MaxInt64 {
		return 0, errors.New("invalid id")
	}
	return uint(n), nil
}

// Usage log IDs are uint64 regardless of the native size of resource IDs.
func parseUsageLogID(s string) (uint64, error) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, errors.New("invalid id")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("invalid id")
	}
	return n, nil
}

func parseUsageQuery(v url.Values, now time.Time, continuation bool) (UsageLogQuery, repository.UsageLogFilter, string) {
	var q UsageLogQuery
	var f repository.UsageLogFilter
	valid := map[string]bool{"from": true, "to": true, "page_size": true, "action": true, "outcome": true, "auth_type": true, "user_id": true, "api_key_id": true, "resource_type": true, "resource_id": true, "server_id": true, "request_id": true, "cursor": true}
	for k, vals := range v {
		if !valid[k] || len(vals) != 1 {
			return q, f, server.ReasonInvalidQuery
		}
	}
	if continuation && (v.Get("from") == "" || v.Get("to") == "" || v.Get("page_size") == "") {
		return q, f, server.ReasonCursorFilterMismatch
	}
	from, to := now.Add(-24*time.Hour).Truncate(time.Microsecond), now.Truncate(time.Microsecond)
	if (v.Get("from") == "") != (v.Get("to") == "") {
		return q, f, server.ReasonInvalidQuery
	}
	if v.Get("from") != "" {
		var err error
		from, err = time.Parse(time.RFC3339Nano, v.Get("from"))
		if err != nil {
			return q, f, server.ReasonInvalidQuery
		}
		to, err = time.Parse(time.RFC3339Nano, v.Get("to"))
		if err != nil {
			return q, f, server.ReasonInvalidQuery
		}
		if from.Nanosecond()%1000 != 0 || to.Nanosecond()%1000 != 0 {
			return q, f, server.ReasonInvalidQuery
		}
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) || to.Sub(from) > 90*24*time.Hour {
		return q, f, server.ReasonInvalidQuery
	}
	q.From, q.To, q.PageSize = from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano), 25
	if s := v.Get("page_size"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || (n != 25 && n != 50 && n != 100) {
			return q, f, server.ReasonInvalidQuery
		}
		q.PageSize = n
	}
	q.Action, q.Outcome, q.AuthType, q.ResourceType, q.RequestID = v.Get("action"), v.Get("outcome"), v.Get("auth_type"), v.Get("resource_type"), v.Get("request_id")
	if !oneOf(q.Action, usageActions) || !oneOf(q.Outcome, strings.Fields("running succeeded failed rejected cancelled unknown")) || !oneOf(q.AuthType, strings.Fields("jwt api_key unauthenticated legacy_unknown")) || !oneOf(q.ResourceType, strings.Fields("server credential api_key service")) || (q.RequestID != "" && !mw.ValidRequestID(q.RequestID)) {
		return q, f, server.ReasonInvalidQuery
	}
	f.From, f.To, f.Action, f.Outcome, f.AuthType, f.ResourceType, f.RequestID = from, to, q.Action, q.Outcome, q.AuthType, q.ResourceType, q.RequestID
	for _, field := range []struct {
		name string
		text *string
		id   **uint
	}{{"user_id", &q.UserID, &f.UserID}, {"api_key_id", &q.APIKeyID, &f.APIKeyID}, {"resource_id", &q.ResourceID, &f.ResourceID}, {"server_id", &q.ServerID, &f.ServerID}} {
		if s := v.Get(field.name); s != "" {
			n, err := parsePositiveID(s)
			if err != nil {
				return q, f, server.ReasonInvalidQuery
			}
			*field.text = strconv.FormatUint(uint64(n), 10)
			*field.id = &n
		}
	}
	return q, f, ""
}

func usageQueryHash(q UsageLogQuery) string {
	b, _ := json.Marshal(q)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (h *UsageLogHandler) encodeCursor(c usageCursor) string {
	b, _ := json.Marshal(c)
	m := hmac.New(sha256.New, h.key)
	_, _ = m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (h *UsageLogHandler) decodeCursor(s string, subject uint, now time.Time) (usageCursor, string) {
	var c usageCursor
	if len(s) > 4096 {
		return c, server.ReasonInvalidCursor
	}
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return c, server.ReasonInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, server.ReasonInvalidCursor
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, server.ReasonInvalidCursor
	}
	m := hmac.New(sha256.New, h.key)
	_, _ = m.Write(b)
	if !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &c) != nil {
		return c, server.ReasonInvalidCursor
	}
	last, err := time.Parse(time.RFC3339Nano, c.LastAt)
	from, e1 := time.Parse(time.RFC3339Nano, c.From)
	to, e2 := time.Parse(time.RFC3339Nano, c.To)
	if c.Version != 1 || c.Subject != subject || c.Subject == 0 || len(c.Hash) != 64 || c.LastID == 0 || c.LastID > c.Bound || (c.Size != 25 && c.Size != 50 && c.Size != 100) || err != nil || e1 != nil || e2 != nil || last.Nanosecond()%1000 != 0 || last.Before(from) || !last.Before(to) || !from.Before(to) || to.Sub(from) > 90*24*time.Hour || c.Issued > now.Unix()+30 || c.Expires-c.Issued != 1800 {
		return c, server.ReasonInvalidCursor
	}
	if now.Unix() >= c.Expires {
		return c, server.ReasonCursorExpired
	}
	return c, ""
}
