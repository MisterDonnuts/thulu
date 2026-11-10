package sandbox

// Measurements ride on an existing sandbox through the runtime's exec, never Manager.Exec (which
// could start one), and must never fail or slow the run.

import (
	"bufio"
	"context"
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	CacheSourcePrewarm = "prewarm"
	CacheSourceTask    = "task"
)

const (
	CacheAreaNpm       = "npm"
	CacheAreaGoMod     = "go_mod"
	CacheAreaGoBuild   = "go_build"
	CacheAreaPip       = "pip"
	CacheAreaMise      = "mise"
	CacheAreaSnapshots = "snapshots"
	CacheAreaOther     = "other"
)

var cacheAreaDirs = map[string]string{
	"npm":       CacheAreaNpm,
	"go/mod":    CacheAreaGoMod,
	"go/build":  CacheAreaGoBuild,
	"pip":       CacheAreaPip,
	"mise":      CacheAreaMise,
	"snapshots": CacheAreaSnapshots,
}

const (
	cacheMeasureEvery     = time.Hour
	cacheMeasureTimeoutMs = 120_000
	cacheMeasureDuSeconds = 50
)

type CacheTool struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Bytes   int64  `json:"bytes"`
}

type CacheSnapshotDir struct {
	ProjectID string `json:"project_id"`
	Count     int    `json:"count"`
	Bytes     int64  `json:"bytes"`
	Newest    string `json:"newest,omitempty"`
}

type CacheUsage struct {
	ID            string
	WorkspaceID   string
	Source        string
	TotalBytes    int64
	CapacityBytes int64
	UsedBytes     int64
	Areas         map[string]int64
	Tools         []CacheTool
	Snapshots     []CacheSnapshotDir
	MeasuredAt    time.Time
}

var cacheUsageMigration = migrate.Migration{ID: 4, SQL: `
	CREATE TABLE IF NOT EXISTS sandbox_cache_usage (
		id             TEXT      PRIMARY KEY,
		workspace_id   TEXT      NOT NULL,
		source         TEXT      NOT NULL,
		total_bytes    BIGINT    NOT NULL DEFAULT 0,
		capacity_bytes BIGINT    NOT NULL DEFAULT 0,
		used_bytes     BIGINT    NOT NULL DEFAULT 0,
		areas          TEXT      NOT NULL DEFAULT '{}',
		tools          TEXT      NOT NULL DEFAULT '[]',
		snapshots      TEXT      NOT NULL DEFAULT '[]',
		measured_at    TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_cache_usage_ws ON sandbox_cache_usage(workspace_id, measured_at);
	CREATE INDEX IF NOT EXISTS idx_cache_usage_measured ON sandbox_cache_usage(measured_at);`}

func (s *Store) RecordCacheUsage(ctx context.Context, u CacheUsage) error {
	if u.ID == "" {
		u.ID = newRowID()
	}
	if u.MeasuredAt.IsZero() {
		u.MeasuredAt = time.Now()
	}
	areas, err := json.Marshal(nonNilAreas(u.Areas))
	if err != nil {
		return apperr.Wrap(err, "sandbox.errors.cache_usage_save")
	}
	tools, err := json.Marshal(nonNil(u.Tools))
	if err != nil {
		return apperr.Wrap(err, "sandbox.errors.cache_usage_save")
	}
	snaps, err := json.Marshal(nonNil(u.Snapshots))
	if err != nil {
		return apperr.Wrap(err, "sandbox.errors.cache_usage_save")
	}
	_, err = s.db.ExecContext(ctx, s.rb(`INSERT INTO sandbox_cache_usage
		(id, workspace_id, source, total_bytes, capacity_bytes, used_bytes, areas, tools, snapshots, measured_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`),
		u.ID, u.WorkspaceID, u.Source, u.TotalBytes, u.CapacityBytes, u.UsedBytes,
		string(areas), string(tools), string(snaps), u.MeasuredAt.UTC())
	if err != nil {
		return apperr.Wrap(err, "sandbox.errors.cache_usage_save")
	}
	return nil
}

func nonNilAreas(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

const cacheUsageColumns = `id, workspace_id, source, total_bytes, capacity_bytes, used_bytes, areas, tools, snapshots, measured_at`

func scanCacheUsage(r rowScanner) (CacheUsage, error) {
	var u CacheUsage
	var areas, tools, snaps string
	if err := r.Scan(&u.ID, &u.WorkspaceID, &u.Source, &u.TotalBytes, &u.CapacityBytes, &u.UsedBytes,
		&areas, &tools, &snaps, &u.MeasuredAt); err != nil {
		return u, apperr.Wrap(err, "sandbox.errors.cache_usage_load")
	}
	u.MeasuredAt = u.MeasuredAt.UTC()
	// A damaged row reads as empty rather than hiding the workspace.
	for _, col := range []struct {
		raw string
		dst any
	}{{areas, &u.Areas}, {tools, &u.Tools}, {snaps, &u.Snapshots}} {
		if err := json.Unmarshal([]byte(col.raw), col.dst); err != nil {
			logx.Warn(err, "sandbox.log.cache_usage_decode", "workspace", u.WorkspaceID)
		}
	}
	return u, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) cacheUsagePerWorkspace(ctx context.Context, since time.Time, newest bool) ([]CacheUsage, error) {
	order := "ASC"
	if newest {
		order = "DESC"
	}
	//i18n:ignore SQL
	q := `SELECT DISTINCT ON (workspace_id) ` + cacheUsageColumns + `
		  FROM sandbox_cache_usage WHERE measured_at >= ?
		 ORDER BY workspace_id, measured_at ` + order + `, id`
	rows, err := s.db.QueryContext(ctx, s.rb(q), since.UTC())
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.cache_usage_load")
	}
	defer rows.Close()
	var out []CacheUsage
	for rows.Next() {
		u, err := scanCacheUsage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.cache_usage_load")
	}
	return out, nil
}

//i18n:ignore shell command run in the sandbox
var cacheMeasureCmd = `c="${OCTOPUS_CACHE_DIR:-}"
if [ -z "$c" ] || [ ! -d "$c" ]; then echo cache=none; exit 0; fi
T=""; command -v timeout >/dev/null 2>&1 && T="timeout ` + strconv.Itoa(cacheMeasureDuSeconds) + `"
echo "cache=$c"
df -Pk "$c" 2>/dev/null | awk 'NR==2 {print "df=" $2 " " $3}'
$T du -k -d 2 "$c" 2>/dev/null | sed 's/^/du=/'
m="${MISE_DATA_DIR:-$c/mise}/installs"
if [ -d "$m" ]; then $T du -k -d 2 "$m" 2>/dev/null | sed 's/^/mise=/'; echo "mise_root=$m"; fi
for p in "$c"/snapshots/*/; do
  [ -d "$p" ] || continue
  p=${p%/}
  n=$(ls -1 "$p"/*.tar.zst 2>/dev/null | wc -l | tr -d ' ')
  newest=$(ls -1t "$p"/*.tar.zst 2>/dev/null | head -n 1)
  [ -n "$newest" ] && newest=$(basename "$newest" .tar.zst)
  echo "snap=${p##*/} $n $newest"
done
true`

type cacheMeasureLines struct {
	root, miseRoot string
	du, mise       map[string]int64
	df             [2]int64
	snaps          []CacheSnapshotDir
}

func parseCacheMeasure(out string) (CacheUsage, bool) {
	l := cacheMeasureLines{du: map[string]int64{}, mise: map[string]int64{}}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		l.read(sc.Text())
	}
	total, ok := l.du[l.root]
	if l.root == "" || !ok {
		return CacheUsage{}, false
	}
	u := CacheUsage{
		TotalBytes:    total << 10,
		CapacityBytes: l.df[0] << 10,
		UsedBytes:     l.df[1] << 10,
		Areas:         l.areas(total),
		Tools:         l.tools(),
		Snapshots:     l.snapshots(),
	}
	return u, true
}

func (l *cacheMeasureLines) read(line string) {
	k, v, ok := strings.Cut(line, "=")
	if !ok {
		return
	}
	switch k {
	case "cache":
		if v != "none" {
			l.root = path.Clean(v)
		}
	case "mise_root":
		l.miseRoot = path.Clean(v)
	case "df":
		l.readDf(v)
	case "du", "mise":
		kb, p, ok := strings.Cut(v, "\t")
		n, err := strconv.ParseInt(strings.TrimSpace(kb), 10, 64)
		if !ok || err != nil {
			return
		}
		if k == "du" {
			l.du[path.Clean(p)] = n
		} else {
			l.mise[path.Clean(p)] = n
		}
	case "snap":
		l.readSnap(v)
	}
}

func (l *cacheMeasureLines) readDf(v string) {
	f := strings.Fields(v)
	if len(f) != 2 {
		return
	}
	size, sizeErr := strconv.ParseInt(f[0], 10, 64)
	used, usedErr := strconv.ParseInt(f[1], 10, 64)
	if sizeErr == nil && usedErr == nil {
		l.df = [2]int64{size, used}
	}
}

func (l *cacheMeasureLines) readSnap(v string) {
	f := strings.Fields(v)
	if len(f) < 2 {
		return
	}
	count, err := strconv.Atoi(f[1])
	if err != nil {
		return
	}
	s := CacheSnapshotDir{ProjectID: f[0], Count: count}
	if len(f) > 2 {
		s.Newest = f[2]
	}
	l.snaps = append(l.snaps, s)
}

func (l *cacheMeasureLines) areas(totalKB int64) map[string]int64 {
	out := map[string]int64{}
	var known int64
	for dir, area := range cacheAreaDirs {
		if kb, ok := l.du[path.Join(l.root, dir)]; ok {
			out[area] = kb << 10
			known += kb
		}
	}
	if other := totalKB - known; other > 0 {
		out[CacheAreaOther] = other << 10
	}
	return out
}

func (l *cacheMeasureLines) tools() []CacheTool {
	var out []CacheTool
	for p, kb := range l.mise {
		rel, ok := strings.CutPrefix(p, l.miseRoot+"/")
		if !ok || l.miseRoot == "" {
			continue
		}
		tool, version, ok := strings.Cut(rel, "/")
		if !ok || strings.Contains(version, "/") {
			continue
		}
		out = append(out, CacheTool{Tool: tool, Version: version, Bytes: kb << 10})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func (l *cacheMeasureLines) snapshots() []CacheSnapshotDir {
	out := make([]CacheSnapshotDir, 0, len(l.snaps))
	for _, s := range l.snaps {
		s.Bytes = l.du[path.Join(l.root, "snapshots", s.ProjectID)] << 10
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out
}

type cacheMeter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (c *cacheMeter) due(workspaceID string, now time.Time, every time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]time.Time{}
	}
	if t, ok := c.last[workspaceID]; ok && now.Sub(t) < every {
		return false
	}
	c.last[workspaceID] = now
	return true
}

func (m *Manager) measureCache(ctx context.Context, workspaceID, container, source string) {
	res, err := m.docker.Exec(ctx, container, ExecOpts{Command: cacheMeasureCmd, TimeoutMs: cacheMeasureTimeoutMs})
	if err != nil {
		logx.Warn(err, "sandbox.log.cache_measure_failed", "workspace", workspaceID)
		return
	}
	u, ok := parseCacheMeasure(res.Stdout)
	if !ok {
		return
	}
	if q, ok := m.docker.(cacheQuotaRuntime); ok {
		applyCacheQuota(&u, q.CacheQuotaBytes())
	}
	u.WorkspaceID, u.Source = workspaceID, source
	logx.Warn(m.store.RecordCacheUsage(ctx, u), "sandbox.log.cache_measure_failed", "workspace", workspaceID)
}

type cacheQuotaRuntime interface {
	CacheQuotaBytes() int64
}

// Storage classes that do not enforce sizes (k3s local-path) make df report the whole node disk
// (1.8 TB for a 20 GiB claim),
// so the "disk full" signal never fired; measure against the claim instead.
func applyCacheQuota(u *CacheUsage, quota int64) {
	if quota <= 0 || (u.CapacityBytes > 0 && u.CapacityBytes <= quota) {
		return
	}
	u.CapacityBytes, u.UsedBytes = quota, u.TotalBytes
}

func (m *Manager) measurePrewarmCache(ctx context.Context, workspaceID, owner string) {
	if m.store == nil || workspaceID == "" {
		return
	}
	container := m.currentContainer(ctx, owner)
	if container == "" {
		return
	}
	m.meter.due(workspaceID, time.Now(), 0)
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cacheMeasureTimeoutMs*time.Millisecond+10*time.Second)
	defer cancel()
	m.measureCache(mctx, workspaceID, container, CacheSourcePrewarm)
}

func (m *Manager) measureAfterInit(ctx context.Context, workspaceID, container string) {
	if workspaceID == "" || container == "" || !m.meter.due(workspaceID, time.Now(), cacheMeasureEvery) {
		return
	}
	// Detached from the triggering probe so the tool call answers now.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cacheMeasureTimeoutMs*time.Millisecond+10*time.Second)
	go func() {
		defer cancel()
		m.measureCache(mctx, workspaceID, container, CacheSourceTask)
	}()
}
