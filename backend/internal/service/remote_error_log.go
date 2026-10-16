package service

import (
	"errors"
	"log/slog"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
)

// Remote errors may contain credentials, request bytes or remote output. Project
// only the closed diagnostic vocabulary at the log boundary; neither Error()
// strings nor arbitrary application-error parameters are safe log fields.
func remoteFailureLogAttrs(err error) []slog.Attr {
	_, reasonClass := collectionOutcome(outcomeFailure, err)
	attrs := []slog.Attr{slog.String("reason_class", reasonClass)}
	var reason, stage, role string
	var app *server.AppError
	var route *sshpool.RouteError
	var primitive *StageError
	switch {
	case errors.As(err, &app):
		reason = app.Reason
		stage, _ = app.Params["stage"].(string)
		role, _ = app.Params["role"].(string)
	case errors.As(err, &route):
		reason, stage, role = route.Reason, route.Stage, route.Role
	case errors.As(err, &primitive):
		stage = string(primitive.Stage)
	}
	if value := remoteLogReason(reason); value != "" {
		attrs = append(attrs, slog.String("reason", value))
	}
	if value := remoteLogStage(stage); value != "" {
		attrs = append(attrs, slog.String("stage", value))
	}
	switch role {
	case "target":
		attrs = append(attrs, slog.String("role", "target"))
	case "jump":
		attrs = append(attrs, slog.String("role", "jump"))
	case "proxy":
		attrs = append(attrs, slog.String("role", "proxy"))
	}
	return attrs
}

func remoteLogReason(reason string) string {
	switch reason {
	case server.ReasonSSHConnection:
		return server.ReasonSSHConnection
	case server.ReasonSSHAuth:
		return server.ReasonSSHAuth
	case server.ReasonSSHTimeout:
		return server.ReasonSSHTimeout
	case server.ReasonSSHHostKeyMismatch:
		return server.ReasonSSHHostKeyMismatch
	case server.ReasonProxyConnection:
		return server.ReasonProxyConnection
	case server.ReasonProxyAuth:
		return server.ReasonProxyAuth
	case server.ReasonProxyConnectRejected:
		return server.ReasonProxyConnectRejected
	case server.ReasonJumpForwardFailed:
		return server.ReasonJumpForwardFailed
	default:
		return ""
	}
}

func remoteLogStage(stage string) string {
	switch stage {
	case "target_connect":
		return "target_connect"
	case "target_auth":
		return "target_auth"
	case "target_host_key":
		return "target_host_key"
	case "jump_connect":
		return "jump_connect"
	case "jump_auth":
		return "jump_auth"
	case "jump_host_key":
		return "jump_host_key"
	case "jump_forward":
		return "jump_forward"
	case "proxy_connect":
		return "proxy_connect"
	case "proxy_auth":
		return "proxy_auth"
	case "proxy_connect_request":
		return "proxy_connect_request"
	case string(StageQuota):
		return string(StageQuota)
	case string(StageDial):
		return string(StageDial)
	case string(StageHostKey):
		return string(StageHostKey)
	case string(StageAuth):
		return string(StageAuth)
	case string(StageSession):
		return string(StageSession)
	case string(StageTransfer):
		return string(StageTransfer)
	case string(StageRemoteExit):
		return string(StageRemoteExit)
	case string(StageTimeout):
		return string(StageTimeout)
	case string(StageCanceled):
		return string(StageCanceled)
	case string(StageProtocol):
		return string(StageProtocol)
	case string(StageIntegrity):
		return string(StageIntegrity)
	case string(StagePermission):
		return string(StagePermission)
	default:
		return ""
	}
}
