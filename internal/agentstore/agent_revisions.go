package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	DefReasonSettings = "settings"
	DefReasonWorkshop = "workshop"
	DefReasonRestore  = "restore"
)

const keepDefRevisions = 50

type DefRevision struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	Reason     string          `json:"reason"`
	Actor      string          `json:"actor,omitempty"`
	At         time.Time       `json:"created_at"`
	Definition types.AgentType `json:"definition"`
}

type queryExecer interface {
	queryRower
	execer
}

func (t *Tx) SnapshotAgent(tenant, name, reason, actor string) error {
	return t.s.snapshotAgent(t.ctx, t.tx, tenant, name, reason, actor)
}

func (s *Store) snapshotAgent(ctx context.Context, q queryExecer, tenant, name, reason, actor string) error {
	cur, err := scanAgent(q.QueryRowContext(ctx, s.rb(agentSelect+` WHERE tenant_id = ? AND name = ?`), tenant, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.get_agent", "name", name)
	}
	def := cur.AgentType
	def.Dir = ""
	def.SystemSkills = nil
	raw, err := json.Marshal(def)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.encode_agent", "name", name)
	}
	if _, err := q.ExecContext(ctx, s.rb(
		`INSERT INTO agent_def_revisions (tenant_id, name, definition, reason, actor, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		tenant, name, raw, reason, actor, time.Now().UTC()); err != nil {
		return apperr.Wrap(err, "agentstore.errors.record_def_revision", "name", name)
	}
	if _, err := q.ExecContext(ctx, s.rb(
		`DELETE FROM agent_def_revisions
		  WHERE tenant_id = ? AND name = ? AND id NOT IN (
		    SELECT id FROM agent_def_revisions
		     WHERE tenant_id = ? AND name = ? ORDER BY id DESC LIMIT ?)`),
		tenant, name, tenant, name, keepDefRevisions); err != nil {
		return apperr.Wrap(err, "agentstore.errors.record_def_revision", "name", name)
	}
	return nil
}

func (s *Store) ListDefRevisions(ctx context.Context, tenant, name string) ([]DefRevision, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, name, definition, reason, actor, created_at FROM agent_def_revisions
		  WHERE tenant_id = ? AND name = ? ORDER BY id DESC LIMIT ?`), tenant, name, keepDefRevisions)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_def_revisions", "name", name)
	}
	defer func() { _ = rows.Close() }()
	out := []DefRevision{}
	for rows.Next() {
		rev, err := scanDefRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_def_revisions", "name", name)
	}
	return out, nil
}

func scanDefRevision(sc rowScanner) (DefRevision, error) {
	var (
		rev DefRevision
		raw []byte
	)
	if err := sc.Scan(&rev.ID, &rev.Name, &raw, &rev.Reason, &rev.Actor, &rev.At); err != nil {
		return DefRevision{}, err
	}
	if err := json.Unmarshal(raw, &rev.Definition); err != nil {
		return DefRevision{}, apperr.Wrap(err, "agentstore.errors.decode_agent", "name", rev.Name)
	}
	return rev, nil
}

func (s *Store) RestoreDefRevision(ctx context.Context, tenant, name string, id int64, actor string) (Agent, error) {
	var out Agent
	err := s.InTx(ctx, func(tx *Tx) error {
		rev, err := scanDefRevision(tx.tx.QueryRowContext(ctx, s.rb(
			`SELECT id, name, definition, reason, actor, created_at FROM agent_def_revisions
			  WHERE tenant_id = ? AND name = ? AND id = ?`), tenant, name, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return apperr.Wrap(err, "agentstore.errors.get_def_revision", "id", id)
		}
		cur, err := scanAgent(tx.tx.QueryRowContext(ctx, s.rb(agentSelect+` WHERE tenant_id = ? AND name = ?`), tenant, name))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return apperr.Wrap(err, "agentstore.errors.get_agent", "name", name)
		}
		if serr := tx.SnapshotAgent(tenant, name, DefReasonRestore, actor); serr != nil {
			return serr
		}
		cur.AgentType = rev.Definition
		cur.Name = name
		out, err = tx.PutAgent(tenant, cur, actor)
		return err
	})
	return out, err
}
