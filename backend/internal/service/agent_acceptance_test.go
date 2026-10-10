package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

// ---------------------------------------------------------------------------
// REQ-05: a password change revokes the user's JWTs and terminals.
// ---------------------------------------------------------------------------

type agentAuthFixture struct {
	svc      *AuthService
	repo     *repository.UserRepo
	db       *gorm.DB
	user     *model.User
	registry *gate.Registry
	jwtSvc   *token.JWTService
}

func newAgentAuthFixture(t *testing.T) agentAuthFixture {
	t.Helper()
	db := authOwnerTestDB(t)
	repo := repository.NewUserRepo(db)
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "agent", PasswordHash: string(hash), Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	registry := gate.NewRegistry()
	jwtSvc := token.NewJWTService("agent-secret", time.Hour)
	svc := NewAuthService(repo, jwtSvc, db)
	svc.SetUserGate(agentRepoGate(repo))
	svc.SetTerminalRegistry(registry)
	return agentAuthFixture{svc: svc, repo: repo, db: db, user: user, registry: registry, jwtSvc: jwtSvc}
}

// agentRepoGate mirrors production wiring: the version is read from PostgreSQL.
func agentRepoGate(repo *repository.UserRepo) *gate.Gate {
	return gate.New(func(ctx context.Context, userID uint) (int64, error) {
		return repo.TokenVersion(ctx, userID)
	})
}

// agentJWTStatus runs one request through the real authentication middleware.
func agentJWTStatus(t *testing.T, jwtSvc *token.JWTService, g mw.UserGate, raw string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	mw.Auth(jwtSvc, nil, g)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
	return rec.Code
}

// TestAgentPasswordChangeRevokesJWTAndTerminals pins TC-05-01 and TC-05-04: the
// password update and the version bump commit atomically, a JWT issued before
// the change is rejected while a newly issued one is accepted, and a live
// terminal is revoked, cancelled and joined before ChangePassword returns.
func TestAgentPasswordChangeRevokesJWTAndTerminals(t *testing.T) {
	f := newAgentAuthFixture(t)
	ctx := context.Background()

	oldRaw, err := f.jwtSvc.GenerateToken(f.user.ID, f.user.Username, f.user.Role, 0)
	if err != nil {
		t.Fatal(err)
	}
	if code := agentJWTStatus(t, f.jwtSvc, agentRepoGate(f.repo), oldRaw); code != http.StatusOK {
		t.Fatalf("pre-change JWT status = %d, want 200", code)
	}

	var canceled bool
	var sess *gate.Session
	sess = f.registry.Register(f.user.ID, 0, func() { canceled = true; sess.MarkDone() }, nil)
	if !f.registry.Admit(sess.ID) {
		t.Fatal("the terminal should be admitted before the change")
	}

	if err := f.svc.ChangePassword(ctx, f.user.ID, "old-password", "new-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// TC-05-04: revoked, cancelled, joined, no live session left.
	if !canceled {
		t.Fatal("a live terminal must be cancelled on a password change")
	}
	if sess.State() != gate.StateRevoked {
		t.Fatalf("state = %q, want revoked", sess.State())
	}
	select {
	case <-sess.Done():
	default:
		t.Fatal("the revoked terminal must be joined before ChangePassword returns")
	}
	if f.registry.LiveCount() != 0 {
		t.Fatalf("live terminals = %d, want 0", f.registry.LiveCount())
	}

	// TC-05-01: one atomic commit, so the hash and the version agree.
	var stored model.User
	if err := f.db.First(&stored, f.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenVersion != 1 {
		t.Fatalf("token_version = %d, want 1", stored.TokenVersion)
	}
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("new-password")) != nil {
		t.Fatal("the new password was not committed")
	}

	if code := agentJWTStatus(t, f.jwtSvc, agentRepoGate(f.repo), oldRaw); code != http.StatusUnauthorized {
		t.Fatalf("stale JWT status = %d, want 401", code)
	}
	newRaw, err := f.jwtSvc.GenerateToken(f.user.ID, f.user.Username, f.user.Role, stored.TokenVersion)
	if err != nil {
		t.Fatal(err)
	}
	if code := agentJWTStatus(t, f.jwtSvc, agentRepoGate(f.repo), newRaw); code != http.StatusOK {
		t.Fatalf("fresh JWT status = %d, want 200", code)
	}
}

// TestAgentLegacyJWTWithoutVersionIsRejected pins TC-05-03's token rule: a
// pre-upgrade JWT with no `tv` claim is rejected once versioning is enabled,
// while the gate-less wiring (tools, tests) stays permissive so API-key and
// non-browser callers are unaffected.
func TestAgentLegacyJWTWithoutVersionIsRejected(t *testing.T) {
	jwtSvc := token.NewJWTService("agent-secret", time.Hour)
	claims := &token.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "vpsmanager",
		},
		UserID:   1,
		Username: "agent",
		Role:     "admin",
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("agent-secret"))
	if err != nil {
		t.Fatal(err)
	}
	versioned := gate.New(func(context.Context, uint) (int64, error) { return 1, nil })
	if code := agentJWTStatus(t, jwtSvc, versioned, raw); code != http.StatusUnauthorized {
		t.Fatalf("legacy JWT status = %d, want 401 with versioning enabled", code)
	}
	if code := agentJWTStatus(t, jwtSvc, nil, raw); code != http.StatusOK {
		t.Fatalf("legacy JWT status = %d, want 200 without a gate", code)
	}
}

// TestAgentVersionLookupFailureIsServiceUnavailable pins TC-05-05: an unresolved
// primary-database read is a 503, never a fail-open admission.
func TestAgentVersionLookupFailureIsServiceUnavailable(t *testing.T) {
	jwtSvc := token.NewJWTService("agent-secret", time.Hour)
	raw, err := jwtSvc.GenerateToken(1, "agent", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	failing := gate.New(func(context.Context, uint) (int64, error) { return 0, gate.ErrUnavailable })
	if code := agentJWTStatus(t, jwtSvc, failing, raw); code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
}

// TestAgentGateReadsPrimaryWithoutPositiveCaching pins TC-05-05's read rule:
// every admission re-reads the primary database, so a version bumped out of band
// is observed immediately instead of being served from a cache.
func TestAgentGateReadsPrimaryWithoutPositiveCaching(t *testing.T) {
	db := authOwnerTestDB(t)
	repo := repository.NewUserRepo(db)
	user := &model.User{Username: "cached", PasswordHash: "x", Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	var reads int32
	g := gate.New(func(ctx context.Context, userID uint) (int64, error) {
		atomic.AddInt32(&reads, 1)
		return repo.TokenVersion(ctx, userID)
	})
	ctx := context.Background()

	seen := func() int64 {
		var version int64
		if err := g.Admit(ctx, user.ID, func(v int64) error { version = v; return nil }); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		return version
	}
	if v := seen(); v != 0 {
		t.Fatalf("version = %d, want 0", v)
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 7).Error; err != nil {
		t.Fatal(err)
	}
	if v := seen(); v != 7 {
		t.Fatalf("version = %d, want the out-of-band 7 (no positive caching)", v)
	}
	if atomic.LoadInt32(&reads) < 2 {
		t.Fatalf("version reads = %d, want one per admission", reads)
	}
}

// TestAgentRevokedJWTRejectedAtHandshakeAndClosesLiveTerminal pins TC-05-02: a
// revoked JWT is refused during WebSocket authentication, and revoking a user
// closes an already-live terminal instead of leaving it attached.
func TestAgentRevokedJWTRejectedAtHandshakeAndClosesLiveTerminal(t *testing.T) {
	// (a) Handshake: a token pinned to an older version is refused.
	jwtSvc := token.NewJWTService("agent-secret", time.Hour)
	stale, err := jwtSvc.GenerateToken(7, "agent", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	bumped := gate.New(func(context.Context, uint) (int64, error) { return 5, nil })
	if code := agentJWTStatus(t, jwtSvc, bumped, stale); code != http.StatusUnauthorized {
		t.Fatalf("revoked JWT status = %d, want 401", code)
	}

	// (b) A live terminal is closed when the user is revoked.
	const serverID = 1
	svc, _ := startSeededTerminalService(t, serverID)
	registry := gate.NewRegistry()
	svc.SetSessionRegistry(registry)

	version := int64(0)
	ctx := mw.WithUserClaims(context.Background(), &token.Claims{UserID: 7, Username: "agent", Role: "admin", TokenVersion: &version})
	h := beginSessionWithContext(t, svc, serverID, ctx)

	if registry.LiveCount() != 1 {
		t.Fatalf("live terminals = %d, want the session registered", registry.LiveCount())
	}
	if n := registry.RevokeUser(context.Background(), 7); n != 1 {
		t.Fatalf("revoked %d terminals, want 1", n)
	}

	if err := h.wait(t); err != nil {
		t.Fatalf("StartSession returned %v, want a clean cancellation", err)
	}
	facts := <-h.facts
	if facts.Outcome != "cancelled" {
		t.Fatalf("outcome = %q, want cancelled", facts.Outcome)
	}
	if registry.LiveCount() != 0 {
		t.Fatalf("live terminals = %d, want 0 after the revoke", registry.LiveCount())
	}
}

// ---------------------------------------------------------------------------
// REQ-06: Agent upload leases settle exactly once and join their workers.
// ---------------------------------------------------------------------------

type agentUploadFixture struct {
	address string
	hostKey ssh.PublicKey
	pool    *sshpool.Pool
}

func newAgentUploadService(t *testing.T, execute func(string, ssh.Channel, <-chan struct{})) (*SSHService, agentUploadFixture) {
	t.Helper()
	address, hostKey := startExecSSHServer(t, execute)
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	return svc, agentUploadFixture{address: address, hostKey: hostKey, pool: pool}
}

// reseedAfterForcedClose re-seeds the single slot with a fresh client when the
// bounded teardown had to force-close the previous transport. It must only be
// used when Get is expected to find no cached client.
func reseedAfterForcedClose(t *testing.T, f agentUploadFixture) {
	t.Helper()
	seedPool(t, f.pool, 1, dialTestSSH(t, f.address, f.hostKey))
}

// TestAgentUploadQuotaRecoversAfterRepeatedUploads pins TC-06-01: more uploads
// than the lease limit, on both the success and the failure path, must leave the
// single slot free so a later Exec still acquires it.
func TestAgentUploadQuotaRecoversAfterRepeatedUploads(t *testing.T) {
	svc, _ := newAgentUploadService(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
		drainUploadOK(ch)
	}))
	local := writeLocalArtifact(t)

	for i := 0; i < 5; i++ {
		if err := svc.CopyFile(context.Background(), 1, local, "/tmp/agent"); err != nil {
			t.Fatalf("successful upload %d: %v", i, err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := svc.CopyFile(context.Background(), 1, t.TempDir()+"/missing", "/tmp/agent"); err == nil {
			t.Fatal("a missing local file must fail")
		}
	}
	if _, err := svc.Exec(context.Background(), 1, "noop", time.Second); err != nil {
		t.Fatalf("exec after repeated uploads: %v (quota leaked)", err)
	}
}

// TestAgentCancelledUploadIsBoundedAndReclaimsQuota pins TC-06-02: cancelling an
// upload blocked on an unresponsive peer must fail (never report success), reclaim
// the quota inside the local cleanup budget, and join the copy worker.
func TestAgentCancelledUploadIsBoundedAndReclaimsQuota(t *testing.T) {
	var calls int32
	svc, fixture := newAgentUploadService(t, routeUploads(func(ch ssh.Channel, tc <-chan struct{}) {
		if atomic.AddInt32(&calls, 1) == 1 {
			<-tc // block the first upload until the transport is closed
			return
		}
		drainUploadOK(ch)
	}))
	local := writeLocalArtifact(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := svc.CopyFile(ctx, 1, local, "/tmp/agent")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a blocked upload must not report success")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("teardown took %v, want within the 3s local cleanup budget", elapsed)
	}

	reseedAfterForcedClose(t, fixture)
	if err := svc.CopyFile(context.Background(), 1, local, "/tmp/agent"); err != nil {
		t.Fatalf("upload after cancellation: %v", err)
	}
}

// TestAgentUploadRejectsRemoteFailureWithoutReportingSuccess pins TC-06-03: a
// remote rejection must propagate as an error (no "warning then success"), and
// the local teardown stays inside one absolute deadline even when the peer never
// drains the stream.
func TestAgentUploadRejectsRemoteFailureWithoutReportingSuccess(t *testing.T) {
	svc, _ := newAgentUploadService(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
		// Reject: report a non-zero remote exit without reading stdin.
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{3}))
		_ = ch.Close()
	}))
	local := writeLocalArtifact(t)

	start := time.Now()
	err := svc.CopyFile(context.Background(), 1, local, "/tmp/agent")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a remote rejection must not be reported as success")
	}
	if !strings.Contains(err.Error(), "copy failed") && !strings.Contains(err.Error(), "copy file") {
		t.Fatalf("err = %v, want a copy failure", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("teardown took %v, want within the 3s local cleanup budget", elapsed)
	}
}

// ---------------------------------------------------------------------------
// REQ-07: unified execution and transfer budgets.
// ---------------------------------------------------------------------------

// TestAgentExecUsesConfiguredDefaultAndReturnsCompleteResults pins TC-07-01: with
// no per-request timeout the configured effective EXEC_TIMEOUT applies (not a
// hardcoded 15s) and the complete result is returned.
func TestAgentExecUsesConfiguredDefaultAndReturnsCompleteResults(t *testing.T) {
	const payload = 4096
	address, hostKey := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		time.Sleep(300 * time.Millisecond)
		_, _ = ch.Write(bytes.Repeat([]byte("o"), payload))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	// A configured default far above the old 15s HTTP write timeout.
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, 30*time.Second)

	result, err := svc.Exec(context.Background(), 1, "long", 0)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(result.Stdout) != payload {
		t.Fatalf("stdout = %d bytes, want the complete %d", len(result.Stdout), payload)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
}

// TestAgentExecBudgetExceededReclaimsResources pins TC-07-01's failure side: a
// command that outlives its budget is cancelled inside the local cleanup budget
// and the quota is reclaimed for the next command.
func TestAgentExecBudgetExceededReclaimsResources(t *testing.T) {
	var calls int32
	address, hostKey := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		if atomic.AddInt32(&calls, 1) == 1 {
			time.Sleep(5 * time.Second)
			return
		}
		_, _ = ch.Write([]byte("ok"))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)

	start := time.Now()
	if _, err := svc.Exec(context.Background(), 1, "slow", 200*time.Millisecond); err == nil {
		t.Fatal("a command over its budget must fail")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("teardown took %v, want within the 3s local cleanup budget", elapsed)
	}
	// The expiry released a healthy transport back to the pool, so the quota is
	// free without re-seeding: just run another command.
	if _, err := svc.Exec(context.Background(), 1, "fast", time.Second); err != nil {
		t.Fatalf("exec after a budget expiry: %v (quota leaked)", err)
	}
}

// TestAgentRelayBudgetsAndIdleAreEnforced pins TC-07-03: an explicit bounded
// stream outlives the standard budget while making progress, and a stalled body
// after the headers is aborted with a distinguishable reason and without
// appending a second JSON error to the already-committed response.
func TestAgentRelayBudgetsAndIdleAreEnforced(t *testing.T) {
	streaming := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			_, _ = w.Write([]byte("x"))
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer streaming.Close()

	svc := relayTestService(time.Second, 2*time.Second, 150*time.Millisecond, 2*time.Second)
	rec := httptest.NewRecorder()
	start := time.Now()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: streaming.URL},
		RelayInput{Method: "GET", Path: "/", Mode: "bounded_stream"}, rec)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("bounded stream failed: %v", err)
	}
	if result.BytesCopied != 10 {
		t.Fatalf("bytes copied = %d, want 10", result.BytesCopied)
	}
	if elapsed <= 150*time.Millisecond {
		t.Fatalf("elapsed = %v, want the stream to outlive the standard budget", elapsed)
	}

	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("hello"))
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(2 * time.Second)
	}))
	defer stalled.Close()

	svc2 := relayTestService(time.Second, 50*time.Millisecond, 2*time.Second, 2*time.Second)
	rec2 := httptest.NewRecorder()
	result2, err := svc2.relayFromService(context.Background(), &model.Service{BaseURL: stalled.URL},
		RelayInput{Method: "GET", Path: "/"}, rec2)
	if err == nil {
		t.Fatal("a stalled stream must not report success")
	}
	if !result2.HeadersWritten || result2.Reason != "relay_idle_timeout" || result2.Outcome != "timeout" {
		t.Fatalf("result = %+v", result2)
	}
	if strings.Contains(rec2.Body.String(), `"reason"`) {
		t.Fatal("a failure after the headers must not append a JSON error body")
	}
}

// TestAgentCleanupBudgetsAreAbsolute pins TC-07-04's frozen §7.1.1 constants: a
// 3s local cleanup total, a 2s terminal/JWT grace and 100ms SSH/Relay graces.
// Nested deployment or Monitor stages must not restart them.
func TestAgentCleanupBudgetsAreAbsolute(t *testing.T) {
	if sessionTeardownGrace != 2*time.Second {
		t.Fatalf("terminal teardown grace = %v, want 2s", sessionTeardownGrace)
	}
	if execTeardownGrace != 100*time.Millisecond {
		t.Fatalf("exec teardown grace = %v, want 100ms", execTeardownGrace)
	}
	svc := NewSSHService(nil, staticServerSource{}, nil, time.Second, time.Second)
	if svc.uploadTeardownGrace != 100*time.Millisecond {
		t.Fatalf("upload teardown grace = %v, want 100ms", svc.uploadTeardownGrace)
	}
	if gate.CleanupBudget != 3*time.Second {
		t.Fatalf("JWT terminal cleanup total = %v, want 3s", gate.CleanupBudget)
	}
	if gate.CleanupGrace != 2*time.Second {
		t.Fatalf("JWT terminal cleanup grace = %v, want 2s", gate.CleanupGrace)
	}
	if relayStandardBudget != 30*time.Second || relayBoundedStreamBudget != 300*time.Second {
		t.Fatalf("relay totals = %v/%v, want 30s/300s", relayStandardBudget, relayBoundedStreamBudget)
	}
	if relayHeaderWait != 30*time.Second || relayIdleTimeout != 30*time.Second {
		t.Fatalf("relay header/idle = %v/%v, want 30s/30s", relayHeaderWait, relayIdleTimeout)
	}
}
