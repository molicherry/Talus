package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func requireResidentInstallLayout(t *testing.T, node model.AgentNode, uid int, home string, pending []model.AgentInstallLayout) {
	t.Helper()
	if node.InstallUID == nil || *node.InstallUID != uid || node.InstallHome == nil || *node.InstallHome != home {
		t.Fatalf("completed layout = (%v, %v), want (%d, %q)", node.InstallUID, node.InstallHome, uid, home)
	}
	if !slices.Equal(node.PendingInstallLayouts, pending) {
		t.Fatalf("pending layouts = %#v, want %#v", node.PendingInstallLayouts, pending)
	}
}

func TestWindowsInstallLayoutPersistsAcrossRestartAndFailure(t *testing.T) {
	db, repo, _, node, _ := agentRepoFixture(t)
	ctx := context.Background()
	l := model.AgentInstallLayout{OS: "windows", Home: "C:/ProgramData"}
	if err := repo.TrackInstallLayouts(ctx, node, []model.AgentInstallLayout{l}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.PendingInstallLayouts, []model.AgentInstallLayout{l}) {
		t.Fatal("lost Windows layout before activation")
	}
	if err = repo.SetInstallPlatformLayout(ctx, node, l); err != nil {
		t.Fatal(err)
	}
	if err = db.First(&stored, "server_id = ?", node.ServerID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.InstallOS != "windows" || stored.InstallHome == nil || *stored.InstallHome != l.Home || len(stored.PendingInstallLayouts) != 0 {
		t.Fatal("lost completed Windows layout")
	}
	if err = repo.TrackInstallLayouts(ctx, node, []model.AgentInstallLayout{l}); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.PendingInstallLayouts, []model.AgentInstallLayout{l}) {
		t.Fatal("duplicate or misclassified retry layout")
	}
}

func TestResidentTrackInstallLayoutsPreservesAccountsAndFinalizes(t *testing.T) {
	_, repo, _, node, _ := agentRepoFixture(t)
	ctx := context.Background()
	if err := repo.SetInstallLayout(ctx, node, 1001, "/home//alice/."); err != nil {
		t.Fatal(err)
	}
	// Deliberately retain the original node snapshot: tracking must merge the
	// latest committed layout, rather than replace it with the caller's copy.
	for _, layout := range []model.AgentInstallLayout{
		{UID: 1002, Home: "/home/bob/"},
		{UID: 1001, Home: "/home/alice/../alice"},
		{UID: 1002, Home: "/home/./bob"},
		{UID: 1003, Home: "/home/bob"},
	} {
		if err := repo.TrackInstallLayout(ctx, node, layout.UID, layout.Home); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	requireResidentInstallLayout(t, stored, 1001, "/home/alice", []model.AgentInstallLayout{
		{UID: 1001, Home: "/home/alice"},
		{UID: 1002, Home: "/home/bob"},
		{UID: 1003, Home: "/home/bob"},
	})
	if err := repo.SetInstallLayout(ctx, node, 0, "/root/./"); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	requireResidentInstallLayout(t, stored, 0, "/root", nil)
}

func TestResidentInstallLayoutWriteFailurePreservesCompletedAndPendingLayouts(t *testing.T) {
	db, repo, _, node, _ := agentRepoFixture(t)
	ctx := context.Background()
	if err := repo.SetInstallLayout(ctx, node, 1001, "/home/alice"); err != nil {
		t.Fatal(err)
	}
	if err := repo.TrackInstallLayout(ctx, node, 1002, "/home/bob"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE FUNCTION reject_install_layout_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulated layout write failure'; END $$;
CREATE TRIGGER reject_install_layout_write BEFORE UPDATE OF pending_install_layouts ON agent_nodes FOR EACH ROW EXECUTE FUNCTION reject_install_layout_write()`).Error; err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]func() error{
		"track":    func() error { return repo.TrackInstallLayout(ctx, node, 1003, "/home/carol") },
		"finalize": func() error { return repo.SetInstallLayout(ctx, node, 0, "/root") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(); err == nil || !strings.Contains(err.Error(), "simulated layout write failure") {
				t.Fatalf("failed layout write = %v", err)
			}
			stored, err := repo.Get(ctx, node.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			requireResidentInstallLayout(t, stored, 1001, "/home/alice", []model.AgentInstallLayout{
				{UID: 1001, Home: "/home/alice"},
				{UID: 1002, Home: "/home/bob"},
			})
		})
	}
}

func TestResidentTrackInstallLayoutsDiscoveryPublishesAllOrNothing(t *testing.T) {
	db, repo, _, node, _ := agentRepoFixture(t)
	ctx := context.Background()
	// Legacy agents have no recorded completed layout. Rejecting the second
	// discovered account must also roll back the first account, otherwise the
	// nonempty partial result could suppress discovery on the next attempt.
	if err := db.Exec(`CREATE FUNCTION reject_second_discovered_layout() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
IF NEW.pending_install_layouts::jsonb @> '[{"UID":1003}]'::jsonb THEN RAISE EXCEPTION 'simulated second layout failure'; END IF;
RETURN NEW; END $$;
CREATE TRIGGER reject_second_discovered_layout BEFORE UPDATE OF pending_install_layouts ON agent_nodes FOR EACH ROW EXECUTE FUNCTION reject_second_discovered_layout()`).Error; err != nil {
		t.Fatal(err)
	}
	discovered := []model.AgentInstallLayout{
		{UID: 1002, Home: "/home/bob/"},
		{UID: 1003, Home: "/home//carol"},
		{UID: 1002, Home: "/home/./bob"},
	}
	if err := repo.TrackInstallLayouts(ctx, node, discovered); err == nil || !strings.Contains(err.Error(), "simulated second layout failure") {
		t.Fatalf("failed discovery write = %v", err)
	}
	stored, err := repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.InstallUID != nil || stored.InstallHome != nil || len(stored.PendingInstallLayouts) != 0 {
		t.Fatalf("failed discovery published partial layouts: uid=%v home=%v pending=%#v", stored.InstallUID, stored.InstallHome, stored.PendingInstallLayouts)
	}
	if err := db.Exec("DROP TRIGGER reject_second_discovered_layout ON agent_nodes").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.TrackInstallLayouts(ctx, node, discovered); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.InstallUID != nil || stored.InstallHome != nil || !slices.Equal(stored.PendingInstallLayouts, []model.AgentInstallLayout{
		{UID: 1002, Home: "/home/bob"},
		{UID: 1003, Home: "/home/carol"},
	}) {
		t.Fatalf("successful discovery lost an account or changed completed layout: uid=%v home=%v pending=%#v", stored.InstallUID, stored.InstallHome, stored.PendingInstallLayouts)
	}
}

func TestResidentInstallLayoutRejectsMissingAndStaleCredentials(t *testing.T) {
	_, repo, _, node, _ := agentRepoFixture(t)
	ctx := context.Background()
	if err := repo.SetInstallLayout(ctx, node, 1001, "/home/alice"); err != nil {
		t.Fatal(err)
	}
	if err := repo.TrackInstallLayout(ctx, node, 1002, "/home/bob"); err != nil {
		t.Fatal(err)
	}
	stale := node
	stale.TokenHash = strings.Repeat("f", 64)
	missing := node
	missing.ServerID++
	for name, invalid := range map[string]model.AgentNode{"stale-token": stale, "missing-node": missing} {
		t.Run(name, func(t *testing.T) {
			if err := repo.TrackInstallLayout(ctx, invalid, 0, "/root"); !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("track unknown credential = %v, want record not found", err)
			}
			if err := repo.TrackInstallLayouts(ctx, invalid, []model.AgentInstallLayout{{UID: 0, Home: "/root"}}); !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("track discovered layouts for unknown credential = %v, want record not found", err)
			}
			if err := repo.SetInstallLayout(ctx, invalid, 0, "/root"); !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("finalize unknown credential = %v, want record not found", err)
			}
		})
	}
	stored, err := repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	requireResidentInstallLayout(t, stored, 1001, "/home/alice", []model.AgentInstallLayout{
		{UID: 1001, Home: "/home/alice"},
		{UID: 1002, Home: "/home/bob"},
	})
}

func TestResidentConcurrentInstallLayoutTrackingRetainsEveryAttempt(t *testing.T) {
	_, repo, _, node, _ := agentRepoFixture(t)
	ctx := context.Background()
	if err := repo.SetInstallLayout(ctx, node, 1001, "/home/alice"); err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	var wg sync.WaitGroup
	errors := make(chan error, attempts)
	for attempt := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- repo.TrackInstallLayout(ctx, node, 2000+attempt, fmt.Sprintf("/home/attempt-%d/", attempt))
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored, err := repo.Get(ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.PendingInstallLayouts) != attempts+1 {
		t.Fatalf("concurrent tracking lost an attempted layout: %#v", stored.PendingInstallLayouts)
	}
	for attempt := range attempts {
		if !slices.Contains(stored.PendingInstallLayouts, model.AgentInstallLayout{UID: 2000 + attempt, Home: fmt.Sprintf("/home/attempt-%d", attempt)}) {
			t.Fatalf("missing attempted layout %d: %#v", attempt, stored.PendingInstallLayouts)
		}
	}
}
