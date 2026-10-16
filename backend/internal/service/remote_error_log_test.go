package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
)

const remoteErrorSecret = "private-proxy-password-in-remote-error"

func TestMonitorFailureLogsOnlySafeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want map[string]string
	}{
		{
			name: "opaque remote error",
			err:  errors.New(remoteErrorSecret),
			want: map[string]string{"reason_class": telReasonOther},
		},
		{
			name: "wrapped and joined proxy error",
			err: errors.Join(errors.New(remoteErrorSecret), fmt.Errorf("collection: %w", &sshpool.RouteError{
				Reason: server.ReasonProxyAuth, Stage: "proxy_auth", Role: "proxy", Cause: errors.New(remoteErrorSecret),
			})),
			want: map[string]string{"reason_class": telReasonOther, "reason": server.ReasonProxyAuth, "stage": "proxy_auth", "role": "proxy"},
		},
		{
			name: "application error with private message and parameters",
			err: &server.AppError{
				Reason: server.ReasonProxyAuth, Message: remoteErrorSecret, Err: errors.New(remoteErrorSecret),
				Params: map[string]any{"stage": "proxy_auth", "role": "proxy", "detail": remoteErrorSecret},
			},
			want: map[string]string{"reason_class": telReasonOther, "reason": server.ReasonProxyAuth, "stage": "proxy_auth", "role": "proxy"},
		},
		{
			name: "unrecognized diagnostic fields",
			err: &server.AppError{
				Reason: remoteErrorSecret, Message: remoteErrorSecret,
				Params: map[string]any{"stage": remoteErrorSecret, "role": remoteErrorSecret},
			},
			want: map[string]string{"reason_class": telReasonOther},
		},
		{
			name: "primitive stage error",
			err:  &StageError{Stage: StageTransfer, Err: errors.New(remoteErrorSecret)},
			want: map[string]string{"reason_class": telReasonOther, "stage": string(StageTransfer)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			svc := NewMonitorService(nil, nil, nil, time.Minute)
			svc.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			svc.taskFn = func(context.Context, uint) error { return tc.err }
			outcome, err := svc.runTask(context.Background(), 17)
			if outcome != outcomeFailure || err != tc.err {
				t.Fatal("the log projection changed the task's outcome or error identity")
			}
			record := safeRemoteLogRecord(t, logs.String(), "failed to collect metrics")
			if record["server_id"] != float64(17) {
				t.Fatal("the failure log lost its server identity")
			}
			assertRemoteLogDiagnostics(t, record, tc.want)
		})
	}
}

func TestMonitorPanicDoesNotLogPayload(t *testing.T) {
	var logs bytes.Buffer
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	svc.taskFn = func(context.Context, uint) error { panic(remoteErrorSecret) }
	outcome, err := svc.runTask(context.Background(), 17)
	if outcome != outcomeFailure || err == nil || strings.Contains(err.Error(), remoteErrorSecret) {
		t.Fatal("a recovered panic must return a failure without retaining its private payload")
	}
	record := safeRemoteLogRecord(t, logs.String(), "monitor goroutine panicked")
	assertRemoteLogDiagnostics(t, record, map[string]string{"reason_class": telReasonOther})
	if _, ok := record["panic"]; ok {
		t.Fatal("the panic payload reached the failure log")
	}
}

// cleanupFailureLease keeps the normal deployment protocol and injects an error
// only at its cleanup boundary, as a broken transport can do after an upload.
type cleanupFailureLease struct {
	Lease
	err    error
	called bool
}

func (l *cleanupFailureLease) Exec(ctx context.Context, command string, budget time.Duration) (*ExecResult, error) {
	if strings.HasPrefix(command, "rm -f ") {
		l.called = true
		return nil, l.err
	}
	return l.Lease.Exec(ctx, command, budget)
}

func TestAgentUploadCleanupLogsOnlySafeDiagnostics(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.uploadWriteThenFail = true
	var logs bytes.Buffer
	deployer.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	deployer.telemetry = newMonitorTelemetry(nil, deployer.logger)
	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	wrapped := &cleanupFailureLease{Lease: lease, err: fmt.Errorf("cleanup: %w", &sshpool.RouteError{
		Reason: server.ReasonProxyConnection, Stage: "proxy_connect", Role: "proxy", Cause: errors.New(remoteErrorSecret),
	})}
	if _, err := deployer.Collect(context.Background(), wrapped); err == nil || !wrapped.called {
		t.Fatal("the failed upload must reach its cleanup attempt")
	}
	record := safeRemoteLogRecord(t, logs.String(), "agent upload cleanup failed")
	assertRemoteLogDiagnostics(t, record, map[string]string{
		"reason_class": telReasonOther, "reason": server.ReasonProxyConnection, "stage": "proxy_connect", "role": "proxy",
	})
	if record["server_id"] != float64(1) || record["exit_code"] != float64(-1) {
		t.Fatal("the cleanup log lost its server identity or exit status")
	}
	if path, _ := record["path"].(string); !strings.HasPrefix(path, agentPath+".upload-") {
		t.Fatal("the cleanup log lost the generated temporary artifact path")
	}
}

func safeRemoteLogRecord(t *testing.T, output, message string) map[string]any {
	t.Helper()
	if strings.Contains(output, remoteErrorSecret) {
		t.Fatal("a remote error's private payload reached the log")
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if record["msg"] == message {
			if _, ok := record["error"]; ok {
				t.Fatal("a raw error field reached the failure log")
			}
			return record
		}
	}
	t.Fatalf("missing failure log %q", message)
	return nil
}

func assertRemoteLogDiagnostics(t *testing.T, record map[string]any, want map[string]string) {
	t.Helper()
	for _, key := range []string{"reason_class", "reason", "stage", "role"} {
		value, expected := want[key]
		actual, exists := record[key]
		if exists != expected || expected && actual != value {
			t.Fatalf("diagnostic field %q = %v, want %q (present=%t)", key, actual, value, expected)
		}
	}
}
