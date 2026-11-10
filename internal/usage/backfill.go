package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type backfillRow struct {
	id, eng, tier, logs string
}

type SessionLabelGap struct {
	ID        string
	Engine    string
	ProjectID string
}

func (s *Store) SessionsMissingLabels(ctx context.Context) ([]SessionLabelGap, error) {
	rows, err := s.db.QueryContext(ctx,
		s.rb(`SELECT id, engine, project_id FROM sessions
		 WHERE engine = '' OR project_id = ''`))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SessionLabelGap
	for rows.Next() {
		var g SessionLabelGap
		if err := rows.Scan(&g.ID, &g.Engine, &g.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SetSessionLabels(ctx context.Context, id, engineName, projectID string) error {
	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE sessions SET
		engine     = CASE WHEN ? != '' THEN ? ELSE engine     END,
		project_id = CASE WHEN ? != '' THEN ? ELSE project_id END
		WHERE id = ?`),
		engineName, engineName, projectID, projectID, id,
	); err != nil {
		return err
	}
	// Stamp workspace_id too: otherwise the session shows in /usage/by-project but stays invisible
	// to the tenant that owns the project.
	if projectID == "" || s.workspaceOf == nil {
		return nil
	}
	wid := s.workspaceOf(ctx, "", projectID)
	if wid == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		s.rb(`UPDATE sessions SET workspace_id = ? WHERE id = ? AND workspace_id = ''`), wid, id)
	return err
}

func (s *Store) BackfillToolEvents(ctx context.Context) (int, error) {
	queue, err := s.loadBackfillQueue(ctx)
	if err != nil {
		return 0, err
	}
	touched := 0
	for _, r := range queue {
		if s.backfillOne(ctx, r) {
			touched++
		}
	}
	return touched, nil
}

func (s *Store) loadBackfillQueue(ctx context.Context) ([]backfillRow, error) {
	rows, err := s.db.QueryContext(ctx,
		s.rb(`SELECT id, engine, service_tier, logs_file FROM sessions WHERE logs_file != ''`))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var queue []backfillRow
	for rows.Next() {
		var r backfillRow
		if err := rows.Scan(&r.id, &r.eng, &r.tier, &r.logs); err != nil {
			return nil, err
		}
		queue = append(queue, r)
	}
	return queue, rows.Err()
}

func (s *Store) backfillOne(ctx context.Context, r backfillRow) bool {
	if s.SessionHasToolEvents(ctx, r.id) {
		return false
	}
	if _, err := os.Stat(r.logs); err != nil {
		return false
	}
	events, tier, err := replayLogToolEvents(r.id, r.eng, r.logs)
	if err != nil {
		logx.Warn(err, "usage.log.backfill_failed", "id", r.id)
		return false
	}
	if len(events) == 0 && tier == "" {
		return false
	}
	if len(events) > 0 {
		if err := s.AddToolEvents(ctx, events); err != nil {
			logx.Warn(err, "usage.log.persist_backfilled_events", "id", r.id)
			return false
		}
	}
	if tier != "" && r.tier == "" {
		if _, err := s.db.ExecContext(ctx,
			s.rb(`UPDATE sessions SET service_tier = ? WHERE id = ?`), tier, r.id); err != nil {
			logx.Warn(err, "usage.log.backfill_service_tier", "id", r.id)
		}
	}
	return true
}

func replayLogToolEvents(sessionID, engineName, path string) ([]ToolEvent, string, error) {
	//nolint:gosec // G304: path comes from the usage store (server-written), not external input
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()

	eng := engine.MustGet(engineName)
	var (
		events    []ToolEvent
		tier      string
		turnIndex int
		ts        = time.Now().UTC()
	)
	for ev := range eng.ParseStream(f) {
		switch ev.Kind {
		case engine.EventAssistant:
			turnIndex++
			if ev.Usage != nil && ev.Usage.ServiceTier != "" {
				tier = ev.Usage.ServiceTier
			}
		case engine.EventToolUse:
			events = append(events, toolEventFrom(sessionID, turnIndex, ts, ev))
		case engine.EventInit, engine.EventRateLimit, engine.EventResult, engine.EventUnknown,
			engine.EventToolResult, engine.EventUserMessage:
		}
	}
	return events, tier, nil
}

func toolEventFrom(sessionID string, turnIndex int, ts time.Time, ev engine.Event) ToolEvent {
	skill := ""
	if ev.ToolName == "Skill" { //i18n:ignore engine tool name
		skill = SkillFromInput(ev.ToolInput)
	}
	return ToolEvent{
		SessionID:  sessionID,
		TS:         ts,
		TurnIndex:  turnIndex,
		ToolName:   ev.ToolName,
		ToolKind:   ClassifyTool(ev.ToolName),
		SkillName:  skill,
		InputBytes: InputSize(ev.ToolInput),
	}
}

func (s *Store) BackfillAdoptOrphanSessions(ctx context.Context) (int, error) {
	logsDir := filepath.Join(s.dataDir, "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return 0, nil
	}
	known, err := s.loadKnownSessionIDs(ctx)
	if err != nil {
		return 0, err
	}
	adopted := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if s.adoptOne(ctx, logsDir, e.Name(), known) {
			adopted++
		}
	}
	return adopted, nil
}

func (s *Store) loadKnownSessionIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id FROM sessions`))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	known := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
}

func (s *Store) adoptOne(ctx context.Context, logsDir, name string, known map[string]bool) bool {
	id := containerIDFromLogName(name)
	if id == "" || known[id] {
		return false
	}
	path := filepath.Join(logsDir, name)
	sess, evs := buildSessionFromLog(id, path)
	if sess.ID == "" {
		return false
	}
	if err := s.Add(ctx, sess); err != nil {
		logx.Warn(err, "usage.log.adopt_orphan", "id", id)
		return false
	}
	if len(evs) > 0 {
		if err := s.AddToolEvents(ctx, evs); err != nil {
			logx.Warn(err, "usage.log.store_adopted_events", "id", id)
		}
	}
	return true
}

// Coupled to the log name RecordRun writes: run ids never contain '_', so the last '_'-segment is
// the id.
func containerIDFromLogName(name string) string {
	base := strings.TrimSuffix(name, ".log")
	parts := strings.Split(base, "_")
	if len(parts) < 3 {
		return ""
	}
	id := parts[len(parts)-1]
	if len(id) < 8 {
		return ""
	}
	return id
}

func buildSessionFromLog(id, path string) (Session, []ToolEvent) {
	//nolint:gosec // G304: path constructed from operator-owned logs dir
	f, err := os.Open(path)
	if err != nil {
		return Session{}, nil
	}
	defer func() { _ = f.Close() }()
	finished := fileModTime(f)

	sess := Session{
		ID:         id,
		LogsFile:   path,
		StartedAt:  finished,
		FinishedAt: finished,
	}
	evs := walkLogIntoSession(id, finished, f, &sess)
	if sess.Turns == 0 && sess.InputTokens == 0 && sess.OutputTokens == 0 {
		return Session{}, nil
	}
	return sess, evs
}

func fileModTime(f *os.File) time.Time {
	st, err := f.Stat()
	if err != nil || st == nil {
		return time.Now().UTC()
	}
	return st.ModTime().UTC()
}

func walkLogIntoSession(id string, ts time.Time, f *os.File, sess *Session) []ToolEvent {
	eng := engine.MustGet("")
	var (
		evs       []ToolEvent
		turnIndex int
	)
	for ev := range eng.ParseStream(f) {
		switch ev.Kind {
		case engine.EventInit:
			sess.Model = ev.Model
		case engine.EventAssistant:
			turnIndex++
			if ev.Usage != nil && ev.Usage.ServiceTier != "" {
				sess.ServiceTier = ev.Usage.ServiceTier
			}
		case engine.EventToolUse:
			evs = append(evs, toolEventFrom(id, turnIndex, ts, ev))
		case engine.EventResult:
			// One result per message the run answered; sum them, as the collector does.
			sess.Turns += ev.NumTurns
			sess.DurationMs += ev.DurationMs
			sess.CostUSD += ev.CostUSD
			sess.StopReason = ev.StopReason
			if ev.Usage != nil {
				sess.InputTokens += ev.Usage.InputTokens
				sess.OutputTokens += ev.Usage.OutputTokens
				sess.CacheRead += ev.Usage.CacheRead
				sess.CacheCreation += ev.Usage.CacheCreation
			}
		case engine.EventRateLimit, engine.EventUnknown, engine.EventToolResult, engine.EventUserMessage:
		}
	}
	return evs
}
