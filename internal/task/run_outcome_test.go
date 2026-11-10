package task

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestNoteReadsInTheReadersLanguage(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := gatedTask(t, store)
	n := NoteOf("task.comments.launch_failed", "err", apperr.New("task.errors.run_in_progress"))
	d.systemComment(context.Background(), tk.ID, n)

	for _, locale := range []string{"en", "es"} {
		ctx := i18n.WithLocale(context.Background(), locale)
		comments, err := store.ListComments(ctx, tk.ID)
		if err != nil {
			t.Fatalf("ListComments: %v", err)
		}
		got := comments[len(comments)-1]
		want := i18n.T(locale, "task.comments.launch_failed", "err", i18n.T(locale, "task.errors.run_in_progress"))
		if got.Body != want {
			t.Errorf("%s body = %q, want %q", locale, got.Body, want)
		}
		if got.Key != "task.comments.launch_failed" {
			t.Errorf("key = %q, want the note's", got.Key)
		}
	}
}

func TestStatusReasonOnListAndGet(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := gatedTask(t, store)
	d.systemComment(context.Background(), tk.ID, NoteOf("task.comments.launch_failed", "err", apperr.New("task.errors.run_in_progress")))
	d.setStatus(context.Background(), tk.ID, StatusNotStarted)

	for _, locale := range []string{"en", "es"} {
		ctx := i18n.WithLocale(context.Background(), locale)
		want := i18n.T(locale, "task.comments.launch_failed", "err", i18n.T(locale, "task.errors.run_in_progress"))
		got, err := store.Get(ctx, tk.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.StatusReason != want {
			t.Errorf("%s Get reason = %q, want %q", locale, got.StatusReason, want)
		}
		list, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, x := range list {
			if x.ID == tk.ID && x.StatusReason != want {
				t.Errorf("%s List reason = %q, want %q", locale, x.StatusReason, want)
			}
		}
	}

	d.setStatus(context.Background(), tk.ID, StatusRunning)
	if got, _ := store.Get(context.Background(), tk.ID); got.StatusReason != "" {
		t.Errorf("a running task has no reason, got %q", got.StatusReason)
	}
}

func TestNoteThen(t *testing.T) {
	a := NoteOf("task.comments.launch_failed", "err", "x")
	b := NoteOf("task.comments.run_engine_api_failed", "message", "y")
	if got, want := a.Then(b).Render("en"), a.Render("en")+" "+b.Render("en"); got != want {
		t.Errorf("Then = %q, want %q", got, want)
	}
	if got := a.Then(Note{}).Render("en"); got != a.Render("en") {
		t.Errorf("Then(empty) = %q", got)
	}
	if got := (Note{}).Then(b); got.Key != b.Key || got.Render("en") != b.Render("en") {
		t.Errorf("empty.Then = %q (key %q)", got.Render("en"), got.Key)
	}
}

func relaunchable(t *testing.T, store *Store) *Task {
	t.Helper()
	tk := &Task{
		WorkspaceID: testWS, Title: "ship it", Stage: types.StageInProgress,
		StageAssignments: map[Stage]string{types.StageInProgress: "developer"},
	}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	return tk
}

func notStartedEvent(t *testing.T, store *Store, taskID string) (notStartedRun, bool) {
	t.Helper()
	events, err := store.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var out notStartedRun
	found := false
	for _, ev := range events {
		if ev.Kind == EventRunNotStarted {
			found = json.Unmarshal(ev.Payload, &out) == nil
		}
	}
	return out, found
}

func TestRelaunchReportsARunThatDidNotStart(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) { return expiredFault(), nil })
	tk := relaunchable(t, store)

	err := d.RelaunchStage(context.Background(), tk, 0)
	var ns *NotStartedError
	if !errors.As(err, &ns) || ns.Cause != CauseCredential {
		t.Fatalf("RelaunchStage = %v, want a NotStartedError for a credential", err)
	}
	if s := statusOf(t, store, tk.ID); s != StatusCredentialBlocked {
		t.Errorf("status = %q, want credential_blocked", s)
	}
	ev, ok := notStartedEvent(t, store, tk.ID)
	if !ok {
		t.Fatal("no run_not_started event")
	}
	if ev.Cause != CauseCredential || ev.Agent != "developer" || ev.Stage != string(types.StageInProgress) {
		t.Errorf("event = %+v", ev)
	}
	if !strings.Contains(ev.ReasonI18n["es"], "prod-aws") || ev.ReasonI18n["es"] == ev.ReasonI18n["en"] {
		t.Errorf("reason_i18n = %v, want it in both languages naming the credential", ev.ReasonI18n)
	}
}

func TestLaunchWithoutModelCredentialDidNotStart(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	d.WithLLMResolver(resolverReturning(secrets.ResolvedLLM{}, errors.New("decrypt: bad key")), nil, nil)
	tk := relaunchable(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", l.agent, launchOpts{})

	if s := statusOf(t, store, tk.ID); s != StatusNotStarted {
		t.Errorf("status = %q, want not_started", s)
	}
	if ev, ok := notStartedEvent(t, store, tk.ID); !ok || ev.Cause != CauseNoLLMProfile {
		t.Errorf("event = %+v (found %v), want cause %q", ev, ok, CauseNoLLMProfile)
	}
}

func TestContinuationThatDidNotStartKeepsTheStatus(t *testing.T) {
	if _, ok := notStartedStatus(LaunchContinuation, CauseRuntime); ok {
		t.Error("a continuation must not change the task's status")
	}
	if s, _ := notStartedStatus(LaunchAnswerResume, CauseRuntime); s != StatusResumeFailed {
		t.Errorf("a resume that did not start = %q, want resume_failed", s)
	}
	if s, _ := notStartedStatus(LaunchStage, CauseNoAgent); s != StatusNoAgent {
		t.Errorf("no agent = %q, want no_agent", s)
	}
}

func TestStageWithoutAgentShowsOnTheCard(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.launcher = newRecordingLauncher()
	other := &Task{WorkspaceID: testWS, Title: "other", StageAssignments: map[Stage]string{"verify": "checker"}}
	if err := store.Create(t.Context(), other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	tk := gatedTask(t, store)

	d.HandleStageEntered(context.Background(), tk, "build")

	if s := statusOf(t, store, tk.ID); s != StatusNoAgent {
		t.Errorf("status = %q, want no_agent", s)
	}
	body := lastComment(t, store, tk.ID)
	if !strings.Contains(body, "**Verify**") || strings.Contains(body, "stage_assignments") {
		t.Errorf("note = %q, want the stage's name and no internal field", body)
	}
	if ev, ok := notStartedEvent(t, store, tk.ID); !ok || ev.Cause != CauseNoAgent {
		t.Errorf("event = %+v (found %v)", ev, ok)
	}
}

func TestNoAgentReasonNamesASeededColumnInTheReadersLanguage(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "backlog", Label: "Backlog", Position: 10, Initial: true},
		{ID: "in_progress", Label: "In progress", Position: 20},
		{ID: "revision", Label: "Revisión", Position: 30},
		{ID: "done", Label: "Done", Position: 40, Terminal: true},
	})
	d, store := newStageTestDispatcher(t, reg)
	d.launcher = newRecordingLauncher()
	other := &Task{WorkspaceID: testWS, Title: "other", StageAssignments: map[Stage]string{"in_progress": "checker"}}
	if err := store.Create(t.Context(), other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	tk := &Task{WorkspaceID: testWS, Title: "ship it", Stage: "in_progress"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	d.HandleStageEntered(context.Background(), tk, "in_progress")

	ev, ok := notStartedEvent(t, store, tk.ID)
	if !ok {
		t.Fatal("no run_not_started event")
	}
	if got := ev.ReasonI18n["es"]; !strings.HasPrefix(got, "En curso ") {
		t.Errorf("es reason = %q, want the column as En curso", got)
	}
	if got := ev.ReasonI18n["en"]; !strings.HasPrefix(got, "In progress ") {
		t.Errorf("en reason = %q, want the column as In progress", got)
	}

	names := d.stageNames(tk, []Stage{"in_progress", "revision"})
	if got := apperr.Localize(apperr.New("task.comments.agent_invalid_for_stage", "agent", "a", "stage", "x", "valid", names), "es"); !strings.Contains(got, "En curso, Revisión") {
		t.Errorf("valid stages = %q, want them as the Spanish reader names them", got)
	}
}

func TestRelaunchAndApprovalNotesNameASeededColumnInTheReadersLanguage(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "backlog", Label: "Backlog", Position: 10, Initial: true},
		{
			ID: "in_progress", Label: "In progress", Position: 20, AutoAdvance: true,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "qa", Label: "QA sign-off"}},
		},
		{ID: "done", Label: "Done", Position: 30, Terminal: true},
	})
	d, store := newStageTestDispatcher(t, reg)
	tk := createIn(t, store, "in_progress", "")

	relaunch := NoteOf("api.tasks.comments.relaunch_requested", "stage", d.StageNote(tk, tk.Stage), "turns", 30)
	if got := relaunch.Render("es"); !strings.Contains(got, "**En curso**") {
		t.Errorf("es relaunch note = %q, want the column as En curso", got)
	}
	if got := relaunch.Render("en"); !strings.Contains(got, "**In progress**") {
		t.Errorf("en relaunch note = %q, want the column as In progress", got)
	}

	d.autoAdvanceStage(context.Background(), tk, tk.ID, "in_progress", 0, "")
	if got := lastCommentIn(t, store, tk.ID, "es"); !strings.Contains(got, "**En curso**") {
		t.Errorf("es approvals note = %q, want the column as En curso", got)
	}
	if got := lastCommentIn(t, store, tk.ID, "en"); !strings.Contains(got, "**In progress**") {
		t.Errorf("en approvals note = %q, want the column as In progress", got)
	}
}

func TestStoppedRunIsNotAFailure(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := relaunchable(t, store)
	d.launchStage(context.Background(), tk, tk.Stage, "developer", l.agent, launchOpts{})
	runID := "ctr-a"
	d.MarkStopped(runID, "Ana")
	l.mu.Lock()
	cb := l.exits[runID]
	l.mu.Unlock()
	if cb == nil {
		t.Fatalf("no OnExit registered for %s", runID)
	}
	cb(137, "", "")

	if s := statusOf(t, store, tk.ID); s != StatusStopped {
		t.Fatalf("status = %q, want stopped", s)
	}
	events, _ := store.ListEvents(t.Context(), tk.ID)
	for _, ev := range events {
		if ev.Kind != EventAgentRunFinished {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		if p["stopped_by"] != "Ana" {
			t.Errorf("finish payload = %v, want stopped_by Ana", p)
		}
		return
	}
	t.Fatal("no agent_run_finished event")
}

func TestRunOutcomeReadsTurns(t *testing.T) {
	dir := t.TempDir()
	writeRunFile(t, dir, "session.log", initLine+"\n"+
		`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":30,"result":""}`+"\n")
	outcome, turns := runOutcome(dir)
	if outcome == "" || turns != 30 {
		t.Errorf("runOutcome = %q, %d; want an outcome and 30 turns", outcome, turns)
	}
	if o, n := runOutcome(filepath.Join(dir, "missing")); o != "" || n != 0 {
		t.Errorf("no log = %q, %d", o, n)
	}
}

func TestRelaunchTurnsAccumulate(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	l.agent.TurnsByStage = map[string]string{string(types.StageInProgress): "30"}
	tk := relaunchable(t, store)
	if got := d.RelaunchTurns(t.Context(), tk); got != 30 {
		t.Errorf("a failed run's retry = %d, want the stage's 30", got)
	}
	if err := store.RecordEvent(t.Context(), tk.ID, EventAgentRunStarted, "i1", "agent:developer", "",
		map[string]string{"stage": string(types.StageInProgress), "max_turns": "60"}); err != nil {
		t.Fatal(err)
	}
	tk.Status = StatusOutOfTurns
	if got := d.RelaunchTurns(t.Context(), tk); got != 120 {
		t.Errorf("more turns after a 60-turn run = %d, want 120", got)
	}
}

func TestSlotWaitShowsAndCancels(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	d.slots = newAgentSlots(func() int { return 1 })
	if err := d.slots.acquire(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	tk := relaunchable(t, store)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.launchStage(context.Background(), tk, tk.Stage, "developer", l.agent, launchOpts{})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for statusOf(t, store, tk.ID) != StatusQueued {
		if time.Now().After(deadline) {
			t.Fatal("the task never showed queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
	q, ok := store.latestQueuedRun(t.Context(), tk.ID)
	if !ok || q.Wait != "install" || !strings.Contains(q.ReasonI18n["en"], "1 of 1") {
		t.Errorf("queued run = %+v, want the install's cap as the reason", q)
	}
	cur, _ := store.Get(t.Context(), tk.ID)
	if err := d.CancelQueued(context.Background(), cur, "Ana"); err != nil {
		t.Fatalf("CancelQueued: %v", err)
	}
	<-done
	if s := statusOf(t, store, tk.ID); s != StatusIdle {
		t.Errorf("status = %q after the canceled wait, want idle", s)
	}
	if n := len(l.launchedTasks()); n != 0 {
		t.Errorf("%d launches after a canceled wait", n)
	}
	cur, _ = store.Get(t.Context(), tk.ID)
	if err := d.CancelQueued(context.Background(), cur, "Ana"); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("second cancel = %v, want Conflict", err)
	}
}

func TestResumeFailureIsListedAsStalled(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := relaunchable(t, store)
	d.resumeFailed(context.Background(), tk, NoteOf("task.questions.comment.resume_failed", "err", "x"),
		apperr.Precondition("task.sessions.errors.no_agent_to_resume", "stage", "x"))
	if s := statusOf(t, store, tk.ID); s != StatusResumeFailed {
		t.Fatalf("status = %q, want resume_failed", s)
	}
	stalled, err := store.ListStalledTasks(t.Context(), testWS, false)
	if err != nil || len(stalled) != 1 || stalled[0].TaskID != tk.ID {
		t.Fatalf("stalled = %+v, %v", stalled, err)
	}
	if other, _ := store.ListStalledTasks(t.Context(), "another-ws", false); len(other) != 0 {
		t.Errorf("another workspace sees %d stalled tasks", len(other))
	}
}

func TestSlotsFullSaysWhy(t *testing.T) {
	cases := []struct {
		full slotsFull
		key  string
	}{
		{slotsFull{Used: 4, Cap: 4}, "task.errors.slots_full_install"},
		{slotsFull{Workspace: true, Used: 2, Cap: 2}, "task.errors.slots_full_workspace"},
		{slotsFull{Line: 3}, "task.errors.slots_line"},
	}
	for _, tc := range cases {
		if got := apperr.Code(tc.full.err()); got != tc.key {
			t.Errorf("%+v → %q, want %q", tc.full, got, tc.key)
		}
	}
	var got slotsFull
	s := newAgentSlots(func() int { return 1 })
	if err := s.acquire(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = s.acquireOr(ctx, "b", func(f slotsFull) { got = f; cancel() })
	}()
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)
	if got.Workspace || got.Used != 1 || got.Cap != 1 {
		t.Errorf("onWait = %+v, want the install's 1 of 1", got)
	}
}
