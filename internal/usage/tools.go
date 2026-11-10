package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

type ToolEvent struct {
	SessionID  string
	TS         time.Time
	TurnIndex  int
	ToolName   string
	ToolKind   string
	SkillName  string
	InputBytes int64

	// Zero on rows written before attribution.go; Attributed tells them apart.
	ToolUseID        string
	ParentToolUseID  string
	MCPServer        string
	InputSummary     string
	ResultBytes      int64
	ResultTokens     int64
	ArgsTokens       int64
	CarriedCalls     int
	WeightedTokens   float64
	CostUSD          float64
	IsError          bool
	DelegationID     string
	DelegatedCostUSD float64
	Attributed       bool
}

const toolEventCols = `session_id, ts, turn_index, tool_name, tool_kind, skill_name, input_bytes,
	tool_use_id, parent_tool_use_id, mcp_server, input_summary, result_bytes, result_tokens,
	args_tokens, carried_calls, weighted_tokens, cost_usd, is_error, delegation_id,
	delegated_cost_usd, attributed`

func (s *Store) AddToolEvents(ctx context.Context, evs []ToolEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.insertToolEvents(ctx, tx, evs); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) insertToolEvents(ctx context.Context, tx *sql.Tx, evs []ToolEvent) error {
	if len(evs) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, s.rb(`INSERT INTO session_tool_events
		(`+toolEventCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`))
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for i := range evs {
		e := &evs[i]
		if _, err := stmt.ExecContext(ctx,
			e.SessionID, e.TS, e.TurnIndex, e.ToolName, e.ToolKind, e.SkillName, e.InputBytes,
			e.ToolUseID, e.ParentToolUseID, e.MCPServer, e.InputSummary, e.ResultBytes, e.ResultTokens,
			e.ArgsTokens, e.CarriedCalls, e.WeightedTokens, e.CostUSD, e.IsError, e.DelegationID,
			e.DelegatedCostUSD, e.Attributed,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ToolEventsForSession(ctx context.Context, sessionID string) ([]ToolEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+toolEventCols+`
		FROM session_tool_events WHERE session_id = ? ORDER BY ts ASC, turn_index ASC`), sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ToolEvent
	for rows.Next() {
		e, err := scanToolEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanToolEvent(sc rowScanner) (ToolEvent, error) {
	var e ToolEvent
	err := sc.Scan(&e.SessionID, &e.TS, &e.TurnIndex, &e.ToolName, &e.ToolKind, &e.SkillName, &e.InputBytes,
		&e.ToolUseID, &e.ParentToolUseID, &e.MCPServer, &e.InputSummary, &e.ResultBytes, &e.ResultTokens,
		&e.ArgsTokens, &e.CarriedCalls, &e.WeightedTokens, &e.CostUSD, &e.IsError, &e.DelegationID,
		&e.DelegatedCostUSD, &e.Attributed)
	return e, err
}

func (s *Store) SessionHasToolEvents(ctx context.Context, sessionID string) bool {
	var n int
	if err := s.db.QueryRowContext(ctx,
		s.rb(`SELECT 1 FROM session_tool_events WHERE session_id = ? LIMIT 1`), sessionID,
	).Scan(&n); err == sql.ErrNoRows || err != nil {
		return false
	}
	return true
}

func ClassifyTool(name string) string {
	switch name {
	case "":
		return ""
	case "Skill": //i18n:ignore engine tool names
		return "skill"
	case "Bash", "BashOutput", "KillShell": //i18n:ignore engine tool names
		return "bash"
	case "Read", "Write", "Edit", "Glob", "Grep", "NotebookEdit": //i18n:ignore engine tool names
		return "fs"
	case "WebFetch", "WebSearch":
		return "web"
	case "Agent", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet": //i18n:ignore engine tool names
		return "agent"
	}
	if strings.HasPrefix(name, "mcp__") {
		return "mcp"
	}
	return "other"
}

// Claude Code versions differ: current uses "skill", older ones "name".
func SkillFromInput(input map[string]any) string {
	for _, k := range []string{"skill", "name"} {
		if v, ok := input[k]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func InputSize(input map[string]any) int64 {
	if len(input) == 0 {
		return 0
	}
	b, err := json.Marshal(input)
	if err != nil {
		return 0
	}
	return int64(len(b))
}
