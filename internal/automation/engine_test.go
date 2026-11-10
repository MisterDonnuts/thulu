package automation_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}

type harness struct {
	store   *automation.Store
	engine  *automation.Engine
	sched   *automation.Scheduler
	tasks   *fakeTasks
	mcp     *fakeMCP
	scripts *fakeScripts
	metrics *fakeMetrics
	keys    *fakeKeys
	creds   *fakeCreds
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, err := automation.NewStore(tempDB(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &harness{
		store:   store,
		tasks:   newFakeTasks(),
		mcp:     newFakeMCP(),
		scripts: &fakeScripts{},
		metrics: &fakeMetrics{},
		keys:    &fakeKeys{},
		creds:   &fakeCreds{secrets: map[string]string{"grafana": "s3cr3t"}},
	}
	h.engine = automation.NewEngine(store, automation.Deps{
		Tasks: h.tasks, MCP: h.mcp, Scripts: h.scripts,
		Metrics: h.metrics, Creds: h.creds, Keys: h.keys,
	})
	h.sched = automation.NewScheduler(store, h.engine)
	// Registered after testpg's cleanup so it runs before the database is dropped; in-flight runs
	// would otherwise hit a closed DB.
	t.Cleanup(h.sched.Wait)
	return h
}

func (h *harness) create(t *testing.T, spec string) *automation.Automation {
	t.Helper()
	a, err := h.store.Create(context.Background(), "ws-1", spec, true)
	if err != nil {
		t.Fatalf("Create: %v\nspec:\n%s", err, spec)
	}
	return a
}

func (h *harness) run(t *testing.T, a *automation.Automation) *automation.Run {
	t.Helper()
	run, err := h.engine.Execute(context.Background(), a, automation.Firing{Trigger: "manual"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return run
}

const errorGuardSpec = `
name: guardia de errores
trigger:
  schedule: "*/15 * * * *"
  tz: UTC
policy:
  concurrency: forbid
  max_agent_runs_per_day: 8
steps:
  - id: errs
    mcp:
      server: thulu-cloud
      tool: query_errors
      args: {env: prod, window: 15m}
  - id: gate
    condition: "size(steps.errs.json.groups) > 0"
  - id: cada
    fanout:
      over: "steps.errs.json.groups"
      max_items: 3
    steps:
      - id: ticket
        task:
          board: bugfix
          stage: refinement
          fingerprint: "err:{{item.signature}}"
          on_duplicate: comment
          title: "Errores en {{item.service}}: {{item.title}}"
          body: "{{item.count}} events in the last 15m."
          stage_assignments: {refinement: investigador}
          tags: [auto, incidente]
`

func TestEngine_ErrorGuard_OpensOneTaskPerGroup(t *testing.T) {
	h := newHarness(t)
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{
		"groups": []any{
			map[string]any{"service": "api", "signature": "sig-1", "title": "TypeError", "count": 12},
			map[string]any{"service": "web", "signature": "sig-2", "title": "500 on /pay", "count": 4},
		},
	})
	a := h.create(t, errorGuardSpec)
	run := h.run(t, a)

	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if got := h.tasks.createdCount(); got != 2 {
		t.Fatalf("created %d tasks, want 2", got)
	}
	first := h.tasks.created[0]
	if first.Title != "Errores en api: TypeError" {
		t.Errorf("title = %q", first.Title)
	}
	if first.BoardID != "bugfix" {
		t.Errorf("board = %q", first.BoardID)
	}
	if first.StageAssignments["refinement"] != "investigador" {
		t.Errorf("stage assignments = %v", first.StageAssignments)
	}
	if !strings.Contains(first.Body, "12 events") {
		t.Errorf("body = %q", first.Body)
	}
	if run.AgentRuns != 2 {
		t.Errorf("agent_runs = %d, want 2", run.AgentRuns)
	}
	if h.mcp.calls[0].APIKey == "" {
		t.Error("mcp call went out without the run key")
	}
	if len(h.keys.revoked) != 1 {
		t.Errorf("run key was not revoked: %v", h.keys.revoked)
	}
}

func TestEngine_Fingerprint_SecondFiringCommentsInsteadOfOpening(t *testing.T) {
	h := newHarness(t)
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{
		"groups": []any{map[string]any{"service": "api", "signature": "sig-1", "title": "TypeError", "count": 12}},
	})
	a := h.create(t, errorGuardSpec)

	h.run(t, a)
	h.run(t, a)
	h.run(t, a)

	if got := h.tasks.createdCount(); got != 1 {
		t.Fatalf("created %d tasks across three firings, want 1", got)
	}
	if got := h.tasks.commentCount("task-1"); got != 2 {
		t.Errorf("comments on the open task = %d, want 2", got)
	}
}

func TestEngine_Fingerprint_ReleasedWhenTheTaskCloses(t *testing.T) {
	h := newHarness(t)
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{
		"groups": []any{map[string]any{"service": "api", "signature": "sig-1", "title": "TypeError", "count": 1}},
	})
	a := h.create(t, errorGuardSpec)

	h.run(t, a)
	h.tasks.closed["task-1"] = true
	h.run(t, a)

	if got := h.tasks.createdCount(); got != 2 {
		t.Fatalf("created %d tasks, want 2 (the second after the first closed)", got)
	}
}

func TestEngine_Fingerprint_SkipPolicy(t *testing.T) {
	h := newHarness(t)
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{
		"groups": []any{map[string]any{"service": "api", "signature": "sig-1", "title": "T", "count": 1}},
	})
	spec := strings.Replace(errorGuardSpec, "on_duplicate: comment", "on_duplicate: skip", 1)
	a := h.create(t, spec)

	h.run(t, a)
	h.run(t, a)

	if got := h.tasks.createdCount(); got != 1 {
		t.Errorf("created %d tasks, want 1", got)
	}
	if got := h.tasks.commentCount("task-1"); got != 0 {
		t.Errorf("skip policy still commented %d time(s)", got)
	}
}

func TestEngine_ConditionFalse_EndsTheRunCleanly(t *testing.T) {
	h := newHarness(t)
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{"groups": []any{}})
	a := h.create(t, errorGuardSpec)
	run := h.run(t, a)

	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s, want success", run.Status)
	}
	if h.tasks.createdCount() != 0 {
		t.Errorf("a false condition still created %d task(s)", h.tasks.createdCount())
	}
	if !strings.Contains(run.Reason, "gate") {
		t.Errorf("reason = %q, want it to name the condition step", run.Reason)
	}
	full, err := h.store.GetRun(context.Background(), run.ID, automation.InWorkspace("ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Steps) != 2 {
		t.Fatalf("recorded %d steps, want 2 (the fanout never ran)", len(full.Steps))
	}
}

func TestEngine_FanoutCapsItems(t *testing.T) {
	h := newHarness(t)
	var groups []any
	for i := 0; i < 20; i++ {
		groups = append(groups, map[string]any{
			"service": "api", "signature": "sig-" + string(rune('a'+i)), "title": "T", "count": 1,
		})
	}
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{"groups": groups})
	a := h.create(t, errorGuardSpec)
	h.run(t, a)

	if got := h.tasks.createdCount(); got != 3 {
		t.Errorf("created %d tasks, want the max_items cap of 3", got)
	}
}

func TestEngine_BudgetStopsTheSpend(t *testing.T) {
	h := newHarness(t)
	var groups []any
	for i := 0; i < 5; i++ {
		groups = append(groups, map[string]any{
			"service": "api", "signature": "sig-" + string(rune('a'+i)), "title": "T", "count": 1,
		})
	}
	h.mcp.replies["thulu-cloud.query_errors"] = jsonString(map[string]any{"groups": groups})
	spec := strings.Replace(errorGuardSpec, "max_agent_runs_per_day: 8", "max_agent_runs_per_day: 2", 1)
	spec = strings.Replace(spec, "max_items: 3", "max_items: 5", 1)
	a := h.create(t, spec)
	run := h.run(t, a)

	if got := h.tasks.createdCount(); got != 2 {
		t.Errorf("created %d tasks, want 2 (the daily budget)", got)
	}
	if run.Status != automation.RunSkipped {
		t.Errorf("status = %s, want skipped — an exhausted budget is a guard rail, not a fault", run.Status)
	}
	if !strings.Contains(run.Reason, "budget") {
		t.Errorf("reason = %q, want it to mention the budget", run.Reason)
	}
	if run.Error != "" {
		t.Errorf("error = %q, want empty — nothing went wrong", run.Error)
	}

	full, err := h.store.GetRun(context.Background(), run.ID, automation.InWorkspace(a.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	full.Localize("es")
	if !strings.HasPrefix(full.Reason, "se agotó el presupuesto") {
		t.Errorf("es reason = %q, want it to open with the budget, no prefix", full.Reason)
	}
	skipped := 0
	for _, st := range full.Steps {
		if st.Status != automation.StepSkipped {
			continue
		}
		skipped++
		if !strings.Contains(st.Output, "presupuesto") || strings.Contains(st.Output, "budget exhausted") {
			t.Errorf("es output of skipped step %s = %s, want the reason in Spanish", st.StepID, st.Output)
		}
	}
	if skipped == 0 {
		t.Error("no skipped step recorded for the budget")
	}
}

func TestEngine_StepFailureAborts(t *testing.T) {
	h := newHarness(t)
	h.mcp.err = context.DeadlineExceeded
	a := h.create(t, errorGuardSpec)
	run := h.run(t, a)

	if run.Status != automation.RunFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
	if h.tasks.createdCount() != 0 {
		t.Error("the recipe carried on after a failed step")
	}
}

func TestEngine_ContinueOnError(t *testing.T) {
	h := newHarness(t)
	h.mcp.err = context.DeadlineExceeded
	spec := `
name: sigue
trigger: {manual: true}
steps:
  - id: flaky
    continue_on_error: true
    mcp: {server: x, tool: y}
  - id: ticket
    task:
      title: "ran anyway"
`
	a := h.create(t, spec)
	run := h.run(t, a)

	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if h.tasks.createdCount() != 1 {
		t.Error("continue_on_error did not let the recipe finish")
	}
}

func TestEngine_ScriptThenModel(t *testing.T) {
	h := newHarness(t)
	h.scripts.result = automation.ScriptResult{
		Stdout: `{"status":"degraded","p95":1240}`, ExitCode: 2,
	}
	spec := `
name: script then model
trigger: {schedule: "*/5 * * * *"}
steps:
  - id: probe
    script:
      run: "./scripts/healthcheck.sh"
      env: {TARGET: "https://api.example.com"}
  - id: gate
    condition: 'steps.probe.exit_code != 0 && steps.probe.json.p95 > 1000'
  - id: ticket
    task:
      title: "Degraded health: p95 {{steps.probe.json.p95}}ms"
      body: "exit={{steps.probe.exit_code}}\n\n{{steps.probe.stdout}}"
      fingerprint: "health:{{steps.probe.json.status}}"
`
	a := h.create(t, spec)
	run := h.run(t, a)

	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if h.tasks.createdCount() != 1 {
		t.Fatalf("created %d tasks, want 1", h.tasks.createdCount())
	}
	if got := h.tasks.created[0].Title; got != "Degraded health: p95 1240ms" {
		t.Errorf("title = %q", got)
	}
	if len(h.scripts.owners) != 1 || !strings.HasPrefix(h.scripts.owners[0], "auto:") {
		t.Errorf("sandbox owner = %v, want auto:<run id>", h.scripts.owners)
	}
	if len(h.scripts.released) != 1 {
		t.Errorf("sandbox was not released: %v", h.scripts.released)
	}
	if h.scripts.runs[0].Env["TARGET"] != "https://api.example.com" {
		t.Errorf("env = %v", h.scripts.runs[0].Env)
	}
}

func TestEngine_NoMCPNoKey(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: sin mcp
trigger: {manual: true}
steps:
  - id: ticket
    task: {title: "hola"}
`)
	h.run(t, a)
	if h.keys.minted != 0 {
		t.Errorf("minted %d key(s) for a recipe with no mcp step", h.keys.minted)
	}
}

func TestEngine_TimeoutIsReported(t *testing.T) {
	h := newHarness(t)
	h.mcp.delay = 2 * time.Second
	a := h.create(t, `
name: lento
trigger: {manual: true}
policy: {timeout: 1s}
steps:
  - id: slow
    mcp: {server: x, tool: y}
`)
	run := h.run(t, a)
	if run.Status != automation.RunTimeout {
		t.Fatalf("status = %s, want timeout", run.Status)
	}
	if !strings.Contains(run.Error, "timeout") {
		t.Errorf("error = %q", run.Error)
	}
}

func TestEngine_HTTPCredentialNeverLeaksIntoHistory(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: http
trigger: {manual: true}
steps:
  - id: call
    continue_on_error: true
    http:
      url: "http://127.0.0.1:1/nope"
      credential: grafana
`)
	run := h.run(t, a)
	full, err := h.store.GetRun(context.Background(), run.ID, automation.InWorkspace("ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range full.Steps {
		if strings.Contains(st.Output, "s3cr3t") || strings.Contains(st.Error, "s3cr3t") {
			t.Fatalf("the credential leaked into the run history: %s / %s", st.Output, st.Error)
		}
	}
}

func TestEngine_MentionOnExistingTask(t *testing.T) {
	h := newHarness(t)
	h.tasks.tasksBack = []automation.TaskSummary{
		{ID: "task-42", Title: "Feature X", Stage: "handoff", PRURLs: map[string]string{"p1": "https://github.com/o/r/pull/7"}, Open: true},
	}
	a := h.create(t, `
name: reviews sin contestar
trigger: {schedule: "*/30 * * * *"}
steps:
  - id: abiertas
    query: {source: tasks, with_pr: true, limit: 20}
  - id: hay
    condition: "steps.abiertas.count > 0"
  - id: cada
    fanout: {over: "steps.abiertas.json.tasks", max_items: 5}
    steps:
      - id: avisar
        mention:
          task_id: "{{item.id}}"
          agent: dev
          body: "Hay comentarios de review sin contestar en {{item.pr_urls.p1}}."
`)
	run := h.run(t, a)
	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	got := h.tasks.mentions["task-42"]
	if len(got) != 1 || !strings.Contains(got[0], "pull/7") {
		t.Fatalf("mentions = %v", got)
	}
	if !strings.HasPrefix(got[0], "dev:") {
		t.Errorf("mention did not name the agent: %q", got[0])
	}
	if len(h.tasks.writeWS) != 1 || h.tasks.writeWS[0] != a.WorkspaceID || a.WorkspaceID == "" {
		t.Errorf("mention asked to write in %v, want the automation's workspace %q", h.tasks.writeWS, a.WorkspaceID)
	}
}

func TestEngine_FiredDateIsAvailableToFingerprints(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: daily digest
trigger: {manual: true}
steps:
  - id: ticket
    task:
      title: "digest for {{trigger.fired_date}}"
      fingerprint: "digest:{{trigger.fired_date}}"
      on_duplicate: skip
`)
	h.run(t, a)
	h.run(t, a)

	if got := h.tasks.createdCount(); got != 1 {
		t.Fatalf("created %d tasks in one day, want 1", got)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	if got := h.tasks.created[0].Title; got != "digest for "+today {
		t.Errorf("title = %q, want it to carry today's date %s", got, today)
	}
}

func TestEngine_BacklogTaskDoesNotSpendTheAgentBudget(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: backlog
trigger: {manual: true}
policy: {max_agent_runs_per_day: 1}
steps:
  - id: one
    task: {title: "uno"}
  - id: two
    task: {title: "dos", stage: in_progress, stage_assignments: {in_progress: dev}}
`)
	run := h.run(t, a)
	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s (%s %s), want success: the backlog card spent the budget", run.Status, run.Reason, run.Error)
	}
	if run.AgentRuns != 1 {
		t.Errorf("agent_runs = %d, want 1 (only the task that entered a stage with an agent)", run.AgentRuns)
	}
	if h.tasks.createdCount() != 2 {
		t.Errorf("created %d tasks, want 2", h.tasks.createdCount())
	}
}

func TestEngine_SpentBudgetStillOpensBacklogTasks(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: backlog
trigger: {manual: true}
policy: {max_agent_runs_per_day: 1}
steps:
  - id: spend
    task: {title: "con agente", stage: in_progress, stage_assignments: {in_progress: dev}}
`)
	if run := h.run(t, a); run.Status != automation.RunSuccess || run.AgentRuns != 1 {
		t.Fatalf("first run = %s agent_runs=%d (%s), want success spending the budget", run.Status, run.AgentRuns, run.Reason)
	}
	b, err := h.store.Update(context.Background(), a.ID, automation.InWorkspace("ws-1"), `
name: backlog
trigger: {manual: true}
policy: {max_agent_runs_per_day: 1}
steps:
  - id: card
    task: {title: "sin etapa"}
  - id: note
    mention: {task_id: task-9, body: "nota"}
  - id: woken
    task: {title: "otra con agente", stage: in_progress, stage_assignments: {in_progress: dev}}
`, true)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	run := h.run(t, b)
	if h.tasks.createdCount() != 2 {
		t.Errorf("created %d tasks, want 2: the backlog card goes through, the second agent does not", h.tasks.createdCount())
	}
	if h.tasks.commentCount("task-9") != 1 {
		t.Errorf("task-9 comments = %d, want 1: a mention with no agent to wake costs nothing", h.tasks.commentCount("task-9"))
	}
	if run.Status != automation.RunSkipped || !strings.Contains(run.Reason, "budget") {
		t.Errorf("status = %s (%s), want skipped on the budget at the step that would wake an agent", run.Status, run.Reason)
	}
}

func TestEngine_MentionWithoutAgentWakesTheStageAgent(t *testing.T) {
	h := newHarness(t)
	h.tasks.stageAgents["task-7"] = "frontend"
	h.tasks.stageAgents["task-9"] = "builder"
	h.tasks.quietAgents["builder"] = true
	a := h.create(t, `
name: avisos
trigger: {manual: true}
steps:
  - id: con_agente
    mention: {task_id: task-7, body: "hay un comentario nuevo"}
  - id: sin_agente
    mention: {task_id: task-8, body: "nota"}
  - id: agente_sordo
    mention: {task_id: task-9, body: "otra nota"}
`)
	run := h.run(t, a)
	if run.Status != automation.RunSuccess {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if got := h.tasks.mentions["task-7"]; len(got) != 1 || !strings.HasPrefix(got[0], "frontend:") {
		t.Errorf("mentions on task-7 = %v, want one for frontend", got)
	}
	if h.tasks.commentCount("task-8") != 1 || len(h.tasks.mentions["task-8"]) != 0 {
		t.Errorf("task-8 has no stage agent: want one plain comment, got comments=%d mentions=%v",
			h.tasks.commentCount("task-8"), h.tasks.mentions["task-8"])
	}
	if h.tasks.commentCount("task-9") != 1 || len(h.tasks.mentions["task-9"]) != 0 {
		t.Errorf("task-9's agent does not answer mentions: want one plain comment, got comments=%d mentions=%v",
			h.tasks.commentCount("task-9"), h.tasks.mentions["task-9"])
	}
	if run.AgentRuns != 1 {
		t.Errorf("agent_runs = %d, want 1", run.AgentRuns)
	}
	full, err := h.store.GetRun(context.Background(), run.ID, automation.InWorkspace(a.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range full.Steps {
		if !strings.Contains(st.Output, `"title":"title of task-`) {
			t.Errorf("step %s output %s carries no task title", st.StepID, st.Output)
		}
		skipped := strings.Contains(st.Output, `"skipped_agent":"builder"`)
		if skipped != (st.StepID == "agente_sordo") {
			t.Errorf("step %s output %s: skipped_agent present = %v", st.StepID, st.Output, skipped)
		}
	}
	for _, ws := range h.tasks.readWS {
		if ws != a.WorkspaceID || ws == "" {
			t.Errorf("stage agent looked up in %q, want the automation's workspace %q", ws, a.WorkspaceID)
		}
	}
}

func TestEngine_TaskQueryOpenFilter(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: abiertas
trigger: {manual: true}
steps:
  - id: q
    query: {source: tasks, tag: deps, open: true}
`)
	h.run(t, a)
	if len(h.tasks.queries) != 1 || !h.tasks.queries[0].Open {
		t.Fatalf("queries = %+v, want one with Open set", h.tasks.queries)
	}
}

func TestEngine_TaskCarriesItsProvenance(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, `
name: cadena
trigger: {event: task.created}
steps:
  - id: t
    task: {title: "hija"}
`)
	_, err := h.engine.Execute(context.Background(), a, automation.Firing{
		Trigger: automation.TriggerEvent,
		Payload: map[string]any{automation.PayloadChainDepth: float64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.tasks.created) != 1 {
		t.Fatalf("created %d tasks", len(h.tasks.created))
	}
	got := h.tasks.created[0]
	if got.AutomationID != a.ID || got.ChainDepth != 2 {
		t.Errorf("provenance = (%q, %d), want (%q, 2)", got.AutomationID, got.ChainDepth, a.ID)
	}
}
