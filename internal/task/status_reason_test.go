package task

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestNeedsPersonMatchesTheBoard(t *testing.T) {
	needs := []RunStatus{
		StatusAwaitingInput, StatusAwaitingApproval, StatusFailed, StatusOutOfTurns, StatusNeedsMoreTurns,
		StatusBlocked, StatusNotStarted, StatusCredentialBlocked, StatusNoAgent, StatusResumeFailed,
	}
	for _, s := range needs {
		if !s.NeedsPerson() {
			t.Errorf("%s: NeedsPerson = false, want true", s)
		}
	}
	for _, s := range []RunStatus{StatusIdle, StatusRunning, StatusQueued, StatusStopped, ""} {
		if s.NeedsPerson() {
			t.Errorf("%q: NeedsPerson = true, want false", s)
		}
	}
	for _, s := range []RunStatus{StatusAwaitingInput, StatusAwaitingApproval} {
		if s.Stalled() {
			t.Errorf("%s is listed by the inbox as a question or an approval, not as stalled", s)
		}
	}
}

func TestStalledListsEveryStatusThatWaits(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	want := map[string]RunStatus{}
	for _, s := range []RunStatus{StatusNotStarted, StatusNoAgent, StatusBlocked, StatusResumeFailed} {
		tk := relaunchable(t, store)
		d.setStatus(context.Background(), tk.ID, s)
		want[tk.ID] = s
	}
	idle := relaunchable(t, store)
	stopped := relaunchable(t, store)
	d.setStatus(context.Background(), stopped.ID, StatusStopped)
	archived := relaunchable(t, store)
	d.setStatus(context.Background(), archived.ID, StatusFailed)
	archive(t, store, archived.ID)

	stalled, err := store.ListStalledTasks(t.Context(), testWS, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RunStatus{}
	for _, st := range stalled {
		got[st.TaskID] = st.Status
	}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("task in %s: listed as %q", s, got[id])
		}
	}
	if _, ok := got[idle.ID]; ok {
		t.Error("an idle task is listed as stalled")
	}
	if _, ok := got[stopped.ID]; ok {
		t.Error("a stopped task is listed as stalled")
	}
	if _, ok := got[archived.ID]; ok {
		t.Error("an archived failed task is listed as stalled: it is off the board")
	}
}

func archive(t *testing.T, store *Store, id string) {
	t.Helper()
	yes := true
	if _, err := store.Update(t.Context(), id, Patch{Archived: &yes}); err != nil {
		t.Fatalf("archive: %v", err)
	}
}

func TestStatusReasonCoversQueuedStoppedAndOutOfTurns(t *testing.T) {
	cases := []struct {
		status RunStatus
		note   Note
	}{
		{StatusQueued, NoteOf("task.comments.run_queued", "agent", "developer", "stage", "Build", "reason", "x")},
		{StatusStopped, NoteOf("task.comments.run_stopped", "stage", "Build", "who", "Ana", "count", 0)},
		{StatusOutOfTurns, NoteOf("task.comments.run_out_of_turns", "count", 30)},
		{StatusNeedsMoreTurns, NoteOf("task.comments.auditor_relaunch_limit", "max", 3)},
	}
	for _, tc := range cases {
		d, store, _ := newConvDispatcher(t)
		tk := relaunchable(t, store)
		d.systemComment(context.Background(), tk.ID, tc.note)
		d.setStatus(context.Background(), tk.ID, tc.status)
		got, err := store.Get(i18n.WithLocale(context.Background(), "es"), tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if want := tc.note.Render("es"); got.StatusReason != want {
			t.Errorf("%s reason = %q, want %q", tc.status, got.StatusReason, want)
		}
	}
}

func TestOutOfTurnsRunSaysHowManyTurns(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := relaunchable(t, store)
	instanceID := "developer-task-" + tk.ID + "-in_progress-1727500000000"
	outDir := filepath.Join(t.TempDir(), "runs", instanceID)
	writeRunFile(t, outDir, "session.log", initLine+"\n"+
		`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":30,"result":""}`+"\n")

	d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instanceID, outDir, 1,
		postRunOpts{NoAdvance: true})

	if s := statusOf(t, store, tk.ID); s != StatusOutOfTurns {
		t.Fatalf("status = %q, want out_of_turns", s)
	}
	got, _ := store.Get(i18n.WithLocale(context.Background(), "es"), tk.ID)
	if want := i18n.T("es", "task.comments.run_out_of_turns", "count", 30); !strings.HasSuffix(got.StatusReason, want) {
		t.Errorf("reason = %q, want it to end with %q", got.StatusReason, want)
	}
}

func TestFailedRelaunchKeepsTheStatusItStartedFrom(t *testing.T) {
	for _, tc := range []struct {
		status RunStatus
		note   Note
	}{
		{StatusBlocked, NoteOf("task.comments.auditor_block")},
		{StatusResumeFailed, NoteOf("task.questions.comment.resume_failed", "err", "x")},
		{StatusOutOfTurns, NoteOf("task.comments.run_out_of_turns", "count", 30)},
	} {
		d, store, _ := newConvDispatcher(t)
		d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) { return expiredFault(), nil })
		tk := relaunchable(t, store)
		d.systemComment(context.Background(), tk.ID, tc.note)
		d.setStatus(context.Background(), tk.ID, tc.status)
		tk, _ = store.Get(t.Context(), tk.ID)

		err := d.RelaunchStage(context.Background(), tk, 0)
		var ns *NotStartedError
		if !errors.As(err, &ns) || ns.Cause != CauseCredential {
			t.Fatalf("%s: RelaunchStage = %v, want a NotStartedError for a credential", tc.status, err)
		}
		got, _ := store.Get(i18n.WithLocale(context.Background(), "en"), tk.ID)
		if got.Status != tc.status {
			t.Errorf("status = %q after a failed relaunch, want %q kept", got.Status, tc.status)
		}
		if want := tc.note.Render("en"); got.StatusReason != want {
			t.Errorf("%s reason = %q, want the one it had: %q", tc.status, got.StatusReason, want)
		}
		if ev, ok := notStartedEvent(t, store, tk.ID); !ok || ev.Cause != CauseCredential {
			t.Errorf("%s: event = %+v (found %v), want the run card's run_not_started", tc.status, ev, ok)
		}
	}

	d, store, _ := newConvDispatcher(t)
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) { return expiredFault(), nil })
	tk := relaunchable(t, store)
	d.setStatus(context.Background(), tk.ID, StatusNotStarted)
	tk, _ = store.Get(t.Context(), tk.ID)
	_ = d.RelaunchStage(context.Background(), tk, 0)
	if s := statusOf(t, store, tk.ID); s != StatusCredentialBlocked {
		t.Errorf("status = %q, want credential_blocked", s)
	}
}

type refusingConvLauncher struct{ *convLauncher }

func (refusingConvLauncher) LaunchForStage(context.Context, *Task, Stage, *types.AgentType, map[string]string, map[string]string, map[string]string, bool) (string, string, string, error) {
	return "", "", "", errors.New("runtime refused the container")
}

func TestRelaunchThatWaitedThenFailedDoesNotStayQueued(t *testing.T) {
	for _, tc := range []struct {
		status RunStatus
		note   Note
	}{
		{StatusBlocked, NoteOf("task.comments.auditor_block")},
		{StatusResumeFailed, NoteOf("task.questions.comment.resume_failed", "err", "x")},
	} {
		_, store, cl := newConvDispatcher(t)
		d := NewDispatcher(store, refusingConvLauncher{cl}, nil, nil, nil)
		d.slots = newAgentSlots(func() int { return 1 })
		if err := d.slots.acquire(context.Background(), "other-workspace"); err != nil {
			t.Fatal(err)
		}
		tk := relaunchable(t, store)
		d.systemComment(context.Background(), tk.ID, tc.note)
		d.setStatus(context.Background(), tk.ID, tc.status)
		tk, _ = store.Get(t.Context(), tk.ID)

		if err := d.RelaunchStage(context.Background(), tk, 0); err != nil {
			t.Fatalf("%s: RelaunchStage = %v, want nil while it waits for a slot", tc.status, err)
		}
		if s := statusOf(t, store, tk.ID); s != StatusQueued {
			t.Fatalf("%s: status = %q while waiting, want queued", tc.status, s)
		}
		d.slots.release("other-workspace")
		waitFor(t, func() bool { _, ok := notStartedEvent(t, store, tk.ID); return ok })
		waitFor(t, func() bool { return statusOf(t, store, tk.ID) != StatusQueued })

		got, _ := store.Get(i18n.WithLocale(context.Background(), "en"), tk.ID)
		if got.Status != tc.status {
			t.Errorf("status = %q after the wait and a failed launch, want %q back", got.Status, tc.status)
		}
		if !strings.Contains(got.StatusReason, "runtime refused the container") {
			t.Errorf("%s reason = %q, want the launch failure, not the stale queued note", tc.status, got.StatusReason)
		}
		if d.waitingSlot(tk.ID) {
			t.Errorf("%s: the task still waits for a slot", tc.status)
		}
	}
}

func TestNoAgentReasonNamesTheFault(t *testing.T) {
	missing := noAgentReason(CauseAgentMissing, "fantasma", "Desarrollo")
	if got := i18n.T("es", "task.errors.stage_agent_missing", "agent", "fantasma", "stage", "Desarrollo"); !strings.Contains(missing.Error(), "fantasma") ||
		got == i18n.T("es", "task.errors.stage_has_no_agent", "stage", "Desarrollo") {
		t.Errorf("agent_missing reason = %q", missing)
	}
	invalid := noAgentReason(CauseAgentInvalid, "revisor", "Desarrollo")
	if !strings.Contains(invalid.Error(), "revisor") {
		t.Errorf("agent_invalid reason = %q, want the agent named", invalid)
	}
	if none := noAgentReason(CauseNoAgent, "", "Desarrollo"); !strings.Contains(none.Error(), "Desarrollo") {
		t.Errorf("no_agent reason = %q", none)
	}
}

func TestAnswerResumeThatDidNotStartSaysTheAnswersAreSaved(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) { return expiredFault(), nil })
	tk := relaunchable(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", l.agent, launchOpts{Kind: LaunchAnswerResume, Message: "a"})

	if s := statusOf(t, store, tk.ID); s != StatusCredentialBlocked {
		t.Fatalf("status = %q, want credential_blocked", s)
	}
	got, _ := store.Get(i18n.WithLocale(context.Background(), "es"), tk.ID)
	saved := strings.SplitN(i18n.T("es", "task.questions.comment.resume_failed", "err", "{err}"), "{err}", 2)[0]
	if !strings.HasPrefix(got.StatusReason, saved) || !strings.Contains(got.StatusReason, "prod-aws") {
		t.Errorf("reason = %q, want the resume note naming the credential", got.StatusReason)
	}
}

func TestAnonymousPersonReadsAsSomeone(t *testing.T) {
	n := NoteOf("task.questions.reply.canceled", "author", personArg("user"))
	if got := n.Render("es"); strings.Contains(got, "user") || !strings.Contains(got, i18n.T("es", "task.prompts.someone")) {
		t.Errorf("es = %q, want someone instead of the raw dev-mode author", got)
	}
	if got := NoteOf("task.questions.reply.canceled", "author", personArg("ana@x.io")).Render("en"); !strings.Contains(got, "ana@x.io") {
		t.Errorf("a named person = %q, want the name kept", got)
	}
}
