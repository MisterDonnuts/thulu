package envmetrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type DashboardStatus string

const (
	DashProposed DashboardStatus = "proposed"
	DashActive   DashboardStatus = "active"
	DashArchived DashboardStatus = "archived"
)

type Dashboard struct {
	ID            string          `json:"id"`
	EnvironmentID string          `json:"environment_id"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Status        DashboardStatus `json:"status"`
	Panels        []Panel         `json:"panels"`
	TaskID        string          `json:"task_id,omitempty"`
	FlagID        string          `json:"flag_id,omitempty"`
	CreatedBy     string          `json:"created_by"`
	ApprovedBy    string          `json:"approved_by,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

var ErrDashboardNotFound error = apperr.NotFound("envmetrics.errors.dashboard_not_found")

const MaxPanelsPerDashboard = 12

const MaxSeriesPerPanel = 12

func dashboardMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 14, SQL: `CREATE TABLE IF NOT EXISTS environment_dashboards (
			id             TEXT PRIMARY KEY,
			environment_id TEXT NOT NULL,
			name           TEXT NOT NULL,
			description    TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'proposed',
			panels         TEXT NOT NULL DEFAULT '[]',
			task_id        TEXT NOT NULL DEFAULT '',
			flag_id        TEXT NOT NULL DEFAULT '',
			created_by     TEXT NOT NULL DEFAULT '',
			approved_by    TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP NOT NULL,
			updated_at     TIMESTAMP NOT NULL
		)`},
		{ID: 15, SQL: `CREATE INDEX IF NOT EXISTS idx_env_dashboards_env
			ON environment_dashboards(environment_id, status)`},
	}
}

const dashboardCols = `id, environment_id, name, description, status, panels,
	task_id, flag_id, created_by, approved_by, created_at, updated_at`

type DashboardInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Panels      []Panel `json:"panels"`
}

func ValidateDashboard(in DashboardInput, reg MetricLookup) error {
	if strings.TrimSpace(in.Name) == "" {
		return panelErr("envmetrics.errors.dashboard_name_required")
	}
	if len(in.Panels) == 0 {
		return panelErr("envmetrics.errors.dashboard_no_panels")
	}
	if len(in.Panels) > MaxPanelsPerDashboard {
		return panelErr("envmetrics.errors.dashboard_too_many_panels",
			"count", len(in.Panels), "max", MaxPanelsPerDashboard)
	}
	for _, p := range in.Panels {
		if err := ValidatePanel(p, reg); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ProposeDashboard(ctx context.Context, envID string, in DashboardInput, taskID, flagID, createdBy string) (*Dashboard, error) {
	metrics, err := s.ListCustomMetrics(ctx, envID)
	if err != nil {
		return nil, err
	}
	if invalid := ValidateDashboard(in, NewMetricMap(metrics)); invalid != nil {
		return nil, invalid
	}
	now := time.Now().UTC()
	d := &Dashboard{
		ID: generateID(), EnvironmentID: envID, Name: strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description), Status: DashProposed,
		Panels: fillPanelIDs(in.Panels), TaskID: taskID, FlagID: flagID,
		CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}
	raw, err := json.Marshal(d.Panels)
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.marshal_panels")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO environment_dashboards (`+dashboardCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
		d.ID, d.EnvironmentID, d.Name, d.Description, d.Status, string(raw),
		d.TaskID, d.FlagID, d.CreatedBy, d.ApprovedBy, d.CreatedAt, d.UpdatedAt); err != nil {
		return nil, err
	}
	return d, nil
}

func fillPanelIDs(panels []Panel) []Panel {
	out := make([]Panel, 0, len(panels))
	for _, p := range panels {
		if strings.TrimSpace(p.ID) == "" {
			p.ID = generateID()
		}
		if p.RangeHours <= 0 {
			p.RangeHours = DefaultRangeHours
		}
		if p.Width <= 0 {
			p.Width = 1
		}
		out = append(out, p)
	}
	return out
}

func (s *Store) ListDashboards(ctx context.Context, envID string) ([]Dashboard, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+dashboardCols+` FROM environment_dashboards
		  WHERE environment_id = ?
		  ORDER BY CASE status WHEN 'proposed' THEN 0 WHEN 'active' THEN 1 ELSE 2 END,
		           name, created_at DESC`), envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Dashboard{}
	for rows.Next() {
		d, scanErr := scanDashboard(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) GetDashboard(ctx context.Context, envID, id string) (*Dashboard, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+dashboardCols+` FROM environment_dashboards
		  WHERE id = ? AND environment_id = ?`), id, envID)
	d, err := scanDashboard(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDashboardNotFound
	}
	return d, err
}

func (s *Store) ApproveDashboard(ctx context.Context, envID, id, by string) (*Dashboard, error) {
	d, err := s.GetDashboard(ctx, envID, id)
	if err != nil {
		return nil, err
	}
	if d.Status == DashActive {
		return d, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE environment_dashboards SET status = ?, updated_at = ?
		  WHERE environment_id = ? AND name = ? AND status = ? AND id != ?`),
		DashArchived, now, envID, d.Name, DashActive, d.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE environment_dashboards SET status = ?, approved_by = ?, updated_at = ?
		  WHERE id = ? AND environment_id = ?`),
		DashActive, by, now, d.ID, envID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	d.Status = DashActive
	d.ApprovedBy = by
	d.UpdatedAt = now
	return d, nil
}

// Keeps the current status. The store does no auth check; the API restricts this to admins.
func (s *Store) UpdateDashboard(ctx context.Context, envID, id string, in DashboardInput) (*Dashboard, error) {
	d, err := s.GetDashboard(ctx, envID, id)
	if err != nil {
		return nil, err
	}
	metrics, err := s.ListCustomMetrics(ctx, envID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = d.Name
	}
	if invalid := ValidateDashboard(in, NewMetricMap(metrics)); invalid != nil {
		return nil, invalid
	}
	d.Name = strings.TrimSpace(in.Name)
	d.Description = strings.TrimSpace(in.Description)
	d.Panels = fillPanelIDs(in.Panels)
	d.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(d.Panels)
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.marshal_panels")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE environment_dashboards SET name = ?, description = ?, panels = ?, updated_at = ?
		  WHERE id = ? AND environment_id = ?`),
		d.Name, d.Description, string(raw), d.UpdatedAt, d.ID, envID); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Store) DeleteDashboard(ctx context.Context, envID, id string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_dashboards WHERE id = ? AND environment_id = ?`), id, envID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrDashboardNotFound
	}
	return nil
}

func (s *Store) PendingDashboardFlagIDs(ctx context.Context, envID, dashID string) ([]string, error) {
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT DISTINCT flag_id FROM environment_dashboards
		  WHERE environment_id = ? AND id = ? AND status = ? AND flag_id != ''`,
		envID, dashID, DashProposed)
}

func deleteDashboardsForEnvironment(ctx context.Context, s *Store, envID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_dashboards WHERE environment_id = ?`), envID)
	return err
}

func scanDashboard(r customMetricScanner) (*Dashboard, error) {
	var (
		d   Dashboard
		raw string
	)
	if err := r.Scan(&d.ID, &d.EnvironmentID, &d.Name, &d.Description, &d.Status, &raw,
		&d.TaskID, &d.FlagID, &d.CreatedBy, &d.ApprovedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Panels = []Panel{}
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &d.Panels); err != nil {
			d.Panels = []Panel{}
		}
	}
	return &d, nil
}

type SeriesData struct {
	Label    string     `json:"label"`
	Points   []*float64 `json:"points,omitempty"`
	Value    float64    `json:"value"`
	Previous *float64   `json:"previous,omitempty"`
}

type PanelData struct {
	PanelID  string       `json:"panel_id"`
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Unit     string       `json:"unit,omitempty"`
	Width    int          `json:"width,omitempty"`
	From     time.Time    `json:"from"`
	To       time.Time    `json:"to"`
	TimeAxis bool         `json:"time_axis"`
	Buckets  []time.Time  `json:"buckets,omitempty"`
	Series   []SeriesData `json:"series"`
	Note     string       `json:"note,omitempty"`
}

func (s *Store) ResolvePanel(ctx context.Context, p Panel, reg MetricLookup, now time.Time) (*PanelData, error) {
	spec, ok := ChartTypeByName(p.Type)
	if !ok {
		return nil, panelErr("envmetrics.errors.unknown_chart_type", "type", p.Type)
	}
	rangeHours := p.RangeHours
	if rangeHours <= 0 {
		rangeHours = DefaultRangeHours
	}
	to := TruncateBucket(now).Add(BucketWidth)
	from := to.Add(-time.Duration(rangeHours) * time.Hour)

	out := &PanelData{
		PanelID: p.ID, Type: p.Type, Title: p.Title, Unit: p.Unit, Width: p.Width,
		From: from, To: to, TimeAxis: spec.TimeAxis, Series: []SeriesData{},
	}
	if spec.TimeAxis {
		out.Buckets = bucketsBetween(from, to)
	}

	groups := map[string]*group{}
	order := []string{}
	stale := []string{}

	for i := range p.Series {
		ps := p.Series[i]
		m, found := reg.Find(ps.Service, ps.Metric)
		if !found || m.Status != MetricApproved {
			stale = append(stale, ps.Metric)
			continue
		}
		if out.Unit == "" {
			out.Unit = m.Unit
		}
		pts, err := s.CustomSeries(ctx, m.ID, from, to)
		if err != nil {
			return nil, err
		}
		order = accumulate(groups, order, p, ps, m, pts)
	}

	out.Series = collapse(groups, order, out.Buckets, spec.TimeAxis)

	if len(out.Series) > MaxSeriesPerPanel {
		folded := len(out.Series) - MaxSeriesPerPanel
		out.Series = foldTail(out.Series, MaxSeriesPerPanel, foldAgg(groups, order))
		out.Note = i18n.TCtx(ctx, "envmetrics.notes.groups_folded", "count", folded, "other", OtherLabelValue)
	}
	if p.Type == "stat" && len(out.Series) > 0 {
		prevFrom := from.Add(-time.Duration(rangeHours) * time.Hour)
		prev, err := s.aggregateWindow(ctx, p, reg, prevFrom, from)
		if err != nil {
			return nil, err
		}
		out.Series[0].Previous = prev
	}
	if len(stale) > 0 {
		note := i18n.TCtx(ctx, "envmetrics.notes.no_approved_metric", "metrics", strings.Join(stale, ", "))
		if out.Note != "" {
			note = out.Note + "; " + note
		}
		out.Note = note
	}
	return out, nil
}

type group struct {
	agg      Agg
	kind     MetricKind
	byBucket map[time.Time][]float64
}

func accumulate(groups map[string]*group, order []string, p Panel, ps PanelSeries, m Metric, pts []CustomPoint) []string {
	agg := ps.Agg
	if agg == "" {
		agg = defaultAggFor(m.Kind)
	}
	for i := range pts {
		pt := pts[i]
		if !matchesWhere(pt.Labels, ps.Where) {
			continue
		}
		label := seriesLabel(p, ps, m, pt.Labels)
		g, seen := groups[label]
		if !seen {
			g = &group{agg: agg, kind: m.Kind, byBucket: map[time.Time][]float64{}}
			groups[label] = g
			order = append(order, label)
		}
		b := pt.Bucket.UTC()
		g.byBucket[b] = append(g.byBucket[b], pt.Value)
	}
	return order
}

func defaultAggFor(k MetricKind) Agg {
	if k == KindCounter {
		return AggSum
	}
	return AggLast
}

func foldAgg(groups map[string]*group, order []string) Agg {
	for _, label := range order {
		if g := groups[label]; g != nil {
			return g.agg
		}
	}
	return AggSum
}

func (s *Store) aggregateWindow(ctx context.Context, p Panel, reg MetricLookup, from, to time.Time) (*float64, error) {
	if len(p.Series) == 0 {
		return nil, nil
	}
	ps := p.Series[0]
	m, found := reg.Find(ps.Service, ps.Metric)
	if !found {
		return nil, nil
	}
	agg := ps.Agg
	if agg == "" {
		agg = defaultAggFor(m.Kind)
	}
	pts, err := s.CustomSeries(ctx, m.ID, from, to)
	if err != nil {
		return nil, err
	}
	vals := make([]float64, 0, len(pts))
	for _, pt := range pts {
		if matchesWhere(pt.Labels, ps.Where) {
			vals = append(vals, pt.Value)
		}
	}
	if len(vals) == 0 {
		return nil, nil
	}
	v := applyAgg(vals, agg)
	return &v, nil
}

func collapse(groups map[string]*group, order []string, buckets []time.Time, timeAxis bool) []SeriesData {
	out := make([]SeriesData, 0, len(groups))
	for _, label := range order {
		g := groups[label]
		sd := SeriesData{Label: label}
		all := make([]float64, 0, len(g.byBucket))
		if timeAxis {
			sd.Points, all = g.timeline(buckets)
		} else {
			// Chronological order matters for AggLast; map iteration order is random.
			for _, b := range sortedBuckets(g.byBucket) {
				all = append(all, applyAgg(g.byBucket[b], g.agg))
			}
		}
		sd.Value = applyAgg(all, g.agg)
		out = append(out, sd)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

// Counters zero-fill only between their first and last observation: the edges were never measured
// and the newest bucket is still open.
func (g *group) timeline(buckets []time.Time) ([]*float64, []float64) {
	points := make([]*float64, len(buckets))
	all := make([]float64, 0, len(g.byBucket))
	first, last := -1, -1
	for i, b := range buckets {
		vals, ok := g.byBucket[b]
		if !ok || len(vals) == 0 {
			continue
		}
		v := applyAgg(vals, g.agg)
		points[i] = &v
		all = append(all, v)
		if first < 0 {
			first = i
		}
		last = i
	}
	if g.kind != KindCounter || first < 0 {
		return points, all
	}
	for i := first; i <= last; i++ {
		if points[i] == nil {
			zero := 0.0
			points[i] = &zero
			all = append(all, 0)
		}
	}
	return points, all
}

func foldTail(series []SeriesData, keep int, agg Agg) []SeriesData {
	head := series[:keep]
	tail := series[keep:]
	other := SeriesData{Label: OtherLabelValue}
	if len(tail[0].Points) > 0 {
		other.Points = make([]*float64, len(tail[0].Points))
		for i := range other.Points {
			vals := []float64{}
			for _, s := range tail {
				if i < len(s.Points) && s.Points[i] != nil {
					vals = append(vals, *s.Points[i])
				}
			}
			if len(vals) > 0 {
				v := applyAgg(vals, agg)
				other.Points[i] = &v
			}
		}
	}
	vals := make([]float64, 0, len(tail))
	for _, s := range tail {
		vals = append(vals, s.Value)
	}
	other.Value = applyAgg(vals, agg)
	return append(append([]SeriesData{}, head...), other)
}

func sortedBuckets(byBucket map[time.Time][]float64) []time.Time {
	out := make([]time.Time, 0, len(byBucket))
	for b := range byBucket {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func applyAgg(vals []float64, agg Agg) float64 {
	if len(vals) == 0 {
		return 0
	}
	switch agg {
	case AggSum:
		total := 0.0
		for _, v := range vals {
			total += v
		}
		return total
	case AggAvg:
		total := 0.0
		for _, v := range vals {
			total += v
		}
		return total / float64(len(vals))
	case AggMax:
		out := math.Inf(-1)
		for _, v := range vals {
			if v > out {
				out = v
			}
		}
		return out
	case AggMin:
		out := math.Inf(1)
		for _, v := range vals {
			if v < out {
				out = v
			}
		}
		return out
	case AggLast:
		return vals[len(vals)-1]
	}
	return vals[len(vals)-1]
}

func matchesWhere(labels, where map[string]string) bool {
	for k, want := range where {
		if labels[k] != want {
			return false
		}
	}
	return true
}

func seriesLabel(p Panel, ps PanelSeries, m Metric, labels map[string]string) string {
	if p.GroupBy != "" {
		if v := labels[p.GroupBy]; v != "" {
			return v
		}
		return "(unset)"
	}
	if ps.Label != "" {
		return ps.Label
	}
	if ps.Service != "" {
		return ps.Service + " · " + m.Title()
	}
	return m.Title()
}

func (s *Store) ResolveDashboard(ctx context.Context, envID string, d *Dashboard, now time.Time) ([]PanelData, error) {
	metrics, err := s.ListCustomMetrics(ctx, envID)
	if err != nil {
		return nil, err
	}
	reg := NewMetricMap(metrics)
	out := make([]PanelData, 0, len(d.Panels))
	for _, p := range d.Panels {
		pd, resolveErr := s.ResolvePanel(ctx, p, reg, now)
		if resolveErr != nil {
			return nil, resolveErr
		}
		out = append(out, *pd)
	}
	return out, nil
}

func bucketsBetween(from, to time.Time) []time.Time {
	out := []time.Time{}
	for b := TruncateBucket(from); b.Before(to); b = b.Add(BucketWidth) {
		out = append(out, b)
	}
	return out
}
