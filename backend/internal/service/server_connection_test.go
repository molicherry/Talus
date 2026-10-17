package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

func parseConnectionInput(t *testing.T, body string) *ConnectionInput {
	t.Helper()
	var input ConnectionInput
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	return &input
}

func connectionTestKey(t *testing.T) *crypto.MasterKey {
	t.Helper()
	key, err := crypto.NewMasterKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestConnectionSecretLifecycle(t *testing.T) {
	svc := &ServerService{masterKey: connectionTestKey(t)}
	input := parseConnectionInput(t, `{"mode":"socks5","host":"PROXY.example.","port":1080,"auth_type":"username_password","username":"operator","password":"private-proxy-secret"}`)
	config, err := svc.buildConnectionConfig(4, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.SocksHost != "proxy.example" || config.EncryptedPassword == nil || *config.EncryptedPassword == "private-proxy-secret" {
		t.Fatal("proxy password was not encrypted or host was not normalized")
	}
	cipher, err := svc.masterKey.ForRecord(config.CryptoVersion, config.Salt, crypto.PurposeSOCKS5)
	if err != nil {
		t.Fatal(err)
	}
	if config.CryptoVersion != crypto.VersionCurrent {
		t.Fatal("new password uses legacy encryption")
	}
	plain, err := cipher.Decrypt("password", *config.EncryptedPassword)
	if err != nil || string(plain) != "private-proxy-secret" {
		t.Fatalf("saved password is not decryptable: %v", err)
	}
	input = parseConnectionInput(t, `{"mode":"socks5","host":"proxy.example","port":1080,"auth_type":"username_password","username":"operator"}`)
	retained, err := svc.buildConnectionConfig(4, input, config)
	if err != nil || !equalConnection(config, retained) {
		t.Fatalf("omitted password did not retain existing ciphertext: %v", err)
	}
	input.Host = "other-proxy.example"
	if _, err := svc.buildConnectionConfig(4, input, config); err == nil {
		t.Fatal("saved password could be sent to a new endpoint without explicit replacement")
	}
	input = parseConnectionInput(t, `{"mode":"socks5","host":"proxy.example","port":1080,"auth_type":"none","password":null}`)
	cleared, err := svc.buildConnectionConfig(4, input, config)
	if err != nil || cleared.EncryptedPassword != nil || len(cleared.Salt) != 0 || cleared.SocksUsername != "" {
		t.Fatalf("disabling proxy auth retained a secret: %v", err)
	}
	unchanged, err := svc.buildConnectionConfig(4, input, cleared)
	if err != nil || !equalConnection(cleared, unchanged) || cleared.CryptoVersion != crypto.VersionLegacy {
		t.Fatalf("an unchanged passwordless route changed its persisted version: %v", err)
	}
	for _, detail := range []bool{false, true} {
		encoded, err := json.Marshal(connectionView(config, detail))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, plain) || bytes.Contains(encoded, []byte(*config.EncryptedPassword)) || bytes.Contains(encoded, []byte("salt")) {
			t.Fatalf("read projection contains secret material: %s", encoded)
		}
		if !detail && (bytes.Contains(encoded, []byte("proxy.example")) || bytes.Contains(encoded, []byte("operator")) || bytes.Contains(encoded, []byte("has_password"))) {
			t.Fatalf("summary exposes proxy metadata: %s", encoded)
		}
	}
}

func TestConnectionRejectsInvalidModeAndSecretCombinations(t *testing.T) {
	svc := &ServerService{masterKey: connectionTestKey(t)}
	for _, body := range []string{
		`{}`, `{"mode":"http"}`, `{"mode":"direct","password":null}`,
		`{"mode":"direct","host":""}`, `{"mode":"ssh_jump","jump_server_id":0}`,
		`{"mode":"ssh_jump","jump_server_id":2,"port":0}`,
		`{"mode":"socks5","host":"socks5://user:secret@proxy","port":1080,"auth_type":"none"}`,
		`{"mode":"socks5","host":"proxy","port":0,"auth_type":"none"}`,
		`{"mode":"socks5","host":"proxy","port":1080,"auth_type":"none","password":"secret"}`,
		`{"mode":"socks5","host":"proxy","port":1080,"auth_type":"username_password","username":"operator"}`,
		`{"mode":"socks5","host":"proxy","port":1080,"auth_type":"username_password","username":"operator","password":null}`,
		`{"mode":"socks5","host":"proxy","port":1080,"auth_type":"username_password","username":"operator","password":""}`,
		`{"mode":"socks5","host":"proxy","port":1080,"auth_type":"none","unknown":true}`,
	} {
		input := parseConnectionInput(t, body)
		if _, err := svc.buildConnectionConfig(1, input, nil); err == nil {
			t.Fatalf("accepted invalid connection %s", body)
		}
	}
	input := &ConnectionInput{Mode: model.ConnectionSOCKS5, Host: "proxy", Port: 1080, AuthType: model.SOCKSAuthPassword, Username: strings.Repeat("中", 86)}
	if err := input.validate(); err == nil {
		t.Fatal("SOCKS5 username limit was measured in runes instead of protocol bytes")
	}
}

func TestConnectionAdminRequiresJWTIdentity(t *testing.T) {
	ctx := mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "api_key", Role: "admin"})
	if IsConnectionAdmin(ctx) {
		t.Fatal("API key inherited owner's admin role as proxy-management authority")
	}
	ctx = mw.WithUserClaims(context.Background(), &token.Claims{UserID: 1, Role: "admin"})
	if !IsConnectionAdmin(ctx) {
		t.Fatal("JWT administrator was rejected")
	}
}

type connectionInvalidations struct {
	mu  sync.Mutex
	ids []uint
}

func (p *connectionInvalidations) Invalidate(id uint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, id)
}

func connectionServiceFixture(t *testing.T) (*ServerService, *repository.ServerRepo, *gorm.DB, context.Context, *connectionInvalidations) {
	t.Helper()
	db := authOwnerTestDB(t)
	if err := db.AutoMigrate(&model.SSHCredential{}, &model.Server{}, &model.ServerConnectionConfig{}, &model.Metric{}); err != nil {
		t.Fatal(err)
	}
	owner := &model.User{Username: "connection-admin", PasswordHash: "unused-test-hash", Role: "admin"}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	ctx := mw.WithUserClaims(context.Background(), &token.Claims{UserID: owner.ID, Role: "admin"})
	repo := repository.NewServerRepo(db)
	pool := &connectionInvalidations{}
	svc := NewServerService(repo, repository.NewMetricRepo(db), pool)
	svc.SetMasterKey(connectionTestKey(t))
	return svc, repo, db, ctx, pool
}

func expectConnectionReason(t *testing.T, err error, reason string) {
	t.Helper()
	var app *server.AppError
	if !errors.As(err, &app) || app.Reason != reason {
		t.Fatalf("error = %v, want %s", err, reason)
	}
}

func TestServerConnectionAtomicUpdatesAndCompatibility(t *testing.T) {
	svc, repo, db, ctx, _ := connectionServiceFixture(t)
	input := parseConnectionInput(t, `{"mode":"socks5","host":"proxy.example","port":1080,"auth_type":"username_password","username":"operator","password":"saved-secret"}`)
	srv, err := svc.Create(ctx, &model.Server{Name: "proxied", Host: "private-target", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID}, ConnectionMutation{Set: true, Config: input})
	if err != nil {
		t.Fatal(err)
	}
	if srv.ConnectionRevision != 1 || srv.Connection.HasPassword == nil || !*srv.Connection.HasPassword {
		t.Fatalf("new proxy response is incomplete: %+v", srv.Connection)
	}
	keyCtx := mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "api_key", Role: "admin"})
	// Old clients can update ordinary fields and echo unchanged endpoint fields
	// without knowing either the connection object or its revision.
	renamed, err := svc.Update(keyCtx, srv.ID, &model.Server{Name: "legacy-renamed", Host: srv.Host, Port: srv.Port}, false)
	if err != nil || renamed.ConnectionRevision != 1 || renamed.Connection.Mode != model.ConnectionSOCKS5 || renamed.Connection.Host != "" {
		t.Fatalf("legacy update changed route or exposed metadata: %v", err)
	}
	_, err = svc.Update(keyCtx, srv.ID, &model.Server{Host: "another-target"}, false)
	expectConnectionReason(t, err, server.ReasonForbidden)
	revision := uint64(1)
	_, err = svc.Update(keyCtx, srv.ID, &model.Server{}, false, ConnectionMutation{Set: true, ExpectedRevision: &revision})
	expectConnectionReason(t, err, server.ReasonForbidden)
	_, err = svc.Update(ctx, srv.ID, &model.Server{}, false, ConnectionMutation{Set: true})
	expectConnectionReason(t, err, server.ReasonInvalidConnection)
	// Supplying a new password increments revision while an outdated request
	// cannot save even unrelated fields from the same request.
	input = parseConnectionInput(t, `{"mode":"socks5","host":"proxy.example","port":1080,"auth_type":"username_password","username":"operator","password":"replacement-secret"}`)
	updated, err := svc.Update(ctx, srv.ID, &model.Server{}, false, ConnectionMutation{Set: true, Config: input, ExpectedRevision: &revision})
	if err != nil || updated.ConnectionRevision != 2 {
		t.Fatalf("replace password: %v", err)
	}
	pinned := []byte("the-target-host-key")
	if err := db.Model(&model.Server{}).Where("id = ?", srv.ID).UpdateColumn("host_key", pinned).Error; err != nil {
		t.Fatal(err)
	}
	_, err = svc.Update(ctx, srv.ID, &model.Server{Name: "must-not-commit"}, false, ConnectionMutation{Set: true, ExpectedRevision: &revision})
	expectConnectionReason(t, err, server.ReasonConnectionConfigChanged)
	after, err := repo.FindByID(ctx, srv.ID)
	if err != nil || after.Name != "legacy-renamed" {
		t.Fatalf("stale write partly committed: %v", err)
	}
	revision = 2
	cleared, err := svc.Update(ctx, srv.ID, &model.Server{}, false, ConnectionMutation{Set: true, ExpectedRevision: &revision})
	if err != nil || cleared.Connection.Mode != model.ConnectionDirect || cleared.ConnectionRevision != 3 {
		t.Fatalf("clear connection: %v", err)
	}
	config, err := repo.FindConnectionConfig(ctx, srv.ID)
	if err != nil || config != nil {
		t.Fatalf("clearing proxy retained its private config: %v", err)
	}
	after, err = repo.FindByID(ctx, srv.ID)
	if err != nil || after.HostKey == nil || !bytes.Equal(*after.HostKey, pinned) {
		t.Fatalf("changing route without changing target discarded its host identity: %v", err)
	}
}

func TestFailedConnectionCreateLeavesNoServer(t *testing.T) {
	svc, _, db, ctx, _ := connectionServiceFixture(t)
	missingJump := uint(999999)
	_, err := svc.Create(ctx, &model.Server{Name: "invalid-route", Host: "target", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID}, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSSHJump, JumpServerID: &missingJump}})
	expectConnectionReason(t, err, server.ReasonInvalidConnection)
	var count int64
	if err := db.Model(&model.Server{}).Where("name = ?", "invalid-route").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed connection created a direct server: count=%d err=%v", count, err)
	}
}

func TestServerJumpDependencyProtection(t *testing.T) {
	svc, repo, db, ctx, pool := connectionServiceFixture(t)
	encrypted := "test-ciphertext"
	credential := &model.SSHCredential{Name: "jump-credential", AuthType: "password", Username: "jump", EncryptedPassword: &encrypted}
	if err := db.Create(credential).Error; err != nil {
		t.Fatal(err)
	}
	makeServer := func(name string) *model.Server {
		srv, err := svc.Create(ctx, &model.Server{Name: name, Host: name + ".example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID, CredentialID: &credential.ID})
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}
	jump, target := makeServer("jump"), makeServer("target")
	revision := uint64(1)
	_, err := svc.Update(ctx, target.ID, &model.Server{}, false, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSSHJump, JumpServerID: &jump.ID}, ExpectedRevision: &revision})
	if err != nil {
		t.Fatal(err)
	}
	deps, err := repo.FindJumpDependentIDs(ctx, jump.ID)
	if err != nil || len(deps) != 1 || deps[0] != target.ID {
		t.Fatalf("jump dependencies missing: %v, %v", deps, err)
	}
	expectConnectionReason(t, svc.Delete(ctx, jump.ID), server.ReasonConnectionInUse)
	_, err = svc.Update(ctx, jump.ID, &model.Server{}, false, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSOCKS5, Host: "proxy", Port: 1080, AuthType: model.SOCKSAuthNone}, ExpectedRevision: &revision})
	expectConnectionReason(t, err, server.ReasonConnectionInUse)
	keyCtx := mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "api_key", Role: "admin"})
	_, err = svc.Update(keyCtx, jump.ID, &model.Server{Host: "other-jump"}, false)
	expectConnectionReason(t, err, server.ReasonForbidden)
	pool.ids = nil
	_, err = svc.Update(ctx, jump.ID, &model.Server{Host: "new-jump"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.ids) != 2 || pool.ids[0] != jump.ID || pool.ids[1] != target.ID {
		t.Fatalf("jump change did not invalidate target: %v", pool.ids)
	}
	if err := svc.Delete(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, jump.ID); err != nil {
		t.Fatalf("deleted targets still reserve their old jump: %v", err)
	}
}

func TestConcurrentJumpBindingsCannotCreateCycle(t *testing.T) {
	svc, repo, db, ctx, _ := connectionServiceFixture(t)
	encrypted := "test-ciphertext"
	credential := &model.SSHCredential{Name: "shared", AuthType: "password", Username: "jump", EncryptedPassword: &encrypted}
	if err := db.Create(credential).Error; err != nil {
		t.Fatal(err)
	}
	servers := make([]*model.Server, 2)
	for i, name := range []string{"first", "second"} {
		var err error
		servers[i], err = svc.Create(ctx, &model.Server{Name: name, Host: name, Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID, CredentialID: &credential.ID})
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			<-start
			revision := uint64(1)
			_, err := svc.Update(ctx, servers[index].ID, &model.Server{}, false, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSSHJump, JumpServerID: &servers[1-index].ID}, ExpectedRevision: &revision})
			results <- err
		}(i)
	}
	close(start)
	succeeded := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent inverse bindings succeeded %d times, want exactly once", succeeded)
	}
	count := 0
	for _, srv := range servers {
		config, err := repo.FindConnectionConfig(ctx, srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if config != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("inverse bindings created %d route records", count)
	}
}

func TestConnectionMigrationPreservesLegacyServersAndForeignKeys(t *testing.T) {
	db := authOwnerTestDB(t)
	if db.DisableForeignKeyConstraintWhenMigrating {
		t.Fatal("migration test must use production foreign-key behavior")
	}
	if err := db.AutoMigrate(&model.SSHCredential{}, &model.Server{}); err != nil {
		t.Fatal(err)
	}
	owner := &model.User{Username: "migration-admin", PasswordHash: "unused", Role: "admin"}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	legacy := &model.Server{Name: "legacy-before-proxies", Host: "legacy.example", Port: 22, OwnerID: owner.ID}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	// Restore the old schema shape before applying the production migration.
	if err := db.Migrator().DropColumn(&model.Server{}, "connection_revision"); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Server{}, &model.ServerConnectionConfig{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewServerRepo(db)
	after, err := repo.FindByID(context.Background(), legacy.ID)
	if err != nil || after.ConnectionRevision != 1 || after.Host != legacy.Host {
		t.Fatalf("existing server did not receive a compatible revision: %+v, %v", after, err)
	}
	config, err := repo.FindConnectionConfig(context.Background(), legacy.ID)
	if err != nil || config != nil {
		t.Fatalf("migration inferred a proxy route for an existing server: %+v, %v", config, err)
	}
	var constraints []struct{ ColumnName, ReferencedTable string }
	if err := db.Raw(`SELECT a.attname AS column_name, foreign_table.relname AS referenced_table
		FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		JOIN pg_class foreign_table ON foreign_table.oid = c.confrelid
		WHERE c.conrelid = 'server_connection_configs'::regclass AND c.contype = 'f'`).Scan(&constraints).Error; err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{}
	for _, constraint := range constraints {
		columns[constraint.ColumnName] = constraint.ReferencedTable
	}
	if len(constraints) != 2 || columns["server_id"] != "servers" || columns["jump_server_id"] != "servers" {
		t.Fatalf("configuration foreign keys do not point to servers: %+v", constraints)
	}
	var reversed int64
	if err := db.Raw(`SELECT COUNT(*) FROM pg_constraint WHERE conrelid = 'servers'::regclass
		AND confrelid = 'server_connection_configs'::regclass AND contype = 'f'`).Scan(&reversed).Error; err != nil || reversed != 0 {
		t.Fatalf("GORM generated a reverse configuration foreign key: %d, %v", reversed, err)
	}
}

type connectionTopologyObserver struct{ notify func() }

func (o connectionTopologyObserver) NotifyTopologyChange() { o.notify() }

func TestServerTopologyNotificationFollowsRouteCommit(t *testing.T) {
	svc, repo, _, ctx, _ := connectionServiceFixture(t)
	ownerID := mw.GetUserClaims(ctx).UserID
	var current *model.Server
	notifications := 0
	deleted := false
	svc.SetTopologyNotifier(connectionTopologyObserver{notify: func() {
		notifications++
		if current == nil {
			t.Fatal("monitor notified before create finished")
		}
		stored, err := repo.FindByID(ctx, current.ID)
		config, configErr := repo.FindConnectionConfig(ctx, current.ID)
		if deleted {
			if !errors.Is(err, gorm.ErrRecordNotFound) || configErr != nil || config != nil {
				t.Fatalf("delete notification preceded atomic deletion: row=%+v err=%v config=%+v configErr=%v", stored, err, config, configErr)
			}
			return
		}
		if err != nil || configErr != nil || config == nil || config.Mode != model.ConnectionSOCKS5 {
			t.Fatalf("create notification preceded route commit: row=%+v err=%v config=%+v configErr=%v", stored, err, config, configErr)
		}
	}})
	missingJump := uint(999999)
	if _, err := svc.Create(ctx, &model.Server{Name: "failed-notification", Host: "target", OwnerID: ownerID}, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSSHJump, JumpServerID: &missingJump}}); err == nil {
		t.Fatal("expected nonexistent jump to reject creation")
	}
	if notifications != 0 {
		t.Fatal("failed creation notified the monitor")
	}
	current = &model.Server{Name: "notified-proxy", Host: "target", Port: 22, OwnerID: ownerID}
	if _, err := svc.Create(ctx, current, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSOCKS5, Host: "proxy", Port: 1080, AuthType: model.SOCKSAuthNone}}); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("successful creation sent %d notifications", notifications)
	}
	deleted = true
	if err := svc.Delete(ctx, current.ID); err != nil {
		t.Fatal(err)
	}
	if notifications != 2 {
		t.Fatalf("successful deletion did not notify the monitor: %d", notifications)
	}
}

func TestServerConnectionUsesConfiguredFreshness(t *testing.T) {
	svc, _, db, ctx, _ := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "freshness", Host: "host", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Metric{ServerID: srv.ID, Time: time.Now().UTC().Add(-3 * time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		threshold time.Duration
		status    string
	}{{5 * time.Minute, "online"}, {time.Minute, "offline"}} {
		svc.SetFreshnessThreshold(check.threshold)
		stored, err := svc.Get(ctx, srv.ID)
		if err != nil || stored.Status != check.status || stored.Connection.Mode != model.ConnectionDirect {
			t.Fatalf("detail ignored configured freshness or connection: %+v, %v", stored, err)
		}
		servers, err := svc.List(ctx)
		if err != nil || len(servers) != 1 || servers[0].Status != check.status {
			t.Fatalf("list ignored configured freshness: %+v, %v", servers, err)
		}
		filtered, err := svc.ListFiltered(ctx, []uint{srv.ID})
		if err != nil || len(filtered) != 1 || filtered[0].Status != check.status {
			t.Fatalf("filtered list ignored configured freshness: %+v, %v", filtered, err)
		}
		summaries, err := svc.ListSummaries(ctx)
		if err != nil || len(summaries) != 1 || summaries[0].Status != check.status {
			t.Fatalf("summary ignored configured freshness: %+v, %v", summaries, err)
		}
	}
}

func TestProxyHostKeyTrustKeepsPendingFingerprintAndAdminChecks(t *testing.T) {
	svc, repo, db, ctx, pool := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "pending-proxy", Host: "target", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID}, ConnectionMutation{Set: true, Config: &ConnectionInput{Mode: model.ConnectionSOCKS5, Host: "proxy", Port: 1080, AuthType: model.SOCKSAuthNone}})
	if err != nil {
		t.Fatal(err)
	}
	pinned, seen := []byte("original-key"), []byte("presented-key")
	if err := db.Model(&model.Server{}).Where("id = ?", srv.ID).Updates(map[string]any{"host_key": pinned, "host_key_seen": seen, "host_key_mismatch_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	apiKeyCtx := mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "api_key", Role: "admin"})
	metadataUpdate, err := svc.Update(apiKeyCtx, srv.ID, &model.Server{Name: "pending-proxy-renamed", Host: srv.Host, Port: srv.Port}, false)
	if err != nil || !metadataUpdate.HostKeyMismatch || metadataUpdate.HostKeySeen == nil || !bytes.Equal(*metadataUpdate.HostKeySeen, seen) || metadataUpdate.ConnectionRevision != 1 {
		t.Fatalf("metadata-only edit bypassed pending host-key state: %+v, %v", metadataUpdate, err)
	}
	_, err = svc.TrustHostKey(apiKeyCtx, srv.ID, sshpool.Fingerprint(seen))
	expectConnectionReason(t, err, server.ReasonForbidden)
	_, err = svc.TrustHostKey(ctx, srv.ID, sshpool.Fingerprint(pinned))
	expectConnectionReason(t, err, server.ReasonHostKeyChanged)
	before, err := repo.FindByID(ctx, srv.ID)
	if err != nil || before.ConnectionRevision != 1 || before.HostKeyMismatchAt == nil || !bytes.Equal(*before.HostKey, pinned) {
		t.Fatalf("rejected trust changed pending identity: %+v, %v", before, err)
	}
	trusted, err := svc.TrustHostKey(ctx, srv.ID, sshpool.Fingerprint(seen))
	if err != nil || trusted.ConnectionRevision != 2 || trusted.HostKeyMismatchAt != nil || trusted.HostKeySeen != nil || trusted.HostKey == nil || !bytes.Equal(*trusted.HostKey, seen) {
		t.Fatalf("verified trust did not atomically replace pending identity: %+v, %v", trusted, err)
	}
	if len(pool.ids) != 1 || pool.ids[0] != srv.ID {
		t.Fatalf("trust failed to invalidate old cached identity: %v", pool.ids)
	}
}
