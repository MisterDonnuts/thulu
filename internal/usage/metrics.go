package usage

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Filter struct {
	// Tenant boundary: a zero Scope matches nothing, not everything.
	Scope       Scope
	From        time.Time
	To          time.Time
	Engine      string
	Model       string
	AgentType   string
	ProjectID   string
	TaskID      string
	Stage       string
	ServiceTier string
}

func (f Filter) Defaults() Filter {
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-30 * 24 * time.Hour)
	}
	return f
}

func (f Filter) where(prefix string) (string, []any) {
	f = f.Defaults()
	prefix = qualify(prefix)
	conds := []string{
		fmt.Sprintf("%sfinished_at >= ?", prefix),
		fmt.Sprintf("%sfinished_at < ?", prefix),
	}
	args := []any{f.From.UTC(), f.To.UTC()}
	if scoped, scopeArgs := f.Scope.clause(prefix); scoped != "" {
		conds = append(conds, scoped)
		args = append(args, scopeArgs...)
	}
	add := func(col, val string) {
		if val == "" {
			return
		}
		conds = append(conds, fmt.Sprintf("%s%s = ?", prefix, col))
		args = append(args, val)
	}
	add("engine", f.Engine)
	add("model", f.Model)
	add("agent_type", f.AgentType)
	add("project_id", f.ProjectID)
	add("task_id", f.TaskID)
	add("stage", f.Stage)
	add("service_tier", f.ServiceTier)
	return "WHERE " + strings.Join(conds, " AND "), args
}

type TimeBucket string

const (
	BucketHour TimeBucket = "hour"
	BucketDay  TimeBucket = "day"
)

type TimePoint struct {
	Bucket        string  `json:"bucket"`
	Sessions      int     `json:"sessions"`
	Turns         int     `json:"turns"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CacheRead     int     `json:"cache_read"`
	CacheCreation int     `json:"cache_creation"`
	TotalTokens   int     `json:"total_tokens"`
	CostUSD       float64 `json:"cost_usd"`
}

func (s *Store) TimeSeries(ctx context.Context, f Filter, bucket TimeBucket) ([]TimePoint, error) {
	f = f.Defaults()
	if bucket == BucketDay && filterIsRollupCompatible(f) {
		return s.timeSeriesFromRollup(ctx, f)
	}
	return s.timeSeriesFromSessions(ctx, f, bucket)
}

func filterIsRollupCompatible(f Filter) bool {
	return f.TaskID == "" && f.Stage == "" && f.ServiceTier == ""
}

func (s *Store) timeSeriesFromRollup(ctx context.Context, f Filter) ([]TimePoint, error) {
	// day is a date string: the upper bound must be the day after f.To, or sessions earlier on the
	// last day are lost.
	upper := f.To.UTC().AddDate(0, 0, 1).Format("2006-01-02")
	conds := []string{"day >= ?", "day < ?"}
	args := []any{f.From.UTC().Format("2006-01-02"), upper}
	// usage_daily is keyed by workspace (migration 26); without this a scoped caller reads every
	// tenant's cost.
	if scoped, scopeArgs := f.Scope.clause(""); scoped != "" {
		conds = append(conds, scoped)
		args = append(args, scopeArgs...)
	}
	add := func(col, val string) {
		if val == "" {
			return
		}
		conds = append(conds, col+" = ?")
		args = append(args, val)
	}
	add("engine", f.Engine)
	add("model", f.Model)
	add("agent_type", f.AgentType)
	add("project_id", f.ProjectID)

	q := `SELECT day,
		SUM(sessions), SUM(turns),
		SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read), SUM(cache_creation),
		SUM(cost_usd)
		FROM usage_daily WHERE ` + strings.Join(conds, " AND ") + `
		GROUP BY day ORDER BY day ASC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTimePoints(rows)
}

func (s *Store) timeSeriesFromSessions(ctx context.Context, f Filter, bucket TimeBucket) ([]TimePoint, error) {
	where, args := f.where("")
	//i18n:ignore SQL expression
	bucketExpr := `to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:00:00"Z"')`
	if bucket == BucketDay {
		bucketExpr = `to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')` //i18n:ignore SQL expression
	}
	q := `SELECT ` + bucketExpr + ` AS b,
		COUNT(*), SUM(turns),
		SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read), SUM(cache_creation),
		SUM(cost_usd)
		FROM sessions ` + where + ` GROUP BY b ORDER BY b ASC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTimePoints(rows)
}

func scanTimePoints(rows *sql.Rows) ([]TimePoint, error) {
	var out []TimePoint
	for rows.Next() {
		var p TimePoint
		if err := rows.Scan(&p.Bucket, &p.Sessions, &p.Turns,
			&p.InputTokens, &p.OutputTokens, &p.CacheRead, &p.CacheCreation,
			&p.CostUSD); err != nil {
			return nil, err
		}
		p.TotalTokens = p.InputTokens + p.OutputTokens + p.CacheRead + p.CacheCreation
		out = append(out, p)
	}
	return out, rows.Err()
}

type DimensionRow struct {
	Key              string  `json:"key"`
	Sessions         int     `json:"sessions"`
	Tasks            int     `json:"tasks"`
	Turns            int     `json:"turns"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheRead        int     `json:"cache_read"`
	CacheCreation    int     `json:"cache_creation"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	UnpricedSessions int     `json:"unpriced_sessions"`
	AvgCostUSD       float64 `json:"avg_cost_usd"`
	AvgDurationMs    int64   `json:"avg_duration_ms"`
	P50DurationMs    int64   `json:"p50_duration_ms"`
	P95DurationMs    int64   `json:"p95_duration_ms"`
	CacheHitRatio    float64 `json:"cache_hit_ratio"`
	TokensPerTurn    float64 `json:"tokens_per_turn"`
	CostPerSession   float64 `json:"cost_per_session"`
	CostPerTask      float64 `json:"cost_per_task"`
	Label            string  `json:"label,omitempty"`
}

// col is interpolated into SQL; validDimensionCol is what prevents injection.
func (s *Store) ByDimension(ctx context.Context, f Filter, col string) ([]DimensionRow, error) {
	if !validDimensionCol[col] {
		return nil, apperr.Invalid("usage.errors.invalid_dimension", "column", col)
	}
	where, args := f.where("")
	q := `SELECT
		COALESCE(NULLIF(` + col + `, ''), '(none)') AS k,
		COUNT(*), COUNT(DISTINCT NULLIF(task_id, '')), SUM(turns),
		SUM(input_tokens), SUM(output_tokens),
		SUM(cache_read), SUM(cache_creation),
		SUM(cost_usd), COALESCE(SUM(cost_usd) FILTER (WHERE cost_estimated), 0),
		` + unpricedCountSQL + `, AVG(duration_ms),
		percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms),
		percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms)
		FROM sessions ` + where + ` GROUP BY k ORDER BY SUM(cost_usd) DESC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var aggregates []DimensionRow
	for rows.Next() {
		var r DimensionRow
		var avgDur, p50, p95 float64
		if err := rows.Scan(&r.Key, &r.Sessions, &r.Tasks, &r.Turns,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation,
			&r.CostUSD, &r.EstimatedCostUSD, &r.UnpricedSessions, &avgDur,
			nullableFloat{&p50}, nullableFloat{&p95}); err != nil {
			return nil, err
		}
		r.AvgDurationMs = int64(avgDur)
		r.P50DurationMs = int64(p50)
		r.P95DurationMs = int64(p95)
		r.TotalTokens = r.InputTokens + r.OutputTokens + r.CacheRead + r.CacheCreation
		if r.Sessions > 0 {
			r.AvgCostUSD = r.CostUSD / float64(r.Sessions)
			r.CostPerSession = r.AvgCostUSD
		}
		if r.Tasks > 0 {
			r.CostPerTask = r.CostUSD / float64(r.Tasks)
		}
		if r.Turns > 0 {
			r.TokensPerTurn = float64(r.TotalTokens) / float64(r.Turns)
		}
		if r.InputTokens+r.CacheRead > 0 {
			r.CacheHitRatio = float64(r.CacheRead) / float64(r.InputTokens+r.CacheRead)
		}
		aggregates = append(aggregates, r)
	}
	return aggregates, rows.Err()
}

var validDimensionCol = map[string]bool{
	"engine":       true,
	"model":        true,
	"agent_type":   true,
	"project_id":   true,
	"task_id":      true,
	"stage":        true,
	"service_tier": true,
}

// CostShareUSD duplicates CostUSD under the name older dashboards read.
type ToolRow struct {
	ToolName      string  `json:"tool_name"`
	ToolKind      string  `json:"tool_kind"`
	Invocations   int     `json:"invocations"`
	Sessions      int     `json:"sessions"`
	Tasks         int     `json:"tasks"`
	InputBytes    int64   `json:"input_bytes"`
	AvgInputBytes int64   `json:"avg_input_bytes"`
	ResultTokens  int64   `json:"result_tokens"`
	CarriedCalls  int64   `json:"carried_calls"`
	CostUSD       float64 `json:"cost_usd"`
	CostShareUSD  float64 `json:"cost_share_usd"`
}

//i18n:ignore SQL expression
const toolCostSQL = `SUM(CASE WHEN e.attributed THEN e.cost_usd + e.delegated_cost_usd
			ELSE s.cost_usd / NULLIF(stc.n, 0) END) AS cost_share`

func (s *Store) ByTool(ctx context.Context, f Filter) ([]ToolRow, error) {
	where, args := f.where("s.")
	q := `WITH session_tool_counts AS (
			SELECT session_id, COUNT(*) AS n FROM session_tool_events GROUP BY session_id
		)
		SELECT
			e.tool_name,
			COALESCE(MAX(e.tool_kind), '') AS tool_kind,
			COUNT(*) AS invocations,
			COUNT(DISTINCT e.session_id) AS sessions,
			COUNT(DISTINCT s.task_id) FILTER (WHERE s.task_id != '') AS tasks,
			SUM(e.input_bytes) AS input_bytes,
			SUM(e.result_tokens), SUM(e.carried_calls),
			` + toolCostSQL + `
		FROM session_tool_events e
		JOIN sessions s ON s.id = e.session_id
		LEFT JOIN session_tool_counts stc ON stc.session_id = e.session_id
		` + where + `
		GROUP BY e.tool_name
		ORDER BY cost_share DESC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolRow
	for rows.Next() {
		var r ToolRow
		var cost any
		if err := rows.Scan(&r.ToolName, &r.ToolKind, &r.Invocations,
			&r.Sessions, &r.Tasks, &r.InputBytes, &r.ResultTokens, &r.CarriedCalls, &cost); err != nil {
			return nil, err
		}
		if v, ok := cost.(float64); ok {
			r.CostUSD, r.CostShareUSD = v, v
		}
		if r.Invocations > 0 {
			r.AvgInputBytes = r.InputBytes / int64(r.Invocations)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type SkillRow struct {
	Skill         string  `json:"skill"`
	Invocations   int     `json:"invocations"`
	Sessions      int     `json:"sessions"`
	Tasks         int     `json:"tasks"`
	InputBytes    int64   `json:"input_bytes"`
	AvgInputBytes int64   `json:"avg_input_bytes"`
	ResultTokens  int64   `json:"result_tokens"`
	CarriedCalls  int64   `json:"carried_calls"`
	CostUSD       float64 `json:"cost_usd"`
	CostShareUSD  float64 `json:"cost_share_usd"`
}

func (s *Store) BySkill(ctx context.Context, f Filter) ([]SkillRow, error) {
	where, args := f.where("s.")
	q := `WITH session_tool_counts AS (
			SELECT session_id, COUNT(*) AS n FROM session_tool_events GROUP BY session_id
		)
		SELECT
			e.skill_name,
			COUNT(*) AS invocations,
			COUNT(DISTINCT e.session_id) AS sessions,
			COUNT(DISTINCT s.task_id) FILTER (WHERE s.task_id != '') AS tasks,
			SUM(e.input_bytes) AS input_bytes,
			SUM(e.result_tokens), SUM(e.carried_calls),
			` + toolCostSQL + `
		FROM session_tool_events e
		JOIN sessions s ON s.id = e.session_id
		LEFT JOIN session_tool_counts stc ON stc.session_id = e.session_id
		` + where + ` AND e.skill_name != ''
		GROUP BY e.skill_name
		ORDER BY cost_share DESC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SkillRow
	for rows.Next() {
		var r SkillRow
		var cost any
		if err := rows.Scan(&r.Skill, &r.Invocations,
			&r.Sessions, &r.Tasks, &r.InputBytes, &r.ResultTokens, &r.CarriedCalls, &cost); err != nil {
			return nil, err
		}
		if v, ok := cost.(float64); ok {
			r.CostUSD, r.CostShareUSD = v, v
		}
		if r.Invocations > 0 {
			r.AvgInputBytes = r.InputBytes / int64(r.Invocations)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type TaskRow struct {
	TaskID           string    `json:"task_id"`
	Title            string    `json:"title,omitempty"`
	Sessions         int       `json:"sessions"`
	Turns            int       `json:"turns"`
	TotalTokens      int       `json:"total_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	UnpricedSessions int       `json:"unpriced_sessions"`
	TotalDurationMs  int64     `json:"total_duration_ms"`
	FirstStart       time.Time `json:"first_start"`
	LastFinish       time.Time `json:"last_finish"`
}

func (s *Store) TopTasks(ctx context.Context, f Filter, limit int) ([]TaskRow, error) {
	if limit <= 0 {
		limit = 25
	}
	return s.taskRollup(ctx, f, ` ORDER BY SUM(cost_usd) DESC LIMIT ?`, limit)
}

func (s *Store) AllTasks(ctx context.Context, f Filter) ([]TaskRow, error) {
	return s.taskRollup(ctx, f, "")
}

func (s *Store) taskRollup(ctx context.Context, f Filter, tail string, tailArgs ...any) ([]TaskRow, error) {
	where, args := f.where("")
	where += " AND task_id != ''"
	q := `SELECT task_id, COUNT(*), SUM(turns),
		SUM(input_tokens + output_tokens + cache_read + cache_creation),
		SUM(cost_usd), COALESCE(SUM(cost_usd) FILTER (WHERE cost_estimated), 0),
		` + unpricedCountSQL + `, SUM(duration_ms),
		MIN(started_at), MAX(finished_at)
		FROM sessions ` + where + `
		GROUP BY task_id` + tail
	args = append(args, tailArgs...)
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var r TaskRow
		// Scan into any: MIN/MAX over timestamps may come back as time.Time, string or []byte
		// depending on driver.
		var firstStart, lastFinish any
		if err := rows.Scan(&r.TaskID, &r.Sessions, &r.Turns,
			&r.TotalTokens, &r.CostUSD, &r.EstimatedCostUSD, &r.UnpricedSessions, &r.TotalDurationMs,
			&firstStart, &lastFinish); err != nil {
			return nil, err
		}
		r.FirstStart = coerceStoredTime(firstStart)
		r.LastFinish = coerceStoredTime(lastFinish)
		out = append(out, r)
	}
	return out, rows.Err()
}

func coerceStoredTime(v any) time.Time {
	switch t := v.(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return t
	case string:
		return parseStoredTime(t)
	case []byte:
		return parseStoredTime(string(t))
	default:
		return time.Time{}
	}
}

func parseStoredTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

type Efficiency struct {
	Sessions         int            `json:"sessions"`
	Turns            int            `json:"turns"`
	TotalTokens      int            `json:"total_tokens"`
	CostUSD          float64        `json:"cost_usd"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd"`
	UnpricedSessions int            `json:"unpriced_sessions"`
	CacheHitRatio    float64        `json:"cache_hit_ratio"`
	OutputInputRatio float64        `json:"output_input_ratio"`
	TokensPerSecond  float64        `json:"tokens_per_second"`
	CostPerKtoken    float64        `json:"cost_per_ktoken"`
	MaxTokensRate    float64        `json:"max_tokens_rate"`
	ErrorRate        float64        `json:"error_rate"`
	StopReasonMix    map[string]int `json:"stop_reason_mix"`
}

func (s *Store) Efficiency(ctx context.Context, f Filter) (Efficiency, error) {
	where, args := f.where("")
	var e Efficiency
	var avgDurMs float64
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT
		COUNT(*), COALESCE(SUM(turns), 0),
		COALESCE(SUM(input_tokens + output_tokens + cache_read + cache_creation), 0),
		COALESCE(SUM(cost_usd), 0),
		COALESCE(SUM(cost_usd) FILTER (WHERE cost_estimated), 0),
		`+unpricedCountSQL+`,
		COALESCE(SUM(cache_read), 0) * 1.0 /
			NULLIF(SUM(input_tokens + cache_read), 0),
		COALESCE(SUM(output_tokens), 0) * 1.0 /
			NULLIF(SUM(input_tokens + cache_read), 0),
		COALESCE(AVG(duration_ms), 0)
		FROM sessions `+where), args...,
	).Scan(&e.Sessions, &e.Turns, &e.TotalTokens, &e.CostUSD, &e.EstimatedCostUSD, &e.UnpricedSessions,
		nullableFloat{&e.CacheHitRatio}, nullableFloat{&e.OutputInputRatio}, &avgDurMs)
	if err != nil {
		return e, err
	}
	if avgDurMs > 0 && e.Turns > 0 {
		seconds := avgDurMs / 1000.0
		if seconds > 0 {
			e.TokensPerSecond = float64(e.TotalTokens) / (seconds * float64(e.Sessions))
		}
	}
	if e.TotalTokens > 0 {
		e.CostPerKtoken = e.CostUSD * 1000.0 / float64(e.TotalTokens)
	}

	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT
		COALESCE(NULLIF(stop_reason, ''), '(unknown)'), COUNT(*)
		FROM sessions `+where+` GROUP BY stop_reason`), args...)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	e.StopReasonMix = map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return e, err
		}
		e.StopReasonMix[k] = n
		if k == "max_tokens" && e.Sessions > 0 {
			e.MaxTokensRate = float64(n) / float64(e.Sessions)
		}
		if strings.HasPrefix(k, "error") && e.Sessions > 0 {
			e.ErrorRate += float64(n) / float64(e.Sessions)
		}
	}
	return e, rows.Err()
}

type BurnRate struct {
	WindowHours  int     `json:"window_hours"`
	BaselineDays int     `json:"baseline_days"`
	RecentUSD    float64 `json:"recent_usd"`
	BaselineUSD  float64 `json:"baseline_usd"`
	Ratio        float64 `json:"ratio"`
	SessionsLast int     `json:"sessions_last"`
	SessionsBase int     `json:"sessions_baseline"`
}

func (s *Store) BurnRate(ctx context.Context, base Filter, windowHours, baselineDays int) (BurnRate, error) {
	if windowHours <= 0 {
		windowHours = 24
	}
	if baselineDays <= 0 {
		baselineDays = 30
	}
	now := time.Now().UTC()
	recent := base
	recent.To = now
	recent.From = now.Add(-time.Duration(windowHours) * time.Hour)
	baseline := base
	baseline.To = recent.From
	baseline.From = recent.From.Add(-time.Duration(baselineDays) * 24 * time.Hour)

	rCost, rSess, err := s.sumCostSessions(ctx, recent)
	if err != nil {
		return BurnRate{}, err
	}
	bCost, bSess, err := s.sumCostSessions(ctx, baseline)
	if err != nil {
		return BurnRate{}, err
	}
	br := BurnRate{
		WindowHours:  windowHours,
		BaselineDays: baselineDays,
		RecentUSD:    rCost,
		SessionsLast: rSess,
		SessionsBase: bSess,
	}
	hours := float64(baselineDays * 24)
	if hours > 0 {
		br.BaselineUSD = bCost * float64(windowHours) / hours
	}
	if br.BaselineUSD > 0 {
		br.Ratio = br.RecentUSD / br.BaselineUSD
	}
	return br, nil
}

func (s *Store) sumCostSessions(ctx context.Context, f Filter) (float64, int, error) {
	where, args := f.where("")
	var cost float64
	var n int
	err := s.db.QueryRowContext(ctx,
		s.rb(`SELECT COALESCE(SUM(cost_usd), 0), COUNT(*) FROM sessions `+where), args...,
	).Scan(&cost, &n)
	return cost, n, err
}

func (s *Store) FilterSessions(ctx context.Context, f Filter, limit int) ([]Session, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	where, args := f.where("")
	q := `SELECT ` + sessionColumns + ` FROM sessions ` + where +
		` ORDER BY finished_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		if err := scanSession(rows, &sess); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

type nullableFloat struct{ dst *float64 }

func (n nullableFloat) Scan(src any) error {
	if src == nil {
		*n.dst = 0
		return nil
	}
	switch v := src.(type) {
	case float64:
		*n.dst = v
	case int64:
		*n.dst = float64(v)
	case []byte:
		// pgx's stdlib bridge returns Postgres NUMERIC as text bytes (aggregates using * 1.0 or
		// NULLIF).
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return apperr.Wrap(err, "usage.errors.parse_float", "value", v)
		}
		*n.dst = f
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return apperr.Wrap(err, "usage.errors.parse_float", "value", v)
		}
		*n.dst = f
	}
	return nil
}
