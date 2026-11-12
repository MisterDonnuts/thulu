package task

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// The board against the real hour store: what a person sees on the task as the month goes by.
func TestHoursEndToEnd_NoticesAndCutsOnTheTask(t *testing.T) {
	ctx := context.Background()
	hours, err := entitlements.NewStore(testpg.New(t), entitlements.PlanTrial)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	hours.SetClock(func() time.Time { return now })
	if err := hours.ApplyPlan(testWS, entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}

	d, store := newStageTestDispatcher(t, customBoard())
	d.WithHourQuota(hours)
	rec := newRecordingLauncher()
	d.launcher = rec

	launch := func() *Task {
		t.Helper()
		tk := gatedTask(t, store)
		d.launchStage(ctx, tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
		after, err := store.Get(t.Context(), tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		return after
	}
	// keys lists the hour notices and cuts on the task, leaving out the "launched" comment.
	keys := func(tk *Task) []string {
		var out []string
		for _, c := range tk.Comments {
			if c.Key != "task.comments.launched" {
				out = append(out, c.Key)
			}
		}
		return out
	}
	has := func(tk *Task, key, text string) bool {
		for _, c := range tk.Comments {
			if c.Key == key && strings.Contains(c.Body, text) {
				return true
			}
		}
		return false
	}

	if tk := launch(); len(rec.launches()) != 1 || len(keys(tk)) != 0 {
		t.Fatalf("fresh month: launches=%d comments=%v", len(rec.launches()), keys(tk))
	}
	if err := hours.AddSeconds(ctx, testWS, "2026-12", 161*3600); err != nil {
		t.Fatal(err)
	}
	if tk := launch(); len(rec.launches()) != 2 || !has(tk, "task.comments.hours_notice_80", "$10 cap") {
		t.Fatalf("161 of 200 h: launches=%d comments=%v", len(rec.launches()), keys(tk))
	}
	if tk := launch(); len(keys(tk)) != 0 {
		t.Fatalf("the 80%% notice repeated: %v", keys(tk))
	}
	if err := hours.AddSeconds(ctx, testWS, "2026-12", 40*3600); err != nil {
		t.Fatal(err)
	}
	if tk := launch(); len(rec.launches()) != 4 || !has(tk, "task.comments.hours_notice_100", "$10 cap") {
		t.Fatalf("201 h: launches=%d comments=%v", len(rec.launches()), keys(tk))
	}
	if err := hours.AddSeconds(ctx, testWS, "2026-12", 200*3600); err != nil {
		t.Fatal(err)
	}
	tk := launch()
	if len(rec.launches()) != 4 || tk.Status != StatusNotStarted ||
		!has(tk, "task.comments.hours_cap_reached", "$10 cap on extra hours for 2026-12") ||
		!has(tk, "task.comments.hours_cap_reached", "2027-01-01") {
		t.Fatalf("401 h: launches=%d status=%s comments=%v", len(rec.launches()), tk.Status, keys(tk))
	}

	// An admin raises the cap: the next launch goes through.
	if err := hours.SetExtraHoursCap(testWS, 30); err != nil {
		t.Fatal(err)
	}
	if launch(); len(rec.launches()) != 5 {
		t.Fatal("cap raised to $30, the run still did not start")
	}

	// Stripe cancels the subscription.
	if err := hours.SetPlan(testWS, entitlements.PlanInactive); err != nil {
		t.Fatal(err)
	}
	if tk := launch(); len(rec.launches()) != 5 || !has(tk, "task.comments.plan_inactive", "") {
		t.Fatalf("inactive: launches=%d comments=%v", len(rec.launches()), keys(tk))
	}
}

func TestHoursEndToEnd_TrialHoursSpent(t *testing.T) {
	ctx := context.Background()
	hours, err := entitlements.NewStore(testpg.New(t), entitlements.PlanTrial)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC)
	hours.SetClock(func() time.Time { return now })

	d, store := newStageTestDispatcher(t, customBoard())
	d.WithHourQuota(hours)
	rec := newRecordingLauncher()
	d.launcher = rec

	tk := gatedTask(t, store)
	d.launchStage(ctx, tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
	if err := hours.AddSeconds(ctx, testWS, "2026-12", 41*3600); err != nil {
		t.Fatal(err)
	}
	tk = gatedTask(t, store)
	d.launchStage(ctx, tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
	if body := commentWithKey(t, store, tk.ID, "task.comments.hours_notice_80_trial"); !strings.Contains(body, "80% of the 50 agent hours of its trial") ||
		!strings.Contains(body, "2027-01-19") {
		t.Fatalf("trial 80%% notice = %q", body)
	}

	// January: the trial's hours do not come back.
	now = time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC)
	if err := hours.AddSeconds(ctx, testWS, "2026-12", 10*3600); err != nil {
		t.Fatal(err)
	}
	tk = gatedTask(t, store)
	d.launchStage(ctx, tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})
	if body := lastComment(t, store, tk.ID); len(rec.launches()) != 2 ||
		!strings.Contains(body, "used the 50 agent hours of its 30-day trial") {
		t.Fatalf("January after 51 trial hours: launches=%d comment=%q", len(rec.launches()), body)
	}
}

func commentWithKey(t *testing.T, store *Store, taskID, key string) string {
	t.Helper()
	full, err := store.Get(t.Context(), taskID)
	if err != nil || full == nil {
		t.Fatalf("get task: %v", err)
	}
	for _, c := range full.Comments {
		if c.Key == key {
			return c.Body
		}
	}
	return ""
}
