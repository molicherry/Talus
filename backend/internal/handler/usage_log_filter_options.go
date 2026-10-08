package handler

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
)

// Keeping the label lookup separate preserves the existing list/detail reader
// contract and prevents entity projections from being mixed with log history.
type usageLogFilterOptionsReader interface {
	FilterOptions(context.Context, string, string) ([]repository.UsageLogFilterOption, bool, error)
}

type UsageLogFilterOptionsHandler struct{ repo usageLogFilterOptionsReader }

func NewUsageLogFilterOptionsHandler(repo usageLogFilterOptionsReader) *UsageLogFilterOptionsHandler {
	return &UsageLogFilterOptionsHandler{repo: repo}
}

// List handles GET /api/v1/usage-logs/filter-options for JWT administrators.
func (h *UsageLogFilterOptionsHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, allowed := usageAdmin(r); !allowed {
		server.WriteError(w, r, server.ErrForbidden)
		return
	}
	raw, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidQuery))
		return
	}
	for name, values := range raw {
		if (name != "kind" && name != "q") || len(values) != 1 {
			server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidQuery))
			return
		}
	}
	kind := raw.Get("kind")
	if kind != "server" && kind != "user" && kind != "api_key" {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidQuery))
		return
	}
	text := strings.TrimSpace(raw.Get("q"))
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 128 || strings.ContainsRune(text, 0) {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidQuery))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	items, hasMore, err := h.repo.FilterOptions(ctx, kind, text)
	if err != nil {
		server.WriteError(w, r, server.ErrInternal)
		return
	}
	if items == nil {
		items = []repository.UsageLogFilterOption{}
	}
	if len(items) > repository.UsageLogFilterOptionsLimit {
		items, hasMore = items[:repository.UsageLogFilterOptionsLimit], true
	}
	server.WriteJSON(w, http.StatusOK, struct {
		Items   []repository.UsageLogFilterOption `json:"items"`
		HasMore bool                              `json:"has_more"`
	}{Items: items, HasMore: hasMore})
}
