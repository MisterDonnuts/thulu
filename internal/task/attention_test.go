package task

import (
	"slices"
	"strings"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Testing decides with its verdict and moves on by itself; review asks for a sign-off.
func attentionBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "intake", Label: "Intake", Position: 10, Initial: true},
		{ID: "testing", Label: "Testing", Position: 20, AgentVerdict: true, AutoAdvance: true},
		{ID: "readiness", Label: "Readiness", Position: 30},
		{ID: "review", Label: "Review", Position: 40,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "qa", Label: "QA sign-off"}}},
		{ID: "shipped", Label: "Shipped", Position: 50, Terminal: true},
	})
}

// A task blocked in testing with TC-1 passed and TC-2, TC-3 run but stuck.
func blockedTesting(t *testing.T, verdict string) (*Dispatcher, *Store, *Task) {
	t.Helper()
	d, store := newStageTestDispatcher(t, attentionBoard())
	ctx := t.Context()
	tk := createIn(t, store, "testing", "")
	if _, err := store.AddTestCases(ctx, tk.ID, "user", []TestCase{
		{Title: "Pay with a saved card"}, {Title: "Pay with a new card"}, {Title: "Pay on an iPhone"},
	}); err != nil {
		t.Fatal(err)
	}
	for ref, v := range map[string]string{"TC-1": TestCasePass, "TC-2": TestCaseBlocked, "TC-3": TestCaseFail} {
		if _, _, err := store.RecordTestCaseRun(ctx, tk.ID, ref, TestCaseRun{Verdict: v, Observed: ref + " observed", Author: "agent:tester"}); err != nil {
			t.Fatal(err)
		}
	}
	instance := "tester-task-" + tk.ID + "-testing-1"
	if verdict != "" {
		if _, err := store.AddReport(ctx, tk.ID, SourceAgent, "tester", ReportKindStageVerdict, "", verdict, instance); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordEvent(ctx, tk.ID, EventAgentRunFinished, instance, "agent:tester", "",
		map[string]any{"container_id": "runner-j1", "instance_id": instance, "stage": "testing", "exit": 0}); err != nil {
		t.Fatal(err)
	}
	cause := BlockVerdict
	if verdict == "" {
		cause = BlockVerdictUndelivered
	}
	d.block(ctx, tk.ID, "testing", BlockInfo{Cause: cause, Agent: "tester"})
	got, err := store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d, store, got
}

const needsPersonVerdict = `---
verdict: block
reason: needs_decision
needs_person:
  - {ref: TC-2, ask: Pay with a real card, why: real_account}
  - {ref: tc-3, ask: Try it on an iPhone, why: device}
---
Two cases need a person.
`

func refsOf(items []AttentionItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Ref)
	}
	return out
}

func TestAttentionOfABlockedStageListsWhatNeedsAPerson(t *testing.T) {
	d, _, tk := blockedTesting(t, needsPersonVerdict)
	a, _ := d.Attention(t.Context(), tk)
	if a == nil {
		t.Fatal("no attention for a blocked task")
	}
	if a.Kind != AttentionDecision || a.Stage != "testing" || a.RunID != "runner-j1" || a.VerdictLost {
		t.Errorf("attention = %+v", a)
	}
	want := []AttentionItem{
		{Ref: "TC-2", Title: "Pay with a new card", Ask: "Pay with a real card", Why: "real_account"},
		{Ref: "TC-3", Title: "Pay on an iPhone", Ask: "Try it on an iPhone", Why: "device"},
	}
	if !slices.Equal(a.Items, want) {
		t.Errorf("items = %+v\nwant %+v", a.Items, want)
	}
	if !slices.Equal(a.Options, []string{OptionTestByHand, OptionWaiveCases, OptionSendBack, OptionRerun}) {
		t.Errorf("options = %v", a.Options)
	}
	if a.Headline != "Testing finished: 1 of 3 cases passed, 2 need a person." {
		t.Errorf("headline = %q", a.Headline)
	}
}

func TestAttentionPrefersTheAgentsHeadline(t *testing.T) {
	body := strings.Replace(needsPersonVerdict, "reason: needs_decision", "reason: needs_decision\nheadline: Two payment cases need a real card", 1)
	d, _, tk := blockedTesting(t, body)
	if a, _ := d.Attention(t.Context(), tk); a == nil || a.Headline != "Two payment cases need a real card" {
		t.Errorf("attention = %+v", a)
	}
}

// The verdict was lost on the way: the card falls back to the run's blocked and failed cases and
// says the verdict did not arrive.
func TestAttentionFallsBackToTheRunsCasesWhenTheVerdictIsLost(t *testing.T) {
	d, _, tk := blockedTesting(t, "")
	a, _ := d.Attention(t.Context(), tk)
	if a == nil || a.Kind != AttentionDecision || !a.VerdictLost {
		t.Fatalf("attention = %+v, want a decision with verdict_lost", a)
	}
	if got := refsOf(a.Items); !slices.Equal(got, []string{"TC-2", "TC-3"}) {
		t.Errorf("items = %v, want TC-2 and TC-3", got)
	}
	if a.Items[0].Ask != "TC-2 observed" || a.Items[0].Why != NeedsPersonOther {
		t.Errorf("item = %+v", a.Items[0])
	}
}

// Skipping every needs-person case unblocks the task and moves it on like a continue verdict; a
// partial skip leaves it where it is.
func TestWaivingTheNeedsPersonCasesAdvancesTheTask(t *testing.T) {
	d, store, tk := blockedTesting(t, needsPersonVerdict)
	ctx := t.Context()
	if _, err := d.WaiveTestCases(ctx, tk, []string{"TC-2"}, "ana@example.com", " "); err == nil {
		t.Fatal("a waiver without a reason passed")
	}
	res, err := d.WaiveTestCases(ctx, tk, []string{"TC-2"}, "ana@example.com", "No real card in staging")
	if err != nil {
		t.Fatal(err)
	}
	if res.Waived != 1 || res.Advanced || res.Stage != "testing" {
		t.Errorf("partial waiver = %+v, want 1 waived and no advance", res)
	}
	a, _ := d.Attention(ctx, mustGet(t, store, tk.ID))
	if a == nil || !slices.Equal(refsOf(a.Items), []string{"TC-3"}) {
		t.Errorf("after waiving TC-2 the card lists %+v, want TC-3 only", a)
	}

	res, err = d.WaiveTestCases(ctx, mustGet(t, store, tk.ID), []string{"TC-3"}, "ana@example.com", "No iPhone at hand")
	if err != nil {
		t.Fatal(err)
	}
	after := mustGet(t, store, tk.ID)
	if !res.Advanced || res.Stage != "readiness" || after.Stage != "readiness" || after.Status == StatusBlocked {
		t.Errorf("waiver = %+v, task %s/%s; want it moved to readiness", res, after.Stage, after.Status)
	}
	c := findCase(after.TestCases, "TC-3")
	if c == nil || c.Status != TestCaseWaived || len(c.Runs) == 0 {
		t.Fatalf("TC-3 = %+v", c)
	}
	if r := c.Runs[0]; r.Verdict != TestCaseWaived || r.Author != "ana@example.com" || r.Note != "No iPhone at hand" {
		t.Errorf("waiver run = %+v", r)
	}
}

func mustGet(t *testing.T, store *Store, id string) *Task {
	t.Helper()
	got, err := store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A waiver goes through its own route with a reason, never a status edit.
func TestAStatusEditNeverWaivesACase(t *testing.T) {
	_, store := newStageTestDispatcher(t, attentionBoard())
	tk := createIn(t, store, "testing", "")
	if _, err := store.AddTestCases(t.Context(), tk.ID, "user", []TestCase{{Title: "a"}}); err != nil {
		t.Fatal(err)
	}
	waived := TestCaseWaived
	if _, err := store.UpdateTestCase(t.Context(), tk.ID, "TC-1", "user", TestCasePatch{Status: &waived}); err == nil {
		t.Error("PATCH status=waived passed")
	}
	if _, err := store.AddTestCases(t.Context(), tk.ID, "user", []TestCase{{Title: "b", Status: TestCaseWaived}}); err == nil {
		t.Error("a new case born waived passed")
	}
	if _, _, err := store.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCaseWaived, Author: "user"}); err == nil {
		t.Error("a waived run without a reason passed")
	}
}

func TestAttentionForApprovalsAndLaterOnes(t *testing.T) {
	d, store := newStageTestDispatcher(t, attentionBoard())
	ctx := t.Context()

	early := createIn(t, store, "testing", "")
	a, upcoming := d.Attention(ctx, early)
	if a != nil {
		t.Errorf("an idle task with nothing pending has attention %+v", a)
	}
	if len(upcoming) != 1 || upcoming[0].Key != "qa" || upcoming[0].Stage != "review" ||
		upcoming[0].StageLabel != "Review" || upcoming[0].ID != "" || upcoming[0].Stale {
		t.Errorf("upcoming = %+v, want the review sign-off", upcoming)
	}

	inReview := createIn(t, store, "review", "")
	info, _ := attentionBoard().Get("review")
	if _, err := store.EnsureStageApprovals(ctx, inReview, info); err != nil {
		t.Fatal(err)
	}
	a, upcoming = d.Attention(ctx, mustGet(t, store, inReview.ID))
	if a == nil || a.Kind != AttentionApproval || a.ApprovalID == "" || len(a.Items) != 1 || a.Items[0].Title != "QA sign-off" {
		t.Fatalf("attention = %+v, want the sign-off", a)
	}
	if !slices.Equal(a.Options, []string{OptionDecideApproval}) || !strings.Contains(a.Headline, "QA sign-off") {
		t.Errorf("attention = %+v", a)
	}
	if len(upcoming) != 0 {
		t.Errorf("upcoming = %+v, want none: the approval is the current stage's", upcoming)
	}

	running := createIn(t, store, "testing", "")
	d.setStatus(ctx, running.ID, StatusRunning)
	if a, _ := d.Attention(ctx, mustGet(t, store, running.ID)); a != nil {
		t.Errorf("a running task has attention %+v", a)
	}
}
