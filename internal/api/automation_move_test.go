package api

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// The automation move step goes through the same gate as a person's move and never jumps an
// approval; a held move is an answer, a task it must not touch is an error.
func TestAutomationMoveTask(t *testing.T) {
	e := newBatchEnv(t)
	ctx := t.Context()
	ops := e.srv.AutomationDeps().Tasks
	move := func(taskID, stage, board, note string) (automation.MoveResult, error) {
		return ops.MoveTask(ctx, automation.MoveRequest{WorkspaceID: e.ws, TaskID: taskID, Stage: stage, BoardID: board,
			Note: note, AutomationID: "auto-1", ChainDepth: 1})
	}

	ready := e.task(t, "ready", rbRelease)
	res, err := move(ready.ID, string(rbDone), rbBoard, "production deploy 77 succeeded")
	if err != nil || !res.Moved || res.From != string(rbRelease) || res.To != string(rbDone) || res.Title != "ready" {
		t.Fatalf("plain move = %+v %v", res, err)
	}
	if e.stage(t, ready.ID) != rbDone {
		t.Fatal("the task did not move")
	}
	if n := (mergeEnv{srv: e.srv}).notes(t, ready.ID, "api.automations.comments.moved"); n != 1 {
		t.Errorf("move notes = %d, want 1", n)
	}
	evs, err := e.srv.taskStore.ListEvents(ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		p := string(ev.Payload)
		if ev.Kind == task.EventStageEntered && strings.Contains(p, `"via":"automation"`) && strings.Contains(p, `"automation_id":"auto-1"`) {
			found = true
		}
	}
	if !found {
		t.Error("no stage_entered event via automation naming the automation")
	}
	if res, err := move(ready.ID, string(rbDone), "", ""); err != nil || res.Moved {
		t.Errorf("already there = %+v %v, want a no-op", res, err)
	}

	held := e.task(t, "held", rbRelease)
	if err := e.srv.taskStore.AddApproval(ctx, &task.Approval{TaskID: held.ID, Key: "deploy_prod", Label: "Deploy to production",
		Stage: rbRelease, Source: task.ApprovalFromAgent, Status: task.ApprovalPending, CreatedBy: "agent"}); err != nil {
		t.Fatal(err)
	}
	if res, err := move(held.ID, string(rbDone), "", ""); err != nil || res.Moved || !res.Gated || res.Approvals[0] != "Deploy to production" {
		t.Errorf("held move = %+v %v, want gated by the deploy approval", res, err)
	}

	early := e.task(t, "early", rbBacklog)
	if res, err := move(early.ID, string(rbRelease), "", ""); err != nil || !res.Gated || res.Approvals[0] != "Merge" {
		t.Errorf("jump over review = %+v %v, want gated by Merge", res, err)
	}
	if e.stage(t, early.ID) != rbBacklog || e.stage(t, held.ID) != rbRelease {
		t.Error("a held task moved")
	}

	if _, err := move(early.ID, string(rbReview), "other", ""); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("other board = %v, want conflict", err)
	}
	running := e.task(t, "running", rbRelease)
	st := task.StatusRunning
	if _, err := e.srv.taskStore.Update(ctx, running.ID, task.Patch{Status: &st}); err != nil {
		t.Fatal(err)
	}
	if _, err := move(running.ID, string(rbDone), "", ""); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("running = %v, want conflict", err)
	}
	foreign := &task.Task{Title: "theirs", WorkspaceID: "ws-other", BoardID: rbBoard, Stage: rbRelease}
	if err := e.srv.taskStore.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := move(foreign.ID, string(rbDone), "", ""); apperr.KindOf(err) != apperr.KindNotFound {
		t.Errorf("another workspace's task = %v, want not found", err)
	}
}

func TestAutomationMoveStepRefsAreChecked(t *testing.T) {
	e := newBatchEnv(t)
	for name, c := range map[string]struct {
		step automation.MoveStep
		ok   bool
	}{
		"board and stage":                  {automation.MoveStep{TaskID: "{{ trigger.payload.task_id }}", Stage: string(rbDone), Board: rbBoard}, true},
		"no board":                         {automation.MoveStep{TaskID: "t", Stage: string(rbRelease)}, true},
		"no board, no such stage anywhere": {automation.MoveStep{TaskID: "t", Stage: "anything"}, false},
		"unknown stage":                    {automation.MoveStep{TaskID: "t", Stage: "shipped", Board: rbBoard}, false},
		"unknown board":                    {automation.MoveStep{TaskID: "t", Stage: string(rbDone), Board: "nope"}, false},
		"templated stage":                  {automation.MoveStep{TaskID: "t", Stage: "{{ vars.stage }}"}, false},
	} {
		step := c.step
		err := e.srv.checkStepRefs(t.Context(), e.ws, []automation.Step{{ID: "m", Move: &step}})
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", name, err, c.ok)
		}
	}
}

// The task.stage_changed a move step causes names the automation and its chain depth: without them
// two recipes that move a task back and forth would fire each other forever, since the chain guard
// reads the depth from the event.
func TestAutomationMoveStampsTheChainOnStageChanged(t *testing.T) {
	e := newBatchEnv(t)
	autoStore, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(autoStore, automation.Deps{})
	e.srv.SetAutomations(autoStore, engine, automation.NewScheduler(autoStore, engine))

	tk := e.task(t, "ready", rbRelease)
	res, err := e.srv.AutomationDeps().Tasks.MoveTask(t.Context(), automation.MoveRequest{WorkspaceID: e.ws, TaskID: tk.ID,
		Stage: string(rbDone), AutomationID: "auto-7", ChainDepth: 3})
	if err != nil || !res.Moved {
		t.Fatalf("move = %+v %v", res, err)
	}
	var found bool
	for _, ev := range queuedEvents(t, autoStore) {
		if ev.Kind != automation.EventTaskStageChanged || ev.Payload["task_id"] != tk.ID {
			continue
		}
		found = true
		if ev.Payload[automation.PayloadAutomationID] != "auto-7" || automation.ChainDepthOf(ev.Payload) != 3 {
			t.Errorf("stage_changed payload = %v, want automation auto-7 at depth 3", ev.Payload)
		}
	}
	if !found {
		t.Fatal("the move published no task.stage_changed")
	}
}

// A move into a stage whose agent the task has assigned launches that agent, so it asks the
// engine's budget first and reports the agent; a refused budget leaves the task where it was.
func TestAutomationMoveAsksTheBudgetBeforeWakingAnAgent(t *testing.T) {
	e := newBatchEnv(t)
	ops := e.srv.AutomationDeps().Tasks
	tk := &task.Task{Title: "with agent", WorkspaceID: e.ws, BoardID: rbBoard, Stage: rbBacklog,
		StageAssignments: map[task.Stage]string{rbReview: "reviewer"}}
	if err := e.srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	var asked []string
	spent := apperr.Precondition("automation.errors.budget_spent", "spent", 5, "limit", 5)
	_, err := ops.MoveTask(t.Context(), automation.MoveRequest{WorkspaceID: e.ws, TaskID: tk.ID, Stage: string(rbReview),
		BeforeAgent: func(agent string) error { asked = append(asked, agent); return spent }})
	if !errors.Is(err, spent) || !slices.Equal(asked, []string{"reviewer"}) {
		t.Fatalf("budget refusal = %v, asked %v", err, asked)
	}
	if e.stage(t, tk.ID) != rbBacklog {
		t.Fatal("a move the budget refused went through")
	}

	plain := e.task(t, "no agent", rbRelease)
	res, err := ops.MoveTask(t.Context(), automation.MoveRequest{WorkspaceID: e.ws, TaskID: plain.ID, Stage: string(rbDone),
		BeforeAgent: func(string) error { t.Error("asked the budget for a move that wakes nobody"); return nil }})
	if err != nil || !res.Moved || res.Agent != "" {
		t.Fatalf("move without agent = %+v %v", res, err)
	}
}
