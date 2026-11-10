package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func mkAged(t *testing.T, path string, dir bool, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if dir {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "session.log"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestPruneRunFiles(t *testing.T) {
	base := t.TempDir()
	logs := filepath.Join(base, "data", "logs")
	runs := runOutputsDir(filepath.Join(base, "data"))
	agents := filepath.Join(base, "agents")
	outside := filepath.Join(base, "outside")
	old := time.Now().Add(-40 * 24 * time.Hour)
	fresh := time.Now().Add(-time.Hour)

	mkAged(t, filepath.Join(logs, "dev_2026-01-01_abc.log"), false, old)
	mkAged(t, filepath.Join(logs, "dev_2026-09-27_def.log"), false, fresh)
	mkAged(t, filepath.Join(logs, ".keep"), false, old)
	mkAged(t, filepath.Join(runs, "dev-task-t1-in_progress-1"), true, old)
	mkAged(t, filepath.Join(runs, "dev-task-t2-in_progress-2"), true, fresh)
	mkAged(t, filepath.Join(agents, "dev", "runs", "inst-old"), true, old)
	mkAged(t, filepath.Join(agents, "dev", "runs", "chat-old.jsonl"), false, old)
	mkAged(t, filepath.Join(agents, "dev", "runs", "inst-new"), true, fresh)
	mkAged(t, filepath.Join(outside, "victim.log"), false, old)
	mkAged(t, filepath.Join(outside, "runs", "inst-old"), true, old)
	if err := os.Symlink(filepath.Join(outside, "victim.log"), filepath.Join(logs, "link.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "runs", "inst-old"), filepath.Join(runs, "run-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(agents, "linked-agent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "runs", "inst-old"), filepath.Join(agents, "dev", "runs", "inst-link")); err != nil {
		t.Fatal(err)
	}

	r := &retention{logsDir: logs, runsDir: runs, agentsDir: agents, now: time.Now}
	r.pruneRunFiles(t.Context(), time.Now().Add(-30*24*time.Hour))

	for path, want := range map[string]bool{
		filepath.Join(logs, "dev_2026-01-01_abc.log"):             false,
		filepath.Join(logs, "dev_2026-09-27_def.log"):             true,
		filepath.Join(logs, ".keep"):                              true,
		filepath.Join(logs, "link.log"):                           true,
		filepath.Join(runs, "dev-task-t1-in_progress-1"):          false,
		filepath.Join(runs, "dev-task-t2-in_progress-2"):          true,
		filepath.Join(runs, "run-link"):                           true,
		filepath.Join(agents, "dev", "runs", "inst-old"):          false,
		filepath.Join(agents, "dev", "runs", "chat-old.jsonl"):    false,
		filepath.Join(agents, "dev", "runs", "inst-new"):          true,
		filepath.Join(agents, "dev", "runs", "inst-link"):         true,
		filepath.Join(outside, "victim.log"):                      true,
		filepath.Join(outside, "runs", "inst-old", "session.log"): true,
	} {
		if got := exists(path); got != want {
			t.Errorf("%s exists=%t, want %t", path, got, want)
		}
	}

	(&retention{logsDir: filepath.Join(base, "nope"), runsDir: filepath.Join(base, "nope3"),
		agentsDir: filepath.Join(base, "nope2"), now: time.Now}).
		pruneRunFiles(t.Context(), time.Now())
}

func TestStuckWorktreeRoots(t *testing.T) {
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	recent := time.Now()
	wt := map[string]string{"p1": "/data/projects/.worktrees/x"}
	tasks := []task.Task{
		{ID: "stuck", Status: task.StatusFailed, UpdatedAt: old, WorktreePaths: wt},
		{ID: "recent", Status: task.StatusBlocked, UpdatedAt: recent, WorktreePaths: wt},
		{ID: "idle", Status: task.StatusIdle, UpdatedAt: old, WorktreePaths: wt},
		{ID: "running", Status: task.StatusRunning, UpdatedAt: old, WorktreePaths: wt},
		{ID: "no-wt", Status: task.StatusOutOfTurns, UpdatedAt: old},
		{ID: "group-live", Status: task.StatusNeedsMoreTurns, UpdatedAt: old, WorktreePaths: wt},
		{ID: "group-live-sub", ParentID: "group-live", Status: task.StatusFailed, UpdatedAt: recent, WorktreePaths: wt},
		{ID: "group-run", Status: task.StatusFailed, UpdatedAt: old, WorktreePaths: wt},
		{ID: "group-run-sub", ParentID: "group-run", Status: task.StatusRunning, UpdatedAt: old, WorktreePaths: wt},
		{ID: "group-idle", Status: task.StatusBlocked, UpdatedAt: old, WorktreePaths: wt},
		{ID: "group-idle-sub", ParentID: "group-idle", Status: task.StatusFailed, UpdatedAt: old, WorktreePaths: wt},
	}
	got := stuckWorktreeRoots(tasks, cutoff)
	want := map[string]bool{"stuck": true, "group-idle": true}
	if len(got) != len(want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected root %s in %v", id, got)
		}
	}
}

func TestPressureSweepDue(t *testing.T) {
	now := time.Now()
	cooldown := 30 * time.Minute
	cases := []struct {
		name string
		free float64
		min  int
		last time.Time
		want bool
	}{
		{"plenty of space", 40, 10, time.Time{}, false},
		{"exactly at the floor", 10, 10, time.Time{}, false},
		{"below, first time", 5, 10, time.Time{}, true},
		{"below, cooling down", 5, 10, now.Add(-10 * time.Minute), false},
		{"below, cooldown over", 5, 10, now.Add(-31 * time.Minute), true},
		{"check disabled", 1, 0, time.Time{}, false},
	}
	for _, tc := range cases {
		if got := pressureSweepDue(tc.free, tc.min, tc.last, now, cooldown); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestCheckDiskPressure_SweepsOnceThenCoolsDown(t *testing.T) {
	j := newJanitor(nil, nil, nil, nil, t.TempDir())
	calls := 0
	j.diskFree = func(string) (float64, error) {
		calls++
		return 3, nil
	}
	j.checkDiskPressure(t.Context())
	if j.lastPressure.IsZero() {
		t.Fatal("no pressure sweep at 3% free")
	}
	if calls != 2 {
		t.Errorf("diskFree called %d times, want 2", calls)
	}
	first := j.lastPressure
	j.checkDiskPressure(t.Context())
	if !j.lastPressure.Equal(first) || calls != 3 {
		t.Errorf("swept again inside the cooldown (calls=%d)", calls)
	}
}

func TestDiskFreePercent(t *testing.T) {
	got, err := diskFreePercent(t.TempDir())
	if err != nil {
		t.Skipf("disk free not supported here: %v", err)
	}
	if got < 0 || got > 100 {
		t.Errorf("diskFreePercent = %.2f, want a percentage", got)
	}
}

func TestJanitorStatus(t *testing.T) {
	j := newJanitor(nil, nil, nil, nil, t.TempDir())
	j.diskFree = func(string) (float64, error) { return 42.5, nil }
	st := j.Status()
	if !st.LastSweep.IsZero() || st.Every != time.Hour || !st.DiskFreeKnown || st.DiskFreePercent != 42.5 {
		t.Fatalf("before any pass: %+v", st)
	}
	before := time.Now()
	j.sweep(t.Context(), true)
	st = j.Status()
	if st.LastSweep.Before(before) || !st.LastSweepPressure {
		t.Errorf("after a pressure pass: %+v", st)
	}
	j.Sweep(t.Context())
	if j.Status().LastSweepPressure {
		t.Error("a regular pass still reads as a pressure one")
	}
	j.diskFree = func(string) (float64, error) { return 0, errors.New("statfs: not supported") }
	if j.Status().DiskFreeKnown {
		t.Error("an unmeasured disk reads as known")
	}
}
