package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
)

type SFTPHandler struct{ svc *service.SFTPService }

func NewSFTPHandler(svc *service.SFTPService) *SFTPHandler { return &SFTPHandler{svc: svc} }
func sftpServerID(w http.ResponseWriter, r *http.Request) (uint, bool) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := parseIDParam(r)
	if err != nil || id == 0 {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID))
		return 0, false
	}
	usage.FromContext(r.Context()).SetResource("server", &id, "", &id)
	if !mw.CheckServerAccess(mw.GetUserClaims(r.Context()), id) {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonAPIKeyServerDenied))
		return 0, false
	}
	return id, true
}
func sftpResult(w http.ResponseWriter, r *http.Request, data any, err error) {
	op := usage.FromContext(r.Context())
	if err != nil {
		var app *server.AppError
		reason := "sftp_failed"
		if errors.Is(err, context.Canceled) {
			op.SetResult("cancelled", "client_cancelled")
			err = server.NewAppError(499, "client_cancelled")
		} else {
			if errors.Is(err, context.DeadlineExceeded) {
				err = server.ErrSSHTimeout
			}
			if errors.As(err, &app) {
				reason = app.Reason
			}
			op.SetResult("failed", reason)
		}
		server.WriteError(w, r, err)
		return
	}
	op.SetResult("succeeded", "")
	server.WriteJSON(w, http.StatusOK, data)
}
func (h *SFTPHandler) List(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	data, err := h.svc.List(r.Context(), id, r.URL.Query().Get("path"))
	sftpResult(w, r, data, err)
}
func (h *SFTPHandler) Stat(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	data, err := h.svc.Stat(r.Context(), id, r.URL.Query().Get("path"))
	sftpResult(w, r, data, err)
}

type sftpIdleReader struct {
	body       io.ReadCloser
	controller *http.ResponseController
	deadline   time.Time
}

func (b *sftpIdleReader) Read(p []byte) (int, error) {
	_ = b.controller.SetReadDeadline(minDeadline(b.deadline, time.Now().Add(30*time.Second)))
	return b.body.Read(p)
}
func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (h *SFTPHandler) Upload(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	size := r.ContentLength
	if raw := r.URL.Query().Get("size"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			sftpResult(w, r, nil, service.SFTPError("sftp_size_mismatch"))
			return
		}
		size = parsed
	}
	if size < 0 || size > service.MaxSFTPFileBytes {
		sftpResult(w, r, nil, service.SFTPError("sftp_too_large"))
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != size {
		sftpResult(w, r, nil, service.SFTPError("sftp_size_mismatch"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SFTPTransferBudget)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	r.Body = http.MaxBytesReader(w, r.Body, size+1)
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetReadDeadline(time.Now()); _ = r.Body.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	usage.FromContext(r.Context()).Begin()
	err := h.svc.Upload(ctx, id, r.URL.Query().Get("path"), &sftpIdleReader{r.Body, controller, deadline}, size, r.URL.Query().Get("overwrite") == "true")
	if err == nil {
		usage.FromContext(r.Context()).SetMetadata("bytes_copied", size)
	}
	sftpResult(w, r, map[string]any{"size": size}, err)
}

type sftpIdleWriter struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
	deadline   time.Time
}

func (w *sftpIdleWriter) Write(p []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(minDeadline(w.deadline, time.Now().Add(30*time.Second)))
	return w.writer.Write(p)
}
func (h *SFTPHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SFTPTransferBudget)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	usage.FromContext(r.Context()).Begin()
	started := false
	err := h.svc.DownloadRange(ctx, id, r.URL.Query().Get("path"), service.SFTPDownloadRequest{Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range")}, func(download service.SFTPDownload) (io.Writer, error) {
		entry := download.Entry
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(entry.Path)}))
		w.Header().Set("Content-Length", strconv.FormatInt(download.Length, 10))
		w.Header().Set("ETag", download.ETag)
		w.Header().Set("Accept-Ranges", "bytes")
		status := http.StatusOK
		if download.Partial {
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", download.Start, download.Start+download.Length-1, download.Total))
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		started = true
		return &sftpIdleWriter{w, controller, deadline}, nil
	})
	if !started {
		sftpResult(w, r, nil, err)
		return
	}
	op := usage.FromContext(r.Context())
	if err != nil {
		op.SetResult("failed", "sftp_failed")
		if ctx.Err() != nil {
			op.SetResult("cancelled", "client_cancelled")
		}
		panic(http.ErrAbortHandler)
	}
	op.SetResult("succeeded", "")
}
func readSFTPBody(w http.ResponseWriter, r *http.Request, dst any, limits ...int64) bool {
	limit := int64(32 << 10)
	if len(limits) > 0 {
		limit = limits[0]
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return false
	}
	return true
}
func (h *SFTPHandler) Mkdir(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Parents bool   `json:"parents,omitempty"`
	}
	if !readSFTPBody(w, r, &req) {
		return
	}
	sftpResult(w, r, map[string]bool{"success": true}, h.svc.Mkdir(r.Context(), id, req.Path, req.Parents))
}
func (h *SFTPHandler) Rename(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !readSFTPBody(w, r, &req) {
		return
	}
	sftpResult(w, r, map[string]bool{"success": true}, h.svc.Rename(r.Context(), id, req.From, req.To))
}
func (h *SFTPHandler) Remove(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	sftpResult(w, r, map[string]bool{"success": true}, h.svc.Remove(r.Context(), id, r.URL.Query().Get("path")))
}

func (h *SFTPHandler) ReadText(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	data, err := h.svc.ReadText(r.Context(), id, r.URL.Query().Get("path"))
	sftpResult(w, r, data, err)
}
func (h *SFTPHandler) SaveText(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Version string `json:"version"`
	}
	if !readSFTPBody(w, r, &req, 6*service.MaxSFTPTextBytes+32<<10) {
		return
	}
	data, err := h.svc.SaveText(r.Context(), id, req.Path, req.Content, req.Version)
	sftpResult(w, r, data, err)
}
func (h *SFTPHandler) StartUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path      string `json:"path"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
		Overwrite bool   `json:"overwrite"`
	}
	if !readSFTPBody(w, r, &req) {
		return
	}
	usage.FromContext(r.Context()).Begin()
	data, err := h.svc.StartUpload(r.Context(), id, req.Path, req.Size, req.SHA256, req.Overwrite)
	sftpResult(w, r, data, err)
}
func (h *SFTPHandler) UploadStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !readSFTPBody(w, r, &req) {
		return
	}
	data, err := h.svc.UploadStatus(r.Context(), id, req.Token)
	sftpResult(w, r, data, err)
}
func (h *SFTPHandler) CompleteUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !readSFTPBody(w, r, &req) {
		return
	}
	usage.FromContext(r.Context()).Begin()
	sftpResult(w, r, map[string]bool{"success": true}, h.svc.CompleteUpload(r.Context(), id, req.Token))
}
func (h *SFTPHandler) CancelUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	sftpResult(w, r, map[string]bool{"success": true}, h.svc.CancelUpload(r.Context(), id, r.Header.Get("X-Upload-Token")))
}
func (h *SFTPHandler) UploadChunk(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		sftpResult(w, r, nil, service.SFTPError("sftp_invalid_upload"))
		return
	}
	size := r.ContentLength
	if raw := r.URL.Query().Get("size"); raw != "" {
		size, err = strconv.ParseInt(raw, 10, 64)
	}
	if err != nil || size <= 0 || size > service.MaxSFTPChunkBytes || (r.ContentLength >= 0 && r.ContentLength != size) {
		sftpResult(w, r, nil, service.SFTPError("sftp_invalid_upload"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SFTPTransferBudget)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	r.Body = http.MaxBytesReader(w, r.Body, size+1)
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetReadDeadline(time.Now()); _ = r.Body.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	usage.FromContext(r.Context()).Begin()
	data, err := h.svc.UploadChunk(ctx, id, r.Header.Get("X-Upload-Token"), offset, size, &sftpIdleReader{r.Body, controller, deadline})
	if err == nil {
		usage.FromContext(r.Context()).SetMetadata("bytes_copied", size)
	}
	sftpResult(w, r, data, err)
}
func (h *SFTPHandler) Archive(w http.ResponseWriter, r *http.Request) {
	id, ok := sftpServerID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SFTPTransferBudget)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	usage.FromContext(r.Context()).Begin()
	started := false
	err := h.svc.Archive(ctx, id, r.URL.Query().Get("path"), func(name string) (io.Writer, error) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		started = true
		return &sftpIdleWriter{w, controller, deadline}, nil
	})
	if !started {
		sftpResult(w, r, nil, err)
		return
	}
	if err != nil {
		usage.FromContext(r.Context()).SetResult("failed", "sftp_failed")
		panic(http.ErrAbortHandler)
	}
	usage.FromContext(r.Context()).SetResult("succeeded", "")
}
