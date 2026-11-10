package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// A delegated chat's deletions are held as Actions until the delegator confirms with their own
// session. The API rules are in docs/api-reference.md.

const DelegationTTL = 8 * time.Hour

func (c *Chat) Delegated(now time.Time) bool {
	return c.DelegatedBy != "" && c.DelegatedUntil != nil && now.Before(*c.DelegatedUntil)
}

// Setting or clearing a delegation cancels its pending actions, so an old request (possibly planted
// by a
// poisoned task) cannot become confirmable again.
func (s *Store) SetDelegation(ctx context.Context, id, by string, until time.Time) error {
	return s.changeDelegation(ctx, id,
		`UPDATE chats SET delegated_by = ?, delegated_until = ?, updated_at = ? WHERE id = ?`,
		by, until.UTC(), time.Now(), id)
}

func (s *Store) ClearDelegation(ctx context.Context, id string) error {
	return s.changeDelegation(ctx, id,
		`UPDATE chats SET delegated_by = '', delegated_until = NULL, updated_at = ? WHERE id = ?`,
		time.Now(), id)
}

func (s *Store) changeDelegation(ctx context.Context, id, update string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.rb(update), args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE chat_actions SET status = ?, decided_at = ? WHERE chat_id = ? AND status = ?`),
		string(ActionCanceled), time.Now().UTC(), id, string(ActionPending)); err != nil {
		return err
	}
	return tx.Commit()
}

// Includes lapsed delegations: turning delegation off for the workspace must clear them all.
func (s *Store) ListDelegated(ctx context.Context, workspaceID string) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+chatCols+` FROM chats WHERE workspace_id = ? AND delegated_by <> ''`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) DelegationAllowed(ctx context.Context, workspaceID string) (bool, error) {
	var disabled bool
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT delegation_disabled FROM chat_policies WHERE workspace_id = ?`), workspaceID).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return !disabled && err == nil, err
}

func (s *Store) SetDelegationAllowed(ctx context.Context, workspaceID string, allowed bool) error {
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO chat_policies (workspace_id, delegation_disabled) VALUES (?, ?)
		ON CONFLICT (workspace_id) DO UPDATE SET delegation_disabled = excluded.delegation_disabled`),
		workspaceID, !allowed)
	return err
}

type ActionStatus string

const (
	ActionPending   ActionStatus = "pending"
	ActionConfirmed ActionStatus = "confirmed"
	ActionFailed    ActionStatus = "failed"
	ActionCanceled  ActionStatus = "canceled"
)

var ErrActionDecided error = apperr.Conflict("chat.errors.action_decided")

type Action struct {
	ID          string       `json:"id"`
	ChatID      string       `json:"chat_id"`
	WorkspaceID string       `json:"workspace_id"`
	Agent       string       `json:"agent"`
	Method      string       `json:"method"`
	Path        string       `json:"path"`
	Body        []byte       `json:"-"`
	Summary     string       `json:"summary"`
	Status      ActionStatus `json:"status"`
	ResultCode  int          `json:"result_code,omitempty"`
	ResultError string       `json:"result_error,omitempty"`
	DecidedBy   string       `json:"decided_by,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	DecidedAt   *time.Time   `json:"decided_at,omitempty"`
}

const actionCols = `id, chat_id, workspace_id, agent, method, path, body, summary, status,
	result_code, result_error, decided_by, created_at, decided_at`

func scanAction(row interface{ Scan(...any) error }) (*Action, error) {
	var a Action
	var status string
	var decided sql.NullTime
	err := row.Scan(&a.ID, &a.ChatID, &a.WorkspaceID, &a.Agent, &a.Method, &a.Path, &a.Body, &a.Summary,
		&status, &a.ResultCode, &a.ResultError, &a.DecidedBy, &a.CreatedAt, &decided)
	if err != nil {
		return nil, err
	}
	a.Status = ActionStatus(status)
	if decided.Valid {
		t := decided.Time
		a.DecidedAt = &t
	}
	return &a, nil
}

func (s *Store) CreateAction(ctx context.Context, a *Action) error {
	a.ID = newID("act-")
	a.Status = ActionPending
	a.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO chat_actions (`+actionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', '', ?, NULL)`),
		a.ID, a.ChatID, a.WorkspaceID, a.Agent, a.Method, a.Path, a.Body, a.Summary, string(a.Status), a.CreatedAt)
	return err
}

func (s *Store) GetAction(ctx context.Context, chatID, id string) (*Action, error) {
	a, err := scanAction(s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+actionCols+` FROM chat_actions WHERE id = ? AND chat_id = ?`), id, chatID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) ListActions(ctx context.Context, chatID string, status ActionStatus) ([]Action, error) {
	q := `SELECT ` + actionCols + ` FROM chat_actions WHERE chat_id = ?`
	args := []any{chatID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	rows, err := s.db.QueryContext(ctx, s.rb(q+` ORDER BY created_at`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// The status = pending guard makes exactly one caller win, so a held request runs at most once.
func (s *Store) DecideAction(ctx context.Context, chatID, id string, status ActionStatus, by string) (*Action, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE chat_actions SET status = ?, decided_by = ?, decided_at = ?
		  WHERE id = ? AND chat_id = ? AND status = ?`),
		string(status), by, time.Now().UTC(), id, chatID, string(ActionPending))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetAction(ctx, chatID, id); err != nil {
			return nil, err
		}
		return nil, ErrActionDecided
	}
	return s.GetAction(ctx, chatID, id)
}

func (s *Store) RecordActionResult(ctx context.Context, a *Action, code int, errText string) error {
	a.ResultCode, a.ResultError = code, errText
	if code >= 400 {
		a.Status = ActionFailed
	}
	return s.execOne(ctx, `UPDATE chat_actions SET status = ?, result_code = ?, result_error = ? WHERE id = ?`,
		string(a.Status), code, errText, a.ID)
}
