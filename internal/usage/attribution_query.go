package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type CategoryShare struct {
	Category string `json:"category"`
	Share
}

type ToolCostRow struct {
	ToolUseID        string    `json:"tool_use_id"`
	ToolName         string    `json:"tool_name"`
	ToolKind         string    `json:"tool_kind"`
	MCPServer        string    `json:"mcp_server"`
	SkillName        string    `json:"skill_name"`
	TurnIndex        int       `json:"turn_index"`
	TS               time.Time `json:"ts"`
	ResultBytes      int64     `json:"result_bytes"`
	ResultTokens     int64     `json:"result_tokens"`
	ArgsTokens       int64     `json:"args_tokens"`
	CarriedCalls     int       `json:"carried_calls"`
	WeightedTokens   float64   `json:"weighted_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	IsError          bool      `json:"is_error"`
	DelegationID     string    `json:"delegation_id"`
	DelegatedCostUSD float64   `json:"delegated_cost_usd"`
	InputSummary     string    `json:"input_summary"`
}

func toolCostRow(e *ToolEvent) ToolCostRow {
	return ToolCostRow{
		ToolUseID: e.ToolUseID, ToolName: e.ToolName, ToolKind: e.ToolKind, MCPServer: e.MCPServer,
		SkillName: e.SkillName, TurnIndex: e.TurnIndex, TS: e.TS, ResultBytes: e.ResultBytes,
		ResultTokens: e.ResultTokens, ArgsTokens: e.ArgsTokens, CarriedCalls: e.CarriedCalls,
		WeightedTokens: e.WeightedTokens, CostUSD: e.CostUSD, IsError: e.IsError,
		DelegationID: e.DelegationID, DelegatedCostUSD: e.DelegatedCostUSD, InputSummary: e.InputSummary,
	}
}

type CallRow struct {
	Idx           int        `json:"idx"`
	Model         string     `json:"model"`
	InputTokens   int64      `json:"input_tokens"`
	CacheRead     int64      `json:"cache_read"`
	CacheCreation int64      `json:"cache_creation"`
	OutputTokens  int64      `json:"output_tokens"`
	PromptSize    int64      `json:"prompt_size"`
	Compaction    bool       `json:"compaction"`
	TS            *time.Time `json:"ts"`
}

type SessionBreakdown struct {
	SessionID          string          `json:"session_id"`
	AttributionVersion int             `json:"attribution_version"`
	CostUSD            float64         `json:"cost_usd"`
	UnattributedUSD    float64         `json:"unattributed_usd"`
	APICalls           int             `json:"api_calls"`
	BaseTokens         int64           `json:"base_tokens"`
	Compactions        int             `json:"compactions"`
	TokenOptimizer     string          `json:"token_optimizer"`
	Categories         []CategoryShare `json:"categories"`
	Tools              []ToolCostRow   `json:"tools"`
	Calls              []CallRow       `json:"calls"`
}

// No access check here: the caller must verify the session is one the requester may see.
func (s *Store) Breakdown(ctx context.Context, sessionID string) (SessionBreakdown, error) {
	b := SessionBreakdown{SessionID: sessionID, Categories: []CategoryShare{}, Tools: []ToolCostRow{}, Calls: []CallRow{}}
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT attribution_version, cost_usd, unattributed_usd, api_calls,
		base_tokens, compactions, token_optimizer FROM sessions WHERE id = ?`), sessionID).
		Scan(&b.AttributionVersion, &b.CostUSD, &b.UnattributedUSD, &b.APICalls, &b.BaseTokens, &b.Compactions, &b.TokenOptimizer)
	if err != nil {
		return b, apperr.Wrap(err, "usage.errors.attribution_read", "id", sessionID)
	}
	cats, err := s.categoriesFor(ctx, []string{sessionID})
	if err != nil {
		return b, err
	}
	b.Categories = cats
	evs, err := s.ToolEventsForSession(ctx, sessionID)
	if err != nil {
		return b, apperr.Wrap(err, "usage.errors.attribution_read", "id", sessionID)
	}
	for i := range evs {
		b.Tools = append(b.Tools, toolCostRow(&evs[i]))
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT idx, model, input_tokens, cache_read, cache_creation,
		output_tokens, prompt_size, compaction, ts FROM session_calls WHERE session_id = ? ORDER BY idx`), sessionID)
	if err != nil {
		return b, apperr.Wrap(err, "usage.errors.attribution_read", "id", sessionID)
	}
	defer rows.Close()
	for rows.Next() {
		var c CallRow
		var ts sql.NullTime
		if err := rows.Scan(&c.Idx, &c.Model, &c.InputTokens, &c.CacheRead, &c.CacheCreation,
			&c.OutputTokens, &c.PromptSize, &c.Compaction, &ts); err != nil {
			return b, apperr.Wrap(err, "usage.errors.attribution_read", "id", sessionID)
		}
		if ts.Valid {
			t := ts.Time.UTC()
			c.TS = &t
		}
		b.Calls = append(b.Calls, c)
	}
	return b, rows.Err()
}

func (s *Store) categoriesFor(ctx context.Context, ids []string) ([]CategoryShare, error) {
	out := []CategoryShare{}
	if len(ids) == 0 {
		return out, nil
	}
	marks, args := inList(ids)
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT category, SUM(weighted_tokens), SUM(cost_usd)
		FROM session_cost_breakdown WHERE session_id IN (`+marks+`) GROUP BY category`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.attribution_read", "id", ids[0])
	}
	defer rows.Close()
	got := map[string]Share{}
	for rows.Next() {
		var c string
		var sh Share
		if err := rows.Scan(&c, &sh.WeightedTokens, &sh.CostUSD); err != nil {
			return nil, apperr.Wrap(err, "usage.errors.attribution_read", "id", ids[0])
		}
		got[c] = sh
	}
	for _, c := range Categories {
		if sh, ok := got[c]; ok {
			out = append(out, CategoryShare{Category: c, Share: sh})
		}
	}
	return out, rows.Err()
}

type TopCallRow struct {
	ToolCostRow
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Stage     string `json:"stage"`
}

type TaskBreakdown struct {
	Categories      []CategoryShare `json:"categories"`
	CostUSD         float64         `json:"cost_usd"`
	UnattributedUSD float64         `json:"unattributed_usd"`
	TopCalls        []TopCallRow    `json:"top_calls"`
	Delegations     struct {
		Count   int     `json:"count"`
		CostUSD float64 `json:"cost_usd"`
	} `json:"delegations"`
}

func (s *Store) TaskBreakdownFor(ctx context.Context, taskID string, ids []string) (TaskBreakdown, bool, error) {
	b := TaskBreakdown{Categories: []CategoryShare{}, TopCalls: []TopCallRow{}}
	if len(ids) == 0 {
		return b, false, nil
	}
	attributed, err := s.attributedAmong(ctx, ids, &b)
	if err != nil || len(attributed) == 0 {
		return b, false, err
	}
	if b.Categories, err = s.categoriesFor(ctx, attributed); err != nil {
		return b, false, err
	}
	if b.TopCalls, err = s.topCalls(ctx, attributed, 10); err != nil {
		return b, false, err
	}
	if taskID != "" {
		err = s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*), COALESCE(SUM(cost_usd), 0) FROM delegations WHERE task_id = ?`), taskID).
			Scan(&b.Delegations.Count, &b.Delegations.CostUSD)
		if err != nil {
			return b, false, apperr.Wrap(err, "usage.errors.list_delegations")
		}
	}
	return b, true, nil
}

func (s *Store) attributedAmong(ctx context.Context, ids []string, b *TaskBreakdown) ([]string, error) {
	marks, args := inList(ids)
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id, cost_usd, unattributed_usd FROM sessions
		WHERE id IN (`+marks+`) AND attribution_version > 0`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.attribution_read", "id", ids[0])
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		var cost, un float64
		if err := rows.Scan(&id, &cost, &un); err != nil {
			return nil, apperr.Wrap(err, "usage.errors.attribution_read", "id", ids[0])
		}
		out = append(out, id)
		b.CostUSD += cost
		b.UnattributedUSD += un
	}
	return out, rows.Err()
}

func (s *Store) topCalls(ctx context.Context, ids []string, limit int) ([]TopCallRow, error) {
	marks, args := inList(ids)
	args = append(args, limit)
	//i18n:ignore SQL
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+prefixed("e.", toolEventCols)+`, s.agent, s.stage
		FROM session_tool_events e JOIN sessions s ON s.id = e.session_id
		WHERE e.session_id IN (`+marks+`) AND e.attributed AND e.parent_tool_use_id = ''
		ORDER BY e.cost_usd + e.delegated_cost_usd DESC LIMIT ?`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.attribution_read", "id", ids[0])
	}
	defer rows.Close()
	out := []TopCallRow{}
	for rows.Next() {
		var e ToolEvent
		var agent, stage string
		if err := rows.Scan(&e.SessionID, &e.TS, &e.TurnIndex, &e.ToolName, &e.ToolKind, &e.SkillName, &e.InputBytes,
			&e.ToolUseID, &e.ParentToolUseID, &e.MCPServer, &e.InputSummary, &e.ResultBytes, &e.ResultTokens,
			&e.ArgsTokens, &e.CarriedCalls, &e.WeightedTokens, &e.CostUSD, &e.IsError, &e.DelegationID,
			&e.DelegatedCostUSD, &e.Attributed, &agent, &stage); err != nil {
			return nil, apperr.Wrap(err, "usage.errors.attribution_read", "id", ids[0])
		}
		out = append(out, TopCallRow{ToolCostRow: toolCostRow(&e), SessionID: e.SessionID, Agent: agent, Stage: stage})
	}
	return out, rows.Err()
}

type MCPServerRow struct {
	MCPServer      string  `json:"mcp_server"`
	Invocations    int     `json:"invocations"`
	Sessions       int     `json:"sessions"`
	ResultTokens   int64   `json:"result_tokens"`
	WeightedTokens float64 `json:"weighted_tokens"`
	CostUSD        float64 `json:"cost_usd"`
}

func (s *Store) ByMCPServer(ctx context.Context, f Filter) ([]MCPServerRow, error) {
	where, args := f.where("s.")
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT e.mcp_server, COUNT(*), COUNT(DISTINCT e.session_id),
			COALESCE(SUM(e.result_tokens), 0), COALESCE(SUM(e.weighted_tokens), 0), COALESCE(SUM(e.cost_usd), 0)
		FROM session_tool_events e JOIN sessions s ON s.id = e.session_id
		`+where+` AND e.mcp_server != ''
		GROUP BY e.mcp_server ORDER BY 6 DESC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServerRow
	for rows.Next() {
		var r MCPServerRow
		if err := rows.Scan(&r.MCPServer, &r.Invocations, &r.Sessions, &r.ResultTokens, &r.WeightedTokens, &r.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type OptimizerRow struct {
	Level              string  `json:"level"`
	Sessions           int     `json:"sessions"`
	CostUSD            float64 `json:"cost_usd"`
	AvgCostUSD         float64 `json:"avg_cost_usd"`
	ToolsCostUSD       float64 `json:"tools_cost_usd"`
	DelegationsCostUSD float64 `json:"delegations_cost_usd"`
}

// Only attributed runs: older runs have no level recorded and would pass for "off".
func (s *Store) ByTokenOptimizer(ctx context.Context, f Filter) ([]OptimizerRow, error) {
	where, args := f.where("s.")
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT COALESCE(NULLIF(s.token_optimizer, ''), 'off'), COUNT(*), COALESCE(SUM(s.cost_usd), 0),
			COALESCE(SUM((SELECT b.cost_usd FROM session_cost_breakdown b WHERE b.session_id = s.id AND b.category = 'tools')), 0),
			COALESCE(SUM((SELECT b.cost_usd FROM session_cost_breakdown b WHERE b.session_id = s.id AND b.category = 'delegation')), 0)
		FROM sessions s `+where+` AND s.attribution_version > 0
		GROUP BY 1 ORDER BY 1`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OptimizerRow
	for rows.Next() {
		var r OptimizerRow
		if err := rows.Scan(&r.Level, &r.Sessions, &r.CostUSD, &r.ToolsCostUSD, &r.DelegationsCostUSD); err != nil {
			return nil, err
		}
		if r.Sessions > 0 {
			r.AvgCostUSD = (r.CostUSD + r.DelegationsCostUSD) / float64(r.Sessions)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type DelegationReport struct {
	Summary struct {
		Count         int     `json:"count"`
		OK            int     `json:"ok"`
		Failed        int     `json:"failed"`
		CostUSD       float64 `json:"cost_usd"`
		InputTokens   int64   `json:"input_tokens"`
		OutputTokens  int64   `json:"output_tokens"`
		AvgDurationMs int64   `json:"avg_duration_ms"`
	} `json:"summary"`
	ByAgent []DelegationAgentRow `json:"by_agent"`
	Recent  []Delegation         `json:"recent"`
}

type DelegationAgentRow struct {
	Agent   string  `json:"agent"`
	Stage   string  `json:"stage"`
	Count   int     `json:"count"`
	CostUSD float64 `json:"cost_usd"`
}

// Engine, model, project and service tier are session columns; delegations do not have them.
func (f Filter) delegationWhere() (string, []any) {
	f = f.Defaults()
	conds := []string{"created_at >= ?", "created_at < ?"}
	args := []any{f.From.UTC(), f.To.UTC()}
	if c, a := f.Scope.clause(""); c != "" {
		conds = append(conds, c)
		args = append(args, a...)
	}
	for col, v := range map[string]string{"task_id": f.TaskID, "stage": f.Stage, "agent": f.AgentType} {
		if v != "" {
			conds = append(conds, col+" = ?")
			args = append(args, v)
		}
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) Delegations(ctx context.Context, f Filter, recent int) (DelegationReport, error) {
	r := DelegationReport{ByAgent: []DelegationAgentRow{}, Recent: []Delegation{}}
	where, args := f.delegationWhere()
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*),
			COUNT(*) FILTER (WHERE status = 'ok'), COUNT(*) FILTER (WHERE status != 'ok'),
			COALESCE(SUM(cost_usd), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(AVG(duration_ms), 0)::BIGINT
		FROM delegations `+where), args...).
		Scan(&r.Summary.Count, &r.Summary.OK, &r.Summary.Failed, &r.Summary.CostUSD,
			&r.Summary.InputTokens, &r.Summary.OutputTokens, &r.Summary.AvgDurationMs)
	if err != nil {
		return r, apperr.Wrap(err, "usage.errors.list_delegations")
	}
	if r.ByAgent, err = s.delegationsByAgent(ctx, where, args); err != nil {
		return r, err
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+delegationCols+` FROM delegations `+where+
		fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, recent)), args...)
	if err != nil {
		return r, apperr.Wrap(err, "usage.errors.list_delegations")
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDelegation(rows)
		if err != nil {
			return r, apperr.Wrap(err, "usage.errors.list_delegations")
		}
		r.Recent = append(r.Recent, d)
	}
	return r, rows.Err()
}

func (s *Store) delegationsByAgent(ctx context.Context, where string, args []any) ([]DelegationAgentRow, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT agent, stage, COUNT(*), COALESCE(SUM(cost_usd), 0)
		FROM delegations `+where+` GROUP BY agent, stage ORDER BY 4 DESC, 1, 2`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.list_delegations")
	}
	defer rows.Close()
	out := []DelegationAgentRow{}
	for rows.Next() {
		var a DelegationAgentRow
		if err := rows.Scan(&a.Agent, &a.Stage, &a.Count, &a.CostUSD); err != nil {
			return nil, apperr.Wrap(err, "usage.errors.list_delegations")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func inList(ids []string) (string, []any) {
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	return strings.Join(marks, ","), args
}

func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
