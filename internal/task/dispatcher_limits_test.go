package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type fakeQuota struct {
	mu       sync.Mutex
	limit    int
	used     map[string]int
	refunded int
	err      error
}

func newFakeQuota(limit int) *fakeQuota { return &fakeQuota{limit: limit, used: map[string]int{}} }

func (q *fakeQuota) ConsumeRun(_ context.Context, ws string) (entitlements.RunUsage, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return entitlements.RunUsage{}, false, q.err
	}
	u := entitlements.RunUsage{Period: "2026-10", Limit: q.limit,
		ResetsAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if q.used[ws] >= q.limit {
		u.Used = q.used[ws]
		return u, false, nil
	}
	q.used[ws]++
	u.Used = q.used[ws]
	u.Remaining = q.limit - u.Used
	return u, true, nil
}

func (q *fakeQuota) RefundRun(_ context.Context, ws, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used[ws]--
	q.refunded++
	return nil
}

func (q *fakeQuota) usedBy(ws string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.used[ws]
}

type killingLauncher struct {
	*recordingLauncher
	killMu  sync.Mutex
	killed  []string
	fireOut bool
	t       *testing.T
}

func (k *killingLauncher) KillRun(_ context.Context, runID string) error {
	k.killMu.Lock()
	k.killed = append(k.killed, runID)
	k.killMu.Unlock()
	if k.fireOut {
		k.exit(k.t, runID)
	}
	return nil
}

func (k *killingLauncher) kills() []string {
	k.killMu.Lock()
	defer k.killMu.Unlock()
	return append([]string(nil), k.killed...)
}

type failingLauncher struct{ *recordingLauncher }

func (failingLauncher) LaunchForStage(context.Context, *Task, Stage, *types.AgentType,
	map[string]string, map[string]string, map[string]string, bool,
) (string, string, string, error) {
	return "", "", "", errors.New("no docker")
}

func TestRunQuota_SpentWorkspaceDoesNotLaunch(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	q := newFakeQuota(0)
	d.WithRunQuota(q)
	tk := gatedTask(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	after, _ := store.Get(t.Context(), tk.ID)
	if after.Status != StatusNotStarted {
		t.Errorf("status = %q, want not_started", after.Status)
	}
	body := lastComment(t, store, tk.ID)
	for _, want := range []string{"2026-10", "2026-11-01", "agent runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("quota comment does not mention %q:\n%s", want, body)
		}
	}
}

func TestRunQuota_FailedLaunchIsRefunded(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.launcher = failingLauncher{newRecordingLauncher()}
	q := newFakeQuota(5)
	d.WithRunQuota(q)
	tk := gatedTask(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	if used := q.usedBy(testWS); used != 0 || q.refunded != 1 {
		t.Fatalf("used=%d refunded=%d after a failed launch, want 0 and 1", used, q.refunded)
	}
}

func TestRunQuota_UnreadableQuotaLetsTheRunThrough(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	q := newFakeQuota(0)
	q.err = errors.New("db down")
	d.WithRunQuota(q)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c1", Body: "hola"})
	if len(l.launches()) != 1 {
		t.Fatal("mention did not launch while the quota store was failing")
	}
}

func TestRunQuota_MentionCountsAndIsRefused(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	q := newFakeQuota(1)
	d.WithRunQuota(q)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c1", Body: "uno"})
	if q.usedBy(testWS) != 1 || len(l.launches()) != 1 {
		t.Fatalf("first mention: used=%d launches=%d", q.usedBy(testWS), len(l.launches()))
	}
	l.exit(t, "ctr-a")
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c2", Body: "dos"})
	if len(l.launches()) != 1 {
		t.Fatal("second mention launched past a spent quota")
	}
	if body := lastComment(t, store, tk.ID); !strings.Contains(body, "@devops did not answer") {
		t.Errorf("thread notice = %q", body)
	}
}

func newDeadlineDispatcher(t *testing.T, limit time.Duration, fire bool) (*Dispatcher, *Store, *killingLauncher) {
	t.Helper()
	d, store, rec := newMentionDispatcher(t)
	k := &killingLauncher{recordingLauncher: rec, fireOut: fire, t: t}
	d.launcher = k
	d.WithRunDeadline(func(string) time.Duration { return limit })
	return d, store, k
}

func TestRunDeadline_StopsARunPastTheLimit(t *testing.T) {
	d, store, k := newDeadlineDispatcher(t, 30*time.Millisecond, true)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c1", Body: "loop forever"})

	waitFor(t, func() bool { return len(k.kills()) == 1 })
	if k.kills()[0] != "ctr-a" {
		t.Errorf("killed %v, want ctr-a", k.kills())
	}
	waitFor(t, func() bool {
		full, _ := store.Get(t.Context(), tk.ID)
		for _, c := range full.Comments {
			if strings.Contains(c.Body, "agent.max_run_minutes") {
				return true
			}
		}
		return false
	})
}

func TestRunDeadline_IsPerWorkspace(t *testing.T) {
	d, store, rec := newMentionDispatcher(t)
	k := &killingLauncher{recordingLauncher: rec, fireOut: true, t: t}
	d.launcher = k
	var mu sync.Mutex
	asked := map[string]int{}
	d.WithRunDeadline(func(ws string) time.Duration {
		mu.Lock()
		asked[ws]++
		mu.Unlock()
		if ws == "ws-short" {
			return 30 * time.Millisecond
		}
		return time.Hour
	})
	long := &Task{WorkspaceID: testWS, Title: "default limit"}
	short := &Task{WorkspaceID: "ws-short", Title: "own limit"}
	for _, tk := range []*Task{long, short} {
		if err := store.Create(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
		full, _ := store.Get(t.Context(), tk.ID)
		d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c-" + tk.WorkspaceID, Body: "work"})
	}
	waitFor(t, func() bool { return len(k.launches()) == 2 })
	waitFor(t, func() bool { return len(k.kills()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if kills := k.kills(); len(kills) != 1 || kills[0] != "ctr-b" {
		t.Fatalf("killed %v, want only ctr-b (the ws-short run)", kills)
	}
	mu.Lock()
	if asked["ws-short"] == 0 || asked[testWS] == 0 {
		t.Errorf("deadline asked for %v, want both workspaces", asked)
	}
	mu.Unlock()
	k.exit(t, "ctr-a")
}

func TestRunDeadline_RunEndingInTimeIsNotKilled(t *testing.T) {
	d, store, k := newDeadlineDispatcher(t, 80*time.Millisecond, false)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c1", Body: "quick"})
	k.exit(t, "ctr-a")
	time.Sleep(200 * time.Millisecond)
	if kills := k.kills(); len(kills) != 0 {
		t.Fatalf("a run that ended in time was killed: %v", kills)
	}
}

func TestRunDeadline_ZeroIsUnlimited(t *testing.T) {
	d, _, _ := newDeadlineDispatcher(t, 0, false)
	stop := d.armDeadline(context.Background(), testWS, "t", "r", func(int) { t.Error("deadline fired with limit 0") })
	stop()
}
