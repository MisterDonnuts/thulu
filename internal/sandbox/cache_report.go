package sandbox

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	SignalGrowthPct       = 50.0
	SignalCapacityPct     = 85.0
	SignalLowHitRate      = 0.5
	SignalLowHitMinRuns   = 5
	SignalPrewarmFailures = 3
	SignalKeyChurnKeys    = 5
	SignalKeyChurnWindow  = 7 * 24 * time.Hour
	SignalToolUnusedAfter = 30 * 24 * time.Hour
)

const (
	SignalCacheGrowth    = "cache_growth"
	SignalCacheFull      = "cache_full"
	SignalLowHit         = "low_hit_rate"
	SignalPrewarmFailing = "prewarm_failing"
	SignalKeyChurn       = "key_churn"
	SignalToolUnused     = "tool_unused"
)

var CacheReportRanges = map[string]int{"7d": 7, "30d": 30, "90d": 90}

type CacheReport struct {
	Range       string           `json:"range"`
	Since       time.Time        `json:"since"`
	GeneratedAt time.Time        `json:"generated_at"`
	LogSince    *time.Time       `json:"log_since"`
	Totals      CacheTotals      `json:"totals"`
	Daily       []CacheDay       `json:"daily"`
	Workspaces  []CacheWorkspace `json:"workspaces"`
	Projects    []CacheProject   `json:"projects"`
	Tools       []CacheToolRow   `json:"tools"`
	Signals     []CacheSignal    `json:"signals"`
	Thresholds  CacheThresholds  `json:"thresholds"`
}

type CacheThresholds struct {
	GrowthPct       float64 `json:"growth_pct"`
	CapacityPct     float64 `json:"capacity_pct"`
	LowHitRate      float64 `json:"low_hit_rate"`
	LowHitMinRuns   int     `json:"low_hit_min_runs"`
	PrewarmFailures int     `json:"prewarm_failures"`
	KeyChurnKeys    int     `json:"key_churn_keys"`
	KeyChurnDays    int     `json:"key_churn_days"`
	ToolUnusedDays  int     `json:"tool_unused_days"`
}

// Prewarms are counted apart: they install by design and would drag the hit rate down.
type CacheCounts struct {
	Runs           int            `json:"runs"`
	WithSetup      int            `json:"with_setup"`
	Hits           int            `json:"hits"`
	HitRate        *float64       `json:"hit_rate"`
	Statuses       map[string]int `json:"statuses"`
	ToolDownloads  int            `json:"tool_downloads"`
	Prewarms       int            `json:"prewarms"`
	PrewarmsFailed int            `json:"prewarms_failed"`
}

type CacheTotals struct {
	CacheCounts
	NoCache          int              `json:"no_cache"`
	RunsDownloading  int              `json:"runs_downloading"`
	AvgMs            map[string]int64 `json:"avg_ms"`
	AvgRanSetupMs    int64            `json:"avg_ran_setup_ms"`
	TimeSavedMs      int64            `json:"time_saved_ms"`
	CacheBytes       int64            `json:"cache_bytes"`
	FullestPct       *float64         `json:"fullest_pct"`
	FullestWorkspace string           `json:"fullest_workspace,omitempty"`
}

type CacheDay struct {
	Date           string `json:"date"`
	Skipped        int    `json:"skipped"`
	Restored       int    `json:"restored"`
	Ran            int    `json:"ran"`
	Failed         int    `json:"failed"`
	None           int    `json:"none"`
	Prewarms       int    `json:"prewarms"`
	PrewarmsFailed int    `json:"prewarms_failed"`
	ToolDownloads  int    `json:"tool_downloads"`
}

type CacheWorkspace struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	CacheCounts
	MeasuredAt      *time.Time       `json:"measured_at"`
	TotalBytes      int64            `json:"total_bytes"`
	CapacityBytes   int64            `json:"capacity_bytes"`
	UsedBytes       int64            `json:"used_bytes"`
	CapacityPct     *float64         `json:"capacity_pct"`
	Areas           map[string]int64 `json:"areas"`
	FirstMeasuredAt *time.Time       `json:"first_measured_at"`
	GrowthBytes     int64            `json:"growth_bytes"`
	GrowthPct       *float64         `json:"growth_pct"`
}

type CachePrewarm struct {
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

type CacheProject struct {
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name,omitempty"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	CacheCounts
	LastStatus        string        `json:"last_status,omitempty"`
	LastRunAt         *time.Time    `json:"last_run_at"`
	AvgSetupMs        int64         `json:"avg_setup_ms"`
	SnapshotBytes     int64         `json:"snapshot_bytes"`
	SnapshotCount     int           `json:"snapshot_count"`
	SnapshotNewest    string        `json:"snapshot_newest,omitempty"`
	DistinctKeys      int           `json:"distinct_keys"`
	RecentKeys        int           `json:"recent_keys"`
	LastPrewarm       *CachePrewarm `json:"last_prewarm"`
	PrewarmFailStreak int           `json:"prewarm_fail_streak"`
}

type CacheToolRow struct {
	Tool       string     `json:"tool"`
	Version    string     `json:"version"`
	Bytes      int64      `json:"bytes"`
	Workspaces []string   `json:"workspaces"`
	LastUsed   *time.Time `json:"last_used"`
	Unused     bool       `json:"unused"`
}

type CacheSignal struct {
	Kind        string  `json:"kind"`
	WorkspaceID string  `json:"workspace_id,omitempty"`
	ProjectID   string  `json:"project_id,omitempty"`
	Tool        string  `json:"tool,omitempty"`
	Value       float64 `json:"value"`
	Threshold   float64 `json:"threshold"`
}

func (s *Store) CacheReport(ctx context.Context, rangeName string, now time.Time) (*CacheReport, error) {
	days, ok := CacheReportRanges[rangeName]
	if !ok {
		return nil, apperr.Invalid("sandbox.errors.cache_report_range", "range", rangeName)
	}
	since := rangeStart(now, days)
	loadFrom := since
	if t := now.Add(-SignalToolUnusedAfter); t.Before(loadFrom) {
		loadFrom = t
	}
	runs, err := s.setupRunsSince(ctx, loadFrom)
	if err != nil {
		return nil, err
	}
	latest, err := s.cacheUsagePerWorkspace(ctx, time.Unix(0, 0), true)
	if err != nil {
		return nil, err
	}
	first, err := s.cacheUsagePerWorkspace(ctx, since, false)
	if err != nil {
		return nil, err
	}
	logSince, err := s.oldestSetupRun(ctx)
	if err != nil {
		return nil, err
	}
	return buildCacheReport(cacheReportInput{
		now: now, rangeName: rangeName, days: days,
		runs: runs, latest: latest, first: first, logSince: logSince,
	}), nil
}

func rangeStart(now time.Time, days int) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
}

type cacheReportInput struct {
	now       time.Time
	rangeName string
	days      int
	runs      []SetupRun
	latest    []CacheUsage
	first     []CacheUsage
	logSince  *time.Time
}

func isHit(status string) bool { return status == SetupStateSkipped || status == SetupStateRestored }

func (c *CacheCounts) add(r *SetupRun) {
	c.ToolDownloads += len(r.ToolsDownloaded)
	if r.Kind == SetupRunPrewarm {
		c.Prewarms++
		if r.Status == SetupStateFailed {
			c.PrewarmsFailed++
		}
		return
	}
	c.Runs++
	if c.Statuses == nil {
		c.Statuses = map[string]int{}
	}
	c.Statuses[r.Status]++
	if r.Status != SetupStateNone {
		c.WithSetup++
	}
	if isHit(r.Status) {
		c.Hits++
	}
}

func (c *CacheCounts) finish() {
	if c.Statuses == nil {
		c.Statuses = map[string]int{}
	}
	if c.WithSetup > 0 {
		v := float64(c.Hits) / float64(c.WithSetup)
		c.HitRate = &v
	}
}

type mean struct {
	sum int64
	n   int64
}

func (a *mean) add(v int64) { a.sum += v; a.n++ }
func (a *mean) get() int64 {
	if a.n == 0 {
		return 0
	}
	return a.sum / a.n
}

type projectAcc struct {
	row       CacheProject
	inRange   bool
	keys      map[string]bool
	churnKeys map[string]bool
	setup     mean
	prewarms  []*SetupRun
}

type reportBuilder struct {
	in         cacheReportInput
	since      time.Time
	totals     CacheTotals
	byStatus   map[string]*mean
	ranSetup   mean
	prewarmRan mean
	hitRestore []int64
	days       map[string]*CacheDay
	dayOrder   []string
	ws         map[string]*CacheWorkspace
	projects   map[string]*projectAcc
}

func buildCacheReport(in cacheReportInput) *CacheReport {
	b := &reportBuilder{
		in: in, since: rangeStart(in.now, in.days),
		byStatus: map[string]*mean{}, days: map[string]*CacheDay{},
		ws: map[string]*CacheWorkspace{}, projects: map[string]*projectAcc{},
	}
	for d := b.since; !d.After(in.now.UTC()); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		b.days[key] = &CacheDay{Date: key}
		b.dayOrder = append(b.dayOrder, key)
	}
	for i := range in.runs {
		b.addRun(&in.runs[i])
	}
	b.addUsage()
	rep := &CacheReport{
		Range: in.rangeName, Since: b.since, GeneratedAt: in.now.UTC(), LogSince: in.logSince,
		Totals:     b.finishTotals(),
		Daily:      make([]CacheDay, 0, len(b.dayOrder)),
		Workspaces: b.workspaceRows(),
		Projects:   b.projectRows(),
		Tools:      b.toolRows(),
		Thresholds: CacheThresholds{
			GrowthPct: SignalGrowthPct, CapacityPct: SignalCapacityPct,
			LowHitRate: SignalLowHitRate, LowHitMinRuns: SignalLowHitMinRuns,
			PrewarmFailures: SignalPrewarmFailures, KeyChurnKeys: SignalKeyChurnKeys,
			KeyChurnDays:   int(SignalKeyChurnWindow / (24 * time.Hour)),
			ToolUnusedDays: int(SignalToolUnusedAfter / (24 * time.Hour)),
		},
	}
	for _, k := range b.dayOrder {
		rep.Daily = append(rep.Daily, *b.days[k])
	}
	rep.Signals = buildSignals(rep)
	return rep
}

func (b *reportBuilder) workspace(id string) *CacheWorkspace {
	w, ok := b.ws[id]
	if !ok {
		w = &CacheWorkspace{WorkspaceID: id, Areas: map[string]int64{}}
		b.ws[id] = w
	}
	return w
}

func (b *reportBuilder) project(id, workspaceID string) *projectAcc {
	p, ok := b.projects[id]
	if !ok {
		p = &projectAcc{row: CacheProject{ProjectID: id, WorkspaceID: workspaceID},
			keys: map[string]bool{}, churnKeys: map[string]bool{}}
		b.projects[id] = p
	}
	if p.row.WorkspaceID == "" {
		p.row.WorkspaceID = workspaceID
	}
	return p
}

func (b *reportBuilder) addRun(r *SetupRun) {
	p := b.project(r.ProjectID, r.WorkspaceID)
	if r.Kind == SetupRunPrewarm {
		p.prewarms = append(p.prewarms, r)
	}
	if r.SetupKey != "" && !r.CreatedAt.Before(b.in.now.Add(-SignalKeyChurnWindow)) {
		p.churnKeys[r.SetupKey] = true
	}
	if r.CreatedAt.Before(b.since) {
		return
	}
	p.inRange = true
	if r.SetupKey != "" {
		p.keys[r.SetupKey] = true
	}
	b.totals.add(r)
	b.workspace(r.WorkspaceID).add(r)
	p.row.add(r)
	b.addToDay(r)
	if r.Kind == SetupRunPrewarm {
		if r.Status == SetupStateRan {
			b.prewarmRan.add(r.SetupMs)
		}
		return
	}
	b.addTaskRun(r, p)
}

func (b *reportBuilder) addTaskRun(r *SetupRun, p *projectAcc) {
	acc, ok := b.byStatus[r.Status]
	if !ok {
		acc = &mean{}
		b.byStatus[r.Status] = acc
	}
	acc.add(r.TotalMs)
	switch {
	case r.Status == SetupStateRan:
		b.ranSetup.add(r.SetupMs)
		p.setup.add(r.SetupMs)
	case isHit(r.Status):
		b.hitRestore = append(b.hitRestore, r.RestoreMs)
	}
	if r.NoCache {
		b.totals.NoCache++
	}
	if len(r.ToolsDownloaded) > 0 {
		b.totals.RunsDownloading++
	}
	t := r.CreatedAt
	p.row.LastStatus, p.row.LastRunAt = r.Status, &t
}

func (b *reportBuilder) addToDay(r *SetupRun) {
	d, ok := b.days[r.CreatedAt.UTC().Format(time.DateOnly)]
	if !ok {
		return
	}
	d.ToolDownloads += len(r.ToolsDownloaded)
	if r.Kind == SetupRunPrewarm {
		d.Prewarms++
		if r.Status == SetupStateFailed {
			d.PrewarmsFailed++
		}
		return
	}
	switch r.Status {
	case SetupStateSkipped:
		d.Skipped++
	case SetupStateRestored:
		d.Restored++
	case SetupStateRan:
		d.Ran++
	case SetupStateFailed:
		d.Failed++
	default:
		d.None++
	}
}

func (b *reportBuilder) addUsage() {
	firstBy := map[string]*CacheUsage{}
	for i := range b.in.first {
		firstBy[b.in.first[i].WorkspaceID] = &b.in.first[i]
	}
	for i := range b.in.latest {
		u := &b.in.latest[i]
		w := b.workspace(u.WorkspaceID)
		at := u.MeasuredAt
		w.MeasuredAt = &at
		w.TotalBytes, w.CapacityBytes, w.UsedBytes = u.TotalBytes, u.CapacityBytes, u.UsedBytes
		if u.Areas != nil {
			w.Areas = u.Areas
		}
		if u.CapacityBytes > 0 {
			pct := float64(u.UsedBytes) / float64(u.CapacityBytes) * 100
			w.CapacityPct = &pct
		}
		if f, ok := firstBy[u.WorkspaceID]; ok && f.MeasuredAt.Before(u.MeasuredAt) {
			fat := f.MeasuredAt
			w.FirstMeasuredAt = &fat
			w.GrowthBytes = u.TotalBytes - f.TotalBytes
			if f.TotalBytes > 0 {
				pct := float64(w.GrowthBytes) / float64(f.TotalBytes) * 100
				w.GrowthPct = &pct
			}
		}
		for _, s := range u.Snapshots {
			p := b.project(s.ProjectID, u.WorkspaceID)
			p.row.SnapshotBytes, p.row.SnapshotCount, p.row.SnapshotNewest = s.Bytes, s.Count, s.Newest
		}
	}
}

func (b *reportBuilder) finishTotals() CacheTotals {
	t := b.totals
	t.finish()
	t.AvgMs = map[string]int64{}
	for status, acc := range b.byStatus {
		t.AvgMs[status] = acc.get()
	}
	t.AvgRanSetupMs = b.ranSetup.get()
	if b.ranSetup.n == 0 {
		t.AvgRanSetupMs = b.prewarmRan.get()
	}
	for _, restore := range b.hitRestore {
		t.TimeSavedMs += max(0, t.AvgRanSetupMs-restore)
	}
	for _, w := range b.ws {
		t.CacheBytes += w.TotalBytes
		if w.CapacityPct != nil && (t.FullestPct == nil || *w.CapacityPct > *t.FullestPct) {
			pct := *w.CapacityPct
			t.FullestPct, t.FullestWorkspace = &pct, w.WorkspaceID
		}
	}
	return t
}

func (b *reportBuilder) workspaceRows() []CacheWorkspace {
	out := make([]CacheWorkspace, 0, len(b.ws))
	for _, w := range b.ws {
		w.finish()
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalBytes != out[j].TotalBytes {
			return out[i].TotalBytes > out[j].TotalBytes
		}
		return out[i].WorkspaceID < out[j].WorkspaceID
	})
	return out
}

func (b *reportBuilder) projectRows() []CacheProject {
	out := make([]CacheProject, 0, len(b.projects))
	for _, p := range b.projects {
		if !p.inRange && p.row.SnapshotCount == 0 && p.row.SnapshotBytes == 0 {
			continue
		}
		row := p.row
		row.finish()
		row.AvgSetupMs = p.setup.get()
		row.DistinctKeys = len(p.keys)
		row.RecentKeys = len(p.churnKeys)
		row.LastPrewarm, row.PrewarmFailStreak = prewarmState(p.prewarms)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	return out
}

func prewarmState(prewarms []*SetupRun) (*CachePrewarm, int) {
	if len(prewarms) == 0 {
		return nil, 0
	}
	last := prewarms[len(prewarms)-1]
	streak := 0
	for i := len(prewarms) - 1; i >= 0 && prewarms[i].Status == SetupStateFailed; i-- {
		streak++
	}
	return &CachePrewarm{Status: last.Status, Error: last.Error, At: last.CreatedAt}, streak
}

func (b *reportBuilder) toolRows() []CacheToolRow {
	rows := map[string]*CacheToolRow{}
	installs := map[string][]CacheTool{}
	for i := range b.in.latest {
		u := &b.in.latest[i]
		installs[u.WorkspaceID] = u.Tools
		for _, t := range u.Tools {
			key := t.Tool + "@" + t.Version
			r, ok := rows[key]
			if !ok {
				r = &CacheToolRow{Tool: t.Tool, Version: t.Version}
				rows[key] = r
			}
			r.Bytes += t.Bytes
			r.Workspaces = append(r.Workspaces, u.WorkspaceID)
		}
	}
	for i := range b.in.runs {
		markToolsUsed(&b.in.runs[i], installs[b.in.runs[i].WorkspaceID], rows)
	}
	covered := b.in.logSince != nil && !b.in.logSince.After(b.in.now.Add(-SignalToolUnusedAfter))
	out := make([]CacheToolRow, 0, len(rows))
	for _, r := range rows {
		sort.Strings(r.Workspaces)
		r.Unused = covered && (r.LastUsed == nil || b.in.now.Sub(*r.LastUsed) > SignalToolUnusedAfter)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Tool+"@"+out[i].Version < out[j].Tool+"@"+out[j].Version
	})
	return out
}

func markToolsUsed(r *SetupRun, installed []CacheTool, rows map[string]*CacheToolRow) {
	for _, item := range append(append([]string(nil), r.ToolsUsed...), r.ToolsDownloaded...) {
		tool, want, _ := strings.Cut(item, "@")
		for _, t := range installed {
			if t.Tool != tool || !versionMatches(want, t.Version) {
				continue
			}
			row := rows[t.Tool+"@"+t.Version]
			if row != nil && (row.LastUsed == nil || r.CreatedAt.After(*row.LastUsed)) {
				at := r.CreatedAt
				row.LastUsed = &at
			}
		}
	}
}

// A non-numeric alias (latest, lts) deliberately matches every version: better to keep a version
// than to call a used one unused.
func versionMatches(want, have string) bool {
	if want == "" || want == have || strings.HasPrefix(have, want+".") {
		return true
	}
	return want[0] < '0' || want[0] > '9'
}

func buildSignals(rep *CacheReport) []CacheSignal {
	out := []CacheSignal{}
	for i := range rep.Workspaces {
		w := &rep.Workspaces[i]
		if w.GrowthPct != nil && *w.GrowthPct > SignalGrowthPct {
			out = append(out, CacheSignal{Kind: SignalCacheGrowth, WorkspaceID: w.WorkspaceID,
				Value: *w.GrowthPct, Threshold: SignalGrowthPct})
		}
		if w.CapacityPct != nil && *w.CapacityPct > SignalCapacityPct {
			out = append(out, CacheSignal{Kind: SignalCacheFull, WorkspaceID: w.WorkspaceID,
				Value: *w.CapacityPct, Threshold: SignalCapacityPct})
		}
	}
	for i := range rep.Projects {
		out = append(out, projectSignals(&rep.Projects[i])...)
	}
	for _, t := range rep.Tools {
		if !t.Unused {
			continue
		}
		days := -1.0
		if t.LastUsed != nil {
			days = float64(int(rep.GeneratedAt.Sub(*t.LastUsed) / (24 * time.Hour)))
		}
		out = append(out, CacheSignal{Kind: SignalToolUnused, Tool: t.Tool + "@" + t.Version, Value: days,
			Threshold: float64(SignalToolUnusedAfter / (24 * time.Hour))})
	}
	return out
}

func projectSignals(p *CacheProject) []CacheSignal {
	var out []CacheSignal
	sig := func(kind string, value, threshold float64) {
		out = append(out, CacheSignal{Kind: kind, WorkspaceID: p.WorkspaceID, ProjectID: p.ProjectID,
			Value: value, Threshold: threshold})
	}
	if p.WithSetup >= SignalLowHitMinRuns && p.HitRate != nil && *p.HitRate < SignalLowHitRate {
		sig(SignalLowHit, *p.HitRate*100, SignalLowHitRate*100)
	}
	if p.PrewarmFailStreak >= SignalPrewarmFailures {
		sig(SignalPrewarmFailing, float64(p.PrewarmFailStreak), SignalPrewarmFailures)
	}
	if p.RecentKeys > SignalKeyChurnKeys {
		sig(SignalKeyChurn, float64(p.RecentKeys), SignalKeyChurnKeys)
	}
	return out
}
