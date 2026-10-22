package handler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/update"
)

// NewUpdateHandler serves an in-memory snapshot. Browsers revalidate the private
// cached response, avoiding repeated release-note downloads between checks.
func NewUpdateHandler(checker interface{ Snapshot() update.Status }) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := checker.Snapshot()
		body, err := json.Marshal(status)
		if err != nil {
			server.WriteError(w, r, err)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("Vary", "Authorization")
		w.Header().Set("ETag", etag)
		for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		server.WriteJSON(w, http.StatusOK, status)
	}
}
