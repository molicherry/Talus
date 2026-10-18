package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/model"
)

func TestAgentArtifactHistorySurvivesUpgradeAndRejectsConflicts(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.TrustedAgentArtifact{}); err != nil {
		t.Fatal(err)
	}
	makeManifest := func(version, digest string) *agentproto.Manifest {
		return &agentproto.Manifest{BuildVersion: version, ProtocolVersion: 1, Agents: []agentproto.Artifact{
			{Platform: "linux/amd64", SHA256: digest, BuildVersion: version, ProtocolVersion: 1},
		}}
	}
	ctx := context.Background()
	oldDigest, newDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	repo := NewAgentArtifactRepo(db)
	if err := repo.Register(ctx, makeManifest("v0.10.0", oldDigest)); err != nil {
		t.Fatal(err)
	}
	// A new repository instance represents restarting with a newer Hub image.
	repo = NewAgentArtifactRepo(db)
	if err := repo.Register(ctx, makeManifest("v0.11.0", newDigest)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Register(ctx, makeManifest("v0.11.0", newDigest)); err != nil {
		t.Fatal(err)
	}
	old, err := repo.Find(ctx, "linux/amd64", oldDigest)
	if err != nil || old.BuildVersion != "v0.10.0" {
		t.Fatalf("lost prior verified artifact: %+v %v", old, err)
	}
	if err := repo.Register(ctx, makeManifest("forged", oldDigest)); err == nil {
		t.Fatal("existing trust metadata was overwritten")
	}
	old, err = repo.Find(ctx, "linux/amd64", oldDigest)
	if err != nil || old.BuildVersion != "v0.10.0" {
		t.Fatal("conflicting registration mutated old trust")
	}
}
