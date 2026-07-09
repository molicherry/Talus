package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/service"
)

// execRequest is the JSON body for command execution.
type execRequest struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// ExecHandler exposes the command execution endpoint.
type ExecHandler struct {
	sshSvc *service.SSHService
}

// NewExecHandler creates an ExecHandler with the given SSH service.
func NewExecHandler(sshSvc *service.SSHService) *ExecHandler {
	return &ExecHandler{sshSvc: sshSvc}
}

// Execute handles POST /api/v1/servers/{id}/exec.
func (h *ExecHandler) Execute(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
		return
	}

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	if req.Command == "" {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "command is required"))
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}

	result, err := h.sshSvc.Exec(r.Context(), id, req.Command, time.Duration(timeout)*time.Second)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}

	server.WriteJSON(w, http.StatusOK, result)
}
