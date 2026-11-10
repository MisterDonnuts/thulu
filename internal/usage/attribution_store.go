package usage

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Matched on the raw log because every engine receives this line and no engine parser knows it.
var optimizerLine = regexp.MustCompile(`"type"\s*:\s*"octopus\.token_optimizer"\s*,\s*"level"\s*:\s*"([a-z]+)"`)

func TokenOptimizerLevel(logs string) string {
	if m := optimizerLine.FindStringSubmatch(logs); m != nil {
		return m[1]
	}
	return ""
}

func (s *Store) SaveAttribution(ctx context.Context, sessionID string, events []engine.Event, optimizer string) error {
	var (
		model    string
		cost     float64
		outTok   int
		run      runScope
		finished time.Time
	)
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT model, cost_usd, output_tokens, task_id, agent, stage, started_at, finished_at
		FROM sessions WHERE id = ?`), sessionID).
		Scan(&model, &cost, &outTok, &run.taskID, &run.agent, &run.stage, &run.started, &finished)
	if err != nil {
		return apperr.Wrap(err, "usage.errors.attribution_session", "id", sessionID)
	}
	run.finished = finished
	prices := map[string]Pricing{}
	att := Attribute(AttributionInput{
		Events: events, Model: model, CostUSD: cost, OutputTokens: outTok,
		Prices: func(m string) (Pricing, bool) {
			if p, ok := prices[m]; ok {
				return p, p.Model != ""
			}
			p, ok := s.PricingForModel(ctx, m)
			if !ok {
				p = Pricing{}
			}
			prices[m] = p
			return p, ok
		},
	})

	delegated, err := s.delegatedCosts(ctx, att)
	if err != nil {
		return err
	}
	runDelegated, err := s.runDelegationCost(ctx, run)
	if err != nil {
		return err
	}
	rows := attributedToolEvents(sessionID, finished, events, att, delegated)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.replaceToolEvents(ctx, tx, sessionID, rows); err != nil {
		return err
	}
	if err := s.replaceCalls(ctx, tx, sessionID, att.Calls); err != nil {
		return err
	}
	if err := s.replaceBreakdown(ctx, tx, sessionID, att, delegated, runDelegated); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`UPDATE sessions SET token_optimizer = ?, attribution_version = ?,
		api_calls = ?, base_tokens = ?, compactions = ?, unattributed_usd = ? WHERE id = ?`),
		optimizer, AttributionVersion, len(att.Calls), att.BaseTokens, att.Compactions, att.UnattributedUSD, sessionID); err != nil {
		return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
	}
	return tx.Commit()
}

func (s *Store) delegatedCosts(ctx context.Context, att Attribution) (map[string]float64, error) {
	var ids []string
	for _, tc := range att.Tools {
		if tc.DelegationID != "" {
			ids = append(ids, tc.DelegationID)
		}
	}
	found, err := s.DelegationsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(found))
	for id := range found {
		out[id] = found[id].CostUSD
	}
	return out, nil
}

// The delegations table has no run id; a run's delegations are matched by task, agent, stage and
// time window.
type runScope struct {
	taskID, agent, stage string
	started, finished    time.Time
}

// Clock skew: the daemon stamps a delegation when it answers, the collector stamps a session when
// it reads the log.
const delegationSlack = time.Minute

func (s *Store) runDelegationCost(ctx context.Context, run runScope) (float64, error) {
	if run.taskID == "" {
		return 0, nil
	}
	var total float64
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT COALESCE(SUM(cost_usd), 0) FROM delegations
		WHERE task_id = ? AND agent = ? AND stage = ? AND created_at >= ? AND created_at <= ?`),
		run.taskID, run.agent, run.stage, run.started.Add(-delegationSlack).UTC(), run.finished.Add(delegationSlack).UTC()).Scan(&total)
	if err != nil {
		return 0, apperr.Wrap(err, "usage.errors.list_delegations")
	}
	return total, nil
}

func attributedToolEvents(sessionID string, fallbackTS time.Time, events []engine.Event, att Attribution, delegated map[string]float64) []ToolEvent {
	callOf := make(map[string]int, len(att.Calls))
	for i := range att.Calls {
		if id := att.Calls[i].MessageID; id != "" {
			callOf[id] = att.Calls[i].Idx + 1
		}
	}
	var (
		out  []ToolEvent
		turn int
		last string
	)
	for i := range events {
		ev := &events[i]
		if ev.Kind == engine.EventAssistant && ev.Usage != nil && (ev.MessageID == "" || ev.MessageID != last) {
			turn++
			last = ev.MessageID
		}
		if ev.Kind != engine.EventToolUse {
			continue
		}
		ts := ev.TS
		if ts.IsZero() {
			ts = fallbackTS
		}
		t := toolEventFrom(sessionID, turn, ts, *ev)
		if k, ok := callOf[ev.MessageID]; ok {
			t.TurnIndex = k
		}
		t.ToolUseID = ev.ToolUseID
		t.ParentToolUseID = ev.ParentToolUseID
		t.MCPServer = MCPServer(ev.ToolName)
		t.InputSummary = InputSummary(ev.ToolName, ev.ToolInput)
		t.Attributed = true
		if tc, ok := att.Tools[ev.ToolUseID]; ok && ev.ToolUseID != "" {
			t.ResultBytes = int64(tc.ResultBytes)
			t.ResultTokens = int64(tc.ResultTokens)
			t.ArgsTokens = int64(tc.ArgsTokens)
			t.CarriedCalls = tc.CarriedCalls
			t.WeightedTokens = tc.WeightedTokens
			t.CostUSD = tc.CostUSD
			t.IsError = tc.IsError
			t.DelegationID = tc.DelegationID
			t.DelegatedCostUSD = delegated[tc.DelegationID]
		}
		out = append(out, t)
	}
	return out
}

func (s *Store) replaceToolEvents(ctx context.Context, tx *sql.Tx, sessionID string, rows []ToolEvent) error {
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM session_tool_events WHERE session_id = ?`), sessionID); err != nil {
		return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
	}
	return s.insertToolEvents(ctx, tx, rows)
}

func (s *Store) replaceCalls(ctx context.Context, tx *sql.Tx, sessionID string, calls []Call) error {
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM session_calls WHERE session_id = ?`), sessionID); err != nil {
		return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
	}
	if len(calls) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, s.rb(`INSERT INTO session_calls
		(session_id, idx, message_id, model, parent_tool_use_id, input_tokens, cache_read,
		 cache_creation, output_tokens, prompt_size, compaction, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`))
	if err != nil {
		return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
	}
	defer func() { _ = stmt.Close() }()
	for i := range calls {
		c := &calls[i]
		var ts any
		if !c.TS.IsZero() {
			ts = c.TS
		}
		if _, err := stmt.ExecContext(ctx, sessionID, c.Idx, c.MessageID, c.Model, c.ParentToolUseID,
			c.Usage.InputTokens, c.Usage.CacheRead, c.Usage.CacheCreation, c.Usage.OutputTokens,
			c.PromptSize, c.Compaction, ts); err != nil {
			return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
		}
	}
	return nil
}

// Delegations are billed by the worker's provider, not the run's engine, so this category adds to
// the run's cost instead of being a share of it.
func (s *Store) replaceBreakdown(ctx context.Context, tx *sql.Tx, sessionID string, att Attribution,
	delegated map[string]float64, runDelegated float64) error {
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM session_cost_breakdown WHERE session_id = ?`), sessionID); err != nil {
		return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
	}
	cats := make(map[string]Share, len(att.Categories)+1)
	for k, v := range att.Categories {
		cats[k] = v
	}
	var dlg float64
	for _, c := range delegated {
		dlg += c
	}
	dlg = max(dlg, runDelegated)
	if dlg > 0 {
		cats[CategoryDelegation] = Share{CostUSD: dlg}
	}
	for _, cat := range Categories {
		v, ok := cats[cat]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.rb(`INSERT INTO session_cost_breakdown
			(session_id, category, weighted_tokens, cost_usd) VALUES (?, ?, ?, ?)`),
			sessionID, cat, v.WeightedTokens, v.CostUSD); err != nil {
			return apperr.Wrap(err, "usage.errors.attribution_save", "id", sessionID)
		}
	}
	return nil
}

func (s *Store) ReattributeSessions(ctx context.Context) (int, error) {
	jobs, err := s.reattributeQueue(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, j := range jobs {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		raw, err := os.ReadFile(j.path)
		if err != nil {
			continue
		}
		logs := string(raw)
		eng, err := engine.Get(j.eng)
		if err != nil {
			continue
		}
		var events []engine.Event
		for ev := range eng.ParseStream(strings.NewReader(logs)) {
			events = append(events, ev)
		}
		if err := s.SaveAttribution(ctx, j.id, events, TokenOptimizerLevel(logs)); err != nil {
			logx.Warn(err, "usage.log.reattribute_failed", "id", j.id)
			continue
		}
		done++
	}
	return done, nil
}

type reattributeJob struct{ id, eng, path string }

func (s *Store) reattributeQueue(ctx context.Context) ([]reattributeJob, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id, engine, logs_file FROM sessions
		WHERE attribution_version < ? AND logs_file != '' ORDER BY finished_at DESC`), AttributionVersion)
	if err != nil {
		return nil, apperr.Wrap(err, "usage.errors.attribution_queue")
	}
	defer rows.Close()
	var jobs []reattributeJob
	for rows.Next() {
		var j reattributeJob
		if err := rows.Scan(&j.id, &j.eng, &j.path); err != nil {
			return nil, apperr.Wrap(err, "usage.errors.attribution_queue")
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "usage.errors.attribution_queue")
	}
	return jobs, nil
}
