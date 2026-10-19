package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"gorm.io/gorm"
)

func agentRepoFixture(t *testing.T) (*gorm.DB, *AgentNodeRepo, model.Server, model.AgentNode, string) {
	t.Helper()
	db := newMetricRepoTestDB(t, &model.Server{}, &model.Metric{}, &model.AgentNode{}, &model.AgentReceipt{})
	key, e := crypto.NewMasterKey(strings.Repeat("ab", 32))
	if e != nil {
		t.Fatal(e)
	}
	r := NewAgentNodeRepo(db, key)
	s := model.Server{Name: "agent", Host: "127.0.0.1", Port: 22, OwnerID: 1}
	if e = db.Create(&s).Error; e != nil {
		t.Fatal(e)
	}
	n, e := r.Ensure(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	secret, e := r.Secret(n)
	if e != nil {
		t.Fatal(e)
	}
	return db, r, s, n, secret
}

func TestResidentCredentialsPersistRotateAndRevoke(t *testing.T) {
	db, r, s, n, secret := agentRepoFixture(t)
	ctx := context.Background()
	if strings.Contains(n.TokenCipher, secret) || n.TokenHash == secret {
		t.Fatal("credential stored as plaintext")
	}
	again, e := r.Ensure(ctx, s)
	if e != nil || again.TokenHash != n.TokenHash || again.InstallID != n.InstallID {
		t.Fatalf("restart rotated identity: %v", e)
	}
	if _, e = r.Auth(ctx, secret); e != nil {
		t.Fatal(e)
	}
	s.Host = "new-host"
	if e = db.Model(&s).Update("host", s.Host).Error; e != nil {
		t.Fatal(e)
	}
	next, e := r.Ensure(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	if next.TokenHash == n.TokenHash || next.InstallID != n.InstallID {
		t.Fatal("replacement did not revoke credential or changed installation id")
	}
	if _, e = r.Auth(ctx, secret); e == nil {
		t.Fatal("old node still authenticated")
	}
	secret, e = r.Secret(next)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Delete(&s).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = r.Auth(ctx, secret); e == nil {
		t.Fatal("deleted node authenticated")
	}
}

func TestResidentReplayIsIdempotentAndTransactionFailureHasNoACK(t *testing.T) {
	db, r, s, n, _ := agentRepoFixture(t)
	ctx := context.Background()
	id, _ := agentwire.NewID()
	now := time.Now().UTC()
	p := 20.0
	sample := agentwire.Sample{ID: id, BootID: "old-boot", Time: now.Add(-time.Hour), CPU: &p}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ack, e := r.Ingest(ctx, n, []agentwire.Sample{sample})
			if e != nil || len(ack) != 1 {
				t.Errorf("replay ACK: %v %v", ack, e)
			}
		}()
	}
	wg.Wait()
	var count int64
	if e := db.Model(&model.Metric{}).Where("server_id = ?", s.ID).Count(&count).Error; e != nil {
		t.Fatal(e)
	}
	if count != 1 {
		t.Fatalf("replay duplicated rows: %d", count)
	}
	var stored model.Metric
	if e := db.First(&stored, "server_id = ?", s.ID).Error; e != nil {
		t.Fatal(e)
	}
	if !stored.Time.Equal(sample.Time.Truncate(time.Microsecond)) {
		t.Fatalf("historical timestamp replaced: %v", stored.Time)
	}
	if e := db.Exec(`CREATE FUNCTION reject_resident_metric() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.cpu_percent = 99 THEN RAISE EXCEPTION 'simulated write failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_resident_metric BEFORE INSERT ON metrics FOR EACH ROW EXECUTE FUNCTION reject_resident_metric()`).Error; e != nil {
		t.Fatal(e)
	}
	a := sample
	a.ID, _ = agentwire.NewID()
	a.Time = now
	b := a
	b.ID, _ = agentwire.NewID()
	bad := 99.0
	b.CPU = &bad
	ack, e := r.Ingest(ctx, n, []agentwire.Sample{a, b})
	if e == nil || len(ack) > 0 {
		t.Fatal("failed transaction acknowledged samples")
	}
	if e = db.Model(&model.AgentReceipt{}).Where("sample_id IN ?", []string{a.ID, b.ID}).Count(&count).Error; e != nil {
		t.Fatal(e)
	}
	if count != 0 {
		t.Fatal("failed transaction left deduplication receipts")
	}
	if e = db.Exec("DROP TRIGGER reject_resident_metric ON metrics").Error; e != nil {
		t.Fatal(e)
	}
	if ack, e = r.Ingest(ctx, n, []agentwire.Sample{a, b}); e != nil || len(ack) != 2 {
		t.Fatalf("failed samples cannot be retried: %v", e)
	}
}

func TestResidentBootChangeDoesNotCreateRateSpike(t *testing.T) {
	db := newMetricRepoTestDB(t, &model.Metric{})
	r := NewMetricRepo(db)
	base := time.Now().UTC().Truncate(time.Minute)
	insertSamples(t, db, 1, []model.Metric{{Time: base, BootID: "before", CPUPercent: f64(10), NetBytesRecv: i64(10)}, {Time: base.Add(5 * time.Second), BootID: "after", CPUPercent: f64(20), NetBytesRecv: i64(100000)}, {Time: base.Add(10 * time.Second), BootID: "after", CPUPercent: f64(30), NetBytesRecv: i64(100050)}})
	rows, e := r.Query(context.Background(), 1, base, base.Add(15*time.Second), "1 second")
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		if row.Time.Before(base.Add(5*time.Second)) && row.NetRecvRate != nil {
			t.Fatal("rate crossed a reboot")
		}
		if row.NetRecvRate != nil && *row.NetRecvRate > 10.001 {
			t.Fatalf("rate spike: %v", *row.NetRecvRate)
		}
	}
}
