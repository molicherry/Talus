package service

import (
	"strings"
	"testing"
	"time"
)

func TestTerminalSessionMessageBudget(t *testing.T) {
	for _, protocol := range []string{"", TerminalBinaryProtocol} {
		name := protocol
		if name == "" {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			svc, pool := startSeededTerminalService(t, 1)
			// This enters the service without a browser authentication frame,
			// as API-key clients do, and proves the service enforces its own cap.
			h := beginSessionWithProtocol(t, svc, 1, nil, protocol)
			_ = h.ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = h.ws.WriteJSON(wsMessage{Type: "input", Data: strings.Repeat("x", int(TerminalMessageMaxBytes))})
			if err := h.wait(t); err != nil {
				t.Fatalf("oversize terminal cleanup did not finish: %v", err)
			}
			facts := <-h.facts
			if facts.Outcome != "failed" || facts.Reason != "terminal_transport_failed" || facts.CleanupPending {
				t.Fatalf("oversize session outcome = %+v", facts)
			}
			// Resource ownership must be returned even after a policy close.
			client, err := pool.Get(1, testFP(1))
			if err != nil {
				t.Fatalf("terminal capacity was not released: %v", err)
			}
			pool.Release(1, client)
		})
	}
}
