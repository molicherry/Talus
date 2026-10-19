package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentNodeRepo struct {
	db  *gorm.DB
	key *crypto.MasterKey
}

func NewAgentNodeRepo(db *gorm.DB, key *crypto.MasterKey) *AgentNodeRepo {
	return &AgentNodeRepo{db, key}
}
func TokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Target excludes transient SSH route/credential changes. Replacing the actual
// machine or trusted host key revokes the old resident credential.
func AgentTarget(s model.Server) string {
	var key []byte
	if s.HostKey != nil {
		key = *s.HostKey
	}
	return TokenHash(fmt.Sprintf("%s:%d:%x", s.Host, s.Port, key))
}

func (r *AgentNodeRepo) Ensure(ctx context.Context, s model.Server) (model.AgentNode, error) {
	var n model.AgentNode
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(s.ID)+7300000000).Error; err != nil {
			return err
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&n, "server_id = ?", s.ID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		target := AgentTarget(s)
		if err == nil && n.Target == target {
			return nil
		}
		var b [32]byte
		if _, err = rand.Read(b[:]); err != nil {
			return err
		}
		token := hex.EncodeToString(b[:])
		salt := make([]byte, crypto.SaltSize)
		if _, err = rand.Read(salt); err != nil {
			return err
		}
		cipher, err := r.key.ForRecord(crypto.VersionCurrent, salt, crypto.PurposeAgent)
		if err != nil {
			return err
		}
		encoded, err := cipher.Encrypt(fmt.Sprint(s.ID), []byte(token))
		if err != nil {
			return err
		}
		installID := n.InstallID
		if installID == "" {
			installID, err = agentwire.NewID()
			if err != nil {
				return err
			}
		}
		n = model.AgentNode{ServerID: s.ID, InstallID: installID, Target: target, TokenHash: TokenHash(token), TokenCipher: encoded, Salt: salt, Deployment: "pending"}
		return tx.Save(&n).Error
	})
	return n, err
}
func (r *AgentNodeRepo) Secret(n model.AgentNode) (string, error) {
	c, e := r.key.ForRecord(crypto.VersionCurrent, n.Salt, crypto.PurposeAgent)
	if e != nil {
		return "", e
	}
	b, e := c.Decrypt(fmt.Sprint(n.ServerID), n.TokenCipher)
	return string(b), e
}
func (r *AgentNodeRepo) Auth(ctx context.Context, token string) (model.AgentNode, error) {
	var n model.AgentNode
	if len(token) != 64 {
		return n, errors.New("invalid credential")
	}
	err := r.db.WithContext(ctx).Table("agent_nodes").Select("agent_nodes.*").Joins("JOIN servers ON servers.id = agent_nodes.server_id AND servers.deleted_at IS NULL").Where("token_hash = ?", TokenHash(token)).Take(&n).Error
	return n, err
}
func (r *AgentNodeRepo) All(ctx context.Context) ([]model.AgentNode, error) {
	var n []model.AgentNode
	e := r.db.WithContext(ctx).Find(&n).Error
	return n, e
}
func (r *AgentNodeRepo) Deployment(ctx context.Context, n model.AgentNode, state, reason, hash, config string) error {
	return r.db.WithContext(ctx).Model(&model.AgentNode{}).Where("server_id = ? AND token_hash = ?", n.ServerID, n.TokenHash).Updates(map[string]any{"deployment": state, "deploy_error": reason, "installed_hash": hash, "config_hash": config, "last_attempt": time.Now().UTC()}).Error
}
func (r *AgentNodeRepo) Contact(ctx context.Context, id uint, at time.Time, version string, sample, persisted *time.Time) error {
	values := map[string]any{"last_contact": at, "version": version}
	if sample != nil {
		values["last_sample"] = gorm.Expr("GREATEST(last_sample, ?)", *sample)
	}
	if persisted != nil {
		values["last_persisted"] = gorm.Expr("GREATEST(last_persisted, ?)", *persisted)
	}
	return r.db.WithContext(ctx).Model(&model.AgentNode{}).Where("server_id = ?", id).Updates(values).Error
}

// Ingest atomically commits deduplication receipts and observations. Lost ACKs
// can safely be replayed, including after a Hub restart.
func (r *AgentNodeRepo) Ingest(ctx context.Context, node model.AgentNode, samples []agentwire.Sample) ([]string, error) {
	ack := make([]string, 0, len(samples))
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var active model.AgentNode
		if err := tx.Table("agent_nodes").Select("agent_nodes.*").Joins("JOIN servers ON servers.id = agent_nodes.server_id AND servers.deleted_at IS NULL").Where("agent_nodes.server_id = ? AND token_hash = ?", node.ServerID, node.TokenHash).Clauses(clause.Locking{Strength: "SHARE"}).Take(&active).Error; err != nil {
			return err
		}
		for _, s := range samples {
			if err := s.Validate(now); err != nil {
				return err
			}
			receipt := model.AgentReceipt{ServerID: node.ServerID, SampleID: s.ID, ReceivedAt: now}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				m := model.Metric{Time: s.Time.UTC(), ServerID: node.ServerID, BootID: s.BootID, CPUPercent: s.CPU, MemoryUsed: s.MemoryUsed, MemoryTotal: s.MemoryTotal, SwapUsed: s.SwapUsed, SwapTotal: s.SwapTotal, DiskUsed: s.DiskUsed, DiskTotal: s.DiskTotal, Load1: s.Load1, Load5: s.Load5, Load15: s.Load15, NetBytesRecv: s.NetRecv, NetBytesSent: s.NetSent, DiskReadBytes: s.DiskRead, DiskWriteBytes: s.DiskWrite, OS: s.OS, CPUModel: s.CPUModel, KernelVersion: s.Kernel, UptimeSeconds: s.Uptime}
				if err := tx.Create(&m).Error; err != nil {
					return err
				}
			}
			ack = append(ack, s.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ack, nil
}

func (r *AgentNodeRepo) Cleanup(ctx context.Context) error {
	if err := r.db.WithContext(ctx).Where("received_at < ?", time.Now().Add(-48*time.Hour)).Delete(&model.AgentReceipt{}).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Where("server_id NOT IN (SELECT id FROM servers WHERE deleted_at IS NULL)").Delete(&model.AgentNode{}).Error
}
