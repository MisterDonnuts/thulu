package task

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
)

type Dist struct {
	N     int   `json:"n"`
	AvgMs int64 `json:"avg_ms"`
	P50Ms int64 `json:"p50_ms"`
	P90Ms int64 `json:"p90_ms"`
	MaxMs int64 `json:"max_ms"`
}

type StageBucket struct {
	Stage string `json:"stage"`
	Count int    `json:"count"`
}

type StageDist struct {
	Stage string `json:"stage"`
	Dist
}

type ThroughputPoint struct {
	Bucket    string `json:"bucket"`
	Completed int    `json:"completed"`
	Created   int    `json:"created"`
}

type FlowMetrics struct {
	TasksTotal   int `json:"tasks_total"`
	TasksOpen    int `json:"tasks_open"`
	TasksDone    int `json:"tasks_done"`
	TasksBlocked int `json:"tasks_blocked"`
	TasksRunning int `json:"tasks_running"`
	TasksFailed  int `json:"tasks_failed"`

	TasksCreated   int `json:"tasks_created"`
	TasksCompleted int `json:"tasks_completed"`
	TasksTouched   int `json:"tasks_touched"`

	ByStage    []StageBucket     `json:"by_stage"`
	Throughput []ThroughputPoint `json:"throughput"`

	LeadTime   Dist        `json:"lead_time"`
	StageDwell []StageDist `json:"stage_dwell"`

	WIPAge Dist `json:"wip_age"`

	ReworkedTasks int     `json:"reworked_tasks"`
	ReworkRate    float64 `json:"rework_rate"`
	Kickbacks     int     `json:"kickbacks"`

	FirstPassTasks int     `json:"first_pass_tasks"`
	FirstPassRate  float64 `json:"first_pass_rate"`

	AgentRuns   int     `json:"agent_runs"`
	RunsPerTask float64 `json:"runs_per_task"`

	HumanComments         int     `json:"human_comments"`
	HumanInterventionRate float64 `json:"human_intervention_rate"`

	FlagsRaised   int `json:"flags_raised"`
	FlagsResolved int `json:"flags_resolved"`
	PRsOpened     int `json:"prs_opened"`
}

type flowTask struct {
	id      string
	stage   Stage
	boardID string
	typeID  string
	status  RunStatus
	created time.Time
}

type flowEvent struct {
	taskID string
	kind   EventKind
	from   Stage
	to     Stage
	at     time.Time
}

func (s *Store) ProjectFlow(ctx context.Context, projectID string, from, to time.Time) (FlowMetrics, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -30)
	}

	tasks, err := s.tasksForProject(ctx, projectID)
	if err != nil {
		return FlowMetrics{}, err
	}
	if len(tasks) == 0 {
		return emptyFlow(from, to), nil
	}

	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.id
	}
	events, err := s.flowEventsFor(ctx, ids)
	if err != nil {
		return FlowMetrics{}, err
	}
	humanComments, err := s.humanCommentsFor(ctx, ids, from, to)
	if err != nil {
		return FlowMetrics{}, err
	}

	m := s.computeFlow(tasks, events, from, to)
	m.HumanComments = humanComments
	if m.TasksCompleted > 0 {
		m.FirstPassRate = float64(m.FirstPassTasks) / float64(m.TasksCompleted)
	}
	if m.TasksTouched > 0 {
		m.HumanInterventionRate = float64(humanComments) / float64(m.TasksTouched)
	}
	return m, nil
}

func (s *Store) humanCommentsFor(ctx context.Context, ids []string, from, to time.Time) (int, error) {
	total := 0
	for start := 0; start < len(ids); start += eventChunk {
		end := min(start+eventChunk, len(ids))
		batch := ids[start:end]

		args := make([]any, 0, len(batch)+3)
		for _, id := range batch {
			args = append(args, id)
		}
		// .UTC() is required: created_at is timestamp without time zone and the driver encodes the
		// time.Time's local wall clock.
		args = append(args, string(SourceUser), from.UTC(), to.UTC())
		row := s.db.QueryRowContext(ctx, s.rb(
			`SELECT COUNT(*) FROM task_comments
			  WHERE task_id IN (`+placeholders(len(batch))+`)
			    AND source = ? AND created_at >= ? AND created_at <= ?`), args...)
		var n int
		if err := row.Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func emptyFlow(from, to time.Time) FlowMetrics {
	return FlowMetrics{
		ByStage:    []StageBucket{},
		Throughput: dayBuckets(from, to, nil, nil),
		StageDwell: []StageDist{},
	}
}

func (s *Store) tasksForProject(ctx context.Context, projectID string) ([]flowTask, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT t.id, t.stage, t.board_id, t.type_id, t.status, t.created_at
		   FROM tasks t
		   JOIN task_projects tp ON tp.task_id = t.id
		  WHERE tp.project_id = ?`), projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []flowTask{}
	for rows.Next() {
		var t flowTask
		var status string
		if err := rows.Scan(&t.id, &t.stage, &t.boardID, &t.typeID, &status, &t.created); err != nil {
			return nil, err
		}
		t.status = RunStatus(status)
		if t.status == "" {
			t.status = StatusIdle
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Stays under the Postgres parameter ceiling and SQLite's variable limit.
const eventChunk = 400

func (s *Store) flowEventsFor(ctx context.Context, ids []string) (map[string][]flowEvent, error) {
	out := map[string][]flowEvent{}
	kinds := []any{
		string(EventStageEntered), string(EventAgentRunStarted),
		string(EventFlagRaised), string(EventFlagResolved), string(EventPROpened),
		string(EventApprovalRequested), string(EventApprovalDecided), string(EventApprovalWaived),
	}
	for start := 0; start < len(ids); start += eventChunk {
		end := min(start+eventChunk, len(ids))
		batch := ids[start:end]

		args := make([]any, 0, len(batch)+len(kinds))
		for _, id := range batch {
			args = append(args, id)
		}
		args = append(args, kinds...)
		q := `SELECT task_id, kind, summary, payload, created_at FROM workflow_events` +
			` WHERE task_id IN (` + placeholders(len(batch)) + `)` +
			` AND kind IN (` + placeholders(len(kinds)) + `)` +
			` ORDER BY created_at ASC`

		if err := s.scanFlowEvents(ctx, q, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) scanFlowEvents(ctx context.Context, q string, args []any, out map[string][]flowEvent) error {
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e flowEvent
		var summary, payload string
		if err := rows.Scan(&e.taskID, &e.kind, &summary, &payload, &e.at); err != nil {
			return err
		}
		if e.kind == EventStageEntered {
			e.from, e.to = stageTransition(summary, payload)
			if e.to == "" {
				continue
			}
		}
		out[e.taskID] = append(out[e.taskID], e)
	}
	return rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.Repeat("?,", n-1) + "?"
}

// The auditor's kick_back path records a nil payload and only a "from → to (kick_back)" summary, so
// the summary is parsed as a fallback.
func stageTransition(summary, payload string) (Stage, Stage) {
	if payload != "" && payload != "{}" {
		var p struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err == nil && p.To != "" {
			return Stage(p.From), Stage(p.To)
		}
	}
	left, right, ok := strings.Cut(summary, "→")
	if !ok {
		return "", ""
	}
	to := strings.TrimSpace(right)
	if i := strings.Index(to, " ("); i >= 0 {
		to = to[:i]
	}
	return Stage(strings.TrimSpace(left)), Stage(to)
}

type flowAcc struct {
	m             FlowMetrics
	stageCounts   map[string]int
	dwellByStage  map[string][]int64
	leadTimes     []int64
	wipAges       []int64
	completedDays map[string]int
	createdDays   map[string]int
	touched       map[string]bool
}

func newFlowAcc(n int) *flowAcc {
	return &flowAcc{
		m:             FlowMetrics{TasksTotal: n},
		stageCounts:   map[string]int{},
		dwellByStage:  map[string][]int64{},
		completedDays: map[string]int{},
		createdDays:   map[string]int{},
		touched:       map[string]bool{},
	}
}

func (s *Store) computeFlow(tasks []flowTask, events map[string][]flowEvent, from, to time.Time) FlowMetrics {
	acc := newFlowAcc(len(tasks))
	for _, t := range tasks {
		reg := s.boards.BoardFor(t.boardID, t.typeID)
		terminal := terminalSet(reg)
		acc.census(t, terminal)
		acc.counters(t.id, events[t.id], from, to)
		acc.walk(reg, terminal, t, events[t.id], from, to)
	}
	return acc.finish(from, to)
}

func (a *flowAcc) census(t flowTask, terminal map[Stage]bool) {
	a.stageCounts[string(t.stage)]++
	if terminal[t.stage] {
		a.m.TasksDone++
	} else {
		a.m.TasksOpen++
	}
	switch t.status {
	case StatusBlocked, StatusCredentialBlocked:
		a.m.TasksBlocked++
	case StatusRunning:
		a.m.TasksRunning++
	case StatusFailed, StatusNotStarted, StatusResumeFailed:
		a.m.TasksFailed++
	default:
	}
}

func (a *flowAcc) counters(taskID string, evs []flowEvent, from, to time.Time) {
	for _, e := range evs {
		if !inWindow(e.at, from, to) {
			continue
		}
		switch e.kind {
		case EventAgentRunStarted:
			a.m.AgentRuns++
			a.touched[taskID] = true
		// flag_* events predate approvals and approval_* follow; never both for the same change, so
		// no double counting.
		case EventFlagRaised, EventApprovalRequested:
			a.m.FlagsRaised++
		case EventFlagResolved, EventApprovalDecided, EventApprovalWaived:
			a.m.FlagsResolved++
		case EventPROpened:
			a.m.PRsOpened++
		default:
		}
	}
}

type replay struct {
	completedAt time.Time
	lastStage   Stage
	lastAt      time.Time
	reworked    bool
	// Ignores the window on purpose: a kickback before `from` still disqualifies a first pass.
	everReworked bool
}

func (a *flowAcc) walk(reg stagepkg.Registry, terminal map[Stage]bool, t flowTask, evs []flowEvent, from, to time.Time) {
	if inWindow(t.created, from, to) {
		a.m.TasksCreated++
		a.createdDays[dayKey(t.created)]++
		a.touched[t.id] = true
	}

	r := a.replayTransitions(reg, terminal, t, evs, from, to)
	if r.reworked {
		a.m.ReworkedTasks++
	}

	if !terminal[t.stage] {
		if age := to.Sub(r.lastAt); age > 0 {
			a.wipAges = append(a.wipAges, age.Milliseconds())
		}
	}
	if !r.completedAt.IsZero() && inWindow(r.completedAt, from, to) {
		a.m.TasksCompleted++
		a.completedDays[dayKey(r.completedAt)]++
		a.touched[t.id] = true
		if !r.everReworked {
			a.m.FirstPassTasks++
		}
		if lt := r.completedAt.Sub(t.created); lt > 0 {
			a.leadTimes = append(a.leadTimes, lt.Milliseconds())
		}
	}
}

func (a *flowAcc) replayTransitions(reg stagepkg.Registry, terminal map[Stage]bool, t flowTask, evs []flowEvent, from, to time.Time) replay {
	r := replay{}
	r.lastStage, r.lastAt = firstStageOf(evs, t)
	for _, e := range evs {
		if e.kind != EventStageEntered {
			continue
		}
		if isBackwards(reg, e.from, e.to) {
			r.everReworked = true
			if inWindow(e.at, from, to) {
				a.m.Kickbacks++
				a.touched[t.id] = true
				r.reworked = true
			}
		}
		if inWindow(e.at, from, to) {
			a.recordInterval(r.lastStage, r.lastAt, e.at)
		}
		if r.completedAt.IsZero() && terminal[e.to] {
			r.completedAt = e.at
		}
		r.lastStage, r.lastAt = e.to, e.at
	}
	return r
}

func (a *flowAcc) recordInterval(stage Stage, openedAt, closedAt time.Time) {
	if d := closedAt.Sub(openedAt); d > 0 {
		a.dwellByStage[string(stage)] = append(a.dwellByStage[string(stage)], d.Milliseconds())
	}
}

func (a *flowAcc) finish(from, to time.Time) FlowMetrics {
	m := a.m
	if n := len(a.touched); n > 0 {
		m.ReworkRate = float64(m.ReworkedTasks) / float64(n)
		m.RunsPerTask = float64(m.AgentRuns) / float64(n)
	}
	m.TasksTouched = len(a.touched)
	m.ByStage = sortedBuckets(a.stageCounts)
	m.StageDwell = sortedDwell(a.dwellByStage)
	m.Throughput = dayBuckets(from, to, a.completedDays, a.createdDays)
	m.LeadTime = Summarize(a.leadTimes)
	m.WIPAge = Summarize(a.wipAges)
	return m
}

func firstStageOf(evs []flowEvent, t flowTask) (Stage, time.Time) {
	for _, e := range evs {
		if e.kind == EventStageEntered {
			if e.from != "" {
				return e.from, t.created
			}
			break
		}
	}
	return t.stage, t.created
}

func terminalSet(reg stagepkg.Registry) map[Stage]bool {
	out := map[Stage]bool{}
	for _, st := range reg.Terminals() {
		out[st] = true
	}
	return out
}

// Stages unknown to the board (renamed or deleted) count as forward so edited boards don't inflate
// rework.
func isBackwards(reg stagepkg.Registry, fromStage, toStage Stage) bool {
	if fromStage == "" || toStage == "" || fromStage == toStage {
		return false
	}
	pos := map[Stage]int{}
	board := reg.List()
	for i := range board {
		pos[board[i].ID] = i
	}
	f, okF := pos[fromStage]
	t, okT := pos[toStage]
	if !okF || !okT {
		return false
	}
	return t < f
}

func inWindow(t, from, to time.Time) bool {
	return !t.Before(from) && !t.After(to)
}

func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Capped at 180 points to bound the payload size.
func dayBuckets(from, to time.Time, completed, created map[string]int) []ThroughputPoint {
	out := []ThroughputPoint{}
	day := from.UTC().Truncate(24 * time.Hour)
	end := to.UTC()
	for i := 0; !day.After(end) && i < 180; i++ {
		k := day.Format("2006-01-02")
		out = append(out, ThroughputPoint{Bucket: k, Completed: completed[k], Created: created[k]})
		day = day.AddDate(0, 0, 1)
	}
	return out
}

func sortedBuckets(counts map[string]int) []StageBucket {
	out := make([]StageBucket, 0, len(counts))
	for k, v := range counts {
		out = append(out, StageBucket{Stage: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Stage < out[j].Stage
	})
	return out
}

func sortedDwell(byStage map[string][]int64) []StageDist {
	out := make([]StageDist, 0, len(byStage))
	for k, v := range byStage {
		out = append(out, StageDist{Stage: k, Dist: Summarize(v)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P50Ms != out[j].P50Ms {
			return out[i].P50Ms > out[j].P50Ms
		}
		return out[i].Stage < out[j].Stage
	})
	return out
}

func Summarize(ms []int64) Dist {
	if len(ms) == 0 {
		return Dist{}
	}
	s := append([]int64(nil), ms...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum int64
	for _, v := range s {
		sum += v
	}
	return Dist{
		N:     len(s),
		AvgMs: sum / int64(len(s)),
		P50Ms: percentile(s, 0.50),
		P90Ms: percentile(s, 0.90),
		MaxMs: s[len(s)-1],
	}
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*p+0.9999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
