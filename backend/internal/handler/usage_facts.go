package handler

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

func usageResource(r *http.Request, resourceType string, id uint, name string, serverID *uint) {
	usage.FromContext(r.Context()).SetResource(resourceType, &id, name, serverID)
}

func usageCommitted(r *http.Request) {
	usage.FromContext(r.Context()).SetResult("succeeded", "")
}

// Security auditing is independent of usage capture admission and persistence.
// Only identifiers and verified identity enter the audit queue.
func writeRevealAudit(r *http.Request, repo *repository.AuditEventRepo, action, resourceType string, resourceID uint) {
	p := mw.GetPrincipal(r.Context())
	if p.AuthType != "jwt" && p.AuthType != "api_key" {
		return
	}
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if net.ParseIP(address) == nil {
		address = ""
	}
	username := p.Username
	if p.AuthType == "api_key" {
		username = p.APIKeyName
	}
	event := &model.AuditEvent{UserID: valueUint(p.UserID), Username: username, Action: action, ResourceType: resourceType, ResourceID: resourceID, IPAddress: address}
	op := usage.FromContext(r.Context())
	if op != nil {
		_ = op.WriteAudit(r.Context(), event)
		return
	}
	// Compatibility for handlers mounted without the capture middleware. These
	// are still new events and must never enter the legacy backfill set.
	id := usage.NewOperation(action, "", "", r.Method, address).OperationID()
	event.OperationID = &id
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if repo == nil || repo.Create(ctx, event) != nil {
		slog.Warn("security audit write failed", "operation_id", id, "action", action, "reason", "audit_unavailable")
	}
}
