package task

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/github"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// checksBoard: verify asks for a merge approval that waits for the PR's required checks.
func checksBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true},
		{
			ID: "verify", Label: "Verify", Position: 20, AutoAdvance: true,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "merge", Label: "Merge", RequiredChecks: true}},
		},
		{ID: "release", Label: "Release", Position: 30, MergeTarget: true},
		{ID: "shipped", Label: "Shipped", Position: 40, Terminal: true},
	})
}

// fakeChecks answers what the test sets and counts the reads, which stand for GitHub calls.
type fakeChecks struct {
	holds atomic.Pointer[[]ChecksHold]
	reads atomic.Int32
}

func (f *fakeChecks) set(holds ...ChecksHold) { f.holds.Store(&holds) }

func (f *fakeChecks) read(context.Context, *Task) []ChecksHold {
	f.reads.Add(1)
	if p := f.holds.Load(); p != nil {
		return *p
	}
	return nil
}

func runningBuild() ChecksHold {
	return ChecksHold{Number: 7, RequiredStatus: github.RequiredStatus{
		State: github.RequiredPending, Required: []string{"build"}, Pending: []string{"build"},
	}}
}

func newChecksStore(t *testing.T) (*Dispatcher, *Store, *fakeChecks) {
	t.Helper()
	d, store := newStageTestDispatcher(t, checksBoard())
	f := &fakeChecks{}
	store.WithChecksGate(f.read)
	return d, store, f
}

func TestChecksGateHoldsAnApprovedApprovalUntilGreen(t *testing.T) {
	_, store, checks := newChecksStore(t)
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")
	checks.set(runningBuild())

	v, err := store.StageExitGate(ctx, tk, "verify", "release", GateManual)
	if err != nil || v.Passed || v.OnlyChecks() {
		t.Fatalf("pending merge approval: %+v, %v; want held by the approval itself", v, err)
	}
	if n := checks.reads.Load(); n != 0 {
		t.Errorf("the gate read GitHub %d times while an approval was still pending", n)
	}

	merge := approvalKeyed(t, store, tk.ID, "merge")
	if _, err := store.SetApprovalStatus(ctx, merge.ID, ApprovalApproved, "ana@x", "", ""); err != nil {
		t.Fatal(err)
	}
	v, err = store.StageExitGate(ctx, tk, "verify", "release", GateManual)
	if err != nil || v.Passed || !v.OnlyChecks() || len(v.Checks) != 1 || v.Labels() != "Merge" {
		t.Fatalf("approved while build runs: %+v, %v; want held by the checks", v, err)
	}
	if v, _ := store.StageExitGate(ctx, tk, "verify", "shipped", GateAutomation); v.Passed || !v.OnlyChecks() {
		t.Errorf("a jump past verify was not held by its checks: %+v", v)
	}
	before := checks.reads.Load()
	if v, _ := store.StageExitGate(ctx, tk, "verify", "release", GateMerge); !v.Passed {
		t.Errorf("a merge on GitHub was held by the checks: %+v", v)
	}
	if v, _ := store.StageExitGate(ctx, tk, "verify", "build", GateManual); !v.Passed {
		t.Errorf("a move back was held by the checks: %+v", v)
	}
	if n := checks.reads.Load(); n != before {
		t.Errorf("administrative and backward moves read GitHub %d times", n-before)
	}

	checks.set()
	if v, _ := store.StageExitGate(ctx, tk, "verify", "release", GateManual); !v.Passed {
		t.Errorf("green checks still hold: %+v", v)
	}

	checks.set(runningBuild())
	if _, err := store.SetApprovalStatus(ctx, merge.ID, ApprovalWaived, "admin@x", "CI is down", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.StageExitGate(ctx, tk, "verify", "release", GateManual); !v.Passed {
		t.Errorf("a waived approval waited for the checks: %+v", v)
	}
}

func TestChecksGateWithoutReaderHoldsNothing(t *testing.T) {
	_, store := newStageTestDispatcher(t, checksBoard())
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")
	merge := approvalKeyed(t, store, tk.ID, "merge")
	if _, err := store.SetApprovalStatus(ctx, merge.ID, ApprovalApproved, "ana@x", "", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.StageExitGate(ctx, tk, "verify", "release", GateManual); !v.Passed {
		t.Errorf("no GitHub reader, yet the gate held: %+v", v)
	}
}

// The run of an auto-advancing stage ends with the merge approved and the build still running: the
// task parks with a note naming the check, and the first re-evaluation after the checks pass moves
// it once, however many times it is repeated.
func TestAutoAdvanceWaitsForChecksThenMovesOnce(t *testing.T) {
	d, store, checks := newChecksStore(t)
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	merge := approvalKeyed(t, store, tk.ID, "merge")
	if _, err := store.SetApprovalStatus(ctx, merge.ID, ApprovalApproved, "ana@x", "", ""); err != nil {
		t.Fatal(err)
	}
	checks.set(runningBuild())

	d.autoAdvanceStage(ctx, tk, tk.ID, "verify", 0, "")
	parked, _ := store.Get(ctx, tk.ID)
	if parked.Stage != "verify" || parked.Status != StatusAwaitingApproval {
		t.Fatalf("after the run: %s/%s, want verify/awaiting_approval", parked.Stage, parked.Status)
	}
	if c := lastCommentIn(t, store, tk.ID, "en"); !strings.Contains(c, "build still running") || !strings.Contains(c, "Merge") {
		t.Errorf("parking note = %q, want the running check and the approval", c)
	}

	// A check finished but another still runs: nothing moves.
	d.ApprovalsSettled(ctx, tk.ID)
	if still, _ := store.Get(ctx, tk.ID); still.Stage != "verify" || still.Status != StatusAwaitingApproval {
		t.Fatalf("still running: %s/%s", still.Stage, still.Status)
	}

	checks.set()
	for range 3 {
		d.ApprovalsSettled(ctx, tk.ID)
	}
	final, _ := store.Get(ctx, tk.ID)
	if final.Stage != "release" {
		t.Fatalf("after green checks: stage %s, want release", final.Stage)
	}
	events, err := store.ListEvents(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	moves := 0
	for i := range events {
		if events[i].Kind == EventStageEntered && strings.Contains(events[i].Summary, "verify → release") {
			moves++
		}
	}
	if moves != 1 {
		t.Errorf("verify → release recorded %d times, want 1", moves)
	}
}

func TestMergeWaitsForChecks(t *testing.T) {
	reg := checksBoard()
	at := func(stage Stage) *Task { return &Task{Stage: stage} }
	cases := []struct {
		name   string
		t      *Task
		list   []Approval
		target Stage
		want   bool
	}{
		{"before the gated stage", at("build"), nil, "release", true},
		{"in the gated stage", at("verify"), nil, "release", true},
		{"past the gated stage", at("release"), nil, "release", false},
		{"no merge target", at("verify"), nil, "", true},
		{"waived by an admin", at("verify"), []Approval{{Key: "merge", Stage: "verify", Source: ApprovalFromStage, Status: ApprovalWaived}}, "release", false},
		{"approved still waits", at("verify"), []Approval{{Key: "merge", Stage: "verify", Source: ApprovalFromStage, Status: ApprovalApproved}}, "release", true},
		{"stage off the board", at("gone"), nil, "release", false},
	}
	for _, c := range cases {
		if got := MergeWaitsForChecks(reg, c.t, c.list, c.target); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReopenApprovedBefore(t *testing.T) {
	_, store := newStageTestDispatcher(t, checksBoard())
	ctx := t.Context()
	tk := createIn(t, store, "verify", "")
	merge := approvalKeyed(t, store, tk.ID, "merge")
	approved, err := store.SetApprovalStatus(ctx, merge.ID, ApprovalApproved, "ana@x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	decided := *approved.DecidedAt

	if got, err := store.ReopenApprovedBefore(ctx, tk.ID, []string{merge.ID}, decided.Add(-time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("a push before the approval reopened %+v, %v", got, err)
	}
	got, err := store.ReopenApprovedBefore(ctx, tk.ID, []string{merge.ID}, decided.Add(time.Minute))
	if err != nil || len(got) != 1 || got[0].Status != ApprovalPending || got[0].DecidedBy != "" {
		t.Fatalf("a push after the approval: %+v, %v", got, err)
	}
	if again, err := store.ReopenApprovedBefore(ctx, tk.ID, []string{merge.ID}, decided.Add(time.Minute)); err != nil || len(again) != 0 {
		t.Errorf("the same push again reopened %+v, %v", again, err)
	}
	if none, err := store.ReopenApprovedBefore(ctx, tk.ID, nil, time.Now()); err != nil || none != nil {
		t.Errorf("no ids: %+v, %v", none, err)
	}
}

func TestChecksHoldBlocks(t *testing.T) {
	cases := []struct {
		name string
		st   github.RequiredStatus
		want bool
	}{
		{"running", github.RequiredStatus{State: github.RequiredPending, Pending: []string{"build"}}, false},
		{"failing", github.RequiredStatus{State: github.RequiredFailing, Failing: []string{"build"}}, true},
		{"unknown", github.RequiredStatus{State: github.RequiredUnknown}, true},
		{"behind", github.RequiredStatus{State: github.RequiredPassed, Behind: true}, true},
		{"running and behind", github.RequiredStatus{State: github.RequiredPending, Behind: true}, true},
	}
	for _, c := range cases {
		h := ChecksHold{RequiredStatus: c.st}
		if got := h.Blocks(); got != c.want {
			t.Errorf("%s: Blocks = %v, want %v", c.name, got, c.want)
		}
	}
}
