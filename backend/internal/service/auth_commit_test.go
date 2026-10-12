package service

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

// authCommitFaultPool decorates real PostgreSQL transactions at the COMMIT
// boundary. Statements and row locks still execute against the isolated schema.
type authCommitFaultPool struct {
	gorm.ConnPool
	mode string
	held chan *sql.Tx
}

func (p *authCommitFaultPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	if p.mode == "unresolved" {
		ctx = context.WithoutCancel(ctx)
	}
	tx, err := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &authCommitFaultTx{Tx: tx, mode: p.mode, held: p.held}, nil
}

type authCommitFaultTx struct {
	*sql.Tx
	mode      string
	held      chan *sql.Tx
	committed atomic.Bool
}

func (t *authCommitFaultTx) Commit() error {
	t.committed.Store(true)
	switch t.mode {
	case "lost_ack":
		if err := t.Tx.Commit(); err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	case "rollback":
		if err := t.Tx.Rollback(); err != nil {
			return err
		}
		return pgx.ErrTxCommitRollback
	case "unresolved":
		t.held <- t.Tx
		return io.ErrUnexpectedEOF
	default:
		return t.Tx.Commit()
	}
}

func (t *authCommitFaultTx) Rollback() error {
	// Model a broken connection whose transaction is still settling on the
	// server. The test owns its eventual commit/rollback and always releases it.
	if t.mode == "unresolved" && t.committed.Load() {
		return io.ErrClosedPipe
	}
	return t.Tx.Rollback()
}

func injectAuthCommitFault(t *testing.T, svc *AuthService, mode string) <-chan *sql.Tx {
	t.Helper()
	held := make(chan *sql.Tx, 1)
	db := svc.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	db.Statement.ConnPool = &authCommitFaultPool{ConnPool: svc.db.Statement.ConnPool, mode: mode, held: held}
	svc.userRepo = repository.NewUserRepo(db)
	return held
}

func requireAuthState(t *testing.T, err error, state string) {
	t.Helper()
	var app *server.AppError
	if !errors.As(err, &app) || app.Code != http.StatusServiceUnavailable || app.Params["state"] != state {
		t.Fatalf("error = %v, want HTTP 503 state %q", err, state)
	}
}

// TestChangePasswordCommitAcknowledgementLostRevokes verifies a real committed
// password survives the lost acknowledgement, sessions are revoked, and locking
// reconciliation observes the committed version before new admission.
func TestChangePasswordCommitAcknowledgementLostRevokes(t *testing.T) {
	svc, repo, user, registry := newRevocationAuth(t)
	svc.SetUserGate(gate.New(repo.TokenVersion, repo.LockedTokenVersion))
	injectAuthCommitFault(t, svc, "lost_ack")
	var session *gate.Session
	session = registry.Register(user.ID, 0, func() { session.MarkDone() }, nil)

	err := svc.ChangePassword(context.Background(), user.ID, "old-password", "new-password")
	requireAuthState(t, err, "password_commit_unknown")
	if !errors.Is(err, repository.ErrCommitUnknown) {
		t.Fatalf("lost commit classification: %v", err)
	}
	if session.State() != gate.StateRevoked || registry.LiveCount() != 0 {
		t.Fatal("uncertain commit did not revoke and reclaim terminals")
	}
	if err := svc.gate.Admit(context.Background(), user.ID, func(version int64) error {
		if version != 1 {
			t.Fatalf("admitted version = %d, want committed version 1", version)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(context.Background(), user.Username, "new-password"); err != nil {
		t.Fatalf("committed password lost: %v", err)
	}
}

// TestChangePasswordDefiniteCommitRollbackKeepsSessions distinguishes an
// explicit database rollback from a transport failure during commit.
func TestChangePasswordDefiniteCommitRollbackKeepsSessions(t *testing.T) {
	svc, repo, user, registry := newRevocationAuth(t)
	injectAuthCommitFault(t, svc, "rollback")
	var canceled atomic.Bool
	session := registry.Register(user.ID, 0, func() { canceled.Store(true) }, nil)
	defer registry.Done(session.ID)
	err := svc.ChangePassword(context.Background(), user.ID, "old-password", "new-password")
	if err == nil || errors.Is(err, repository.ErrCommitUnknown) {
		t.Fatalf("rollback classification = %v", err)
	}
	if canceled.Load() || session.State() != gate.StatePreparing {
		t.Fatal("known rollback must not revoke terminals")
	}
	if version, err := repo.TokenVersion(context.Background(), user.ID); err != nil || version != 0 {
		t.Fatalf("rollback version = %d, %v", version, err)
	}
	if _, err := svc.Login(context.Background(), user.Username, "old-password"); err != nil {
		t.Fatal(err)
	}
}

// TestChangePasswordUnresolvedCommitFencesUntilLockedRecovery verifies an old
// MVCC version cannot clear the fence, recovery is bounded, and both eventual
// commit and rollback outcomes are confirmed by a real row-lock acquisition.
func TestChangePasswordUnresolvedCommitFencesUntilLockedRecovery(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			svc, repo, user, registry := newRevocationAuth(t)
			svc.SetUserGate(gate.New(repo.TokenVersion, repo.LockedTokenVersion))
			held := injectAuthCommitFault(t, svc, "unresolved")
			var session *gate.Session
			session = registry.Register(user.ID, 0, func() { session.MarkDone() }, nil)
			result := make(chan error, 1)
			start := time.Now()
			go func() { result <- svc.ChangePassword(context.Background(), user.ID, "old-password", "new-password") }()
			var tx *sql.Tx
			select {
			case tx = <-held:
			case <-time.After(5 * time.Second):
				t.Fatal("COMMIT fault not reached")
			}
			defer tx.Rollback()
			// An ordinary read returns version 0 while the transaction is open.
			if version, err := repo.TokenVersion(context.Background(), user.ID); err != nil || version != 0 {
				t.Fatalf("MVCC read = %d, %v", version, err)
			}
			select {
			case err := <-result:
				requireAuthState(t, err, "password_commit_unknown")
			case <-time.After(4 * time.Second):
				t.Fatal("recovery held the gate without a bound")
			}
			if time.Since(start) > 4*time.Second {
				t.Fatal("unknown commit exceeded bounded recovery budget")
			}
			if session.State() != gate.StateRevoked {
				t.Fatal("unknown commit did not cancel terminals")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			err := svc.gate.Admit(ctx, user.ID, func(int64) error { t.Fatal("old version admitted while COMMIT is unresolved"); return nil })
			cancel()
			if err == nil {
				t.Fatal("unresolved transaction must block admission")
			}
			want := int64(0)
			if commit {
				want = 1
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.gate.Admit(context.Background(), user.ID, func(version int64) error {
				if version != want {
					t.Fatalf("recovered version = %d, want %d", version, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUserGateRestartWaitsForPasswordTransaction proves a fresh process's first
// admission cannot read the pre-commit MVCC row from a still-running update.
func TestUserGateRestartWaitsForPasswordTransaction(t *testing.T) {
	svc, repo, user, _ := newRevocationAuth(t)
	tx := svc.db.Begin()
	if err := tx.Model(&model.User{}).Where("id = ?", user.ID).Update("token_version", 1).Error; err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	restarted := gate.New(repo.TokenVersion, repo.LockedTokenVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := restarted.Admit(ctx, user.ID, func(int64) error { t.Fatal("restart admitted while previous transaction holds row lock"); return nil })
	cancel()
	if err == nil {
		t.Fatal("restart must wait for the outstanding transaction")
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := restarted.Admit(context.Background(), user.ID, func(version int64) error {
		if version != 1 {
			t.Fatalf("restart read version = %d, want 1", version)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
