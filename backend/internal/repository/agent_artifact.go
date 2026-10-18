package repository

import (
	"context"
	"errors"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentArtifactRepo struct{ db *gorm.DB }

func NewAgentArtifactRepo(db *gorm.DB) *AgentArtifactRepo { return &AgentArtifactRepo{db: db} }

// Register is called only after startup has verified the local manifest and
// binaries. Conflicting metadata must fail rather than rewrite old trust.
func (r *AgentArtifactRepo) Register(ctx context.Context, manifest *agentproto.Manifest) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, artifact := range manifest.Agents {
			row := model.TrustedAgentArtifact{Platform: artifact.Platform, SHA256: artifact.SHA256,
				BuildVersion: artifact.BuildVersion, ProtocolVersion: artifact.ProtocolVersion}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			var stored model.TrustedAgentArtifact
			if err := tx.Where("platform = ? AND sha256 = ?", row.Platform, row.SHA256).First(&stored).Error; err != nil {
				return err
			}
			if stored.BuildVersion != row.BuildVersion || stored.ProtocolVersion != row.ProtocolVersion {
				return errors.New("conflicting trusted agent artifact metadata")
			}
		}
		return nil
	})
}

func (r *AgentArtifactRepo) Find(ctx context.Context, platform, digest string) (agentproto.Artifact, error) {
	var row model.TrustedAgentArtifact
	err := r.db.WithContext(ctx).Where("platform = ? AND sha256 = ?", platform, digest).First(&row).Error
	return agentproto.Artifact{Platform: row.Platform, SHA256: row.SHA256,
		BuildVersion: row.BuildVersion, ProtocolVersion: row.ProtocolVersion}, err
}
