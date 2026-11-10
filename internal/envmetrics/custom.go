package envmetrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type MetricKind string

const (
	KindCounter MetricKind = "counter"
	KindGauge   MetricKind = "gauge"
)

func (k MetricKind) Valid() bool { return k == KindCounter || k == KindGauge }

type MetricStatus string

const (
	MetricDiscovered MetricStatus = "discovered"
	MetricApproved   MetricStatus = "approved"
	MetricRejected   MetricStatus = "rejected"
)

func (s MetricStatus) Valid() bool {
	return s == MetricDiscovered || s == MetricApproved || s == MetricRejected
}

const MaxMetricsPerEnvironment = 50

const DefaultLabelLimit = 20

const MaxLabelsPerMetric = 6

const OtherLabelValue = "other"

type Metric struct {
	ID            string       `json:"id"`
	EnvironmentID string       `json:"environment_id"`
	Service       string       `json:"service"`
	Name          string       `json:"name"`
	Kind          MetricKind   `json:"kind"`
	Unit          string       `json:"unit,omitempty"`
	DisplayName   string       `json:"display_name,omitempty"`
	Description   string       `json:"description,omitempty"`
	Labels        []string     `json:"labels"`
	LabelLimit    int          `json:"label_limit"`
	Status        MetricStatus `json:"status"`
	ApprovedBy    string       `json:"approved_by,omitempty"`
	FirstSeen     time.Time    `json:"first_seen"`
	LastSeen      time.Time    `json:"last_seen"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

func (m Metric) HasLabel(name string) bool {
	for _, l := range m.Labels {
		if l == name {
			return true
		}
	}
	return false
}

func (m Metric) Title() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Name
}

type CustomPoint struct {
	MetricID string            `json:"metric_id"`
	Kind     MetricKind        `json:"-"`
	Labels   map[string]string `json:"labels,omitempty"`
	Bucket   time.Time         `json:"bucket"`
	Value    float64           `json:"value"`
}

type Observation struct {
	Service string
	Name    string
	Kind    MetricKind
	Unit    string
	Labels  []string
}

var ErrMetricNotFound error = apperr.NotFound("envmetrics.errors.metric_not_found")

func customMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 9, SQL: `CREATE TABLE IF NOT EXISTS environment_custom_metrics (
			id             TEXT PRIMARY KEY,
			environment_id TEXT NOT NULL,
			service        TEXT NOT NULL DEFAULT '',
			name           TEXT NOT NULL,
			kind           TEXT NOT NULL DEFAULT 'gauge',
			unit           TEXT NOT NULL DEFAULT '',
			display_name   TEXT NOT NULL DEFAULT '',
			description    TEXT NOT NULL DEFAULT '',
			labels         TEXT NOT NULL DEFAULT '[]',
			label_limit    INTEGER NOT NULL DEFAULT 20,
			status         TEXT NOT NULL DEFAULT 'discovered',
			approved_by    TEXT NOT NULL DEFAULT '',
			first_seen     TIMESTAMP NOT NULL,
			last_seen      TIMESTAMP NOT NULL,
			created_at     TIMESTAMP NOT NULL,
			updated_at     TIMESTAMP NOT NULL
		)`},
		{ID: 10, SQL: `CREATE UNIQUE INDEX IF NOT EXISTS uq_env_custom_metrics
			ON environment_custom_metrics(environment_id, service, name)`},
		{ID: 11, SQL: `CREATE INDEX IF NOT EXISTS idx_env_custom_metrics_status
			ON environment_custom_metrics(environment_id, status)`},

		{ID: 12, SQL: `CREATE TABLE IF NOT EXISTS environment_custom_metric_points (
			metric_id TEXT NOT NULL,
			labels    TEXT NOT NULL DEFAULT '{}',
			bucket    TIMESTAMP NOT NULL,
			value     DOUBLE PRECISION NOT NULL DEFAULT 0,
			PRIMARY KEY (metric_id, labels, bucket)
		)`},
		{ID: 13, SQL: `CREATE INDEX IF NOT EXISTS idx_env_custom_points_bucket
			ON environment_custom_metric_points(bucket)`},
	}
}

const customMetricCols = `id, environment_id, service, name, kind, unit, display_name,
	description, labels, label_limit, status, approved_by, first_seen, last_seen,
	created_at, updated_at`

// Sorted, so one label set always yields one row key. The ingest cardinality budget compares
// against these keys and must use this function.
func CanonicalLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonString(k))
		b.WriteByte(':')
		b.WriteString(jsonString(labels[k]))
	}
	b.WriteByte('}')
	return b.String()
}

func jsonString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

func parseLabels(raw string) map[string]string {
	if raw == "" || raw == "{}" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) RegisterObservations(ctx context.Context, envID string, obs []Observation) (map[string]Metric, int, error) {
	if len(obs) == 0 {
		return map[string]Metric{}, 0, nil
	}
	existing, err := s.ListCustomMetrics(ctx, envID)
	if err != nil {
		return nil, 0, err
	}
	known := NewMetricMap(existing)
	count := len(existing)

	now := time.Now().UTC()
	over := 0
	out := make(map[string]Metric, len(obs))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, o := range obs {
		name := strings.TrimSpace(o.Name)
		if name == "" || !o.Kind.Valid() {
			continue
		}
		labels := normalizeLabels(o.Labels)
		cur, found := known.Find(o.Service, name)
		if !found {
			if count >= MaxMetricsPerEnvironment {
				over++
				continue
			}
			row, insErr := s.insertMetric(ctx, tx, envID, o, name, labels, now)
			if insErr != nil {
				return nil, 0, insErr
			}
			count++
			known[metricKey(row.Service, row.Name)] = *row
			out[metricKey(row.Service, row.Name)] = *row
			continue
		}
		updated, upErr := s.touchMetric(ctx, tx, cur, o, labels, now)
		if upErr != nil {
			return nil, 0, upErr
		}
		known[metricKey(updated.Service, updated.Name)] = *updated
		out[metricKey(updated.Service, updated.Name)] = *updated
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return out, over, nil
}

func (s *Store) insertMetric(
	ctx context.Context, tx *sql.Tx, envID string, o Observation,
	name string, labels []string, now time.Time,
) (*Metric, error) {
	row := Metric{
		ID: generateID(), EnvironmentID: envID, Service: o.Service, Name: name,
		Kind: o.Kind, Unit: o.Unit, Labels: labels, LabelLimit: DefaultLabelLimit,
		Status: MetricDiscovered, FirstSeen: now, LastSeen: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO environment_custom_metrics (`+customMetricCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		row.ID, row.EnvironmentID, row.Service, row.Name, row.Kind, row.Unit,
		row.DisplayName, row.Description, encodeLabelNames(row.Labels), row.LabelLimit, row.Status,
		row.ApprovedBy, row.FirstSeen, row.LastSeen, row.CreatedAt, row.UpdatedAt); err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) touchMetric(ctx context.Context, tx *sql.Tx, cur Metric, o Observation, labels []string, now time.Time) (*Metric, error) {
	merged, changed := mergeLabels(cur.Labels, labels)
	cur.Labels = merged
	cur.LastSeen = now
	if cur.Unit == "" && o.Unit != "" {
		cur.Unit = o.Unit
		changed = true
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE environment_custom_metrics
		    SET labels = ?, unit = ?, last_seen = ?, updated_at = ?
		  WHERE id = ?`),
		encodeLabelNames(cur.Labels), cur.Unit, now, now, cur.ID); err != nil {
		return nil, err
	}
	if changed {
		cur.UpdatedAt = now
	}
	return &cur, nil
}

func encodeLabelNames(names []string) string {
	raw, err := json.Marshal(names)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func normalizeLabels(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
		if len(out) >= MaxLabelsPerMetric {
			break
		}
	}
	sort.Strings(out)
	return out
}

func mergeLabels(cur, incoming []string) ([]string, bool) {
	have := make(map[string]bool, len(cur))
	for _, l := range cur {
		have[l] = true
	}
	changed := false
	out := append(make([]string, 0, len(cur)), cur...)
	for _, l := range incoming {
		if have[l] || len(out) >= MaxLabelsPerMetric {
			continue
		}
		out = append(out, l)
		have[l] = true
		changed = true
	}
	sort.Strings(out)
	return out, changed
}

func (s *Store) ListCustomMetrics(ctx context.Context, envID string) ([]Metric, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+customMetricCols+` FROM environment_custom_metrics
		  WHERE environment_id = ?
		  ORDER BY CASE status WHEN 'discovered' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END,
		           service, name`), envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Metric{}
	for rows.Next() {
		m, scanErr := scanCustomMetric(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *Store) ApprovedMetrics(ctx context.Context, envID string) ([]Metric, error) {
	all, err := s.ListCustomMetrics(ctx, envID)
	if err != nil {
		return nil, err
	}
	out := make([]Metric, 0, len(all))
	for i := range all {
		if all[i].Status == MetricApproved {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func (s *Store) GetCustomMetric(ctx context.Context, envID, id string) (*Metric, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+customMetricCols+` FROM environment_custom_metrics
		  WHERE id = ? AND environment_id = ?`), id, envID)
	m, err := scanCustomMetric(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMetricNotFound
	}
	return m, err
}

type MetricPatch struct {
	Status      *MetricStatus `json:"status,omitempty"`
	DisplayName *string       `json:"display_name,omitempty"`
	Description *string       `json:"description,omitempty"`
	LabelLimit  *int          `json:"label_limit,omitempty"`
}

func (s *Store) UpdateCustomMetric(ctx context.Context, envID, id string, p MetricPatch, by string) (*Metric, error) {
	cur, err := s.GetCustomMetric(ctx, envID, id)
	if err != nil {
		return nil, err
	}
	if p.Status != nil {
		if !p.Status.Valid() {
			return nil, apperr.Invalid("envmetrics.errors.unknown_status", "status", *p.Status)
		}
		if *p.Status == MetricApproved && cur.Status != MetricApproved {
			cur.ApprovedBy = by
		}
		if *p.Status == MetricDiscovered {
			cur.ApprovedBy = ""
		}
		cur.Status = *p.Status
	}
	if p.DisplayName != nil {
		cur.DisplayName = strings.TrimSpace(*p.DisplayName)
	}
	if p.Description != nil {
		cur.Description = strings.TrimSpace(*p.Description)
	}
	if p.LabelLimit != nil {
		if *p.LabelLimit < 1 || *p.LabelLimit > 200 {
			return nil, apperr.Invalid("envmetrics.errors.label_limit_range")
		}
		cur.LabelLimit = *p.LabelLimit
	}
	cur.UpdatedAt = time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE environment_custom_metrics
		    SET status = ?, display_name = ?, description = ?, label_limit = ?,
		        approved_by = ?, updated_at = ?
		  WHERE id = ? AND environment_id = ?`),
		cur.Status, cur.DisplayName, cur.Description, cur.LabelLimit,
		cur.ApprovedBy, cur.UpdatedAt, cur.ID, envID); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Store) DeleteCustomMetric(ctx context.Context, envID, id string) error {
	if _, err := s.GetCustomMetric(ctx, envID, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, s.rb(
		`DELETE FROM environment_custom_metric_points WHERE metric_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`DELETE FROM environment_custom_metrics WHERE id = ? AND environment_id = ?`),
		id, envID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LabelKeysFor(ctx context.Context, metricID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT DISTINCT labels FROM environment_custom_metric_points WHERE metric_id = ?`),
		metricID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func (s *Store) WriteCustomPoints(ctx context.Context, pts []CustomPoint) error {
	if len(pts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range pts {
		conflict := "DO UPDATE SET value = excluded.value"
		if p.Kind == KindCounter {
			conflict = "DO UPDATE SET value = environment_custom_metric_points.value + excluded.value"
		}
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO environment_custom_metric_points (metric_id, labels, bucket, value)
			 VALUES (?,?,?,?)
			 ON CONFLICT (metric_id, labels, bucket) `+conflict),
			p.MetricID, CanonicalLabels(p.Labels), TruncateBucket(p.Bucket), p.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CustomSeries(ctx context.Context, metricID string, from, to time.Time) ([]CustomPoint, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT labels, bucket, value FROM environment_custom_metric_points
		  WHERE metric_id = ? AND bucket >= ? AND bucket <= ?
		  ORDER BY bucket`), metricID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []CustomPoint{}
	for rows.Next() {
		var (
			raw string
			p   = CustomPoint{MetricID: metricID}
		)
		if err := rows.Scan(&raw, &p.Bucket, &p.Value); err != nil {
			return nil, err
		}
		p.Labels = parseLabels(raw)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PurgeCustomOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_custom_metric_points WHERE bucket < ?`), cutoff.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) deleteCustomForEnvironment(ctx context.Context, envID string) error {
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_custom_metric_points
		  WHERE metric_id IN (SELECT id FROM environment_custom_metrics WHERE environment_id = ?)`),
		envID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM environment_custom_metrics WHERE environment_id = ?`), envID)
	return err
}

type customMetricScanner interface{ Scan(dest ...any) error }

func scanCustomMetric(r customMetricScanner) (*Metric, error) {
	var (
		m   Metric
		lbl string
	)
	if err := r.Scan(&m.ID, &m.EnvironmentID, &m.Service, &m.Name, &m.Kind, &m.Unit,
		&m.DisplayName, &m.Description, &lbl, &m.LabelLimit, &m.Status, &m.ApprovedBy,
		&m.FirstSeen, &m.LastSeen, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Labels = []string{}
	if lbl != "" && lbl != "[]" {
		// Older builds stored labels as "null"; the API promises a list.
		if err := json.Unmarshal([]byte(lbl), &m.Labels); err != nil || m.Labels == nil {
			m.Labels = []string{}
		}
	}
	return &m, nil
}
