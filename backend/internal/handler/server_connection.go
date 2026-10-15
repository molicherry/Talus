package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
)

type serverConnectionTester interface {
	TestConnection(context.Context, uint) (*service.ConnectionTestResult, error)
}

// ServerConnectionHandler tests saved routes without accepting arbitrary addresses.
type ServerConnectionHandler struct {
	tester serverConnectionTester
}

func NewServerConnectionHandler(tester serverConnectionTester) *ServerConnectionHandler {
	return &ServerConnectionHandler{tester: tester}
}

func (h *ServerConnectionHandler) Test(w http.ResponseWriter, r *http.Request) {
	if _, allowed := usageAdmin(r); !allowed {
		server.WriteError(w, r, server.ErrForbidden)
		return
	}
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID))
		return
	}
	op := usage.FromContext(r.Context())
	op.SetResource("server", &id, "", &id)
	op.Begin()
	result, err := h.tester.TestConnection(r.Context(), id)
	if err != nil {
		reason := server.ReasonSSHConnection
		var appErr *server.AppError
		if errors.As(err, &appErr) {
			reason = appErr.Reason
		}
		if errors.Is(err, context.Canceled) {
			op.SetResult("cancelled", "client_cancelled")
		} else {
			op.SetResult("failed", reason)
		}
		server.WriteError(w, r, err)
		return
	}
	op.SetResult("succeeded", "")
	server.WriteJSON(w, http.StatusOK, result)
}
