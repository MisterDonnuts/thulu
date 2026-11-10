package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Tx struct {
	s   *Store
	tx  *sql.Tx
	ctx context.Context
}

func (s *Store) InTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.begin_tx")
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(&Tx{s: s, tx: tx, ctx: ctx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, "agentstore.errors.commit_tx")
	}
	return nil
}

func (t *Tx) PutFile(tenant, kind, owner, path string, body []byte, actor string) (File, error) {
	if err := validateFile(kind, owner, path, body); err != nil {
		return File{}, err
	}
	return t.s.putFileTx(t.ctx, t.tx, tenant, kind, owner, path, body, actor, "")
}

func (t *Tx) DeleteFile(tenant, kind, owner, path, actor string) error {
	return t.s.deleteFileTx(t.ctx, t.tx, tenant, kind, owner, path, actor)
}

func (t *Tx) PutAgent(tenant string, a Agent, actor string) (Agent, error) {
	return t.s.putAgent(t.ctx, t.tx, tenant, a, actor)
}

func (t *Tx) SetOrigin(tenant, kind, owner string, o Origin) error {
	renames, err := json.Marshal(nonNilRenames(o.SkillRenames))
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.write_origin", "kind", kind, "owner", owner)
	}
	if _, err := t.tx.ExecContext(t.ctx, t.s.rb(
		`INSERT INTO content_origins (tenant_id, kind, owner, origin, origin_revision, copied_at,
		                              content_hash, skill_renames)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (tenant_id, kind, owner) DO UPDATE SET
		     origin = EXCLUDED.origin, origin_revision = EXCLUDED.origin_revision,
		     copied_at = EXCLUDED.copied_at, content_hash = EXCLUDED.content_hash,
		     skill_renames = EXCLUDED.skill_renames`),
		tenant, kind, owner, o.Origin, o.Revision, o.CopiedAt, o.Hash, renames); err != nil {
		return apperr.Wrap(err, "agentstore.errors.write_origin", "kind", kind, "owner", owner)
	}
	return nil
}

func nonNilRenames(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (t *Tx) ForgetOrigin(tenant, kind, owner string) error {
	if _, err := t.tx.ExecContext(t.ctx, t.s.rb(
		`DELETE FROM content_origins WHERE tenant_id = ? AND kind = ? AND owner = ?`),
		tenant, kind, owner); err != nil {
		return apperr.Wrap(err, "agentstore.errors.forget_origin", "kind", kind, "owner", owner)
	}
	return nil
}

func (s *Store) ChangedSince(ctx context.Context, tenant, kind, owner string, since time.Time) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT 1 FROM content_revisions
		  WHERE tenant_id = ? AND kind = ? AND owner = ? AND created_at > ?
		  LIMIT 1`), tenant, kind, owner, since).Scan(&one)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, apperr.Wrap(err, "agentstore.errors.read_origin", "kind", kind, "owner", owner)
	}
	if kind != KindAgent {
		return false, nil
	}
	err = s.db.QueryRowContext(ctx, s.rb(
		`SELECT 1 FROM agents WHERE tenant_id = ? AND name = ? AND updated_at > ?`),
		tenant, owner, since).Scan(&one)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, apperr.Wrap(err, "agentstore.errors.read_origin", "kind", kind, "owner", owner)
	}
	return false, nil
}
