package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

func (s *APIKeyService) Rotate(ctx context.Context, id uint, expectedRevision int64) (*CreateAPIKeyResult, error) {
	if err := requireAPIKeyAdmin(ctx); err != nil {
		return nil, err
	}
	if expectedRevision <= 0 {
		return nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
	}
	result, err := s.generateKey()
	if err != nil {
		return nil, apiKeyPolicyError(err)
	}
	p := mw.GetPrincipal(ctx)
	op := usage.FromContext(ctx)
	operationID := op.OperationID()
	if operationID == "" {
		operationID = usage.NewOperation("api_key.rotate", "", "", "POST", "").OperationID()
	}
	var revoked []*gate.Session
	err = s.permissionsGate.UpdateWithBudget(ctx, id, apiKeyMutationBudget, func(updateCtx context.Context, _ int64) (bool, error) {
		key, err := s.repo.Rotate(updateCtx, id, result.APIKey, func(key *model.APIKey) (*model.AuditEvent, error) {
			if key.PermissionsRevision != expectedRevision || key.PermissionsRevision == math.MaxInt64 {
				return nil, server.NewAppError(http.StatusConflict, server.ReasonAPIKeyRevisionConflict)
			}
			details, err := json.Marshal(struct {
				Before string `json:"revision_before"`
				After  string `json:"revision_after"`
			}{strconv.FormatInt(expectedRevision, 10), strconv.FormatInt(expectedRevision+1, 10)})
			if err != nil {
				return nil, err
			}
			op.SetResource("api_key", &id, key.Name, nil)
			return &model.AuditEvent{OperationID: &operationID, UserID: *p.UserID, Username: p.Username, Action: "api_key.rotate", ResourceType: "api_key", ResourceID: id, Details: string(details)}, nil
		})
		unknown := errors.Is(err, repository.ErrAPIKeyCommitUnknown)
		if err == nil {
			result.APIKey = key
			op.SetMetadata("permissions_revision_before", strconv.FormatInt(expectedRevision, 10))
			op.SetMetadata("permissions_revision_after", strconv.FormatInt(key.PermissionsRevision, 10))
		}
		if err == nil || unknown {
			revoked = s.terminals.MarkRevoked(id)
		}
		return unknown, err
	})
	s.finishPermissionChange(ctx, id, revoked, err)
	if err != nil {
		// Never return an uncommitted or uncertain replacement secret.
		return nil, apiKeyPolicyError(err)
	}
	return result, nil
}
