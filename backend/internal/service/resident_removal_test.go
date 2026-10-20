package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResidentRemovalWaitsForInstallerAndFencesStaleSnapshots(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	installCanceled := make(chan struct{})
	installDone := make(chan struct{})
	m.installJobs[7] = residentJob{cancel: func() { close(installCanceled) }, done: installDone}
	epoch := m.installEpoch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepared := make(chan preparedAgentRemoval, 1)
	go func() { prepared <- m.PrepareAgentRemoval(ctx, 7) }()
	select {
	case <-installCanceled:
	case <-time.After(time.Second):
		t.Fatal("removal did not cancel the in-flight installer")
	}
	if m.installAllowed(7, epoch) {
		t.Fatal("removal admitted an installation from an old snapshot")
	}
	select {
	case r := <-prepared:
		r.Close()
		t.Fatal("preparation returned before the installer finished")
	default:
	}
	// Exhausting the preparation budget must return without racing a still
	// active installer with remote cleanup, while retaining the fence until
	// the caller has accepted or rejected the database deletion.
	cancel()
	var removal preparedAgentRemoval
	select {
	case removal = <-prepared:
	case <-time.After(time.Second):
		t.Fatal("preparation ignored cancellation while joining the installer")
	}
	if !errors.Is(removal.(*residentRemoval).err, context.Canceled) {
		t.Fatalf("preparation error = %v", removal.(*residentRemoval).err)
	}
	m.installJobsMu.Lock()
	fencedEpoch := m.installEpoch
	m.installJobsMu.Unlock()
	if m.installAllowed(7, fencedEpoch) {
		t.Fatal("timed-out preparation released its fence too soon")
	}
	removal.Close()
	removal.Close()
	m.installJobsMu.Lock()
	currentEpoch := m.installEpoch
	m.installJobsMu.Unlock()
	if m.installAllowed(7, epoch) || m.installAllowed(7, fencedEpoch) {
		t.Fatal("closing a removal revived a stale reconciliation snapshot")
	}
	if !m.installAllowed(7, currentEpoch) {
		t.Fatal("rejected deletion did not allow a fresh reconciliation")
	}
}

func TestResidentRemovalSerializesConcurrentPreparation(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	finish, _, err := m.fenceRemoval(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.fenceRemoval(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("concurrent preparation did not respect its wait budget: %v", err)
	}
	m.installJobsMu.Lock()
	epoch := m.installEpoch
	m.installJobsMu.Unlock()
	if m.installAllowed(7, epoch) {
		t.Fatal("a canceled concurrent preparation lifted the original fence")
	}
	finish()
	nextFinish, _, err := m.fenceRemoval(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	nextFinish()
}
