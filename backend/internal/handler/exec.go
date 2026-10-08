package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
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
	op := usage.FromContext(r.Context())
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID))
		return
	}
	op.SetResource("server", &id, "", &id)

	claims := mw.GetUserClaims(r.Context())
	if !mw.CheckServerAccess(claims, id) {
		op.SetResult("rejected", server.ReasonAPIKeyServerDenied)
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonAPIKeyServerDenied))
		return
	}

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}

	if req.Command == "" {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonCommandRequired))
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}
	op.SetMetadata("timeout_seconds", timeout)
	op.Begin()

	result, err := h.sshSvc.Exec(r.Context(), id, req.Command, time.Duration(timeout)*time.Second)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			op.SetResult("cancelled", "client_cancelled")
		} else {
			reason := server.ReasonSSHConnection
			var appErr *server.AppError
			if errors.As(err, &appErr) {
				reason = appErr.Reason
			}
			op.SetResult("failed", reason)
		}
		server.WriteError(w, r, err)
		return
	}
	op.SetExecResult(&result.ExitCode)
	if result.ExitCode != 0 {
		op.SetResult("failed", "ssh_nonzero_exit")
	} else {
		op.SetResult("succeeded", "")
	}

	server.WriteJSON(w, http.StatusOK, result)
}
