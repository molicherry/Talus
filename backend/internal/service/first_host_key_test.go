package service

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	appcrypto "github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

func TestFirstHostKeyConfirmationLifecycle(t *testing.T) {
	service, repo, db, ctx, _ := connectionServiceFixture(t)
	address, key := startTestSSHServer(t)
	host, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	salt := []byte("0123456789abcdef")
	password, err := appcrypto.Encrypt([]byte("secret"), service.masterKey.DeriveKey(salt))
	if err != nil {
		t.Fatal(err)
	}
	credential := &model.SSHCredential{Name: "first-use", Username: "test", AuthType: "password", EncryptedPassword: &password, Salt: salt}
	if err := db.Create(credential).Error; err != nil {
		t.Fatal(err)
	}
	srv, err := service.Create(ctx, &model.Server{Name: "first-use", Host: host, Port: port, OwnerID: mw.GetUserClaims(ctx).UserID, CredentialID: &credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.HostKeyUnverified || srv.HostKeyMismatch || srv.HostKeyFingerprint != nil {
		t.Fatalf("new server not marked unverified: %+v", srv)
	}
	sshSvc := NewSSHService(newTestPool(t), repo, &CredentialService{masterKey: service.masterKey}, time.Second, time.Second)
	service.pool = sshSvc.pool

	result, err := sshSvc.TestConnection(ctx, srv.ID)
	if result != nil || !errors.Is(err, server.ErrSSHHostKeyUnverified) {
		t.Fatalf("discovery = %+v, %v", result, err)
	}
	pending, err := service.Get(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sshpool.Fingerprint(key.Marshal())
	if pending.HostKey != nil || pending.HostKeySeenFingerprint == nil || *pending.HostKeySeenFingerprint != fingerprint || !pending.HostKeyUnverified || pending.HostKeyMismatch || pending.ConnectionRevision != 2 {
		t.Fatalf("pending key was trusted or not exposed: %+v", pending)
	}
	summaries, err := service.ListSummaries(ctx)
	if err != nil || len(summaries) != 1 || !summaries[0].HostKeyUnverified || summaries[0].HostKeyMismatch {
		t.Fatalf("pending summary = %+v, %v", summaries, err)
	}
	// Repeated background attempts record no new revisions and never execute.
	_, err = sshSvc.AcquireLease(ctx, srv.ID, LeaseConsumerAgent)
	if !errors.Is(err, server.ErrSSHHostKeyUnverified) {
		t.Fatalf("background task bypassed trust: %v", err)
	}
	repeated, _ := repo.FindByID(ctx, srv.ID)
	if repeated.ConnectionRevision != pending.ConnectionRevision {
		t.Fatal("same candidate advanced revision repeatedly")
	}

	apiKey := mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "api_key", Role: "admin"})
	user := mw.WithUserClaims(context.Background(), &token.Claims{UserID: 2, Role: "user"})
	for _, unprivileged := range []context.Context{context.Background(), apiKey, user} {
		_, err = service.TrustHostKey(unprivileged, srv.ID, fingerprint, pending.ConnectionRevision)
		expectConnectionReason(t, err, server.ReasonForbidden)
	}
	_, err = service.TrustHostKey(ctx, srv.ID, "SHA256:stale", pending.ConnectionRevision)
	expectConnectionReason(t, err, server.ReasonHostKeyChanged)
	_, err = service.TrustHostKey(ctx, srv.ID, fingerprint, pending.ConnectionRevision-1)
	expectConnectionReason(t, err, server.ReasonHostKeyChanged)

	// Two confirmations of the same screen must produce only one transition.
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.TrustHostKey(ctx, srv.ID, fingerprint, pending.ConnectionRevision)
			outcomes <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else {
			expectConnectionReason(t, err, server.ReasonNoHostKeyMismatch)
		}
	}
	if successes != 1 {
		t.Fatalf("successful approvals = %d", successes)
	}
	trusted, err := service.Get(ctx, srv.ID)
	if err != nil || trusted.HostKey == nil || !bytes.Equal(*trusted.HostKey, key.Marshal()) || trusted.HostKeySeen != nil || trusted.HostKeyMismatchAt != nil || trusted.HostKeyUnverified || trusted.HostKeyMismatch || trusted.ConnectionRevision != 3 {
		t.Fatalf("trusted state = %+v, %v", trusted, err)
	}
	result, err = sshSvc.TestConnection(ctx, srv.ID)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("trusted connection = %+v, %v", result, err)
	}
	summaries, err = service.ListSummaries(ctx)
	if err != nil || len(summaries) != 1 || summaries[0].HostKeyUnverified {
		t.Fatalf("trusted summary = %+v, %v", summaries, err)
	}

	// An endpoint edit invalidates both trusted and pending identity. A stale
	// browser cannot approve the previous endpoint's key, even with the same hash.
	changed, err := service.Update(ctx, srv.ID, &model.Server{Host: "new-endpoint.invalid"}, false)
	if err != nil || !changed.HostKeyUnverified || changed.HostKey != nil {
		t.Fatalf("endpoint edit retained trust: %+v, %v", changed, err)
	}
	_, err = service.TrustHostKey(ctx, srv.ID, fingerprint, pending.ConnectionRevision)
	expectConnectionReason(t, err, server.ReasonNoHostKeyMismatch)
}

func TestDecorateHostKeyPreservesExistingTrustAndTreatsEmptyKeyAsUnverified(t *testing.T) {
	for _, key := range []*[]byte{nil, ptrHostKey([]byte{})} {
		candidate := &model.Server{HostKey: key}
		decorateHostKey(candidate)
		if !candidate.HostKeyUnverified || candidate.HostKeyMismatch || candidate.HostKeyFingerprint != nil {
			t.Fatalf("empty key treated as trusted: %+v", candidate)
		}
	}
	key := []byte("existing-trusted-key")
	candidate := &model.Server{HostKey: &key, HostKeyUnverified: true}
	decorateHostKey(candidate)
	if candidate.HostKeyUnverified || candidate.HostKeyMismatch || candidate.HostKeyFingerprint == nil || *candidate.HostKeyFingerprint != sshpool.Fingerprint(key) {
		t.Fatalf("existing trust lost: %+v", candidate)
	}
}

func ptrHostKey(key []byte) *[]byte { return &key }
