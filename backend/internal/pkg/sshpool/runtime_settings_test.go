package sshpool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/config"
)

func TestRuntimeSSHSlotWaitUsesUpdatedTimeout(t *testing.T) {
	p := NewPool(time.Minute, 1, time.Hour)
	defer p.Close()
	r := config.NewRuntime(config.Config{SSHTimeout: 30})
	p.SetRuntimeConfig(r)
	lease, err := p.AcquireContext(context.Background(), 1, "route")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	r.Store(config.Config{SSHTimeout: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.AcquireContext(ctx, 1, "route"); !errors.Is(err, ErrSlotTimeout) {
		t.Fatalf("new slot wait kept old timeout: %v", err)
	}
}
