package project

import (
	"errors"
	"testing"
	"time"
)

func snapshotManifest() InitManifest {
	return InitManifest{Setup: "npm ci", Cache: InitCache{Key: []string{"package-lock.json"}, Paths: []string{"node_modules"}}}
}

func TestPrewarmDue(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	m := snapshotManifest()
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	failed := func(key string, ago time.Duration) SetupPrewarm {
		f := at(ago)
		return SetupPrewarm{SetupKey: key, Status: PrewarmFailed, StartedAt: f, FinishedAt: &f}
	}
	cases := []struct {
		name    string
		m       InitManifest
		key     string
		history []SetupPrewarm
		manual  bool
		want    bool
		reason  string
	}{
		{"no setup", InitManifest{Cache: m.Cache}, "k", nil, false, false, PrewarmDueNoSetup},
		{"no cache paths", InitManifest{Setup: "npm ci"}, "k", nil, false, false, PrewarmDueNoSetup},
		{"no key", m, "", nil, false, false, PrewarmDueNoKey},
		{"never prewarmed", m, "k", nil, false, true, PrewarmDueYes},
		{"ok for this key", m, "k", []SetupPrewarm{{SetupKey: "k", Status: PrewarmOK, StartedAt: at(time.Hour)}}, false, false, PrewarmDueFresh},
		{"ok for an older key", m, "k2", []SetupPrewarm{{SetupKey: "k", Status: PrewarmOK, StartedAt: at(time.Hour)}}, false, true, PrewarmDueYes},
		{"manual reruns a fresh key", m, "k", []SetupPrewarm{{SetupKey: "k", Status: PrewarmOK, StartedAt: at(time.Hour)}}, true, true, PrewarmDueYes},
		{"running", m, "k", []SetupPrewarm{{SetupKey: "x", Status: PrewarmRunning, StartedAt: at(time.Minute)}}, true, false, PrewarmDueRunning},
		{"stale running does not block", m, "k", []SetupPrewarm{{SetupKey: "x", Status: PrewarmRunning, StartedAt: at(3 * time.Hour)}}, false, true, PrewarmDueYes},
		{"one failure, within 10 min", m, "k", []SetupPrewarm{failed("k", 5*time.Minute)}, false, false, PrewarmDueBackoff},
		{"one failure, after 10 min", m, "k", []SetupPrewarm{failed("k", 11*time.Minute)}, false, true, PrewarmDueYes},
		{"three failures, 30 min ago (backoff 40)", m, "k", []SetupPrewarm{failed("k", 30*time.Minute), failed("k", time.Hour), failed("k", 2*time.Hour)}, false, false, PrewarmDueBackoff},
		{"manual skips the backoff", m, "k", []SetupPrewarm{failed("k", time.Minute)}, true, true, PrewarmDueYes},
		{"failures of another key do not count", m, "k2", []SetupPrewarm{failed("k", time.Minute)}, false, true, PrewarmDueYes},
		{"a skipped round is not a failure", m, "k", []SetupPrewarm{{SetupKey: "k", Status: PrewarmSkipped, StartedAt: at(time.Minute)}}, false, true, PrewarmDueYes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := PrewarmDue(c.m, c.key, c.history, now, c.manual)
			if got != c.want || reason != c.reason {
				t.Fatalf("PrewarmDue = %v/%s, want %v/%s", got, reason, c.want, c.reason)
			}
		})
	}
	if PrewarmBackoff(1) != 10*time.Minute || PrewarmBackoff(3) != 40*time.Minute || PrewarmBackoff(20) != PrewarmBackoffMax {
		t.Fatalf("backoff = %v %v %v", PrewarmBackoff(1), PrewarmBackoff(3), PrewarmBackoff(20))
	}
}

func TestPrewarmStore(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)
	seedInitProject(t, s, "p2", testWS)

	a, err := s.StartPrewarm(ctx, "p1", "key-a", "c1", PrewarmReasonLoop)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if a.WorkspaceID != testWS || a.Status != PrewarmRunning {
		t.Fatalf("started = %+v", a)
	}
	if _, err := s.StartPrewarm(ctx, "p1", "key-b", "c2", PrewarmReasonManual); !errors.Is(err, ErrPrewarmRunning) {
		t.Fatalf("second running prewarm of p1: %v, want ErrPrewarmRunning", err)
	}
	b, err := s.StartPrewarm(ctx, "p2", "key-x", "c9", PrewarmReasonSave)
	if err != nil {
		t.Fatalf("start p2: %v", err)
	}

	if err := s.FinishPrewarm(ctx, a.ID, PrewarmOutcome{Status: PrewarmOK, SnapshotKey: "snap-a", SizeBytes: 42}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	c, err := s.StartPrewarm(ctx, "p1", "key-c", "c3", PrewarmReasonLoop)
	if err != nil {
		t.Fatalf("start after finish: %v", err)
	}
	if err := s.FinishPrewarm(ctx, c.ID, PrewarmOutcome{Status: PrewarmOK, SnapshotKey: "snap-c", Pruned: []string{"snap-a"}}); err != nil {
		t.Fatalf("finish c: %v", err)
	}
	list, err := s.ListPrewarms(ctx, "p1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != c.ID || list[1].Status != PrewarmPruned || list[1].SizeBytes != 42 || list[0].FinishedAt == nil {
		t.Fatalf("history = %+v", list)
	}

	n, err := s.FailStalePrewarms(ctx, time.Now().Add(time.Minute), nil, "interrupted")
	if err != nil || n != 1 {
		t.Fatalf("FailStalePrewarms = %d, %v; want 1", n, err)
	}
	p2, _ := s.ListPrewarms(ctx, "p2", 0)
	if len(p2) != 1 || p2[0].ID != b.ID || p2[0].Status != PrewarmFailed || p2[0].Error != "interrupted" {
		t.Fatalf("p2 history = %+v", p2)
	}

	for i := 0; i < prewarmKeepPerProject+5; i++ {
		r, err := s.StartPrewarm(ctx, "p1", "k", "c", PrewarmReasonLoop)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishPrewarm(ctx, r.ID, PrewarmOutcome{Status: PrewarmFailed, Error: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := s.ListPrewarms(ctx, "p1", 100); len(list) != prewarmKeepPerProject {
		t.Fatalf("kept %d rows, want %d", len(list), prewarmKeepPerProject)
	}

	if err := s.DeletePrewarms(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListPrewarms(ctx, "p1", 0); len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}
