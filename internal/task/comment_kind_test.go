package task

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func commentsOfTask(t *testing.T, store *Store, taskID string) []Comment {
	t.Helper()
	list, err := store.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func findComment(comments []Comment, id string) *Comment {
	for i := range comments {
		if comments[i].ID == id {
			return &comments[i]
		}
	}
	return nil
}

func TestCommentKindAndMetaRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := createIn(t, store, types.StageInProgress, "")

	plain, err := store.AddComment(ctx, tk.ID, SourceUser, "ana@x", "hola")
	if err != nil {
		t.Fatal(err)
	}
	meta := &CommentMeta{Action: DecisionSendBack, FromStage: "review", ToStage: "in_progress",
		Cases: []string{"TC-1"}, Secrets: []string{"QA_USER"}}
	decision, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, Source: SourceUser, Author: "ana@x",
		Body: "the login breaks", Kind: CommentDecision, Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, Source: SourceUser, Author: "ana@x",
		Body: "x", Kind: "verdict"}); err == nil {
		t.Error("an unknown kind was stored")
	}

	list := commentsOfTask(t, store, tk.ID)
	if got := findComment(list, plain.ID); got == nil || got.Kind != CommentPlain || got.Meta != nil {
		t.Errorf("plain comment = %+v, want kind comment and no meta", got)
	}
	got := findComment(list, decision.ID)
	if got == nil || got.Kind != CommentDecision || got.Meta == nil {
		t.Fatalf("decision = %+v", got)
	}
	if got.Meta.Action != DecisionSendBack || got.Meta.FromStage != "review" || got.Meta.ToStage != "in_progress" ||
		len(got.Meta.Cases) != 1 || got.Meta.Secrets[0] != "QA_USER" {
		t.Errorf("meta = %+v, want what was posted", got.Meta)
	}

	// A row written before the column existed (no kind given) reads as conversation.
	if _, err := store.DB().ExecContext(ctx, store.rb(
		`INSERT INTO task_comments (id, task_id, source, author, body, created_at) VALUES (?,?,?,?,?,?)`),
		"legacy0000000001", tk.ID, SourceUser, "ana@x", "old", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if old := findComment(commentsOfTask(t, store, tk.ID), "legacy0000000001"); old == nil || old.Kind != CommentPlain {
		t.Errorf("legacy row = %+v, want kind comment", old)
	}
}

func TestCommentKindMigrationIsAdditive(t *testing.T) {
	migrations, err := Migrations(migrate.Everything)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(migrations, func(m migrate.Migration) bool { return m.ID == commentKindMigration.ID }) {
		t.Errorf("task migrations lack %d (kind and meta)", commentKindMigration.ID)
	}
	for _, col := range []string{"ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'comment'",
		"ADD COLUMN IF NOT EXISTS meta TEXT NOT NULL DEFAULT ''"} {
		if !strings.Contains(commentKindMigration.SQL, col) {
			t.Errorf("migration = %q, want %q", commentKindMigration.SQL, col)
		}
	}
}

func TestQuestionCommentIsKindQuestion(t *testing.T) {
	store := newTestStore(t)
	tk := createIn(t, store, types.StageInProgress, "")
	q, err := store.AskQuestion(t.Context(), tk, AskTarget{RunID: "run-1", Agent: "dev", Stage: tk.Stage},
		AskInput{Questions: []AskInputItem{{Header: "DB", Question: "Postgres or MySQL?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if c := findComment(commentsOfTask(t, store, tk.ID), q.CommentID); c == nil || c.Kind != CommentQuestion {
		t.Errorf("question comment = %+v, want kind question", c)
	}
}

func TestStopThreadsResolveWhenTheTaskLeavesTheStage(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := createIn(t, store, types.StageInProgress, "")
	stop, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, Source: SourceAgent, Author: "dev",
		Body: "Need a real account.", Kind: CommentBlock})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := store.OpenStopThread(ctx, tk.ID); id != stop.ID {
		t.Fatalf("open stop = %q, want %q", id, stop.ID)
	}

	// A decision answers it without reopening anything; a person's plain reply would.
	if _, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, ThreadID: stop.ID, Source: SourceUser,
		Author: "ana@x", Body: "relaunching", Kind: CommentDecision,
		Meta: &CommentMeta{Action: DecisionRelaunch}}); err != nil {
		t.Fatal(err)
	}
	if c := findComment(commentsOfTask(t, store, tk.ID), stop.ID); c.ResolvedAt != nil {
		t.Fatal("stop resolved before the task moved")
	}

	// Re-entering the same stage (a redo) keeps it open; entering another resolves it.
	if err := store.RecordEvent(ctx, tk.ID, EventStageEntered, "", "octopus", "",
		map[string]string{"from": string(tk.Stage), "to": string(tk.Stage)}); err != nil {
		t.Fatal(err)
	}
	if id, _ := store.OpenStopThread(ctx, tk.ID); id != stop.ID {
		t.Errorf("a redo of the same stage resolved the stop")
	}
	if err := store.RecordEvent(ctx, tk.ID, EventStageEntered, "", "ana@x", "",
		map[string]string{"from": string(tk.Stage), "to": "review"}); err != nil {
		t.Fatal(err)
	}
	if id, _ := store.OpenStopThread(ctx, tk.ID); id != "" {
		t.Errorf("open stop after a move = %q, want none", id)
	}
	c := findComment(commentsOfTask(t, store, tk.ID), stop.ID)
	if c.ResolvedAt == nil || c.ResolvedBy != "octopus" {
		t.Errorf("stop = %+v, want resolved by octopus", c)
	}

	// A late decision posted in the resolved stop does not reopen it.
	if _, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, ThreadID: stop.ID, Source: SourceUser,
		Author: "ana@x", Body: "skipped", Kind: CommentDecision}); err != nil {
		t.Fatal(err)
	}
	if id, _ := store.OpenStopThread(ctx, tk.ID); id != "" {
		t.Error("a decision reopened the resolved stop")
	}
	// A person's plain reply does.
	if _, err := store.AddReply(ctx, tk.ID, stop.ID, SourceUser, "ana@x", "wait"); err != nil {
		t.Fatal(err)
	}
	if id, _ := store.OpenStopThread(ctx, tk.ID); id != stop.ID {
		t.Error("a person's reply did not reopen the stop")
	}
}

func TestStopResolvesOnceNoCaseIsBlocked(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := createIn(t, store, types.StageInProgress, "")
	cases, err := store.AddTestCases(ctx, tk.ID, "designer", []TestCase{
		{Title: "login with a real account"}, {Title: "signup with Google"}, {Title: "logout"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases[:2] {
		if _, _, err := store.RecordTestCaseRun(ctx, tk.ID, c.Key, TestCaseRun{Verdict: TestCaseBlocked, Author: "tester"}); err != nil {
			t.Fatal(err)
		}
	}
	// A pass on a case that was never blocked is not a reason to resolve anything.
	stop, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, Source: SourceAgent, Author: "tester",
		Body: "Two cases need a real account.", Kind: CommentBlock})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.PostComment(ctx, NewComment{TaskID: tk.ID, Source: SourceAgent, Author: "tester",
		Body: "older stop", Kind: CommentBlock})
	if err != nil {
		t.Fatal(err)
	}
	// A stop raised in another stage is not about these cases.
	if _, err := store.DB().ExecContext(ctx, store.rb(`UPDATE task_comments SET stage = 'review' WHERE id = ?`), other.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordTestCaseRun(ctx, tk.ID, cases[2].Key, TestCaseRun{Verdict: TestCasePass, Author: "tester"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordTestCaseRun(ctx, tk.ID, cases[0].Key, TestCaseRun{Verdict: TestCasePass, Author: "ana@x"}); err != nil {
		t.Fatal(err)
	}
	if c := findComment(commentsOfTask(t, store, tk.ID), stop.ID); c.ResolvedAt != nil {
		t.Fatal("stop resolved while a case is still blocked")
	}
	if _, err := store.WaiveTestCases(ctx, tk.ID, []string{cases[1].Key}, "ana@x", "no Google account", ""); err != nil {
		t.Fatal(err)
	}
	list := commentsOfTask(t, store, tk.ID)
	if c := findComment(list, stop.ID); c.ResolvedAt == nil || c.ResolvedBy != "ana@x" {
		t.Errorf("stop = %+v, want resolved by the person who cleared the last blocked case", c)
	}
	if c := findComment(list, other.ID); c.ResolvedAt != nil {
		t.Error("a stop of another stage was resolved by the test cases")
	}
}

func TestStopAsk(t *testing.T) {
	long := "Give the agent a QA account with a confirmed email. " + strings.Repeat("It needs to log in and see the dashboard ", 12)
	for _, tc := range []struct {
		name, ask, body, want string
		cut                   bool
	}{
		{"ask wins", "Approve the new  copy.", "## Report\nEverything else.", "Approve the new copy.", false},
		{"long ask keeps its first sentence", long, "", "Give the agent a QA account with a confirmed email.", true},
		{"no ask: first sentence of the body", "", "## Summary\n\n- Two cases need a real account. TC-3 and TC-5.\n\nMore.", "Two cases need a real account.", false},
		{"no ask, no body", "", "", "", false},
	} {
		got, cut := stopAsk(tc.ask, tc.body)
		if got != tc.want || cut != tc.cut {
			t.Errorf("%s: stopAsk = %q, %v; want %q, %v", tc.name, got, cut, tc.want, tc.cut)
		}
	}
	if got, _ := stopAsk(strings.Repeat("x", 900), ""); len([]rune(got)) > maxStopAskRunes {
		t.Errorf("a sentence with no end is %d runes, want at most %d", len([]rune(got)), maxStopAskRunes)
	}
}

func TestApprovalOpensAThreadAndItsDecisionAnswersIt(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := createIn(t, store, types.StageInProgress, "")
	a := &Approval{TaskID: tk.ID, Label: "Spec ready", Reason: "check the copy", Stage: tk.Stage, CreatedBy: "agent:dev"}
	if err := store.AddApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	full, err := store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := ApprovalThreadOf(full.Comments, a.ID)
	if root == "" {
		t.Fatal("the approval opened no thread")
	}
	if full.Approvals[0].ThreadID != root {
		t.Errorf("approval thread_id = %q, want %q", full.Approvals[0].ThreadID, root)
	}
	rc := findComment(full.Comments, root)
	// The reason is a blockquote: it needs a paragraph of its own, or "> " shows as text.
	if rc.Source != SourceSystem || !strings.Contains(rc.Body, "Spec ready") || !strings.Contains(rc.Body, "\n\n> check the copy") {
		t.Errorf("thread root = %+v", rc)
	}

	changes, err := store.SetApprovalStatus(ctx, a.ID, ApprovalChangesRequested, "ana@x", "shorter title", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostApprovalDecision(ctx, changes, SourceUser, "ana@x"); err != nil {
		t.Fatal(err)
	}
	list := commentsOfTask(t, store, tk.ID)
	if c := findComment(list, root); c.ResolvedAt != nil {
		t.Error("changes requested resolved the thread")
	}
	reply := list[len(list)-1]
	if reply.ParentID != root || reply.Kind != CommentDecision || reply.Meta == nil ||
		reply.Meta.Action != DecisionRequestChanges || reply.Meta.ApprovalID != a.ID ||
		!strings.Contains(reply.Body, "\n\n> shorter title") || strings.Contains(reply.Body, `\n`) {
		t.Errorf("decision = %+v", reply)
	}

	approved, err := store.SetApprovalStatus(ctx, a.ID, ApprovalApproved, "ana@x", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostApprovalDecision(ctx, approved, SourceUser, "ana@x"); err != nil {
		t.Fatal(err)
	}
	if c := findComment(commentsOfTask(t, store, tk.ID), root); c.ResolvedAt == nil {
		t.Error("an approved approval left its thread open")
	}

	// Sent back to pending (the branch changed), the thread waits again.
	if _, err := store.reopenWhere(ctx, `id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if c := findComment(commentsOfTask(t, store, tk.ID), root); c.ResolvedAt != nil {
		t.Error("a reopened approval left its thread resolved")
	}
}

func TestApprovalThreadIsNotAStatusReason(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := createIn(t, store, types.StageInProgress, "")
	failed := StatusFailed
	if _, err := store.Update(ctx, tk.ID, Patch{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddNote(ctx, tk.ID, "", SourceSystem, "octopus", NoteOf("task.comments.verdict_missing",
		"agent", "dev", "stage", "In progress")); err != nil {
		t.Fatal(err)
	}
	if err := store.AddApproval(ctx, &Approval{TaskID: tk.ID, Label: "Ship it", Stage: tk.Stage}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.StatusReason, "Ship it") || !strings.Contains(got.StatusReason, "verdict.md") {
		t.Errorf("status reason = %q, want the failure note, not the approval thread", got.StatusReason)
	}
}

func eventsOfKind(t *testing.T, store *Store, taskID string, kind EventKind) []WorkflowEvent {
	t.Helper()
	events, err := store.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []WorkflowEvent
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// An agent's block verdict opens the stop thread: the ask as the body, written as the agent, with
// the report and the run in meta. A second block replaces the first; leaving the column resolves it.
func TestBlockVerdictOpensTheStopThread(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "release")
	r.personMove(t, tk, "release")
	r.play(t, tk, "release", "thulu-release", runOutput{out: map[string]string{
		"verdict.md": "---\nverdict: block\nreason: other\nask: Confirm the 210 failed requests are the load test.\n---\n## Health\n\nInconclusive after 30 min.\n",
	}})
	r.wantAt(t, tk, "release", StatusBlocked)
	got := r.get(t, tk.ID)
	stopID := OpenStopThreadOf(got.Comments)
	stop := findComment(got.Comments, stopID)
	if stop == nil || stop.Kind != CommentBlock || stop.Source != SourceAgent || stop.Author != "thulu-release" ||
		stop.Body != "Confirm the 210 failed requests are the load test." {
		t.Fatalf("stop = %+v", stop)
	}
	if stop.Meta == nil || stop.Meta.ReportID == "" || stop.Meta.RunID == "" {
		t.Fatalf("stop meta = %+v, want the report and the run", stop.Meta)
	}
	reports, err := r.store.ListReports(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(reports, func(rep Report) bool {
		return rep.ID == stop.Meta.ReportID && strings.Contains(rep.Body, "Inconclusive")
	}) {
		t.Errorf("report_id %q is not the verdict's report", stop.Meta.ReportID)
	}
	if hasComment(t, r.store, tk.ID, "Inconclusive after 30 min") {
		t.Error("the verdict's report was copied into the discussion")
	}
	if a, _ := r.d.Attention(t.Context(), got); a == nil || a.ThreadID != stopID {
		t.Errorf("attention = %+v, want thread_id %q", a, stopID)
	}
	if w := eventsOfKind(t, r.store, tk.ID, EventRunnerWarning); len(w) != 0 {
		t.Errorf("a short ask logged a warning: %+v", w)
	}

	// Relaunched, it blocks again with an ask too long: the new stop replaces the old one, its body
	// keeps the first sentence, and the run log says it was cut.
	if err := r.d.RelaunchStage(t.Context(), r.get(t, tk.ID), 0); err != nil {
		t.Fatal(err)
	}
	long := "Give the agent a QA account with a confirmed email. " + strings.Repeat("It must log in and see the dashboard ", 15)
	r.play(t, tk, "release", "thulu-release", runOutput{out: map[string]string{
		"verdict.md": "---\nverdict: block\nreason: other\nask: " + long + "\n---\nSame.\n",
	}})
	got = r.get(t, tk.ID)
	newID := OpenStopThreadOf(got.Comments)
	if newID == "" || newID == stopID {
		t.Fatalf("open stop = %q, want a new one", newID)
	}
	if old := findComment(got.Comments, stopID); old.ResolvedAt == nil {
		t.Error("the earlier stop is still open")
	}
	if c := findComment(got.Comments, newID); c.Body != "Give the agent a QA account with a confirmed email." {
		t.Errorf("cut ask = %q", c.Body)
	}
	if w := eventsOfKind(t, r.store, tk.ID, EventRunnerWarning); len(w) != 1 {
		t.Errorf("runner warnings = %d, want one for the cut ask", len(w))
	}

	r.personMove(t, tk, "done")
	if id := OpenStopThreadOf(r.get(t, tk.ID).Comments); id != "" {
		t.Errorf("open stop after leaving the column = %q", id)
	}
}
