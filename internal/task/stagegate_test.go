package task

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func gatedBoard(mode string, kickTo types.Stage) stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true},
		{
			ID: "verify", Label: "Verify", Position: 20, AutoAdvance: true,
			RequiredApprovals: []types.ApprovalRequirement{
				{Key: "qa", Label: "QA sign-off", ResetOnChange: true},
				{Key: "sec", Label: "Security", TaskTypes: []string{"feature"}, ResetOnChange: true},
			},
			OnChangesRequested: mode, ChangesKickBackTo: kickTo,
		},
		{ID: "shipped", Label: "Shipped", Position: 30, Terminal: true},
	})
}

func createIn(t *testing.T, store *Store, stage Stage, typeID string) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: stage, TypeID: typeID}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return full
}

func approvalKeyed(t *testing.T, store *Store, taskID, key string) Approval {
	t.Helper()
	list, err := store.ListApprovals(t.Context(), taskID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range list {
		if list[i].Key == key {
			return list[i]
		}
	}
	t.Fatalf("no approval %q on %s: %+v", key, taskID, list)
	return Approval{}
}

func TestStageEntryOpensRequiredApprovals(t *testing.T) {
	_, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := t.Context()

	plain := createIn(t, store, "verify", "")
	if len(plain.Approvals) != 1 || plain.Approvals[0].Key != "qa" || plain.Approvals[0].Source != ApprovalFromStage {
		t.Errorf("untyped task opened %+v, want qa only", plain.Approvals)
	}

	feature := createIn(t, store, "build", "feature")
	if len(feature.Approvals) != 0 {
		t.Fatalf("build requires nothing, opened %+v", feature.Approvals)
	}
	verify := Stage("verify")
	if _, err := store.Update(ctx, feature.ID, Patch{Stage: &verify}); err != nil {
		t.Fatalf("move: %v", err)
	}
	build := Stage("build")
	if _, err := store.Update(ctx, feature.ID, Patch{Stage: &build}); err != nil {
		t.Fatalf("move back: %v", err)
	}
	if _, err := store.Update(ctx, feature.ID, Patch{Stage: &verify}); err != nil {
		t.Fatalf("move again: %v", err)
	}
	list, _ := store.ListApprovals(ctx, feature.ID)
	if len(list) != 2 {
		t.Errorf("feature in verify twice has %d approvals, want qa and sec once each: %+v", len(list), list)
	}
}

func TestStageExitGateDirections(t *testing.T) {
	_, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")

	cases := []struct {
		name     string
		to       Stage
		cause    GateCause
		passed   bool
		blocking int
	}{
		{"manual forward", "shipped", GateManual, false, 1},
		{"auto-advance", "shipped", GateAutoAdvance, false, 1},
		{"automation", "shipped", GateAutomation, false, 1},
		{"another board", OffBoard, GateManual, false, 1},
		{"backward", "build", GateManual, true, 0},
		{"kick back", "shipped", GateKickBack, true, 0},
		{"stage deleted", "shipped", GateStageDeleted, true, 1},
		{"board deleted", OffBoard, GateBoardDeleted, true, 1},
		{"type relocation", "shipped", GateTypeRelocation, true, 1},
	}
	for _, c := range cases {
		v, err := store.StageExitGate(ctx, tk, "verify", c.to, c.cause)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if v.Passed != c.passed || len(v.Blocking) != c.blocking {
			t.Errorf("%s: passed=%v blocking=%d, want %v/%d", c.name, v.Passed, len(v.Blocking), c.passed, c.blocking)
		}
	}

	qa := approvalKeyed(t, store, tk.ID, "qa")
	if _, err := store.SetApprovalStatus(ctx, qa.ID, ApprovalWaived, "ana@x", "hotfix", ""); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if v, _ := store.StageExitGate(ctx, tk, "verify", "shipped", GateManual); !v.Passed {
		t.Errorf("waived approval still holds: %+v", v.Blocking)
	}
}

func TestStageExitGateOpensMissingRequirements(t *testing.T) {
	_, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")
	if _, err := store.db.ExecContext(ctx, store.rb(`DELETE FROM task_approvals WHERE task_id = ?`), tk.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}

	check, err := store.StageExitCheck(ctx, tk, "verify", "shipped", GateManual)
	if err != nil || check.Passed || len(check.Blocking) != 1 || check.Blocking[0].ID != "" {
		t.Fatalf("check = %+v, %v; want one unopened requirement", check, err)
	}
	gate, err := store.StageExitGate(ctx, tk, "verify", "shipped", GateManual)
	if err != nil || gate.Passed || len(gate.Blocking) != 1 || gate.Blocking[0].ID == "" {
		t.Fatalf("gate = %+v, %v; want the requirement opened", gate, err)
	}
}

func TestBlockedByFlagsHoldsLegacyApprovals(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true},
		{ID: "review", Label: "Review", Position: 20, BlockedByFlags: true},
		{ID: "done", Label: "Done", Position: 30, Terminal: true},
	})
	_, store := newStageTestDispatcher(t, reg)
	ctx := t.Context()
	tk := createIn(t, store, "build", "")
	f := &Flag{Kind: FlagCustom, Label: "needs a look"}
	if err := store.AddFlag(ctx, tk.ID, f); err != nil {
		t.Fatalf("flag: %v", err)
	}
	if v, _ := store.StageExitGate(ctx, tk, "build", "review", GateManual); !v.Passed {
		t.Errorf("a stage without blocked_by_flags was held by a flag: %+v", v.Blocking)
	}
	tk.Stage = "review"
	if v, _ := store.StageExitGate(ctx, tk, "review", "done", GateManual); v.Passed {
		t.Error("blocked_by_flags let a pending flag through")
	}
	approved := FlagApproved
	if _, err := store.UpdateFlag(ctx, f.ID, FlagPatch{Status: &approved}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if v, _ := store.StageExitGate(ctx, tk, "review", "done", GateManual); !v.Passed {
		t.Errorf("approved flag still holds: %+v", v.Blocking)
	}
}

func TestAutoAdvanceWaitsForApprovalThenMoves(t *testing.T) {
	d, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := context.Background()
	tk := createIn(t, store, "verify", "feature")

	d.autoAdvanceStage(ctx, tk, tk.ID, "verify", 0, "")
	after, _ := store.Get(ctx, tk.ID)
	if after.Stage != "verify" || after.Status != StatusAwaitingApproval {
		t.Fatalf("after a held auto-advance: stage %q status %q, want verify/awaiting_approval", after.Stage, after.Status)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "QA sign-off") || !strings.Contains(c, "Security") {
		t.Errorf("comment %q does not name the approvals", c)
	}

	if _, err := d.DecideApproval(ctx, after, approvalKeyed(t, store, tk.ID, "qa").ID, ApprovalApproved, "ana@x", ""); err != nil {
		t.Fatalf("approve qa: %v", err)
	}
	if mid, _ := store.Get(ctx, tk.ID); mid.Stage != "verify" || mid.Status != StatusAwaitingApproval {
		t.Fatalf("one of two approvals moved the task: %q/%q", mid.Stage, mid.Status)
	}
	if _, err := d.DecideApproval(ctx, after, approvalKeyed(t, store, tk.ID, "sec").ID, ApprovalWaived, "ana@x", "internal only"); err != nil {
		t.Fatalf("waive sec: %v", err)
	}
	final, _ := store.Get(ctx, tk.ID)
	if final.Stage != "shipped" {
		t.Errorf("after the last approval: stage %q, want shipped", final.Stage)
	}
}

func TestApprovalsSettledWithoutAutoAdvanceGoesIdle(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true},
		{ID: "verify", Label: "Verify", Position: 20, RequiredApprovals: []types.ApprovalRequirement{{Key: "qa", Label: "QA", ResetOnChange: true}}},
		{ID: "shipped", Label: "Shipped", Position: 30, Terminal: true},
	})
	d, store := newStageTestDispatcher(t, reg)
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	d.setStatus(ctx, tk.ID, StatusAwaitingApproval)

	if _, err := d.DecideApproval(ctx, tk, approvalKeyed(t, store, tk.ID, "qa").ID, ApprovalApproved, "ana@x", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	after, _ := store.Get(ctx, tk.ID)
	if after.Stage != "verify" || after.Status != StatusIdle {
		t.Errorf("stage %q status %q, want verify/idle", after.Stage, after.Status)
	}
}

func TestChangesRequestedStay(t *testing.T) {
	d, store := newStageTestDispatcher(t, gatedBoard(types.ChangesStay, ""))
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	qa := approvalKeyed(t, store, tk.ID, "qa")

	got, err := d.DecideApproval(ctx, tk, qa.ID, ApprovalChangesRequested, "ana@x", "the copy is wrong")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got.Status != ApprovalChangesRequested || got.DecidedNote != "the copy is wrong" {
		t.Errorf("decision = %+v", got)
	}
	if after, _ := store.Get(ctx, tk.ID); after.Stage != "verify" {
		t.Errorf("stay moved the task to %q", after.Stage)
	}
	if v, _ := store.StageExitGate(ctx, tk, "verify", "shipped", GateManual); v.Passed {
		t.Error("changes requested let the task through")
	}
}

func TestChangesRequestedKickBack(t *testing.T) {
	d, store := newStageTestDispatcher(t, gatedBoard(types.ChangesKickBack, "build"))
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	qa := approvalKeyed(t, store, tk.ID, "qa")
	d.setStatus(ctx, tk.ID, StatusAwaitingApproval)

	if _, err := d.DecideApproval(ctx, tk, qa.ID, ApprovalChangesRequested, "ana@x", "redo the migration"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	after, _ := store.Get(ctx, tk.ID)
	if after.Stage != "build" {
		t.Fatalf("kick_back left the task in %q, want build", after.Stage)
	}
	if after.Status != StatusIdle {
		t.Errorf("status after kick_back = %q, want idle: nothing holds the task in build", after.Status)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "QA sign-off") {
		t.Errorf("comment %q does not say why", c)
	}

	verify := Stage("verify")
	if _, err := store.Update(ctx, tk.ID, Patch{Stage: &verify}); err != nil {
		t.Fatalf("back to verify: %v", err)
	}
	if a := approvalKeyed(t, store, tk.ID, "qa"); a.Status != ApprovalPending {
		t.Errorf("qa on re-entry = %q, want pending again", a.Status)
	}
}

func TestChangesRequestedResumesTheStage(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: types.StageBacklog, Label: "Backlog", Position: 10, Initial: true},
		{
			ID: types.StageInProgress, Label: "In progress", Position: 20,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "qa", Label: "QA sign-off", ResetOnChange: true}},
		},
		{ID: types.StageDone, Label: "Done", Position: 30, Terminal: true},
	})
	store.WithStages(reg)
	d.WithStages(reg)
	ctx := context.Background()
	tk := &Task{
		WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress,
		StageAssignments: map[Stage]string{types.StageInProgress: "developer"},
	}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, _ := store.Get(ctx, tk.ID)
	qa := approvalKeyed(t, store, tk.ID, "qa")

	if _, err := d.DecideApproval(ctx, full, qa.ID, ApprovalChangesRequested, "ana@x", "rename the flag"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	waitFor(t, func() bool { return len(l.launches()) == 1 })
	env := l.launches()[0]
	if env[EnvLaunchKind] != string(LaunchChangesResume) || !strings.Contains(env["RESUME_MESSAGE"], "rename the flag") {
		t.Errorf("resume env = %v, want a changes resume carrying the note", env)
	}
	waitFor(t, func() bool {
		l.recordingLauncher.mu.Lock()
		defer l.recordingLauncher.mu.Unlock()
		return l.exits["ctr-a"] != nil
	})
	l.exit(t, "ctr-a")
	waitFor(t, func() bool {
		a := approvalKeyed(t, store, tk.ID, "qa")
		return a.Status == ApprovalPending
	})
}

func TestDecideApprovalOfAnotherTask(t *testing.T) {
	d, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := context.Background()
	a := createIn(t, store, "verify", "")
	b := createIn(t, store, "verify", "")
	if _, err := d.DecideApproval(ctx, b, approvalKeyed(t, store, a.ID, "qa").ID, ApprovalApproved, "ana@x", ""); err == nil {
		t.Error("decided another task's approval")
	}
	if _, err := d.DecideApproval(ctx, a, approvalKeyed(t, store, a.ID, "qa").ID, ApprovalPending, "ana@x", ""); err == nil {
		t.Error("pending accepted as a decision")
	}
}

func TestGateHoldsApprovalsOfAStageJumpedOver(t *testing.T) {
	_, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")
	build := Stage("build")
	if _, err := store.Update(ctx, tk.ID, Patch{Stage: &build}); err != nil {
		t.Fatalf("back to build: %v", err)
	}
	inBuild, _ := store.Get(ctx, tk.ID)
	v, err := store.StageExitGate(ctx, inBuild, "build", "shipped", GateManual)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if v.Passed || len(v.Blocking) != 1 || v.Blocking[0].Key != "qa" {
		t.Errorf("build → shipped: passed=%v blocking=%+v, want held by verify's qa", v.Passed, v.Blocking)
	}
	if v, _ := store.StageExitGate(ctx, inBuild, "build", "verify", GateManual); !v.Passed {
		t.Errorf("build → verify is held by %+v: it jumps over nothing", v.Blocking)
	}
}

func TestFlagRejectionMovesNothing(t *testing.T) {
	d, store := newStageTestDispatcher(t, gatedBoard(types.ChangesKickBack, "build"))
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	qa := approvalKeyed(t, store, tk.ID, "qa")

	got, err := d.DecideFlag(ctx, tk, qa.ID, ApprovalChangesRequested, "ana@x", "no")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got.Status != ApprovalChangesRequested {
		t.Errorf("status = %q, want changes_requested", got.Status)
	}
	if after, _ := store.Get(ctx, tk.ID); after.Stage != "verify" {
		t.Errorf("a rejected flag kicked the task back to %q", after.Stage)
	}
}

func TestChangesRequestedDuringARunWaitForItsEnd(t *testing.T) {
	d, store := newStageTestDispatcher(t, gatedBoard(types.ChangesKickBack, "build"))
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	qa := approvalKeyed(t, store, tk.ID, "qa")
	d.setStatus(ctx, tk.ID, StatusRunning)
	running, _ := store.Get(ctx, tk.ID)
	instance := fmt.Sprintf("dev-task-%s-verify-%d", tk.ID, time.Now().Add(-time.Minute).UnixMilli())

	if _, err := d.DecideApproval(ctx, running, qa.ID, ApprovalChangesRequested, "ana@x", "redo it"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if after, _ := store.Get(ctx, tk.ID); after.Stage != "verify" {
		t.Fatalf("the live run's task was kicked back to %q", after.Stage)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "QA sign-off") || !strings.Contains(c, "run ends") {
		t.Errorf("comment %q does not say the changes wait for the run", c)
	}

	later := fmt.Sprintf("dev-task-%s-verify-%d", tk.ID, time.Now().Add(time.Minute).UnixMilli())
	if d.deliverLateChanges(ctx, tk.ID, later) {
		t.Error("a run that started after the decision delivered it")
	}
	if !d.deliverLateChanges(ctx, tk.ID, instance) {
		t.Fatal("the run's end did not deliver the changes")
	}
	if after, _ := store.Get(ctx, tk.ID); after.Stage != "build" || after.Status != StatusIdle {
		t.Errorf("after delivery: %q/%q, want build/idle", after.Stage, after.Status)
	}
}

func TestReopenChangesRequestedDecidedBefore(t *testing.T) {
	_, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")
	qa := approvalKeyed(t, store, tk.ID, "qa")
	if _, err := store.SetApprovalStatus(ctx, qa.ID, ApprovalChangesRequested, "ana@x", "redo", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	got, err := store.ReopenChangesRequested(ctx, tk.ID, "verify", time.Now().Add(-time.Hour))
	if err != nil || len(got) != 0 {
		t.Fatalf("reopened %+v (err %v), want none: decided after the run started", got, err)
	}
	got, err = store.ReopenChangesRequested(ctx, tk.ID, "verify", time.Now().Add(time.Hour))
	if err != nil || len(got) != 1 {
		t.Fatalf("reopened %+v (err %v), want qa", got, err)
	}
}
