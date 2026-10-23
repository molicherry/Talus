package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

type SystemSettingsHandler struct {
	svc   *service.SystemSettingsService
	usage *repository.UsageLogRepo
}

func NewSystemSettingsHandler(s *service.SystemSettingsService, u *repository.UsageLogRepo) *SystemSettingsHandler {
	return &SystemSettingsHandler{svc: s, usage: u}
}

func settingsAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := mw.GetPrincipal(r.Context())
	if p.AuthType != "jwt" || p.Role != "admin" {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonForbidden))
		return false
	}
	return true
}

func (h *SystemSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !settingsAdmin(w, r) {
		return
	}
	view, err := h.svc.Get(r.Context())
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusServiceUnavailable, server.ReasonUnavailable))
		return
	}
	migration := map[string]any{"state": "waiting"}
	if h.svc.Runtime().Current().UsageLegacyWritersDrained {
		migration["state"] = "pending"
	}
	if h.usage != nil {
		state, err := h.usage.BackfillStatus(r.Context())
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			migration["state"] = "unavailable"
		}
		if err == nil {
			migration["imported"] = state.Imported
			if state.CompletedAt != nil {
				migration["state"] = "completed"
			}
		}
	}
	server.WriteJSON(w, http.StatusOK, struct {
		service.SettingsView
		Migration map[string]any `json:"migration"`
	}{view, migration})
}

func (h *SystemSettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !settingsAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req service.SettingsUpdate
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}
	p := mw.GetPrincipal(r.Context())
	audit := model.AuditEvent{Username: p.Username, IPAddress: r.RemoteAddr}
	if p.UserID != nil {
		audit.UserID = *p.UserID
	}
	op := usage.FromContext(r.Context())
	id := uint(1)
	op.SetResource("settings", &id, "", nil)
	op.Begin()
	if id := op.OperationID(); id != "" {
		audit.OperationID = &id
	}
	view, err := h.svc.Update(r.Context(), req, audit)
	if err != nil {
		code, reason := http.StatusServiceUnavailable, server.ReasonUnavailable
		switch {
		case errors.Is(err, repository.ErrSettingsConflict):
			code, reason = http.StatusConflict, server.ReasonConflict
		case errors.Is(err, service.ErrSettingsInvalid):
			code, reason = http.StatusUnprocessableEntity, server.ReasonSettingsInvalid
		case errors.Is(err, service.ErrSettingsRetentionConfirmation):
			code, reason = http.StatusConflict, server.ReasonSettingsRetentionConfirmation
		}
		server.WriteError(w, r, server.NewAppError(code, reason))
		return
	}
	op.SetResult("succeeded", "")
	server.WriteJSON(w, http.StatusOK, view)
}
