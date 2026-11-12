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
	mu     sync.Mutex
	answer entitlements.Admission
	err    error
	calls  int
}

func spentQuota(r entitlements.Refusal) *fakeQuota {
	return &fakeQuota{answer: entitlements.Admission{Refusal: r, Usage: entitlements.HourUsage{
		Period: "2026-10", IncludedHours: 200, ExtraHours: 1000, CapUSD: 50,
		ResetsAt:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		TrialEndsAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
	}}}
}

func (q *fakeQuota) AdmitRun(_ context.Context, _ string) (entitlements.Admission, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	return q.answer, q.err
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

func TestHourQuota_RefusedWorkspaceDoesNotLaunch(t *testing.T) {
	cases := []struct {
		refusal entitlements.Refusal
		want    []string
	}{
		{entitlements.RefusedHours, []string{"200 agent hours", "2026-10", "2026-11-01"}},
		{entitlements.RefusedCap, []string{"$50 cap", "1000 extra hours", "2026-11-01"}},
		{entitlements.RefusedTrialEnded, []string{"trial ended on 2026-10-20"}},
		{entitlements.RefusedPlanInactive, []string{"no active plan"}},
	}
	for _, c := range cases {
		t.Run(string(c.refusal), func(t *testing.T) {
			d, store := newStageTestDispatcher(t, customBoard())
			d.WithHourQuota(spentQuota(c.refusal))
			tk := gatedTask(t, store)

			d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

			after, _ := store.Get(t.Context(), tk.ID)
			if after.Status != StatusNotStarted {
				t.Errorf("status = %q, want not_started", after.Status)
			}
			body := lastComment(t, store, tk.ID)
			for _, want := range c.want {
				if !strings.Contains(body, want) {
					t.Errorf("refusal comment does not mention %q:\n%s", want, body)
				}
			}
		})
	}
}

func TestHourQuota_NoticeIsPostedAndTheRunStarts(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	q := spentQuota("")
	q.answer.OK, q.answer.Notice = true, 80
	d.WithHourQuota(q)
	rec := newRecordingLauncher()
	d.launcher = rec
	tk := gatedTask(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	comments, err := store.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		if c.Key == "task.comments.hours_notice_80" && strings.Contains(c.Body, "$50 cap") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no 80%% notice among %d comments", len(comments))
	}
	if len(rec.launches()) != 1 {
		t.Fatal("a run with a notice should still start")
	}
}

func TestHourQuota_UnreadableQuotaLetsTheRunThrough(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	q := spentQuota(entitlements.RefusedHours)
	q.err = errors.New("db down")
	d.WithHourQuota(q)
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

func TestHourQuota_MentionIsRefused(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	q := spentQuota(entitlements.RefusedHours)
	d.WithHourQuota(q)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c2", Body: "dos"})
	if len(l.launches()) != 0 || q.calls != 1 {
		t.Fatalf("mention launched past spent hours: launches=%d calls=%d", len(l.launches()), q.calls)
	}
	body := lastComment(t, store, tk.ID)
	if !strings.Contains(body, "@devops did not answer") || !strings.Contains(body, "200 agent hours") {
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
