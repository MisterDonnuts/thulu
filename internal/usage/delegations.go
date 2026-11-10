package usage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	DelegationOK      = "ok"
	DelegationFailed  = "failed"
	DelegationTimeout = "timeout"
	DelegationStarted = "started"
)

type Delegation struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id,omitempty"`
	TaskID        string    `json:"task_id,omitempty"`
	ChatID        string    `json:"chat_id,omitempty"`
	Agent         string    `json:"agent"`
	Stage         string    `json:"stage,omitempty"`
	Kind          string    `json:"kind"`
	ProfileID     string    `json:"profile_id,omitempty"`
	Provider      string    `json:"provider,omitempty"`
	Model         string    `json:"model,omitempty"`
	Files         int       `json:"files"`
	RequestBytes  int64     `json:"request_bytes"`
	ResponseBytes int64     `json:"response_bytes"`
	InputTokens   int64     `json:"input_tokens"`
	OutputTokens  int64     `json:"output_tokens"`
	CostUSD       float64   `json:"cost_usd"`
	CostEstimated bool      `json:"cost_estimated,omitempty"`
	DurationMs    int64     `json:"duration_ms"`
	Status        string    `json:"status"`
	ErrorCode     string    `json:"error_code,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Store) AddDelegation(ctx context.Context, d Delegation) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	if d.CostUSD == 0 && d.InputTokens+d.OutputTokens > 0 {
		if p, ok := s.PricingForModel(ctx, d.Model); ok {
			if c := p.Cost(int(d.InputTokens), int(d.OutputTokens), 0, 0); c > 0 {
				d.CostUSD = c
				d.CostEstimated = true
			}
		}
	}
	_, err := s.db.ExecContext(ctx, s.rb(`INSERT INTO delegations
		(id, workspace_id, task_id, chat_id, agent, stage, kind, profile_id, provider, model,
		 files, request_bytes, response_bytes, input_tokens, output_tokens, cost_usd,
		 cost_estimated, duration_ms, status, error_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		d.ID, d.WorkspaceID, d.TaskID, d.ChatID, d.Agent, d.Stage, d.Kind, d.ProfileID, d.Provider, d.Model,
		d.Files, d.RequestBytes, d.ResponseBytes, d.InputTokens, d.OutputTokens, d.CostUSD,
		d.CostEstimated, d.DurationMs, d.Status, d.ErrorCode, d.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "usage.errors.add_delegation", "id", d.ID)
	}
	return nil
}

// The match on status and on task_id/chat_id/agent stops a run from finishing another run's
// delegation or its own twice.
func (s *Store) FinishDelegation(ctx context.Context, d Delegation) (bool, error) {
	const match = ` WHERE id = ? AND status = ? AND task_id = ? AND chat_id = ? AND agent = ?`
	matchArgs := []any{d.ID, DelegationStarted, d.TaskID, d.ChatID, d.Agent}
	var model string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT model FROM delegations`+match), matchArgs...).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(err, "usage.errors.finish_delegation", "id", d.ID)
	}
	d.CostEstimated = false
	if d.CostUSD == 0 && d.InputTokens+d.OutputTokens > 0 {
		if p, ok := s.PricingForModel(ctx, model); ok {
			if c := p.Cost(int(d.InputTokens), int(d.OutputTokens), 0, 0); c > 0 {
				d.CostUSD = c
				d.CostEstimated = true
			}
		}
	}
	args := append([]any{d.Files, d.RequestBytes, d.ResponseBytes, d.InputTokens, d.OutputTokens,
		d.CostUSD, d.CostEstimated, d.DurationMs, d.Status, d.ErrorCode}, matchArgs...)
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE delegations SET
		files = ?, request_bytes = ?, response_bytes = ?, input_tokens = ?, output_tokens = ?,
		cost_usd = ?, cost_estimated = ?, duration_ms = ?, status = ?, error_code = ?`+match), args...)
	if err != nil {
		return false, apperr.Wrap(err, "usage.errors.finish_delegation", "id", d.ID)
	}
	// Two racing finishes both pass the SELECT; the status in the WHERE lets only one update.
	n, err := res.RowsAffected()
	if err != nil {
		return false, apperr.Wrap(err, "usage.errors.finish_delegation", "id", d.ID)
	}
	return n > 0, nil
}

func (s *Store) CloseStartedDelegations(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE delegations SET status = ?, error_code = ?
		WHERE status = ? AND created_at < ?`),
		DelegationFailed, "tokenopt.errors.unfinished", DelegationStarted, cutoff.UTC())
	if err != nil {
		return 0, apperr.Wrap(err, "usage.errors.close_started_delegations")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(err, "usage.errors.close_started_delegations")
	}
	return n, nil
}

const delegationCols = `id, workspace_id, task_id, chat_id, agent, stage, kind, profile_id, provider, model,
	files, request_bytes, response_bytes, input_tokens, output_tokens, cost_usd,
	cost_estimated, duration_ms, status, error_code, created_at`

func scanDelegation(sc rowScanner) (Delegation, error) {
	var d Delegation
	err := sc.Scan(&d.ID, &d.WorkspaceID, &d.TaskID, &d.ChatID, &d.Agent, &d.Stage, &d.Kind,
		&d.ProfileID, &d.Provider, &d.Model, &d.Files, &d.RequestBytes, &d.ResponseBytes,
		&d.InputTokens, &d.OutputTokens, &d.CostUSD, &d.CostEstimated, &d.DurationMs,
		&d.Status, &d.ErrorCode, &d.CreatedAt)
	return d, err
}

func (s *Store) DelegationsByID(ctx context.Context, ids []string) (map[string]Delegation, error) {
	out := make(map[string]Delegation, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]byte, 0, 2*len(ids))
	for i, id := range ids {
		args[i] = id
		if i > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+delegationCols+` FROM delegations WHERE id IN (`+string(marks)+`)`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.list_delegations")
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDelegation(rows)
		if err != nil {
			return nil, apperr.Wrap(err, "usage.errors.list_delegations")
		}
		out[d.ID] = d
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}
