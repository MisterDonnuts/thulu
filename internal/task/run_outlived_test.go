package task

import (
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Knows the spec agent (audited in spec) and the code agent, so a task can go from one to the
// other while the first still works.
type twoAgentLauncher struct {
	*recordingLauncher
	agents map[string]*types.AgentType
}

func (l *twoAgentLauncher) FindAgentType(_, name string) *types.AgentType { return l.agents[name] }

func outlivedSetup(t *testing.T, audited bool) (*Dispatcher, *Store, *twoAgentLauncher, *Task) {
	t.Helper()
	d, store := newKickDispatcher(t, 0)
	refiner := &types.AgentType{Name: "refiner"}
	if audited {
		refiner.Auditor = &types.AuditorSpec{Stages: []string{"spec"}}
	}
	l := &twoAgentLauncher{recordingLauncher: newRecordingLauncher(), agents: map[string]*types.AgentType{
		"refiner": refiner,
		"tester":  {Name: "tester"},
	}}
	d.launcher = l
	tk := &Task{WorkspaceID: testWS, Title: "ABC-123", Stage: "spec",
		StageAssignments: map[Stage]string{"spec": "refiner", "code": "tester"}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d, store, l, full
}

// A person's move, as the PATCH handler makes it: the task lands idle in the new stage and the
// dispatcher handles the entry.
func personMoves(t *testing.T, d *Dispatcher, store *Store, tk *Task, to Stage) *Task {
	t.Helper()
	idle := StatusIdle
	moved, err := store.UpdateIfStage(t.Context(), tk.ID, tk.Stage, Patch{Stage: &to, Status: &idle})
	if err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
	d.HandleStageEntered(context.WithoutCancel(t.Context()), moved, tk.Stage)
	return moved
}

func launchedAgents(l *twoAgentLauncher) []string {
	var out []string
	for _, env := range l.launches() {
		name := env["INSTANCE_ID"]
		if i := strings.Index(name, "-"); i > 0 {
			name = name[:i]
		}
		if env["AUDIT_ONLY"] == "1" {
			name += "(audit)"
		}
		out = append(out, name)
	}
	return out
}

// On a real task the refiner was still writing the spec when a person moved the task to the next stage.
// The next stage's agent must wait for the refiner instead of sharing the branch with it, and the
// refiner's run, once it ends, must not launch its stage's auditor, point the task at it or touch
// the task's status.
func TestMoveWhileTheAgentWorksWaitsForItAndDropsItsResult(t *testing.T) {
	d, store, l, tk := outlivedSetup(t, true)
	ctx := context.WithoutCancel(t.Context())

	d.launchStage(ctx, tk, "spec", "refiner", l.agents["refiner"], launchOpts{})
	personMoves(t, d, store, tk, "code")

	waitFor(t, func() bool { return hasEvent(t, store, tk.ID, EventStageLaunchDeferred) })
	if got := launchedAgents(l); len(got) != 1 {
		t.Fatalf("launches = %v, want only the refiner while it works", got)
	}
	mid, _ := store.Get(ctx, tk.ID)
	if mid.Stage != "code" || mid.Status != StatusRunning || mid.CurrentRunID != "ctr-a" {
		t.Fatalf("waiting task = %s/%s run %q, want code/running on the refiner's run", mid.Stage, mid.Status, mid.CurrentRunID)
	}
	if !hasCommentKey(t, store, tk.ID, "task.comments.stage_launch_deferred") {
		t.Error("nothing tells the person the tester waits")
	}

	l.exit(t, "ctr-a")
	waitFor(t, func() bool { return len(l.launches()) == 2 })
	if got := launchedAgents(l); got[1] != "tester" {
		t.Fatalf("launches = %v, want the tester after the refiner and no auditor", got)
	}
	if hasEvent(t, store, tk.ID, EventAuditorRunStarted) {
		t.Error("the refiner's auditor ran for a task that left spec")
	}
	if !hasEvent(t, store, tk.ID, EventRunOutlived) || !hasCommentKey(t, store, tk.ID, "task.comments.run_outlived") {
		t.Error("the dropped result left no trail")
	}
	after, _ := store.Get(ctx, tk.ID)
	if after.Stage != "code" || after.Status != StatusRunning || after.CurrentRunID != "ctr-b" {
		t.Errorf("task = %s/%s run %q, want code/running on the tester's run", after.Stage, after.Status, after.CurrentRunID)
	}

	// The tester's own run is not outlived: it finishes as usual.
	l.exit(t, "ctr-b")
	done, _ := store.Get(ctx, tk.ID)
	if done.Status != StatusIdle {
		t.Errorf("after the tester = %s, want idle", done.Status)
	}
}

// The task moved while the auditor checked the run: its verdict must not block, kick back or
// advance the task from a stage it left, and the next stage's agent starts once it ends.
func TestAuditorOutlivedByAMoveDropsItsVerdict(t *testing.T) {
	d, store, l, tk := outlivedSetup(t, true)
	ctx := context.WithoutCancel(t.Context())

	d.launchStage(ctx, tk, "spec", "refiner", l.agents["refiner"], launchOpts{})
	l.exit(t, "ctr-a")
	if got := launchedAgents(l); len(got) != 2 || got[1] != "refiner(audit)" {
		t.Fatalf("launches = %v, want the refiner's auditor", got)
	}
	personMoves(t, d, store, tk, "code")
	waitFor(t, func() bool { return hasEvent(t, store, tk.ID, EventStageLaunchDeferred) })

	// No verdict: in spec this blocks the task (auditor_no_verdict).
	l.exit(t, "ctr-b")
	waitFor(t, func() bool { return len(l.launches()) == 3 })
	if hasEvent(t, store, tk.ID, EventTaskBlocked) {
		t.Error("an outlived auditor blocked the task")
	}
	if !hasCommentKey(t, store, tk.ID, "task.comments.auditor_outlived") {
		t.Error("the dropped verdict left no trail")
	}
	after, _ := store.Get(ctx, tk.ID)
	if after.Stage != "code" || after.CurrentRunID != "ctr-c" {
		t.Errorf("task = %s run %q, want code on the tester's run", after.Stage, after.CurrentRunID)
	}
}

// The auditor decided to launch, but the task moved before it started: it never runs.
func TestAuditorLaunchSkipsATaskThatMoved(t *testing.T) {
	d, store, l, tk := outlivedSetup(t, true)
	ctx := context.WithoutCancel(t.Context())

	moved := moveTo(t, store, tk.ID, "code")
	if !d.launchAuditor(ctx, tk, "spec", l.agents["refiner"], auditedRun{Agent: "refiner", Instance: "refiner-task-x-spec-1"}) {
		t.Fatal("a skipped audit must still end the post-run flow")
	}
	if n := len(l.launches()); n != 0 {
		t.Fatalf("launches = %d, want no auditor for a task in %s", n, moved.Stage)
	}
	if !hasEvent(t, store, tk.ID, EventRunOutlived) {
		t.Error("the skipped audit left no trail")
	}
}

// A stage's own approval vouches for its agent's work: it cannot be approved while that agent is
// still producing it. On a real task "definition ready" was approved before the refiner wrote the spec.
func TestStageApprovalWaitsForTheStageAgent(t *testing.T) {
	d, store, l, tk := outlivedSetup(t, false)
	ctx := context.WithoutCancel(t.Context())
	ready := &Approval{TaskID: tk.ID, Key: "definition_ready", Label: "Definition ready", Stage: "spec", Source: ApprovalFromStage}
	if err := store.AddApproval(ctx, ready); err != nil {
		t.Fatal(err)
	}
	agentAsked := &Approval{TaskID: tk.ID, Key: "deploy_prod", Label: "Deploy", Stage: "spec", Source: ApprovalFromAgent}
	if err := store.AddApproval(ctx, agentAsked); err != nil {
		t.Fatal(err)
	}

	d.launchStage(ctx, tk, "spec", "refiner", l.agents["refiner"], launchOpts{})
	working, _ := store.Get(ctx, tk.ID)
	_, err := d.DecideApproval(ctx, working, ready.ID, ApprovalApproved, "ana", "")
	if apperr.Code(err) != "task.approvals.errors.stage_agent_working" {
		t.Fatalf("approve while the refiner works: %v, want stage_agent_working", err)
	}
	if _, err := d.DecideApproval(ctx, working, agentAsked.ID, ApprovalApproved, "ana", ""); err != nil {
		t.Errorf("an approval the agent asked for waits on its own run: %v", err)
	}

	l.exit(t, "ctr-a")
	done, _ := store.Get(ctx, tk.ID)
	if _, err := d.DecideApproval(ctx, done, ready.ID, ApprovalApproved, "ana", ""); err != nil {
		t.Errorf("approve after the refiner ended: %v", err)
	}
}

// After a restart the exit callback is gone and the replay reads the task's stage at launch from
// the run-started event.
func TestReplayKnowsTheTaskStageAtLaunch(t *testing.T) {
	d, store, l, tk := outlivedSetup(t, false)
	ctx := context.WithoutCancel(t.Context())

	d.launchStage(ctx, tk, "spec", "refiner", l.agents["refiner"], launchOpts{})
	instance := l.launches()[0]["INSTANCE_ID"]
	if got := d.replayTaskStage(ctx, tk.ID, instance, LaunchStage); got != "spec" {
		t.Errorf("replayed task stage = %q, want spec", got)
	}
	if got := d.replayTaskStage(ctx, tk.ID, "refiner-task-"+tk.ID+"-spec-1", LaunchStage); got != "" {
		t.Errorf("a run with no recorded stage = %q, want its own stage (empty)", got)
	}
	moveTo(t, store, tk.ID, "code")
	if got := d.replayTaskStage(ctx, tk.ID, "refiner-task-"+tk.ID+"-spec-1", LaunchContinuation); got != "code" {
		t.Errorf("an old continuation = %q, want the task's stage now", got)
	}
}

func TestRunOutlived(t *testing.T) {
	cases := []struct {
		name       string
		task       *Task
		launchedIn Stage
		run        string
		want       bool
	}{
		{"same stage, same run", &Task{Stage: "spec", CurrentRunID: "a"}, "spec", "a", false},
		{"task moved", &Task{Stage: "code", CurrentRunID: "a"}, "spec", "a", true},
		{"another run took the task", &Task{Stage: "spec", CurrentRunID: "b"}, "spec", "a", true},
		{"no run recorded", &Task{Stage: "spec"}, "spec", "a", false},
		{"no task", nil, "spec", "a", false},
	}
	for _, c := range cases {
		if got := runOutlived(c.task, c.launchedIn, c.run); got != c.want {
			t.Errorf("%s: runOutlived = %v, want %v", c.name, got, c.want)
		}
	}
}
