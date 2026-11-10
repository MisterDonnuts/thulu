package infra

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type SettingsReader interface {
	AgentMaxContainers(ctx context.Context) (int, error)
	MentionMaxContainers(ctx context.Context) (int, error)
	SandboxMax(ctx context.Context) (int, error)
	ChatMax(ctx context.Context) (int, error)
}

const (
	limitAgents   = "agent.max_containers"
	limitMentions = "mention.max_containers"
	limitSandbox  = "sandbox.max"
	limitChats    = "chat.max_containers"
)

type dbStats struct {
	postgres   *Postgres
	counts     Counts
	names      map[string]string
	usage      map[string]usageToday
	projects   []projectRow
	workspaces []string
}

type usageToday struct {
	runs   int
	tokens int64
	cost   float64
}

type projectRow struct {
	id, workspaceID, path string
}

func readDB(ctx context.Context, db *sql.DB, since time.Time) (dbStats, []Problem) {
	st := dbStats{names: map[string]string{}, usage: map[string]usageToday{}}
	var problems []Problem
	fail := func(err error, key string) {
		problems = append(problems, Problem{Source: "postgres", Err: apperr.Wrap(err, key)})
	}
	st.postgres = readPostgres(ctx, db, fail)
	st.counts = readCounts(ctx, db)
	st.names, st.workspaces = readWorkspaces(ctx, db)
	if u, err := readUsageToday(ctx, db, since); err != nil {
		fail(err, "infra.errors.usage_today")
	} else {
		st.usage = u
	}
	st.projects = readProjects(ctx, db)
	return st, problems
}

func readPostgres(ctx context.Context, db *sql.DB, fail func(error, string)) *Postgres {
	pg := &Postgres{}
	if err := db.QueryRowContext(ctx, `SELECT pg_database_size(current_database()),
		(SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()),
		current_setting('max_connections')::int, current_setting('server_version')`).
		Scan(&pg.DatabaseSize, &pg.Connections, &pg.MaxConnections, &pg.Version); err != nil {
		fail(err, "infra.errors.pg_size")
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT c.relname, pg_total_relation_size(c.oid), GREATEST(c.reltuples, 0)::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		ORDER BY 2 DESC LIMIT 10`)
	if err != nil {
		fail(err, "infra.errors.pg_tables")
		return pg
	}
	defer closeRows(rows)
	for rows.Next() {
		var t TableSize
		if rows.Scan(&t.Name, &t.Bytes, &t.Rows) == nil {
			pg.LargestTables = append(pg.LargestTables, t)
		}
	}
	if err := rows.Err(); err != nil {
		fail(err, "infra.errors.pg_tables")
	}
	return pg
}

// A missing table means this install lacks that component: count zero, not a problem.
func readCounts(ctx context.Context, db *sql.DB) Counts {
	var c Counts
	for _, q := range []struct {
		dst   *int
		query string
	}{
		{&c.Workspaces, `SELECT count(*) FROM workspaces`},
		{&c.Users, `SELECT count(*) FROM users`},
		{&c.Tasks, `SELECT count(*) FROM tasks`},
		{&c.Projects, `SELECT count(*) FROM projects`},
	} {
		if err := db.QueryRowContext(ctx, q.query).Scan(q.dst); err != nil {
			*q.dst = 0
		}
	}
	return c
}

func readWorkspaces(ctx context.Context, db *sql.DB) (map[string]string, []string) {
	names := map[string]string{}
	var ids []string
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM workspaces ORDER BY lower(name), id`)
	if err != nil {
		return names, nil
	}
	defer closeRows(rows)
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			names[id] = name
			ids = append(ids, id)
		}
	}
	logx.Warn(rows.Err(), "infra.log.rows_read")
	return names, ids
}

func readUsageToday(ctx context.Context, db *sql.DB, since time.Time) (map[string]usageToday, error) {
	rows, err := db.QueryContext(ctx, `SELECT workspace_id, count(*),
			COALESCE(SUM(input_tokens + output_tokens + cache_read + cache_creation), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM sessions WHERE finished_at >= $1 GROUP BY workspace_id`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	out := map[string]usageToday{}
	for rows.Next() {
		var (
			id string
			u  usageToday
		)
		if rows.Scan(&id, &u.runs, &u.tokens, &u.cost) == nil {
			out[id] = u
		}
	}
	return out, rows.Err()
}

func readProjects(ctx context.Context, db *sql.DB) []projectRow {
	rows, err := db.QueryContext(ctx, `SELECT id, workspace_id, path FROM projects`)
	if err != nil {
		return nil
	}
	defer closeRows(rows)
	var out []projectRow
	for rows.Next() {
		var p projectRow
		if rows.Scan(&p.id, &p.workspaceID, &p.path) == nil {
			out = append(out, p)
		}
	}
	logx.Warn(rows.Err(), "infra.log.rows_read")
	return out
}

func closeRows(rows *sql.Rows) {
	logx.Warn(rows.Close(), "infra.log.rows_close")
}

var dataDirParts = []string{"projects", "worktrees", "task-attachments", "logs", "runs"}

type storageWalk struct {
	storage   Storage
	byProject map[string]int64
}

// Task worktrees are named projects/.worktrees/<project>--<branch>; the split on "--" depends on
// that layout.
func walkDataDir(ctx context.Context, dir string) storageWalk {
	w := &dataDirWalker{
		ctx:   ctx,
		parts: map[string]int64{},
		out:   storageWalk{storage: Storage{DataDir: dir}, byProject: map[string]int64{}},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return w.out
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		switch e.Name() {
		case "projects":
			w.projects(full)
		case "task-attachments", "logs", "runs":
			w.add(e.Name(), w.size(full))
		default:
			w.add("other", w.size(full))
		}
	}
	for _, name := range append(dataDirParts, "other") {
		if n, ok := w.parts[name]; ok {
			w.out.storage.Dirs = append(w.out.storage.Dirs, DirSize{Name: name, Bytes: n})
		}
	}
	dirs := w.out.storage.Dirs
	sort.SliceStable(dirs, func(i, j int) bool { return dirs[i].Bytes > dirs[j].Bytes })
	return w.out
}

type dataDirWalker struct {
	ctx   context.Context
	parts map[string]int64
	out   storageWalk
}

func (w *dataDirWalker) add(part string, n int64) {
	w.parts[part] += n
	w.out.storage.Total += n
}

func (w *dataDirWalker) size(path string) int64 {
	n, partial := dirSize(w.ctx, path)
	w.out.storage.Partial = w.out.storage.Partial || partial
	return n
}

func (w *dataDirWalker) projects(dir string) {
	sub, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, p := range sub {
		full := filepath.Join(dir, p.Name())
		if p.Name() == ".worktrees" {
			w.worktrees(full)
			continue
		}
		n := w.size(full)
		w.add("projects", n)
		if p.IsDir() && !strings.HasPrefix(p.Name(), ".") {
			w.out.byProject[p.Name()] += n
		}
	}
}

func (w *dataDirWalker) worktrees(dir string) {
	wts, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, wt := range wts {
		n := w.size(filepath.Join(dir, wt.Name()))
		w.add("worktrees", n)
		if proj, _, ok := strings.Cut(wt.Name(), "--"); ok {
			w.out.byProject[proj] += n
		}
	}
}

func dirSize(ctx context.Context, root string) (total int64, partial bool) {
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			partial = true
			return fs.SkipAll
		}
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		partial = true
	}
	return total, partial
}

func workspaceStorage(byProject map[string]int64, projects []projectRow) map[string]int64 {
	out := map[string]int64{}
	for _, p := range projects {
		if p.path == "" {
			continue
		}
		out[p.workspaceID] += byProject[filepath.Base(p.path)]
	}
	return out
}

// UTC to match the Usage page's usage_daily buckets, so "today" adds up to the same figure there.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
