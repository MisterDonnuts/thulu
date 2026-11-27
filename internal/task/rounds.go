package task

import (
	"context"
	"encoding/json"

	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
)

// A round is one pass of the task down its board. Round 1 starts when the task is created; the
// next one starts each time the task enters a stage earlier in board order than the stage it left
// (a send-back or a manual move back). Every stage_entered event carries its round in the payload
// so the History view does not have to replay the moves; the UI applies the same rule to older
// events that have none.

// Shares the task store's single sequence with Migrations. tasks.round 0 means not computed yet
// (a task from before the column): TaskRound replays its events once and keeps the result.
var roundMigrations = []migrate.Migration{
	{ID: 57, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS round INTEGER NOT NULL DEFAULT 0`},
	{ID: 58, SQL: `ALTER TABLE workflow_events ADD COLUMN IF NOT EXISTS round INTEGER NOT NULL DEFAULT 0`},
}

// TaskRound is the task's current round; 0 when the task does not exist.
func (s *Store) TaskRound(ctx context.Context, taskID string) int {
	var round int
	if err := s.db.QueryRowContext(ctx, s.rb(`SELECT round FROM tasks WHERE id = ?`), taskID).Scan(&round); err != nil {
		return 0
	}
	if round > 0 {
		return round
	}
	t, err := s.getRaw(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.stamp_round", "task", taskID)
		return 0
	}
	round = s.currentRound(ctx, taskID, s.boardFor(t))
	_, err = s.db.ExecContext(ctx, s.rb(`UPDATE tasks SET round = ? WHERE id = ? AND round = 0`), round, taskID)
	logx.Warn(err, "task.log.stamp_round", "task", taskID)
	return round
}

func (s *Store) setRound(ctx context.Context, taskID string, round int) {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE tasks SET round = ? WHERE id = ?`), round, taskID)
	logx.Warn(err, "task.log.stamp_round", "task", taskID)
}

// stampRound adds "round" to a stage_entered payload that does not carry one yet, and returns the
// round the event starts or continues.
func (s *Store) stampRound(ctx context.Context, taskID, raw string) (string, int) {
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p == nil {
		return raw, s.TaskRound(ctx, taskID)
	}
	if n, ok := p["round"].(float64); ok && n >= 1 {
		return raw, int(n)
	}
	t, err := s.getRaw(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.stamp_round", "task", taskID)
		return raw, 0
	}
	round := s.TaskRound(ctx, taskID)
	if movesBack(s.boardFor(t), payloadString(p, "from"), payloadString(p, "to")) {
		round++
	}
	p["round"] = round
	b, err := json.Marshal(p)
	if err != nil {
		return raw, round
	}
	return string(b), round
}

// currentRound replays the task's stage_entered events: a stamped round wins, an older event moves
// the count on by the same rule.
func (s *Store) currentRound(ctx context.Context, taskID string, reg stagepkg.Registry) int {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT payload FROM workflow_events WHERE task_id = ? AND kind = ? ORDER BY created_at ASC, id ASC`),
		taskID, EventStageEntered)
	if err != nil {
		logx.Warn(err, "task.log.stamp_round", "task", taskID)
		return 1
	}
	defer rows.Close()
	round := 1
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		var p map[string]any
		if json.Unmarshal([]byte(raw), &p) != nil {
			continue
		}
		if n, ok := p["round"].(float64); ok && n >= 1 {
			round = int(n)
			continue
		}
		if movesBack(reg, payloadString(p, "from"), payloadString(p, "to")) {
			round++
		}
	}
	logx.Warn(rows.Err(), "task.log.stamp_round", "task", taskID)
	return round
}

// Whether entering to from from goes back on the board. A stage the board no longer has, or a
// virtual one, never starts a round.
func movesBack(reg stagepkg.Registry, from, to string) bool {
	if from == "" || to == "" || from == to {
		return false
	}
	f, okFrom := reg.Get(Stage(from))
	t, okTo := reg.Get(Stage(to))
	return okFrom && okTo && !f.Virtual && !t.Virtual && t.Position < f.Position
}

func payloadString(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}
