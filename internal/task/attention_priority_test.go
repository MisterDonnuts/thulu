package task

import (
	"strings"
	"testing"
	"time"
)

// What the developer wrote when it finished: a summary of its run, not a question for a person.
const developerContinueVerdict = `---
verdict: continue
headline: I changed no code this run; the fixes from the review are in
---
Summary of the run.
`

func askIn(t *testing.T, store *Store, tk *Task, stage Stage, runID string, blocking bool) *Question {
	t.Helper()
	in := AskInput{
		Kind: QuestionKindQuestion, Blocking: &blocking,
		Questions: []AskInputItem{{Header: "Consent", Question: "Keep the SMS consent text?"}},
	}
	if !blocking {
		in.Default = "Keep it on the registration page"
	}
	q, err := store.AskQuestion(t.Context(), tk, AskTarget{RunID: runID, Agent: "refiner", Stage: stage}, in)
	if err != nil {
		t.Fatalf("AskQuestion: %v", err)
	}
	return q
}

// The auditor of testing blocked after the developer's run: its feedback lands as a report of the
// audit instance, which names the stage.
func auditorBlocks(t *testing.T, d *Dispatcher, store *Store, tk *Task, feedback string) *Task {
	t.Helper()
	ctx := t.Context()
	instance := "tester-audit-" + tk.ID + "-testing-" + time.Now().Format("150405000")
	if feedback != "" {
		if _, err := store.AddReport(ctx, tk.ID, SourceAuditor, "auditor", ReportKindAuditorFeedback, "", feedback, instance); err != nil {
			t.Fatal(err)
		}
	}
	d.block(ctx, tk.ID, "testing", BlockInfo{Cause: BlockAuditor, Agent: "tester"})
	return mustGet(t, store, tk.ID)
}

// On a real task, with Development's block active, "Needs you" showed the refiner's non-blocking question,
// still open from an earlier stage. The current block wins.
func TestAttentionTheCurrentBlockWinsOverAQuestionOfAnEarlierStage(t *testing.T) {
	body := strings.Replace(needsPersonVerdict, "reason: needs_decision", "reason: needs_decision\nheadline: Two payment cases need a real card", 1)
	d, store, tk := blockedTesting(t, body)
	askIn(t, store, tk, "intake", "ctr-refiner", false)

	a, _ := d.Attention(t.Context(), mustGet(t, store, tk.ID))
	if a == nil || a.Kind != AttentionDecision || a.QuestionID != "" {
		t.Fatalf("attention = %+v, want the block's decision", a)
	}
	if a.Headline != "Two payment cases need a real card" {
		t.Errorf("headline = %q, want the block's", a.Headline)
	}
}

// A question of the current stage asked before the block does not cover it either.
func TestAttentionTheCurrentBlockWinsOverAnOlderQuestionOfTheSameStage(t *testing.T) {
	d, store := newStageTestDispatcher(t, attentionBoard())
	ctx := t.Context()
	tk := createIn(t, store, "testing", "")
	askIn(t, store, tk, "testing", "ctr-1", false)
	d.block(ctx, tk.ID, "testing", BlockInfo{Cause: BlockKickBackLimit})

	a, _ := d.Attention(ctx, mustGet(t, store, tk.ID))
	if a == nil || a.Kind == AttentionQuestion || a.QuestionID != "" {
		t.Fatalf("attention = %+v, want the block", a)
	}
}

// A stale non-blocking question from another stage never becomes the headline, even with nothing
// else pending.
func TestAttentionNeverHeadlinesAQuestionOfAnotherStage(t *testing.T) {
	d, store := newStageTestDispatcher(t, attentionBoard())
	tk := createIn(t, store, "readiness", "")
	askIn(t, store, tk, "intake", "ctr-refiner", false)

	if a, _ := d.Attention(t.Context(), mustGet(t, store, tk.ID)); a != nil {
		t.Errorf("attention = %+v, want none: the question belongs to intake", a)
	}

	q := askIn(t, store, tk, "readiness", "ctr-ready", false)
	a, _ := d.Attention(t.Context(), mustGet(t, store, tk.ID))
	if a == nil || a.Kind != AttentionQuestion || a.QuestionID != q.ID {
		t.Errorf("attention = %+v, want the readiness question", a)
	}
}

// The approval the stage waits on wins over a question asked before it and over any non-blocking
// one; a blocking question asked after it, whose run waits, comes first.
func TestAttentionTheStagesApprovalWinsOverAnOlderQuestion(t *testing.T) {
	d, store := newStageTestDispatcher(t, attentionBoard())
	ctx := t.Context()
	tk := createIn(t, store, "review", "")
	info, _ := attentionBoard().Get("review")
	if _, err := store.EnsureStageApprovals(ctx, tk, info); err != nil {
		t.Fatal(err)
	}
	older := askIn(t, store, tk, "review", "ctr-1", true)
	// Asked an hour before the sign-off was opened.
	if _, err := store.DB().ExecContext(ctx, store.rb(
		`UPDATE task_questions SET created_at = created_at - interval '1 hour' WHERE id = ?`), older.ID); err != nil {
		t.Fatal(err)
	}
	askIn(t, store, tk, "review", "ctr-2", false)

	a, _ := d.Attention(ctx, mustGet(t, store, tk.ID))
	if a == nil || a.Kind != AttentionApproval {
		t.Fatalf("attention = %+v, want the sign-off over the older blocking and the non-blocking question", a)
	}

	if _, err := d.CancelQuestion(ctx, mustGet(t, store, tk.ID), older.ID, "ana@example.com"); err != nil {
		t.Fatal(err)
	}
	newer := askIn(t, store, tk, "review", "ctr-3", true)
	a, _ = d.Attention(ctx, mustGet(t, store, tk.ID))
	if a == nil || a.Kind != AttentionQuestion || a.QuestionID != newer.ID {
		t.Errorf("attention = %+v, want the blocking question asked after the approval", a)
	}
}

// On a real task, with the auditor's block active, the headline was the developer's run summary. The card
// speaks with the auditor's ask.
func TestAttentionOfAnAuditorBlockUsesTheAuditorsAsk(t *testing.T) {
	d, store, tk := blockedTesting(t, developerContinueVerdict)
	tk = auditorBlocks(t, d, store, tk, `---
verdict: block
ask: The production diff is 790 lines against a budget of 400. Accept one PR or split it.
---
# Notes
Most of the diff is deleted code.
`)
	a, _ := d.Attention(t.Context(), tk)
	if a == nil || a.Kind != AttentionDecision {
		t.Fatalf("attention = %+v, want a decision", a)
	}
	if strings.Contains(a.Headline, "I changed no code") || !strings.Contains(a.Headline, "790 lines") {
		t.Errorf("headline = %q, want the auditor's ask", a.Headline)
	}
	if len(a.Items) != 1 || !strings.Contains(a.Items[0].Ask, "Accept one PR or split it") {
		t.Errorf("items = %+v, want the auditor's ask alone (not the developer's stuck cases)", a.Items)
	}
	for _, o := range a.Options {
		if o == OptionWaiveCases || o == OptionTestByHand {
			t.Errorf("options = %v: the auditor named no case", a.Options)
		}
	}
	if d.needsPersonSettled(t.Context(), tk) {
		t.Error("waiving the developer's cases would walk the task past the auditor")
	}
}

// An auditor that lists needs_person items: those are the items, its headline the headline.
func TestAttentionOfAnAuditorBlockListsItsNeedsPerson(t *testing.T) {
	d, store, tk := blockedTesting(t, developerContinueVerdict)
	tk = auditorBlocks(t, d, store, tk, `---
verdict: block
headline: The iPhone case was never run
needs_person:
  - {ref: TC-3, ask: Run it on an iPhone, why: device}
---
`)
	a, _ := d.Attention(t.Context(), tk)
	if a == nil || a.Headline != "The iPhone case was never run" {
		t.Fatalf("attention = %+v, want the auditor's headline", a)
	}
	if got := refsOf(a.Items); len(got) != 1 || got[0] != "TC-3" || a.Items[0].Ask != "Run it on an iPhone" {
		t.Errorf("items = %+v, want TC-3 from the auditor", a.Items)
	}
}

// An auditor verdict with no ask: the comment it left for a person says what it needs.
func TestAttentionOfAnAuditorBlockFallsBackToItsComment(t *testing.T) {
	d, store, tk := blockedTesting(t, developerContinueVerdict)
	if _, err := store.AddComment(t.Context(), tk.ID, SourceAuditor, "auditor",
		"Decide whether `pnpm lint` hanging on the whole project blocks this PR."); err != nil {
		t.Fatal(err)
	}
	tk = auditorBlocks(t, d, store, tk, "---\nverdict: block\n---\n# Notes\nLint hangs.\n")
	a, _ := d.Attention(t.Context(), tk)
	if a == nil || !strings.Contains(a.Headline, "pnpm lint") || strings.Contains(a.Headline, "I changed no code") {
		t.Fatalf("attention = %+v, want the auditor's comment", a)
	}
	if len(a.Items) != 1 || !strings.Contains(a.Items[0].Ask, "pnpm lint") {
		t.Errorf("items = %+v, want the auditor's comment", a.Items)
	}
}

// An auditor that failed stopped the task, not the developer's verdict.
func TestAttentionOfAFailedAuditorIgnoresTheAgentsSummary(t *testing.T) {
	d, store, tk := blockedTesting(t, developerContinueVerdict)
	d.block(t.Context(), tk.ID, "testing", BlockInfo{Cause: BlockAuditorFailed, Agent: "tester"})
	a, _ := d.Attention(t.Context(), mustGet(t, store, tk.ID))
	if a == nil || a.Kind != AttentionFailure || strings.Contains(a.Headline, "I changed no code") {
		t.Fatalf("attention = %+v, want the auditor's failure", a)
	}
}
