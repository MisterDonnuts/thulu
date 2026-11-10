package automation_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/automation"
)

const everyMinuteTicket = `
name: cada minuto
trigger: {schedule: "* * * * *"}
policy: {concurrency: forbid}
steps:
  - id: ticket
    task: {title: "tick"}
`

func TestScheduler_TickFiresAndAdvances(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	forceDue(t, h, a.ID, time.Now().Add(-5*time.Second))

	n, err := h.sched.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("fired %d, want 1", n)
	}
	h.sched.Wait()

	if h.tasks.createdCount() != 1 {
		t.Errorf("created %d tasks", h.tasks.createdCount())
	}
	again, err := h.sched.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("the same firing was served twice")
	}
	reloaded, err := h.store.Get(ctx, a.ID, automation.InWorkspace("ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NextRunAt == nil || !reloaded.NextRunAt.After(time.Now()) {
		t.Errorf("next_run_at = %v, want a future instant", reloaded.NextRunAt)
	}
}

func TestScheduler_ConcurrentTicksClaimOnce(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, everyMinuteTicket)
	forceDue(t, h, a.ID, time.Now().Add(-5*time.Second))

	other := automation.NewScheduler(h.store, h.engine)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for _, s := range []*automation.Scheduler{h.sched, other} {
		wg.Add(1)
		go func(sc *automation.Scheduler) {
			defer wg.Done()
			n, err := sc.Tick(context.Background())
			if err != nil {
				t.Errorf("Tick: %v", err)
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	h.sched.Wait()
	other.Wait()

	if total != 1 {
		t.Fatalf("two schedulers fired %d times for one due firing", total)
	}
}

func TestScheduler_ConcurrencyForbidSkipsAndSaysSo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.mcp.delay = 1500 * time.Millisecond
	a := h.create(t, `
name: lenta
trigger: {schedule: "* * * * *"}
policy: {concurrency: forbid, timeout: 30s}
steps:
  - id: slow
    mcp: {server: x, tool: y}
`)
	forceDue(t, h, a.ID, time.Now())
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		n, _ := h.store.RunningCount(ctx, a.ID)
		return n == 1
	}, "first run to start")
	forceDue(t, h, a.ID, time.Now())
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	runs, err := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 10)
	if err != nil {
		t.Fatal(err)
	}
	var skipped *automation.Run
	for i := range runs {
		if runs[i].Status == automation.RunSkipped {
			skipped = &runs[i]
		}
	}
	if skipped == nil {
		t.Fatalf("no skipped run recorded; runs = %+v", runs)
	}
	if !strings.Contains(skipped.Reason, "still going") {
		t.Errorf("skip reason = %q", skipped.Reason)
	}
	h.sched.Wait()
}

func TestScheduler_ConcurrencyReplaceCancelsThePreviousRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.mcp.delay = 3 * time.Second
	a := h.create(t, `
name: reemplaza
trigger: {schedule: "* * * * *"}
policy: {concurrency: replace, timeout: 30s}
steps:
  - id: slow
    mcp: {server: x, tool: y}
`)
	forceDue(t, h, a.ID, time.Now())
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		n, _ := h.store.RunningCount(ctx, a.ID)
		return n == 1
	}, "first run to start")

	forceDue(t, h, a.ID, time.Now())
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()

	runs, err := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 10)
	if err != nil {
		t.Fatal(err)
	}
	canceled := 0
	for _, r := range runs {
		if r.Status == automation.RunCanceled {
			canceled++
		}
	}
	if canceled != 1 {
		t.Fatalf("canceled %d runs, want 1; runs = %+v", canceled, runs)
	}
}

func TestScheduler_StaleFiringIsSkipped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)
	forceDue(t, h, a.ID, time.Now().Add(-6*time.Hour))

	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()

	if h.tasks.createdCount() != 0 {
		t.Error("a six-hour-old firing still ran")
	}
	runs, _ := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 5)
	if len(runs) != 1 || runs[0].Status != automation.RunSkipped {
		t.Fatalf("runs = %+v, want one skipped", runs)
	}
	if !strings.Contains(runs[0].Reason, "starting deadline") {
		t.Errorf("reason = %q", runs[0].Reason)
	}
}

func TestScheduler_StartingDeadlineAllowsCatchUp(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, `
name: con catch-up
trigger: {schedule: "* * * * *"}
policy: {starting_deadline: 12h}
steps:
  - id: ticket
    task: {title: "tick"}
`)
	forceDue(t, h, a.ID, time.Now().Add(-6*time.Hour))
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if h.tasks.createdCount() != 1 {
		t.Error("starting_deadline: 12h did not allow the catch-up run")
	}
}

func TestScheduler_SuspendAndResume(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)

	off, err := h.store.SetEnabled(ctx, a.ID, automation.InWorkspace("ws-1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if off.NextRunAt != nil {
		t.Errorf("suspended row still advertises a next run at %v", off.NextRunAt)
	}
	forceDue(t, h, a.ID, time.Now().Add(-time.Second))
	if n, _ := h.sched.Tick(ctx); n != 0 {
		t.Error("a suspended automation fired")
	}

	resumed, err := h.store.SetEnabled(ctx, a.ID, automation.InWorkspace("ws-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.NextRunAt == nil || !resumed.NextRunAt.After(time.Now()) {
		t.Errorf("resuming did not reschedule from now: %v", resumed.NextRunAt)
	}
}

const reviewSpec = `
name: reviews
trigger:
  event: github.pull_request_review_comment
  filter: 'trigger.payload.action == "created" && !trigger.payload.sender_is_bot'
policy: {debounce: 1h}
steps:
  - id: avisar
    mention:
      task_id: "{{trigger.payload.task_id}}"
      agent: dev
      body: "Nuevo comentario de review: {{trigger.payload.comment}}"
`

func TestScheduler_EventTriggerFiresOnMatch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, reviewSpec)

	publish(t, h, automation.Event{
		WorkspaceID: "ws-1", Kind: automation.EventPRReviewComment,
		DedupeKey: "pr-7",
		Payload: map[string]any{
			"action": "created", "sender_is_bot": false,
			"task_id": "task-9", "comment": "esto rompe en prod",
		},
	})
	n, err := deliverEvents(ctx, t, h)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 1 {
		t.Fatalf("fired %d, want 1", n)
	}
	if got := h.tasks.mentions["task-9"]; len(got) != 1 {
		t.Fatalf("mentions = %v", got)
	}
}

func TestScheduler_EventFilterExcludes(t *testing.T) {
	h := newHarness(t)
	h.create(t, reviewSpec)
	publish(t, h, automation.Event{
		WorkspaceID: "ws-1", Kind: automation.EventPRReviewComment,
		Payload: map[string]any{"action": "created", "sender_is_bot": true, "task_id": "task-9"},
	})
	n, err := h.sched.ProcessEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 0 {
		t.Errorf("a bot's own comment woke the agent")
	}
}

func TestScheduler_EventDebounceCollapsesABurst(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, reviewSpec)

	for i := 0; i < 5; i++ {
		publish(t, h, automation.Event{
			WorkspaceID: "ws-1", Kind: automation.EventPRReviewComment,
			DedupeKey: "pr-7",
			Payload: map[string]any{
				"action": "created", "sender_is_bot": false, "task_id": "task-9",
				"comment": "comentario " + string(rune('a'+i)),
			},
		})
		if n, err := h.sched.ProcessEvents(ctx); err != nil || n != 0 {
			t.Fatalf("ProcessEvents = %d, %v: an event inside the window must wait, not fire", n, err)
		}
	}
	if n, err := h.sched.FlushDebounced(ctx); err != nil || n != 0 {
		t.Fatalf("FlushDebounced before the window closed = %d, %v", n, err)
	}
	closeDebounceWindows(t, h)
	n, err := h.sched.FlushDebounced(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 1 {
		t.Fatalf("closing the window fired %d runs, want 1", n)
	}
	got := h.tasks.mentions["task-9"]
	if len(got) != 1 {
		t.Fatalf("a burst of 5 comments produced %d wake-ups, want 1", len(got))
	}
	if !strings.Contains(got[0], "comentario e") {
		t.Errorf("mention = %q, want the newest comment", got[0])
	}

	runs, _ := h.store.ListRuns(ctx, listFirst(t, h).ID, automation.InWorkspace("ws-1"), 10)
	if len(runs) != 1 {
		t.Fatalf("history has %d runs, want 1: nothing is skipped any more", len(runs))
	}
	if runs[0].Status != automation.RunSuccess || runs[0].Reason != "" {
		t.Errorf("run = %s %q, want success with no reason", runs[0].Status, runs[0].Reason)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(runs[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if events, _ := payload["events"].([]any); len(events) != 5 || payload["event_count"] != float64(5) {
		t.Errorf("payload carries %d events (count %v), want all 5", len(events), payload["event_count"])
	}

	if n, _ := h.sched.FlushDebounced(ctx); n != 0 {
		t.Errorf("the same window fired twice")
	}
}

func TestScheduler_DebounceWaitsForTheRunningRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, reviewSpec)
	comment := func(text string) {
		publish(t, h, automation.Event{
			WorkspaceID: "ws-1", Kind: automation.EventPRReviewComment, DedupeKey: "pr-7",
			Payload: map[string]any{"action": "created", "sender_is_bot": false, "task_id": "task-9", "comment": text},
		})
		if _, err := h.sched.ProcessEvents(ctx); err != nil {
			t.Fatal(err)
		}
	}
	busy, err := h.store.StartRun(ctx, a, automation.Firing{Trigger: automation.TriggerEvent}, "")
	if err != nil {
		t.Fatal(err)
	}
	comment("uno")
	comment("dos")
	closeDebounceWindows(t, h)
	if n, err := h.sched.FlushDebounced(ctx); err != nil || n != 0 {
		t.Fatalf("FlushDebounced with a run going = %d, %v; want 0", n, err)
	}
	comment("tres")

	busy.Status = automation.RunSuccess
	if err := h.store.FinishRun(ctx, busy); err != nil {
		t.Fatal(err)
	}
	closeDebounceWindows(t, h)
	n, err := h.sched.FlushDebounced(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 1 {
		t.Fatalf("after the run ended the window fired %d runs, want 1", n)
	}
	runs, _ := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 10)
	var fired *automation.Run
	for i := range runs {
		switch {
		case runs[i].Status == automation.RunSkipped:
			t.Errorf("skipped run %q: the window's events were dropped", runs[i].Reason)
		case runs[i].ID != busy.ID:
			fired = &runs[i]
		}
	}
	if fired == nil {
		t.Fatalf("runs = %+v, want the debounced one", runs)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fired.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	events, _ := payload["events"].([]any)
	if len(events) != 3 || payload["event_count"] != float64(3) {
		t.Fatalf("payload carries %d events (count %v), want 3", len(events), payload["event_count"])
	}
	if first, _ := events[0].(map[string]any); first["comment"] != "uno" {
		t.Errorf("events[0] = %v, want the oldest first", events[0])
	}
	if payload["comment"] != "tres" {
		t.Errorf("payload.comment = %v, want the newest event on top", payload["comment"])
	}
}

func TestScheduler_BrokenFilterIsRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, `
name: filtro roto
trigger:
  event: task.created
  filter: 'lenght(trigger.payload.tags) > 0'
steps:
  - id: t
    task: {title: "x"}
`)
	publish(t, h, automation.Event{WorkspaceID: "ws-1", Kind: automation.EventTaskCreated,
		Payload: map[string]any{"task_id": "t-1", "tags": []any{"a"}}})
	if n, err := h.sched.ProcessEvents(ctx); err != nil || n != 0 {
		t.Fatalf("ProcessEvents = %d, %v; want 0 fired", n, err)
	}
	runs, _ := h.store.ListRuns(ctx, listFirst(t, h).ID, automation.InWorkspace("ws-1"), 10)
	if len(runs) != 1 || runs[0].Status != automation.RunSkipped {
		t.Fatalf("runs = %+v, want one skipped run", runs)
	}
	if !strings.Contains(runs[0].Reason, "lenght") {
		t.Errorf("reason = %q, want it to name the broken function", runs[0].Reason)
	}
	if h.tasks.createdCount() != 0 {
		t.Error("a broken filter ran the recipe")
	}
}

func TestScheduler_RecipeChainsAreBounded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, `
name: eco
trigger: {event: task.created}
steps:
  - id: t
    task: {title: "eco"}
`)
	publish(t, h, automation.Event{WorkspaceID: "ws-1", Kind: automation.EventTaskCreated,
		Payload: map[string]any{"task_id": "t-1", automation.PayloadAutomationID: a.ID, automation.PayloadChainDepth: 1}})
	if n, _ := h.sched.ProcessEvents(ctx); n != 0 {
		t.Fatalf("the recipe fired on its own task")
	}
	publish(t, h, automation.Event{WorkspaceID: "ws-1", Kind: automation.EventTaskCreated,
		Payload: map[string]any{"task_id": "t-2", automation.PayloadAutomationID: "otra", automation.PayloadChainDepth: automation.MaxChainDepth}})
	if n, _ := h.sched.ProcessEvents(ctx); n != 0 {
		t.Fatalf("an event at the end of the chain fired a run")
	}
	h.sched.Wait()
	runs, _ := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 10)
	if len(runs) != 1 || runs[0].Status != automation.RunSkipped {
		t.Fatalf("runs = %+v, want only the chain skip (the own-task event leaves nothing)", runs)
	}
	publish(t, h, automation.Event{WorkspaceID: "ws-1", Kind: automation.EventTaskCreated,
		Payload: map[string]any{"task_id": "t-3", automation.PayloadAutomationID: "otra", automation.PayloadChainDepth: 1}})
	if n, _ := h.sched.ProcessEvents(ctx); n != 1 {
		t.Fatalf("an event one recipe deep fired %d runs, want 1", n)
	}
}

func TestScheduler_StageMovesOfARecipesTaskAreBounded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, `
name: revisar
trigger:
  event: task.stage_changed
  filter: 'trigger.payload.to == "review"'
steps:
  - id: t
    task: {title: "revisar {{trigger.payload.title}}", stage: in_progress, stage_assignments: {in_progress: dev}}
`)
	moved := func(taskID string) int {
		t.Helper()
		publish(t, h, automation.Event{WorkspaceID: "ws-1", Kind: automation.EventTaskStageChanged,
			Payload: map[string]any{"task_id": taskID, "title": taskID, "from": "in_progress", "to": "review"}})
		n, err := h.sched.ProcessEvents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		h.sched.Wait()
		return n
	}
	if n := moved("t-1"); n != 1 || h.tasks.createdCount() != 1 {
		t.Fatalf("fired %d, created %d; want 1 and 1", n, h.tasks.createdCount())
	}
	if n := moved(h.tasks.ids[0]); n != 0 || h.tasks.createdCount() != 1 {
		t.Fatalf("the recipe fired on its own task's move: fired %d, created %d", n, h.tasks.createdCount())
	}
	if err := h.store.RecordTaskOrigin(ctx, "t-deep", "otra", "ws-1", automation.MaxChainDepth); err != nil {
		t.Fatal(err)
	}
	if n := moved("t-deep"); n != 0 {
		t.Fatalf("an event at the end of the chain fired %d runs", n)
	}
	runs, _ := h.store.ListRuns(ctx, a.ID, automation.InWorkspace("ws-1"), 10)
	skips := 0
	for _, r := range runs {
		if r.Status == automation.RunSkipped {
			skips++
		}
	}
	if len(runs) != 2 || skips != 1 {
		t.Fatalf("runs = %+v, want the first run and one chain skip", runs)
	}
	other := h.create(t, `
name: otra etapa
trigger:
  event: task.stage_changed
  filter: 'trigger.payload.to == "done"'
steps:
  - id: t
    task: {title: "x"}
`)
	if n := moved("t-deep"); n != 0 {
		t.Fatalf("an event at the end of the chain fired %d runs", n)
	}
	if runs, _ := h.store.ListRuns(ctx, other.ID, automation.InWorkspace("ws-1"), 10); len(runs) != 0 {
		t.Errorf("a recipe the filter excluded recorded %+v", runs)
	}
	if err := h.store.RecordTaskOrigin(ctx, "t-2", "otra", "ws-1", 1); err != nil {
		t.Fatal(err)
	}
	if n := moved("t-2"); n != 1 {
		t.Fatalf("an event one recipe deep fired %d runs, want 1", n)
	}
	if got := h.tasks.created[len(h.tasks.created)-1].ChainDepth; got != 2 {
		t.Errorf("the task it opened has chain depth %d, want 2", got)
	}
}

func deliverEvents(ctx context.Context, t *testing.T, h *harness) (int, error) {
	t.Helper()
	n, err := h.sched.ProcessEvents(ctx)
	if err != nil {
		return n, err
	}
	closeDebounceWindows(t, h)
	m, err := h.sched.FlushDebounced(ctx)
	return n + m, err
}

func closeDebounceWindows(t *testing.T, h *harness) {
	t.Helper()
	if _, err := h.store.DB().Exec(`UPDATE automation_debounce SET fire_at = $1`, time.Now().Add(-time.Second).UTC()); err != nil {
		t.Fatalf("close debounce windows: %v", err)
	}
}

func TestScheduler_EventsDoNotCrossWorkspaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.store.Create(ctx, "ws-OTHER", reviewSpec, true); err != nil {
		t.Fatal(err)
	}
	publish(t, h, automation.Event{
		WorkspaceID: "ws-1", Kind: automation.EventPRReviewComment,
		Payload: map[string]any{"action": "created", "sender_is_bot": false, "task_id": "task-9"},
	})
	n, err := h.sched.ProcessEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 0 {
		t.Fatalf("an event from ws-1 fired %d automation(s) owned by ws-OTHER", n)
	}
}

const metricSpec = `
name: error rate
trigger:
  metric:
    environment: env-1
    metric: error_rate
    window: 15m
    agg: avg
    op: ">"
    threshold: 5
    every: 5m
steps:
  - id: ticket
    task:
      title: "error_rate en {{trigger.payload.value}}"
      fingerprint: "er:{{trigger.payload.environment}}"
`

func TestScheduler_MetricTriggerHasHysteresis(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, metricSpec)

	h.metrics.set(2, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 0)

	h.metrics.set(9, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 1)

	h.metrics.set(11, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 0)

	h.metrics.set(1, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 0)

	h.metrics.set(8, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 1)

	if got := h.tasks.createdCount(); got != 1 {
		t.Fatalf("created %d tasks, want 1 — the open ticket owns the incident", got)
	}
	if got := h.tasks.commentCount("task-1"); got != 1 {
		t.Fatalf("second crossing left %d comments on the open ticket, want 1", got)
	}
}

func TestScheduler_MetricNoDataDoesNotRearm(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, metricSpec)

	h.metrics.set(9, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 1)

	h.metrics.set(0, false)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 0)

	h.metrics.set(9, true)
	forceDue(t, h, a.ID, time.Now())
	mustTick(t, h, 0)

	if got := h.tasks.createdCount(); got != 1 {
		t.Errorf("created %d tasks, want 1 — a collection gap re-armed the trigger", got)
	}
}

func forceDue(t *testing.T, h *harness, id string, at time.Time) {
	t.Helper()
	if _, err := h.store.DB().Exec(`UPDATE automations SET next_run_at = $1 WHERE id = $2`, at.UTC(), id); err != nil {
		t.Fatalf("force due: %v", err)
	}
}

func mustTick(t *testing.T, h *harness, want int) {
	t.Helper()
	n, err := h.sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	h.sched.Wait()
	if n != want {
		t.Fatalf("Tick fired %d, want %d", n, want)
	}
}

func publish(t *testing.T, h *harness, e automation.Event) {
	t.Helper()
	if err := h.sched.Publish(context.Background(), e); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func listFirst(t *testing.T, h *harness) automation.Automation {
	t.Helper()
	list, err := h.store.List(context.Background(), automation.InWorkspace("ws-1"))
	if err != nil || len(list) == 0 {
		t.Fatalf("List: %v (%d rows)", err, len(list))
	}
	return list[0]
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestScheduler_EventNeverCrossesWorkspaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, reviewSpec)
	if _, err := h.store.Create(ctx, "ws-2", reviewSpec, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Create(ctx, "", reviewSpec, true); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"action": "created", "sender_is_bot": false, "task_id": "task-9"}

	publish(t, h, automation.Event{Kind: automation.EventPRReviewComment, DedupeKey: "pr-1", Payload: payload})
	n, err := deliverEvents(ctx, t, h)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 0 {
		t.Fatalf("an event with no workspace fired %d automations, want 0", n)
	}

	publish(t, h, automation.Event{WorkspaceID: "ws-2", Kind: automation.EventPRReviewComment, DedupeKey: "pr-2", Payload: payload})
	n, err = deliverEvents(ctx, t, h)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 1 {
		t.Fatalf("ws-2's event fired %d automations, want only ws-2's own", n)
	}
}

func TestStore_EmptyScopeReachesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, everyMinuteTicket)

	list, err := h.store.List(ctx, automation.InWorkspace(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("an empty workspace listed %d automations, want 0", len(list))
	}
	if _, err := h.store.Get(ctx, a.ID, automation.InWorkspace("")); err == nil {
		t.Error("an empty workspace read another tenant's automation")
	}
	if _, err := h.store.Update(ctx, a.ID, automation.InWorkspace(""), everyMinuteTicket, false); err == nil {
		t.Error("an empty workspace edited another tenant's automation")
	}
	if err := h.store.Delete(ctx, a.ID, automation.InWorkspace("")); err == nil {
		t.Error("an empty workspace deleted another tenant's automation")
	}
	run := h.run(t, a)
	if _, err := h.store.GetRun(ctx, run.ID, automation.InWorkspace("")); err == nil {
		t.Error("an empty workspace read another tenant's run")
	}
	if runs, _ := h.store.ListRuns(ctx, a.ID, automation.InWorkspace(""), 10); len(runs) != 0 {
		t.Errorf("an empty workspace listed %d runs, want 0", len(runs))
	}

	all, err := h.store.List(ctx, automation.AllWorkspaces)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("AllWorkspaces listed %d automations, want 1", len(all))
	}
	if _, err := h.store.GetRun(ctx, run.ID, automation.AllWorkspaces); err != nil {
		t.Errorf("AllWorkspaces could not read the run: %v", err)
	}
}

func TestScheduler_RunWithPayloadIsATestRun(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: probe
trigger:
  event: task.created
steps:
  - id: gate
    condition: 'trigger.kind == "event" && trigger.payload.title == "probe"'
  - id: ticket
    task: {title: "seen", fingerprint: probe}
`)
	run, err := h.sched.RunNow(context.Background(), a, map[string]any{"title": "probe"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Trigger != automation.TriggerTest {
		t.Errorf("trigger = %q, want %q", run.Trigger, automation.TriggerTest)
	}
	if len(h.tasks.created) != 1 {
		t.Errorf("created %d tasks, want 1: the recipe should see kind event and the payload", len(h.tasks.created))
	}

	plain, err := h.sched.RunNow(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Trigger != automation.TriggerManual {
		t.Errorf("run now without a payload = %q, want %q", plain.Trigger, automation.TriggerManual)
	}
}
