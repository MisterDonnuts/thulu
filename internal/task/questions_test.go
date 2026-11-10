package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func ptr[T any](v T) *T { return &v }

func oneQuestion() AskInput {
	return AskInput{
		Kind:    QuestionKindQuestion,
		Context: "Two databases would work.",
		Questions: []AskInputItem{{
			Header: "Database", Question: "Which database?",
			Options: []QuestionOption{{Label: "Postgres", Description: "already in the stack"}, {Label: "SQLite"}},
		}},
	}
}

func TestNormalizeAskRefusesWhatTheLimitsSay(t *testing.T) {
	long := strings.Repeat("x", maxQuestionTextRunes+1)
	cases := map[string]func(in *AskInput){
		"unknown kind": func(in *AskInput) { in.Kind = "poll" },
		"no questions": func(in *AskInput) { in.Questions = nil },
		"five questions": func(in *AskInput) {
			in.Questions = append(in.Questions, in.Questions[0], in.Questions[0], in.Questions[0], in.Questions[0])
		},
		"empty question":           func(in *AskInput) { in.Questions[0].Question = "  " },
		"long header":              func(in *AskInput) { in.Questions[0].Header = "Thirteen chars" },
		"long question":            func(in *AskInput) { in.Questions[0].Question = long },
		"duplicate label":          func(in *AskInput) { in.Questions[0].Options[1].Label = " Postgres " },
		"empty label":              func(in *AskInput) { in.Questions[0].Options[1].Label = "" },
		"seven options":            func(in *AskInput) { in.Questions[0].Options = make([]QuestionOption, 7) },
		"non-blocking, no default": func(in *AskInput) { in.Blocking = ptr(false) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := oneQuestion()
			in.Questions = append([]AskInputItem(nil), in.Questions...)
			in.Questions[0].Options = append([]QuestionOption(nil), in.Questions[0].Options...)
			mutate(&in)
			if _, _, _, err := NormalizeAsk(in); apperr.KindOf(err) != apperr.KindInvalid {
				t.Errorf("err = %v, want Invalid", err)
			}
		})
	}
}

func TestNormalizeAskDefaults(t *testing.T) {
	in := oneQuestion()
	in.Kind = ""
	kind, blocking, p, err := NormalizeAsk(in)
	if err != nil {
		t.Fatalf("NormalizeAsk: %v", err)
	}
	if kind != QuestionKindQuestion || !blocking || !p.Questions[0].AllowOther {
		t.Errorf("kind=%q blocking=%v allow_other=%v, want question, blocking, other allowed", kind, blocking, p.Questions[0].AllowOther)
	}
	in = oneQuestion()
	in.Kind, in.Blocking = QuestionKindBlocker, ptr(false)
	if _, blocking, _, err := NormalizeAsk(in); err != nil || !blocking {
		t.Errorf("blocker: blocking=%v err=%v, want blocking", blocking, err)
	}
	in = oneQuestion()
	in.Questions[0].Options, in.Questions[0].AllowOther = nil, ptr(false)
	if _, _, p, err := NormalizeAsk(in); err != nil || !p.Questions[0].AllowOther {
		t.Errorf("free question: allow_other=%v err=%v, want allowed", p.Questions[0].AllowOther, err)
	}
	in = oneQuestion()
	in.Kind, in.NeedsCredential = QuestionKindBlocker, true
	if _, _, p, err := NormalizeAsk(in); err != nil || !p.NeedsCredential {
		t.Errorf("credential blocker: needs_credential=%v err=%v, want it kept", p.NeedsCredential, err)
	}
	if _, _, p, _ := NormalizeAsk(oneQuestion()); p.NeedsCredential {
		t.Error("a plain question says it needs a credential")
	}
}

func TestNormalizeAnswers(t *testing.T) {
	_, _, p, err := NormalizeAsk(AskInput{Questions: []AskInputItem{
		{Question: "Which?", Options: []QuestionOption{{Label: "A"}, {Label: "B"}}, AllowOther: ptr(false)},
		{Question: "Which ones?", Options: []QuestionOption{{Label: "X"}, {Label: "Y"}}, MultiSelect: true},
	}})
	if err != nil {
		t.Fatalf("NormalizeAsk: %v", err)
	}
	q := &Question{Kind: QuestionKindQuestion, Payload: p}
	bad := map[string]QuestionAnswers{
		"index out of range": {Answers: []QuestionAnswer{{QuestionIndex: 2, Other: "x"}}},
		"unknown option":     {Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"C"}}, {QuestionIndex: 1, Other: "x"}}},
		"two on single":      {Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"A", "B"}}, {QuestionIndex: 1, Other: "x"}}},
		"other not allowed":  {Answers: []QuestionAnswer{{QuestionIndex: 0, Other: "C"}, {QuestionIndex: 1, Other: "x"}}},
		"one unanswered":     {Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"A"}}}},
		"twice":              {Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"A"}}, {QuestionIndex: 0, Selected: []string{"B"}}}},
	}
	for name, in := range bad {
		if _, err := normalizeAnswers(q, in); apperr.KindOf(err) != apperr.KindInvalid {
			t.Errorf("%s: err = %v, want Invalid", name, err)
		}
	}
	ok, err := normalizeAnswers(q, QuestionAnswers{Answers: []QuestionAnswer{
		{QuestionIndex: 1, Selected: []string{"X", "Y"}, Other: "and Z"},
		{QuestionIndex: 0, Selected: []string{"A"}},
	}})
	if err != nil || len(ok.Answers) != 2 || ok.Answers[0].QuestionIndex != 0 {
		t.Errorf("answers = %+v, err %v, want both, in order", ok, err)
	}
	if _, err := normalizeAnswers(q, QuestionAnswers{Note: "do what you think"}); err != nil {
		t.Errorf("note only: %v", err)
	}
	q.Kind = QuestionKindBlocker
	if _, err := normalizeAnswers(q, QuestionAnswers{Answers: []QuestionAnswer{
		{QuestionIndex: 0, Selected: []string{"A"}}, {QuestionIndex: 1, Selected: []string{"X"}},
	}}); apperr.KindOf(err) != apperr.KindInvalid {
		t.Errorf("blocker without text: err = %v, want Invalid", err)
	}
}

func questionTask(t *testing.T, store *Store) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "asks", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	return withSession(t, store, tk.ID, "sess-1", "developer", types.StageInProgress)
}

func ask(t *testing.T, store *Store, tk *Task, runID string, in AskInput) *Question {
	t.Helper()
	q, err := store.AskQuestion(t.Context(), tk, AskTarget{RunID: runID, Agent: "developer", Stage: tk.Stage, SessionID: tk.SessionID}, in)
	if err != nil {
		t.Fatalf("AskQuestion: %v", err)
	}
	return q
}

func TestAskQuestionPostsTheQuestionAsAComment(t *testing.T) {
	_, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-1", oneQuestion())

	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(full.Questions) != 1 || full.Questions[0].ID != q.ID || full.Questions[0].Status != QuestionOpen {
		t.Fatalf("questions = %+v, want the open question", full.Questions)
	}
	var root *Comment
	for i := range full.Comments {
		if full.Comments[i].ID == q.CommentID {
			root = &full.Comments[i]
		}
	}
	if root == nil {
		t.Fatal("no root comment for the question")
	}
	for _, want := range []string{"Which database?", "Postgres", "already in the stack", "SQLite", "Two databases would work."} {
		if !strings.Contains(root.Body, want) {
			t.Errorf("comment %q lacks %q", root.Body, want)
		}
	}
	if root.Source != SourceAgent || root.Author != "developer" {
		t.Errorf("comment signed %s/%s, want the agent", root.Source, root.Author)
	}
	if !hasEvent(t, store, tk.ID, EventQuestionAsked) {
		t.Error("no question_asked event")
	}
}

func TestAskQuestionLimits(t *testing.T) {
	_, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	for range MaxQuestionsPerRun {
		ask(t, store, tk, "ctr-1", oneQuestion())
	}
	_, err := store.AskQuestion(t.Context(), tk, AskTarget{RunID: "ctr-1", Agent: "developer", Stage: tk.Stage}, oneQuestion())
	if apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("fourth ask of a run: err = %v, want RateLimited", err)
	}
	ask(t, store, tk, "ctr-2", oneQuestion())
	ask(t, store, tk, "ctr-2", oneQuestion())
	_, err = store.AskQuestion(t.Context(), tk, AskTarget{RunID: "ctr-3", Agent: "developer", Stage: tk.Stage}, oneQuestion())
	if apperr.KindOf(err) != apperr.KindRateLimited {
		t.Fatalf("sixth open question: err = %v, want RateLimited", err)
	}
}

func startRun(t *testing.T, d *Dispatcher, store *Store, l *convLauncher, tk *Task, runID string) *Task {
	t.Helper()
	before := len(l.launches())
	if err := d.ResumeStage(context.Background(), tk, "go", LaunchAnswerResume); err != nil {
		t.Fatalf("ResumeStage: %v", err)
	}
	waitFor(t, func() bool { return len(l.launches()) == before+1 })
	waitFor(t, func() bool {
		l.recordingLauncher.mu.Lock()
		defer l.recordingLauncher.mu.Unlock()
		return l.exits[runID] != nil
	})
	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if full.CurrentRunID != runID {
		t.Fatalf("current run = %q, want %q", full.CurrentRunID, runID)
	}
	return full
}

func TestBlockingQuestionParksAndTheAnswerResumes(t *testing.T) {
	d, store, l, tk := auditedDispatcher(t)
	tk = withSession(t, store, tk.ID, "sess-1", "developer", types.StageInProgress)
	tk = startRun(t, d, store, l, tk, "ctr-a")
	q := ask(t, store, tk, "ctr-a", oneQuestion())

	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, ".session_id"), []byte("sess-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.exitIn(t, "ctr-a", out)

	parked, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if parked.Status != StatusAwaitingInput {
		t.Fatalf("status = %s, want awaiting_input", parked.Status)
	}
	if n := len(l.launches()); n != 1 {
		t.Fatalf("launches = %d, want no auditor while parked", n)
	}
	if !hasEvent(t, store, tk.ID, EventRunAwaitingInput) {
		t.Error("no run_awaiting_input event")
	}

	answered, err := d.AnswerQuestion(t.Context(), parked, q.ID, QuestionAnswers{
		Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"Postgres"}}},
	}, "ana@example.com")
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answered.Status != QuestionAnswered || answered.AnsweredBy != "ana@example.com" || answered.Delivery != QuestionDeliveryResumed {
		t.Errorf("question = %+v, want answered by ana, resumed", answered)
	}
	waitFor(t, func() bool { return len(l.launches()) == 2 })
	env := l.launches()[1]
	if env[EnvLaunchKind] != string(LaunchAnswerResume) || env["SESSION_ID"] != "sess-2" {
		t.Errorf("resume env kind=%q session=%q, want answer_resume in the parked run's session", env[EnvLaunchKind], env["SESSION_ID"])
	}
	if !strings.Contains(env["RESUME_MESSAGE"], "Postgres") || !strings.Contains(env["RESUME_MESSAGE"], "Which database?") {
		t.Errorf("resume message %q lacks the question and its answer", env["RESUME_MESSAGE"])
	}
	full, _ := store.Get(t.Context(), tk.ID)
	replied := false
	for _, c := range full.Comments {
		if c.ParentID == q.CommentID && c.Source == SourceUser && strings.Contains(c.Body, "Postgres") {
			replied = true
		}
	}
	if !replied {
		t.Error("the answer is not posted in the question's thread")
	}

	waitFor(t, func() bool {
		l.recordingLauncher.mu.Lock()
		defer l.recordingLauncher.mu.Unlock()
		return l.exits["ctr-b"] != nil
	})
	l.exit(t, "ctr-b")
	if n := len(l.launches()); n != 3 {
		t.Errorf("launches = %d, want the auditor after the resumed run", n)
	}
}

func TestAnswerSteersTheLiveRun(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := questionTask(t, store)
	tk = liveRun(t, store, l, tk.ID, "ctr-live")
	d.steerable.Store("ctr-live", true)
	q := ask(t, store, tk, "ctr-live", oneQuestion())

	got, err := d.AnswerQuestion(t.Context(), tk, q.ID, QuestionAnswers{Note: "use SQLite for now"}, "ana@example.com")
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if got.Delivery != QuestionDeliverySteered {
		t.Errorf("delivery = %q, want steered", got.Delivery)
	}
	msgs := l.steeredMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "use SQLite for now") {
		t.Errorf("steered = %v, want the answer", msgs)
	}
	if n := len(l.launches()); n != 0 {
		t.Errorf("launches = %d, want none", n)
	}
	if d.holdForQuestions(t.Context(), tk.ID, "ctr-live") {
		t.Error("holdForQuestions held a run whose answers were steered")
	}
	for locale, heading := range map[string]string{"en": "Answer:", "es": "Respuesta:"} {
		comments, err := store.ListComments(i18n.WithLocale(t.Context(), locale), tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range comments {
			if c.ParentID == q.CommentID && c.Source == SourceUser {
				found = strings.HasPrefix(c.Body, heading) && strings.HasSuffix(c.Body, "use SQLite for now")
			}
		}
		if !found {
			t.Errorf("%s: no reply that opens with %q and keeps the note: %+v", locale, heading, comments)
		}
	}
}

func TestNonBlockingAnswerStaysInTheThread(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := questionTask(t, store)
	in := oneQuestion()
	in.Blocking, in.Default = ptr(false), "Postgres"
	q := ask(t, store, tk, "ctr-gone", in)

	got, err := d.AnswerQuestion(t.Context(), tk, q.ID, QuestionAnswers{
		Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"SQLite"}}},
	}, "ana@example.com")
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if got.Delivery != QuestionDeliveryThread {
		t.Errorf("delivery = %q, want thread", got.Delivery)
	}
	if n := len(l.launches()); n != 0 {
		t.Errorf("launches = %d, want none", n)
	}
	if _, err := d.AnswerQuestion(t.Context(), tk, q.ID, QuestionAnswers{Note: "x"}, "bob"); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("second answer: err = %v, want Conflict", err)
	}
}

func TestListCountsOpenQuestions(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	in := oneQuestion()
	in.Blocking, in.Default = ptr(false), "Postgres"
	ask(t, store, tk, "ctr-1", in)
	answered := ask(t, store, tk, "ctr-1", in)
	if _, err := d.AnswerQuestion(t.Context(), tk, answered.ID, QuestionAnswers{
		Answers: []QuestionAnswer{{QuestionIndex: 0, Selected: []string{"SQLite"}}},
	}, "ana@example.com"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	list, err := store.ListByWorkspace(t.Context(), testWS)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range list {
		if list[i].ID == tk.ID {
			if list[i].OpenQuestions != 1 {
				t.Errorf("open_questions = %d, want 1 (the answered one does not count)", list[i].OpenQuestions)
			}
			return
		}
	}
	t.Fatal("task not listed")
}

func TestThreadReplyAnswersTheQuestion(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	in := oneQuestion()
	in.Blocking, in.Default = ptr(false), "Postgres"
	q := ask(t, store, tk, "ctr-gone", in)

	reply, err := store.AddReply(t.Context(), tk.ID, q.CommentID, SourceUser, "ana@example.com", "MySQL, actually")
	if err != nil {
		t.Fatalf("AddReply: %v", err)
	}
	answered, err := d.AnswerFromThreadReply(t.Context(), tk, reply)
	if err != nil || len(answered) != 1 {
		t.Fatalf("answered = %v, err %v, want the question", answered, err)
	}
	a := answered[0]
	if a.Status != QuestionAnswered || a.AnsweredBy != "ana@example.com" || a.Answers == nil ||
		len(a.Answers.Answers) != 1 || a.Answers.Answers[0].Other != "MySQL, actually" {
		t.Errorf("question = %+v, want answered with the reply as other", a)
	}
	agentReply := &Comment{TaskID: tk.ID, ParentID: q.CommentID, Source: SourceAgent, Author: "developer", Body: "noted"}
	if got, _ := d.AnswerFromThreadReply(t.Context(), tk, agentReply); len(got) != 0 {
		t.Errorf("an agent's reply answered %d question(s)", len(got))
	}
}

func TestCancelOpenQuestionsOnMove(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	q1 := ask(t, store, tk, "ctr-1", oneQuestion())
	q2 := ask(t, store, tk, "ctr-1", oneQuestion())
	canceled := d.CancelOpenQuestions(t.Context(), tk.ID, "ana@example.com", "task.questions.reply.canceled_moved")
	if len(canceled) != 2 {
		t.Fatalf("canceled %d, want 2", len(canceled))
	}
	for _, id := range []string{q1.ID, q2.ID} {
		got, _ := store.GetQuestion(t.Context(), tk.ID, id)
		if got.Status != QuestionCanceled {
			t.Errorf("question %s is %s, want canceled", id, got.Status)
		}
	}
	if !hasEvent(t, store, tk.ID, EventQuestionCanceled) {
		t.Error("no question_canceled event")
	}
}

func TestCancelLastQuestionUnparks(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-gone", oneQuestion())
	d.setStatus(t.Context(), tk.ID, StatusAwaitingInput)

	if _, err := d.CancelQuestion(t.Context(), tk, q.ID, "ana@example.com"); err != nil {
		t.Fatalf("CancelQuestion: %v", err)
	}
	got, _ := store.GetRow(t.Context(), tk.ID)
	if got.Status != StatusIdle {
		t.Errorf("status = %s, want idle", got.Status)
	}
	if n := len(l.launches()); n != 0 {
		t.Errorf("launches = %d, want none", n)
	}
}

func TestReconcileQuestionsResumesParkedTasks(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-gone", oneQuestion())
	d.setStatus(t.Context(), tk.ID, StatusAwaitingInput)
	raw, _ := json.Marshal(QuestionAnswers{Note: "Postgres"})
	if _, err := store.DB().ExecContext(t.Context(), store.rb(
		`UPDATE task_questions SET status = ?, answers = ?, answered_by = ? WHERE id = ?`),
		QuestionAnswered, string(raw), "ana@example.com", q.ID); err != nil {
		t.Fatal(err)
	}
	idle := questionTask(t, store)
	d.setStatus(t.Context(), idle.ID, StatusAwaitingInput)

	d.ReconcileQuestions(t.Context())

	waitFor(t, func() bool { return len(l.launches()) == 1 })
	if env := l.launches()[0]; env[EnvLaunchKind] != string(LaunchAnswerResume) || !strings.Contains(env["RESUME_MESSAGE"], "Postgres") {
		t.Errorf("env = %v, want an answer resume", env)
	}
	got, _ := store.GetQuestion(t.Context(), tk.ID, q.ID)
	if got.Delivery != QuestionDeliveryResumed {
		t.Errorf("delivery = %q, want resumed", got.Delivery)
	}
	if row, _ := store.GetRow(t.Context(), idle.ID); row.Status != StatusIdle {
		t.Errorf("status = %s, want idle", row.Status)
	}
	d.ReconcileQuestions(t.Context())
	if n := len(l.launches()); n != 1 {
		t.Errorf("launches = %d after a second reconcile, want 1", n)
	}
}

func TestHoldForQuestionsYieldsToAResumeAlreadyStarted(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-old", oneQuestion())
	if _, err := store.DB().ExecContext(t.Context(), store.rb(
		`UPDATE task_questions SET status = ?, answers = '{}', delivery = ? WHERE id = ?`),
		QuestionAnswered, QuestionDeliveryResumed, q.ID); err != nil {
		t.Fatal(err)
	}
	if !d.holdForQuestions(t.Context(), tk.ID, "ctr-old") {
		t.Error("the old run's end was not held")
	}
	if d.holdForQuestions(t.Context(), tk.ID, "ctr-without-questions") {
		t.Error("a run that asked nothing was held")
	}
}

func TestListPendingQuestionsIsScopedToTheWorkspace(t *testing.T) {
	_, store, _ := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-1", oneQuestion())
	other := &Task{WorkspaceID: "ws-other", Title: "elsewhere", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	ask(t, store, other, "ctr-2", oneQuestion())
	shelved := questionTask(t, store)
	ask(t, store, shelved, "ctr-3", oneQuestion())
	archive(t, store, shelved.ID)

	got, err := store.ListPendingQuestions(t.Context(), testWS, false)
	if err != nil {
		t.Fatalf("ListPendingQuestions: %v", err)
	}
	if len(got) != 1 || got[0].ID != q.ID || got[0].TaskTitle != "asks" {
		t.Errorf("pending = %+v, want only this workspace's question with its task title", got)
	}
	all, _ := store.ListPendingQuestions(t.Context(), "", true)
	if len(all) != 2 {
		t.Errorf("unrestricted = %d, want 2", len(all))
	}
}

func TestStageChangeCancelsTheQuestionsLeftBehind(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-gone", oneQuestion())
	d.setStatus(t.Context(), tk.ID, StatusAwaitingInput)

	backlog := types.StageBacklog
	if _, err := store.Update(t.Context(), tk.ID, Patch{Stage: &backlog}); err != nil {
		t.Fatalf("move: %v", err)
	}
	got, _ := store.GetQuestion(t.Context(), tk.ID, q.ID)
	if got.Status != QuestionCanceled {
		t.Errorf("question is %s, want canceled", got.Status)
	}
	if row, _ := store.GetRow(t.Context(), tk.ID); row.Status != StatusIdle {
		t.Errorf("status = %s, want idle", row.Status)
	}
	if n := len(l.launches()); n != 0 {
		t.Errorf("launches = %d, want none", n)
	}
}

func TestReconcileRelaunchesAResumeThatNeverStarted(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := questionTask(t, store)
	q := ask(t, store, tk, "ctr-gone", oneQuestion())
	d.setStatus(t.Context(), tk.ID, StatusAwaitingInput)
	raw, _ := json.Marshal(QuestionAnswers{Note: "Postgres"})
	if _, err := store.DB().ExecContext(t.Context(), store.rb(
		`UPDATE task_questions SET status = ?, answers = ?, answered_by = ?, delivery = ? WHERE id = ?`),
		QuestionAnswered, string(raw), "ana@example.com", QuestionDeliveryResumed, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(t.Context(), store.rb(
		`UPDATE tasks SET current_run_id = ? WHERE id = ?`), "ctr-gone", tk.ID); err != nil {
		t.Fatal(err)
	}

	d.ReconcileQuestions(t.Context())

	waitFor(t, func() bool { return len(l.launches()) == 1 })
	if env := l.launches()[0]; !strings.Contains(env["RESUME_MESSAGE"], "Postgres") {
		t.Errorf("env = %v, want the answer resumed", env)
	}
}

func TestImportTaskDropsTheWait(t *testing.T) {
	_, store, _ := newConvDispatcher(t)
	for id, status := range map[string]RunStatus{"imp-q": StatusAwaitingInput, "imp-a": StatusAwaitingApproval} {
		if _, err := store.ImportTask(t.Context(), &Task{ID: id, WorkspaceID: testWS, Title: id, Status: status}); err != nil {
			t.Fatalf("import %s: %v", id, err)
		}
		if row, _ := store.GetRow(t.Context(), id); row.Status != StatusIdle {
			t.Errorf("%s imported as %s, want idle", id, row.Status)
		}
	}
}
