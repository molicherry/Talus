package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// AgentArtifact serves only a bundled, verified artifact to an authenticated
// node. A digest identifies immutable bytes; credentials are never URL params.
// ServeContent supplies HEAD, Range and If-Range semantics for resumable pulls.
func (m *ResidentMonitor) AgentArtifact(w http.ResponseWriter, r *http.Request) {
	authCtx, cancelAuth := context.WithTimeout(r.Context(), 10*time.Second)
	_, err := m.authenticate(authCtx, r)
	cancelAuth()
	if err != nil {
		agentAuthError(w, err)
		return
	}
	select {
	case m.artifactSlots <- struct{}{}:
		defer func() { <-m.artifactSlots }()
	default:
		w.Header().Set("Retry-After", "30")
		http.Error(w, "artifact transfers busy", http.StatusServiceUnavailable)
		return
	}
	digest := chi.URLParam(r, "sha256")
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || digest != strings.ToLower(digest) || m.deployer == nil {
		http.NotFound(w, r)
		return
	}
	manifest, err := m.deployer.Manifest()
	if err != nil {
		http.Error(w, "artifact unavailable", http.StatusServiceUnavailable)
		return
	}
	for _, artifact := range manifest.Agents {
		if artifact.SHA256 != digest {
			continue
		}
		path := filepath.Join(m.deployer.artifactsDir, artifact.File)
		f, err := os.Open(path)
		if err != nil {
			break
		}
		defer f.Close()
		if strings.Contains(r.Header.Get("Range"), ",") {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", artifact.Size))
			http.Error(w, "one byte range is supported", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		info, err := m.deployer.bundleCache.verify(f, path, artifact)
		if err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), agentArtifactTransferBudget)
		defer cancel()
		writer := &artifactResponseWriter{ResponseWriter: w, ctx: ctx}
		writer.refreshDeadline()
		w.Header().Set("ETag", `"`+digest+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "private, no-transform")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(writer, r.WithContext(ctx), artifact.File, info.ModTime(), f)
		return
	}
	http.NotFound(w, r)
}

// A slow reader gets a bounded idle interval as well as a total transfer bound.
// Unwrap preserves ResponseController support through existing middleware.
type artifactResponseWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *artifactResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *artifactResponseWriter) refreshDeadline() {
	deadline := time.Now().Add(30 * time.Second)
	if total, ok := w.ctx.Deadline(); ok && total.Before(deadline) {
		deadline = total
	}
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}
func (w *artifactResponseWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	w.refreshDeadline()
	return w.ResponseWriter.Write(p)
}
