package automation_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/automation"
)

func TestParseSpec_Rejects(t *testing.T) {
	cases := []struct{ name, spec, wantErr string }{
		{"no name", `trigger: {manual: true}
steps: [{id: a, task: {title: x}}]`, "name is required"},

		{"no trigger", `name: n
steps: [{id: a, task: {title: x}}]`, "trigger must declare"},

		{"two triggers", `name: n
trigger: {schedule: "* * * * *", event: task.created}
steps: [{id: a, task: {title: x}}]`, "exactly one"},

		{"bad cron", `name: n
trigger: {schedule: "77 * * * *"}
steps: [{id: a, task: {title: x}}]`, "minute"},

		{"bad timezone", `name: n
trigger: {schedule: "* * * * *", tz: Mars/Olympus}
steps: [{id: a, task: {title: x}}]`, "timezone"},

		{"unknown event", `name: n
trigger: {event: github.something_else}
steps: [{id: a, task: {title: x}}]`, "unknown event"},

		{"no steps", `name: n
trigger: {manual: true}
steps: []`, "at least one step"},

		{"step with no action", `name: n
trigger: {manual: true}
steps: [{id: a}]`, "declares no action"},

		{"two actions in one step", `name: n
trigger: {manual: true}
steps: [{id: a, condition: "1 == 1", task: {title: x}}]`, "exactly one is allowed"},

		{"duplicate ids", `name: n
trigger: {manual: true}
steps:
  - {id: a, task: {title: x}}
  - {id: a, task: {title: y}}`, "duplicate step id"},

		{"bad step id", `name: n
trigger: {manual: true}
steps: [{id: "Step One", task: {title: x}}]`, "step id"},

		{"uncapped fanout", `name: n
trigger: {manual: true}
steps:
  - id: f
    fanout: {over: "vars.items"}
    steps: [{id: a, task: {title: x}}]`, "max_items"},

		{"fanout above the hard cap", `name: n
trigger: {manual: true}
steps:
  - id: f
    fanout: {over: "vars.items", max_items: 500}
    steps: [{id: a, task: {title: x}}]`, "cap"},

		{"nested fanout", `name: n
trigger: {manual: true}
steps:
  - id: f
    fanout: {over: "vars.a", max_items: 2}
    steps:
      - id: g
        fanout: {over: "vars.b", max_items: 2}
        steps: [{id: a, task: {title: x}}]`, "nests a fanout"},

		{"broken condition", `name: n
trigger: {manual: true}
steps: [{id: a, condition: "size(("}]`, "condition"},

		{"broken template", `name: n
trigger: {manual: true}
steps: [{id: a, task: {title: "hola {{item..name}}"}}]`, "template"},

		{"typo'd key", `name: n
trigger: {manual: true}
setps: []
steps: [{id: a, task: {title: x}}]`, `key "setps"`},

		{"bad on_duplicate", `name: n
trigger: {manual: true}
steps: [{id: a, task: {title: x, fingerprint: f, on_duplicate: maybe}}]`, "on_duplicate"},

		{"timeout beyond the ceiling", `name: n
trigger: {manual: true}
policy: {timeout: 3h}
steps: [{id: a, task: {title: x}}]`, "timeout above"},

		{"bad concurrency", `name: n
trigger: {manual: true}
policy: {concurrency: sometimes}
steps: [{id: a, task: {title: x}}]`, "concurrency"},

		{"metric without op", `name: n
trigger: {metric: {environment: e, metric: m, threshold: 1}}
steps: [{id: a, task: {title: x}}]`, "op must be"},

		{"child steps outside a fanout", `name: n
trigger: {manual: true}
steps:
  - id: a
    task: {title: x}
    steps: [{id: b, task: {title: y}}]`, "not a fanout"},

		{"script timeout above the ceiling", `name: n
trigger: {manual: true}
steps: [{id: a, script: {run: "sleep 1", timeout: 30m}}]`, "script.timeout above 10m"},

		{"per-step sandbox image", `name: n
trigger: {manual: true}
steps: [{id: a, script: {run: "echo hi", image: "python:3.12-slim"}}]`, `key "image"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := automation.ParseSpec(c.spec)
			if err == nil {
				t.Fatalf("spec was accepted:\n%s", c.spec)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func TestParseSpec_Defaults(t *testing.T) {
	spec, err := automation.ParseSpec(`
name: n
trigger: {schedule: "@daily"}
steps: [{id: a, task: {title: x, fingerprint: f}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Policy.Concurrency != automation.ConcurrencyForbid {
		t.Errorf("concurrency default = %q, want forbid", spec.Policy.Concurrency)
	}
	if spec.Policy.Timeout.Duration() != 10*time.Minute {
		t.Errorf("timeout default = %s", spec.Policy.Timeout)
	}
	if spec.Policy.HistoryLimit != 200 {
		t.Errorf("history limit default = %d", spec.Policy.HistoryLimit)
	}
	if spec.Steps[0].Task.OnDuplicate != automation.DuplicateComment {
		t.Errorf("on_duplicate default = %q, want comment", spec.Steps[0].Task.OnDuplicate)
	}
}

func TestParseSpec_ContentCalendarNeedsNoProject(t *testing.T) {
	spec, err := automation.ParseSpec(`
name: calendario semanal
trigger: {schedule: "0 9 * * 1", tz: Europe/Madrid}
steps:
  - id: docs
    mcp: {server: gdrive, tool: search, args: {q: "brief de marca"}}
  - id: brief
    task:
      board: content
      title: "Calendario de contenido — semana del {{trigger.fired_at}}"
      body: "{{steps.docs.text}}"
      stage_assignments: {brief: redactor, revision: editor}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Steps[1].Task.ProjectIDs) != 0 {
		t.Error("a content task should not need a repo")
	}
	if spec.Trigger.Kind() != automation.TriggerSchedule {
		t.Errorf("kind = %s", spec.Trigger.Kind())
	}
}

func TestStore_CRUDAndWorkspaceIsolation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)

	if a.NextRunAt == nil {
		t.Fatal("a scheduled automation was stored without a next run")
	}
	if _, err := h.store.Get(ctx, a.ID, automation.InWorkspace("ws-OTHER")); err == nil {
		t.Error("another workspace read the automation")
	}
	if err := h.store.Delete(ctx, a.ID, automation.InWorkspace("ws-OTHER")); err == nil {
		t.Error("another workspace deleted the automation")
	}
	list, err := h.store.List(ctx, automation.InWorkspace("ws-OTHER"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("List for another workspace returned %d rows", len(list))
	}
	if err := h.store.Delete(ctx, a.ID, automation.InWorkspace("ws-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Get(ctx, a.ID, automation.InWorkspace("ws-1")); err == nil {
		t.Error("the automation survived its own deletion")
	}
}

func TestStore_UpdateReschedules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	before := *a.NextRunAt

	updated, err := h.store.Update(ctx, a.ID, automation.InWorkspace("ws-1"), strings.Replace(everyMinuteTicket, `"* * * * *"`, `"0 4 * * *"`, 1), true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(before) {
		t.Errorf("next run after edit = %v, want it moved to the new schedule", updated.NextRunAt)
	}
	if updated.Cron != "0 4 * * *" {
		t.Errorf("cron column = %q", updated.Cron)
	}
}

func TestStore_UnparseableRowStaysVisible(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	if _, err := h.store.DB().Exec(`UPDATE automations SET spec = $1 WHERE id = $2`, "not: [valid", a.ID); err != nil {
		t.Fatal(err)
	}
	list, err := h.store.List(ctx, automation.InWorkspace("ws-1"))
	if err != nil {
		t.Fatalf("List failed on a broken row: %v", err)
	}
	if len(list) != 1 || list[0].Parsed != nil {
		t.Fatalf("list = %+v, want one row with a nil Parsed", list)
	}
	if n, err := h.sched.Tick(ctx); err != nil || n != 0 {
		t.Errorf("Tick on a broken row: fired %d, err %v", n, err)
	}
}

func TestStore_ReconcileParksGhostRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	if _, err := h.store.StartRun(ctx, a, automation.Firing{Trigger: "schedule"}, "{}"); err != nil {
		t.Fatal(err)
	}
	if n, _ := h.store.RunningCount(ctx, a.ID); n != 1 {
		t.Fatalf("running = %d, want 1", n)
	}
	n, err := h.store.ReconcileRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reconciled %d, want 1", n)
	}
	if got, _ := h.store.RunningCount(ctx, a.ID); got != 0 {
		t.Errorf("running after reconcile = %d", got)
	}
}

func TestStore_TrimHistory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	for i := 0; i < 5; i++ {
		r, err := h.store.StartRun(ctx, a, automation.Firing{Trigger: "schedule"}, "{}")
		if err != nil {
			t.Fatal(err)
		}
		r.Status = automation.RunSuccess
		if err := h.store.FinishRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.TrimHistory(ctx, a.ID, 2); err != nil {
		t.Fatal(err)
	}
	runs, err := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Errorf("kept %d runs, want 2", len(runs))
	}
}

func TestStore_FingerprintClaimIsAtomic(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	wins := 0
	for i := 0; i < 5; i++ {
		_, claimed, err := h.store.ClaimFingerprint(ctx, "auto-1", "ws-1", "err:sig", "")
		if err != nil {
			t.Fatal(err)
		}
		if claimed {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d claims succeeded for one fingerprint, want 1", wins)
	}
}

func TestParseSpec_YAMLErrorsSpeakRecipe(t *testing.T) {
	cases := []struct{ name, spec, want, notWant string }{
		{"unknown key", "name: n\ntriger: {manual: true}\nsteps: [{id: a, task: {title: x}}]\n", "line 2", "automation.Spec"},
		{"wrong type", "name: n\ntrigger: {manual: true}\npolicy: {max_agent_runs_per_day: many}\nsteps: [{id: a, task: {title: x}}]\n", "a number", "int"},
		{"list where a value goes", "name: n\ntrigger: {manual: true}\nsteps: [{id: a, task: {title: [x]}}]\n", "a text", "!!seq"},
		{"list where a duration goes", "name: n\ntrigger: {manual: true}\npolicy: {timeout: [1]}\nsteps: [{id: a, task: {title: x}}]\n", "a duration", "a text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := automation.ParseSpec(tc.spec)
			if err == nil {
				t.Fatal("parsed, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("error = %q, want %q and no %q", err, tc.want, tc.notWant)
			}
		})
	}
}

func TestRun_LocalizeReadsTheReadersLanguage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	r, err := h.store.StartRun(ctx, a, automation.Firing{Trigger: "schedule"}, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.CancelRun(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	back, err := h.store.GetRun(ctx, r.ID, automation.InWorkspace("ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	en, es := *back, *back
	en.Localize("en")
	es.Localize("es")
	if en.Reason == "" || es.Reason == "" || en.Reason == es.Reason {
		t.Errorf("reason en=%q es=%q, want two different renderings", en.Reason, es.Reason)
	}
}

func TestCheckFilterFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger automation.Trigger
		bad     string
	}{
		{"missing field", automation.Trigger{Event: "task.created", Filter: "trigger.payload.pr.number > 10"}, "pr"},
		{"from on created", automation.Trigger{Event: "task.created", Filter: `trigger.payload.from == "backlog"`}, "from"},
		{"stage change", automation.Trigger{Event: "task.stage_changed", Filter: `trigger.payload.to == "revision"`}, ""},
		{"github", automation.Trigger{Event: "github.pull_request_review_comment",
			Filter: `trigger.payload.action == "created" && !trigger.payload.sender_is_bot && exists(trigger.payload.task_id)`}, ""},
		{"debounce", automation.Trigger{Event: "task.created", Filter: "trigger.payload.event_count > 1"}, ""},
		{"no filter", automation.Trigger{Event: "task.created"}, ""},
		{"other roots", automation.Trigger{Event: "task.created", Filter: "vars.x == 1"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := automation.CheckFilterFields(&tc.trigger)
			if tc.bad == "" {
				if err != nil {
					t.Fatalf("refused a valid filter: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "trigger.payload."+tc.bad) {
				t.Fatalf("err = %v, want it to name trigger.payload.%s", err, tc.bad)
			}
			if !strings.Contains(err.Error(), "task_id") {
				t.Errorf("err = %v, want the fields the event does carry", err)
			}
		})
	}
}
