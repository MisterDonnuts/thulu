package task

import (
	"context"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Tasks on a deleted board are judged by the default board's terminals, matching
// boardState.board(); the two must agree
// or such tasks block dependents and pin the group sandbox forever. Derived live, not denormalized,
// because boards are hand-edited.
func (s *Store) notTerminalFrag(alias string) (string, []any) {
	if alias != "" {
		alias += "."
	}
	var clauses []string
	var args []any
	var known []string
	for _, b := range s.boards.BoardList() {
		known = append(known, b.ID)
		terminals := s.boards.Board(b.ID).Terminals()
		if len(terminals) == 0 {
			continue
		}
		args = append(args, b.ID)
		args = append(args, stageArgs(terminals)...)
		//i18n:ignore SQL fragment
		clauses = append(clauses, "("+alias+"board_id = ? AND "+alias+"stage IN ("+
			placeholderList(len(terminals))+"))")
	}
	if frag, fallbackArgs := s.ghostBoardClause(alias, known); frag != "" {
		clauses = append(clauses, frag)
		args = append(args, fallbackArgs...)
	}
	if len(clauses) == 0 {
		return "1=1", nil
	}
	return "NOT (" + strings.Join(clauses, " OR ") + ")", args
}

func (s *Store) ghostBoardClause(alias string, known []string) (string, []any) {
	if len(known) == 0 {
		return "", nil
	}
	terminals := s.boards.Board(s.boards.DefaultBoardID()).Terminals()
	if len(terminals) == 0 {
		return "", nil
	}
	args := make([]any, 0, len(known)+len(terminals))
	for _, id := range known {
		args = append(args, id)
	}
	args = append(args, stageArgs(terminals)...)
	return "(" + alias + "board_id NOT IN (" + placeholderList(len(known)) + ") AND " + //i18n:ignore SQL fragment
		alias + "stage IN (" + placeholderList(len(terminals)) + "))", args
}

// n must be >= 1.
func placeholderList(n int) string { return strings.Repeat("?,", n-1) + "?" }

func stageArgs(stages []Stage) []any {
	out := make([]any, len(stages))
	for i, st := range stages {
		out[i] = string(st)
	}
	return out
}

type DepKind string

const (
	DepHard DepKind = "hard"
	DepSoft DepKind = "soft"
)

func (k DepKind) Valid() bool { return k == DepHard || k == DepSoft }

type DepRef struct {
	TaskID    string    `json:"task_id"`
	Title     string    `json:"title"`
	Stage     Stage     `json:"stage"`
	Kind      DepKind   `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AddDependency(ctx context.Context, taskID, dependsOnID string, kind DepKind) error {
	if taskID == "" || dependsOnID == "" {
		return apperr.Invalid("task.errors.dependency_ids_required")
	}
	if taskID == dependsOnID {
		return apperr.Invalid("task.errors.dependency_on_itself")
	}
	if !kind.Valid() {
		return apperr.Invalid("task.errors.dependency_bad_kind")
	}
	if _, err := s.getRaw(ctx, taskID); err != nil {
		return apperr.NotFound("task.errors.task_not_found", "id", taskID)
	}
	if _, err := s.getRaw(ctx, dependsOnID); err != nil {
		return apperr.NotFound("task.errors.task_not_found", "id", dependsOnID)
	}
	reachable, err := s.depsReachable(ctx, dependsOnID, taskID)
	if err != nil {
		return err
	}
	if reachable {
		return apperr.Conflict("task.errors.dependency_cycle")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_dependencies (task_id, depends_on_task_id, kind, created_at)
		 VALUES (?,?,?,?)`),
		taskID, dependsOnID, string(kind), time.Now().UTC(),
	); err != nil {
		return apperr.WrapKind(apperr.KindConflict, err, "task.errors.dependency_insert")
	}
	return nil
}

func (s *Store) depsReachable(ctx context.Context, from, to string) (bool, error) {
	if from == to {
		return true, nil
	}
	row := s.db.QueryRowContext(ctx, s.rb(`
		WITH RECURSIVE reachable(id) AS (
			SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ?
			UNION
			SELECT d.depends_on_task_id
			FROM task_dependencies d
			JOIN reachable r ON d.task_id = r.id
		)
		SELECT count(*) FROM reachable WHERE id = ?`),
		from, to,
	)
	var n int
	if err := row.Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) RemoveDependency(ctx context.Context, taskID, dependsOnID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM task_dependencies WHERE task_id = ? AND depends_on_task_id = ?`),
		taskID, dependsOnID,
	)
	return err
}

func (s *Store) GetDependencies(ctx context.Context, taskID string) ([]DepRef, error) {
	return s.listDeps(ctx, taskID, `
		SELECT d.depends_on_task_id, t.title, t.stage, d.kind, d.created_at
		FROM task_dependencies d
		JOIN tasks t ON t.id = d.depends_on_task_id
		WHERE d.task_id = ?
		ORDER BY d.created_at ASC`)
}

func (s *Store) GetDependents(ctx context.Context, taskID string) ([]DepRef, error) {
	return s.listDeps(ctx, taskID, `
		SELECT d.task_id, t.title, t.stage, d.kind, d.created_at
		FROM task_dependencies d
		JOIN tasks t ON t.id = d.task_id
		WHERE d.depends_on_task_id = ?
		ORDER BY d.created_at ASC`)
}

func (s *Store) listDeps(ctx context.Context, id, query string) ([]DepRef, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(query), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DepRef{}
	for rows.Next() {
		var d DepRef
		if err := rows.Scan(&d.TaskID, &d.Title, &d.Stage, &d.Kind, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) IsBlocked(ctx context.Context, taskID string) (bool, []DepRef, error) {
	frag, fragArgs := s.notTerminalFrag("t")
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT d.depends_on_task_id, t.title, t.stage, d.kind, d.created_at
		FROM task_dependencies d
		JOIN tasks t ON t.id = d.depends_on_task_id
		WHERE d.task_id = ? AND d.kind = ? AND `+frag+`
		ORDER BY d.created_at ASC`),
		append([]any{taskID, string(DepHard)}, fragArgs...)...,
	)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	blockers := []DepRef{}
	for rows.Next() {
		var d DepRef
		if err := rows.Scan(&d.TaskID, &d.Title, &d.Stage, &d.Kind, &d.CreatedAt); err != nil {
			return false, nil, err
		}
		blockers = append(blockers, d)
	}
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	return len(blockers) > 0, blockers, nil
}

func (s *Store) BlockedSummary(ctx context.Context) (map[string]int, error) {
	return s.blockedSummaryFor(ctx, nil)
}

// Only the dependent side is filtered; blockers outside the page or on other boards still count.
func (s *Store) blockedSummaryFor(ctx context.Context, taskIDs []any) (map[string]int, error) {
	frag, fragArgs := s.notTerminalFrag("t")
	args := append([]any{string(DepHard)}, fragArgs...)
	scope := ""
	if len(taskIDs) > 0 {
		scope = ` AND d.task_id IN (` + placeholderList(len(taskIDs)) + `)`
		args = append(args, taskIDs...)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT d.task_id, COUNT(*)
		FROM task_dependencies d
		JOIN tasks t ON t.id = d.depends_on_task_id
		WHERE d.kind = ? AND `+frag+scope+`
		GROUP BY d.task_id`),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Caller must invoke this AFTER the target task's terminal stage is persisted.
func (s *Store) JustUnblockedDependents(ctx context.Context, doneTaskID string) ([]string, error) {
	frag, fragArgs := s.notTerminalFrag("t2")
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT DISTINCT d.task_id
		FROM task_dependencies d
		WHERE d.depends_on_task_id = ? AND d.kind = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM task_dependencies d2
		      JOIN tasks t2 ON t2.id = d2.depends_on_task_id
		      WHERE d2.task_id = d.task_id
		        AND d2.kind = ?
		        AND `+frag+`
		  )`),
		append([]any{doneTaskID, string(DepHard), string(DepHard)}, fragArgs...)...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
