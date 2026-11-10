package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/project"
)

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	//i18n:ignore SQL in a test
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordSetupRun_OncePerRunKey(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	row := SetupRun{RunKey: "task:t1/app/c1/100", WorkspaceID: "ws1", ProjectID: "p1", TaskID: "t1",
		Kind: SetupRunTask, SetupKey: "k", Status: SetupStateRan, ToolsStatus: ToolsDownloaded,
		ToolsDownloaded: []string{"go@1.25.1", "node@22.3.0"}, ToolsUsed: []string{"go@1.25.1"},
		ToolsMs: 5, SetupMs: 7, TotalMs: 12, Error: strings.Repeat("x", 3000)}
	if ok, err := s.RecordSetupRun(ctx, row); err != nil || !ok {
		t.Fatalf("first insert = %t, %v", ok, err)
	}
	row.Status = SetupStateSkipped
	if ok, err := s.RecordSetupRun(ctx, row); err != nil || ok {
		t.Fatalf("second insert of the same key = %t, %v", ok, err)
	}
	if ok, err := s.RecordSetupRun(ctx, SetupRun{RunKey: "prewarm:x", ProjectID: "p1", Kind: SetupRunPrewarm,
		Status: SetupStateFailed}); err != nil || !ok {
		t.Fatalf("prewarm insert = %t, %v", ok, err)
	}
	runs, err := s.setupRunsSince(ctx, time.Unix(0, 0))
	if err != nil || len(runs) != 2 {
		t.Fatalf("rows = %+v, %v", runs, err)
	}
	got := runs[0]
	if got.Status != SetupStateRan || got.TaskID != "t1" || len(got.Error) != setupRunMaxError ||
		!reflect.DeepEqual(got.ToolsDownloaded, row.ToolsDownloaded) || !reflect.DeepEqual(got.ToolsUsed, row.ToolsUsed) {
		t.Fatalf("stored row = %+v", got)
	}
	if runs[1].TaskID != "" {
		t.Fatalf("a prewarm row has task %q", runs[1].TaskID)
	}
}

func TestPruneCacheMetricsBefore(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	old, recent := time.Now().Add(-100*24*time.Hour), time.Now().Add(-time.Hour)
	for i, at := range []time.Time{old, recent} {
		if _, err := s.RecordSetupRun(ctx, SetupRun{RunKey: string(rune('a' + i)), ProjectID: "p", Kind: SetupRunTask,
			Status: SetupStateRan, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordCacheUsage(ctx, CacheUsage{WorkspaceID: "ws", Source: CacheSourceTask, MeasuredAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneCacheMetricsBefore(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if countRows(t, s, "sandbox_setup_runs") != 1 || countRows(t, s, "sandbox_cache_usage") != 1 {
		t.Fatal("pruned the wrong rows")
	}
}

func TestCacheReport_FromStore(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	usage := []CacheUsage{
		{WorkspaceID: "ws1", TotalBytes: 100, MeasuredAt: now.Add(-20 * 24 * time.Hour)},
		{WorkspaceID: "ws1", TotalBytes: 200, MeasuredAt: now.Add(-3 * 24 * time.Hour)},
		{WorkspaceID: "ws1", TotalBytes: 500, CapacityBytes: 1000, UsedBytes: 600, MeasuredAt: now.Add(-time.Hour),
			Areas: map[string]int64{CacheAreaNpm: 300, CacheAreaOther: 200},
			Tools: []CacheTool{{Tool: "go", Version: "1.25.1", Bytes: 50}}},
		{WorkspaceID: "ws2", TotalBytes: 10, MeasuredAt: now.Add(-2 * time.Hour)},
	}
	for _, u := range usage {
		if err := s.RecordCacheUsage(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for i, st := range []string{SetupStateRan, SetupStateSkipped, SetupStateRestored} {
		if _, err := s.RecordSetupRun(ctx, SetupRun{RunKey: st, WorkspaceID: "ws1", ProjectID: "p1", Kind: SetupRunTask,
			Status: st, CreatedAt: now.Add(-time.Duration(i+1) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.CacheReport(ctx, "7d", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Workspaces) != 2 || rep.Workspaces[0].WorkspaceID != "ws1" {
		t.Fatalf("workspaces = %+v", rep.Workspaces)
	}
	w := rep.Workspaces[0]
	if w.TotalBytes != 500 || w.GrowthBytes != 300 || w.GrowthPct == nil || *w.GrowthPct != 150 ||
		w.CapacityPct == nil || *w.CapacityPct != 60 || w.Areas[CacheAreaNpm] != 300 || w.Runs != 3 {
		t.Fatalf("ws1 = %+v", w)
	}
	if rep.Totals.CacheBytes != 510 || rep.Totals.HitRate == nil || *rep.Totals.HitRate < 0.66 || rep.LogSince == nil {
		t.Fatalf("totals = %+v", rep.Totals)
	}
	if len(rep.Tools) != 1 || rep.Tools[0].Workspaces[0] != "ws1" {
		t.Fatalf("tools = %+v", rep.Tools)
	}
	if len(rep.Signals) != 1 || rep.Signals[0].Kind != SignalCacheGrowth {
		t.Fatalf("signals = %+v", rep.Signals)
	}
	if _, err := s.CacheReport(ctx, "1y", now); err == nil {
		t.Fatal("a 1y range was accepted")
	}
}

func TestCacheMeasure_RealShell(t *testing.T) {
	requireBins(t, "bash", "du", "df", "awk", "sed")
	cache := t.TempDir()
	write := func(rel string, size int) {
		p := filepath.Join(cache, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("npm/_cacache/blob", 64<<10)
	write("go/mod/cache/x", 16<<10)
	write("go/build/ab/c", 8<<10)
	write("mise/installs/go/1.25.1/bin/go", 32<<10)
	write("mise/installs/node/22.3.0/bin/node", 16<<10)
	write("snapshots/p1/old.tar.zst", 4<<10)
	write("snapshots/p1/new.tar.zst", 4<<10)
	write("stray/file", 4<<10)
	if err := os.Symlink("22.3.0", filepath.Join(cache, "mise/installs/node/22")); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(cache, "snapshots/p1/old.tar.zst"), past, past); err != nil {
		t.Fatal(err)
	}

	measure := func(env ...string) string {
		cmd := exec.CommandContext(t.Context(), "bash", "-c", cacheMeasureCmd)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("measure: %v", err)
		}
		return string(out)
	}
	u, ok := parseCacheMeasure(measure("OCTOPUS_CACHE_DIR="+cache, "MISE_DATA_DIR="+filepath.Join(cache, "mise")))
	if !ok {
		t.Fatal("no measurement")
	}
	// Directories take blocks of their own on ext4 but not on APFS, so the files alone (148 KiB)
	// are the floor.
	if u.TotalBytes < 148<<10 || u.CapacityBytes <= 0 || u.UsedBytes <= 0 {
		t.Fatalf("usage = %+v", u)
	}
	for _, area := range []string{CacheAreaNpm, CacheAreaGoMod, CacheAreaGoBuild, CacheAreaMise, CacheAreaSnapshots, CacheAreaOther} {
		if u.Areas[area] <= 0 {
			t.Errorf("area %s = %d (%v)", area, u.Areas[area], u.Areas)
		}
	}
	var tools []string
	for _, tl := range u.Tools {
		tools = append(tools, tl.Tool+"@"+tl.Version)
		if tl.Bytes <= 0 {
			t.Errorf("tool %+v has no size", tl)
		}
	}
	if !reflect.DeepEqual(tools, []string{"go@1.25.1", "node@22.3.0"}) {
		t.Errorf("tools = %v", tools)
	}
	if len(u.Snapshots) != 1 || u.Snapshots[0].ProjectID != "p1" || u.Snapshots[0].Count != 2 ||
		u.Snapshots[0].Newest != "new" || u.Snapshots[0].Bytes <= 0 {
		t.Errorf("snapshots = %+v", u.Snapshots)
	}
	if _, ok := parseCacheMeasure(measure("OCTOPUS_CACHE_DIR=")); ok {
		t.Error("a sandbox without cache was measured")
	}
	if _, ok := parseCacheMeasure("cache=/cache\ndu=12\t/cache/npm\n"); ok {
		t.Error("a partial walk was accepted")
	}
}

func TestParseInitProbe_RunID(t *testing.T) {
	if p := parseInitProbe("alive=1\nsetup_run=42-1700000000123\n"); p.setup != nil {
		t.Fatalf("an id alone made a report: %+v", p.setup)
	}
	p := parseInitProbe("alive=1\nphase=start\nsetup_run=42-1700000000123\nsetup_status=skipped\n")
	if p.setup == nil || p.setup.RunID != "42-1700000000123" {
		t.Fatalf("setup = %+v", p.setup)
	}
	run := InitRun{container: "c1", StartedAt: time.Unix(1700000000, 0), Setup: p.setup}
	if k := initRunKey("t1", "app", run); k != "task:t1/app/c1/42-1700000000123" {
		t.Fatalf("key = %s", k)
	}
	run.Setup = &InitSetupReport{Status: SetupStateRan}
	if k := initRunKey("t1", "app", run); k != "task:t1/app/c1/1700000000" {
		t.Fatalf("fallback key = %s", k)
	}
}

func TestParsePrewarmOut_Timings(t *testing.T) {
	res := parsePrewarmOut("prewarm_tools_ms=1200\nprewarm_tools_downloaded=go@1.25.1,\nprewarm_tools_used=go@1.25.1\n" +
		"prewarm_key=abc\nprewarm_setup_ms=45000\nprewarm_size=10\n")
	if res.ToolsMs != 1200 || res.SetupMs != 45000 || res.Key != "abc" ||
		!reflect.DeepEqual(res.ToolsDownloaded, []string{"go@1.25.1"}) || !reflect.DeepEqual(res.ToolsUsed, []string{"go@1.25.1"}) {
		t.Fatalf("result = %+v", res)
	}
}

func fakeMise(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	//i18n:ignore shell script used by a test
	script := `#!/bin/sh
case "$1" in
  install) shift; for a in "$@"; do mkdir -p "$MISE_DATA_DIR/installs/${a%@*}/${a#*@}"; done ;;
  exec) shift; while [ "$1" != "--" ]; do shift; done; shift; exec "$@" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInitRun_InstallLog(t *testing.T) {
	requireBins(t, "sha256sum", "tar", "du", "df")
	cache := t.TempDir()
	t.Setenv("OCTOPUS_CACHE_DIR", cache)
	t.Setenv("MISE_DATA_DIR", filepath.Join(cache, "mise"))
	count := filepath.Join(t.TempDir(), "count")
	t.Setenv("SETUP_COUNT", count)
	fakeMise(t)
	m := project.InitManifest{
		Setup: fakeInstall,
		Tools: map[string]string{"go": "1.25.1"},
		Cache: project.InitCache{Key: []string{"package-lock.json"}, Paths: []string{"node_modules"}},
	}
	r := &setupRig{initRig: newInitRig(t, initScript("test -f node_modules/dep/index.js && echo started", m)), cache: cache, count: count}
	if err := os.WriteFile(filepath.Join(r.work, "package-lock.json"), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st := r.h.mgr.store

	first := r.run(t)
	if first.State != InitStateReady || first.Setup == nil || first.Setup.Status != SetupStateRan ||
		!reflect.DeepEqual(first.Setup.ToolsDownloaded, []string{"go@1.25.1"}) {
		t.Fatalf("first run = %+v / %+v", first, first.Setup)
	}
	for i := 0; i < 3; i++ {
		r.initStatusRun(t)
	}
	second := r.run(t)
	if second.Setup == nil || second.Setup.Status != SetupStateSkipped || len(second.Setup.ToolsDownloaded) != 0 {
		t.Fatalf("second run = %+v", second.Setup)
	}
	raw, _ := json.Marshal(second.Setup)
	if strings.Contains(string(raw), "tools_used") {
		t.Fatalf("tools_used leaked into the tool result: %s", raw)
	}

	runs, err := st.setupRunsSince(t.Context(), time.Unix(0, 0))
	if err != nil || len(runs) != 2 {
		t.Fatalf("install log = %+v, %v", runs, err)
	}
	a, b := runs[0], runs[1]
	if a.Status != SetupStateRan || a.ToolsStatus != ToolsDownloaded || !reflect.DeepEqual(a.ToolsDownloaded, []string{"go@1.25.1"}) ||
		!reflect.DeepEqual(a.ToolsUsed, []string{"go@1.25.1"}) || a.TaskID != "task-1" || a.WorkspaceID != "ws-1" ||
		a.ProjectID != "p-app" || a.Kind != SetupRunTask || a.SetupKey == "" {
		t.Fatalf("first row = %+v", a)
	}
	if b.Status != SetupStateSkipped || b.ToolsStatus != ToolsCached || len(b.ToolsDownloaded) != 0 {
		t.Fatalf("second row = %+v", b)
	}

	r.h.mgr.inits.drop("task-1")
	r.initStatusRun(t)
	if n := countRows(t, st, "sandbox_setup_runs"); n != 2 {
		t.Fatalf("after a restart the log has %d rows", n)
	}

	deadline := time.Now().Add(20 * time.Second)
	for countRows(t, st, "sandbox_cache_usage") == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	usage, err := st.cacheUsagePerWorkspace(t.Context(), time.Unix(0, 0), true)
	if err != nil || len(usage) != 1 || usage[0].WorkspaceID != "ws-1" || usage[0].Source != CacheSourceTask {
		t.Fatalf("measurements = %+v, %v", usage, err)
	}
	if len(usage[0].Tools) != 1 || usage[0].Tools[0].Tool != "go" {
		t.Fatalf("measured tools = %+v", usage[0].Tools)
	}
	if n := countRows(t, st, "sandbox_cache_usage"); n != 1 {
		t.Fatalf("%d measurements within the hour", n)
	}
}

func TestApplyCacheQuota(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name              string
		capacity, used    int64
		quota             int64
		wantCap, wantUsed int64
	}{
		{"node disk behind the claim", 1800 * gib, 900 * gib, 20 * gib, 20 * gib, 3 * gib},
		{"claim reports its own size", 20 * gib, 4 * gib, 20 * gib, 20 * gib, 4 * gib},
		{"df could not tell", 0, 0, 20 * gib, 20 * gib, 3 * gib},
		{"no quota (Docker)", 1800 * gib, 900 * gib, 0, 1800 * gib, 900 * gib},
	}
	for _, c := range cases {
		u := CacheUsage{TotalBytes: 3 * gib, CapacityBytes: c.capacity, UsedBytes: c.used}
		applyCacheQuota(&u, c.quota)
		if u.CapacityBytes != c.wantCap || u.UsedBytes != c.wantUsed {
			t.Errorf("%s: capacity=%d used=%d, want %d/%d", c.name, u.CapacityBytes, u.UsedBytes, c.wantCap, c.wantUsed)
		}
	}
}
