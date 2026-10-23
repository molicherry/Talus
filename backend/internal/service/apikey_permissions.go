package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

func requireAPIKeyAdmin(ctx context.Context) error {
	p := mw.GetPrincipal(ctx)
	if p.AuthType != "jwt" || p.Role != "admin" || p.UserID == nil || *p.UserID == 0 {
		return server.ErrForbidden
	}
	return nil
}

// These are separate instances from the JWT gate/registry. Their subject IDs
// are APIKeyIDs, never owner UserIDs (which can be absent on historical keys).
func (s *APIKeyService) PermissionsGate() *gate.Gate      { return s.permissionsGate }
func (s *APIKeyService) TerminalRegistry() *gate.Registry { return s.terminals }

type UpdateAPIKeyPermissionsInput struct {
	ExpectedRevision int64
	Scopes           *[]string
	ServerIDs        *[]uint
}

func samePermissionSet[T comparable](a, b []T) bool {
	set := make(map[T]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	other := make(map[T]bool, len(b))
	for _, v := range b {
		if !set[v] {
			return false
		}
		other[v] = true
	}
	return len(set) == len(other)
}

func uniquePermissions[T comparable](values []T) []T {
	out := make([]T, 0, len(values))
	seen := make(map[T]bool, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (s *APIKeyService) UpdatePermissions(ctx context.Context, id uint, input UpdateAPIKeyPermissionsInput) (*model.APIKey, error) {
	if err := requireAPIKeyAdmin(ctx); err != nil {
		return nil, err
	}
	if input.ExpectedRevision <= 0 || (input.Scopes == nil && input.ServerIDs == nil) {
		return nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
	}
	if input.Scopes != nil {
		if len(*input.Scopes) == 0 {
			return nil, server.NewAppError(http.StatusBadRequest, server.ReasonScopesRequired)
		}
		if invalid := mw.ValidateScopes(*input.Scopes); len(invalid) > 0 {
			return nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidScopes)
		}
	}
	p := mw.GetPrincipal(ctx)
	operationID := usage.FromContext(ctx).OperationID()
	if operationID == "" {
		operationID = usage.NewOperation("api_key.permissions.update", "", "", "PATCH", "").OperationID()
	}
	var updated *model.APIKey
	var revoked []*gate.Session
	err := s.permissionsGate.Update(ctx, id, func(updateCtx context.Context, _ int64) (bool, error) {
		var changed bool
		var err error
		updated, changed, err = s.repo.UpdatePermissions(updateCtx, id, func(tx *gorm.DB, key *model.APIKey) (*model.AuditEvent, error) {
			if key.PermissionsRevision != input.ExpectedRevision {
				return nil, server.NewAppError(http.StatusConflict, server.ReasonAPIKeyRevisionConflict)
			}
			if key.PermissionsRevision == math.MaxInt64 {
				return nil, server.NewAppError(http.StatusConflict, server.ReasonAPIKeyRevisionConflict)
			}
			type policy struct {
				Scopes    []string `json:"scopes"`
				ServerIDs []uint   `json:"server_ids"`
				Revision  string   `json:"revision"`
			}
			before := policy{key.Scopes, key.ServerIDs, strconv.FormatInt(key.PermissionsRevision, 10)}
			if input.Scopes != nil {
				key.Scopes = uniquePermissions(*input.Scopes)
				sort.Strings(key.Scopes)
			}
			if input.ServerIDs != nil {
				ids := uniquePermissions(*input.ServerIDs)
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				old := make(map[uint]bool, len(key.ServerIDs))
				for _, v := range key.ServerIDs {
					old[v] = true
				}
				var added []uint
				for _, v := range ids {
					if v == 0 || uint64(v) > math.MaxInt64 {
						return nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID)
					}
					if !old[v] {
						added = append(added, v)
					}
				}
				if len(added) > 0 {
					servers, err := repository.NewServerRepo(tx).FindByIDs(updateCtx, added)
					if err != nil {
						return nil, err
					}
					if len(servers) != len(added) {
						return nil, server.NewAppError(http.StatusBadRequest, server.ReasonServersNotFound)
					}
				}
				key.ServerIDs = ids
			}
			if samePermissionSet(before.Scopes, key.Scopes) && samePermissionSet(before.ServerIDs, key.ServerIDs) {
				key.Scopes, key.ServerIDs = before.Scopes, before.ServerIDs
				return nil, nil
			}
			details, err := json.Marshal(struct {
				Before policy `json:"before"`
				After  policy `json:"after"`
			}{before, policy{key.Scopes, key.ServerIDs, strconv.FormatInt(key.PermissionsRevision+1, 10)}})
			if err != nil || len(details) > 256*1024 {
				return nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
			}
			usage.FromContext(ctx).SetResource("api_key", &id, key.Name, nil)
			return &model.AuditEvent{OperationID: &operationID, UserID: *p.UserID, Username: p.Username, Action: "api_key.permissions.update", ResourceType: "api_key", ResourceID: id, Details: string(details)}, nil
		})
		unknown := errors.Is(err, repository.ErrAPIKeyCommitUnknown)
		if changed {
			op := usage.FromContext(ctx)
			op.SetMetadata("permissions_revision_before", strconv.FormatInt(input.ExpectedRevision, 10))
			op.SetMetadata("permissions_revision_after", strconv.FormatInt(updated.PermissionsRevision, 10))
		}
		if changed || unknown {
			revoked = s.terminals.MarkRevoked(id)
		}
		return unknown, err
	})
	s.finishPermissionChange(ctx, id, revoked, err)
	if err != nil {
		return nil, apiKeyPolicyError(err)
	}
	return updated, nil
}

func (s *APIKeyService) finishPermissionChange(ctx context.Context, id uint, revoked []*gate.Session, err error) {
	if errors.Is(err, repository.ErrAPIKeyCommitUnknown) {
		// Recovery is bounded by the gate, independent of HTTP cancellation. A
		// failed recovery retains the fence for the next admission to retry.
		go func() { _ = s.permissionsGate.Recover(context.WithoutCancel(ctx), id) }()
	}
	if len(revoked) > 0 {
		result := s.terminals.Await(context.WithoutCancel(ctx), revoked)
		if !result.Complete {
			usage.FromContext(ctx).SetMetadata("cleanup_pending", true)
			slog.Warn("API key terminal cleanup pending", "api_key_id", id, "pending", result.Pending)
		}
	}
}

func apiKeyPolicyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return server.NewAppError(http.StatusNotFound, server.ReasonAPIKeyNotFound)
	}
	var app *server.AppError
	if errors.As(err, &app) {
		return app
	}
	// Do not attach driver errors, which may contain stored row values.
	return server.NewAppError(http.StatusServiceUnavailable, server.ReasonUnavailable)
}
