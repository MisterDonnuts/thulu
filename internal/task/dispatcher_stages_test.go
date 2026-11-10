package task

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func customBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true, AutoAdvance: true},
		{ID: "verify", Label: "Verify", Position: 20, AutoAdvance: true},
		{ID: "shipped", Label: "Shipped", Position: 30, Terminal: true},
	})
}

func newStageTestDispatcher(t *testing.T, reg stagepkg.Registry) (*Dispatcher, *Store) {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.WithStages(reg)
	d := NewDispatcher(store, nil, nil, nil, nil).WithStages(reg)
	return d, store
}

func lastComment(t *testing.T, store *Store, taskID string) string {
	t.Helper()
	full, err := store.Get(t.Context(), taskID)
	if err != nil || full == nil {
		t.Fatalf("get task: %v", err)
	}
	if len(full.Comments) == 0 {
		return ""
	}
	return full.Comments[len(full.Comments)-1].Body
}

func lastCommentIn(t *testing.T, store *Store, taskID, locale string) string {
	t.Helper()
	full, err := store.Get(t.Context(), taskID)
	if err != nil || full == nil {
		t.Fatalf("get task: %v", err)
	}
	if len(full.Comments) == 0 {
		return ""
	}
	return full.Comments[len(full.Comments)-1].textFor(locale)
}

func TestCreateUsesCustomInitialStage(t *testing.T) {
	_, store := newStageTestDispatcher(t, customBoard())
	tk := &Task{WorkspaceID: testWS, Title: "x"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.Stage != "build" {
		t.Errorf("default stage = %q, want custom initial 'build'", tk.Stage)
	}
}

func TestAutoAdvanceWalksCustomBoard(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	other := &Task{WorkspaceID: testWS, Title: "other", StageAssignments: map[Stage]string{"verify": "checker"}}
	if err := store.Create(t.Context(), other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "build"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	d.autoAdvanceStage(context.Background(), tk, tk.ID, "build", 0, "")

	after, _ := store.Get(t.Context(), tk.ID)
	if after.Stage != "verify" {
		t.Fatalf("stage after auto-advance = %q, want verify", after.Stage)
	}
	c := lastComment(t, store, tk.ID)
	if !strings.Contains(c, "no agent") || !strings.Contains(c, "**Verify**") {
		t.Errorf("expected park comment naming the column, got %q", c)
	}
	if strings.Contains(c, "stage_assignments") {
		t.Errorf("park comment names an internal field: %q", c)
	}
}

func TestNoAgentCommentNamesSeededColumnInMoversLanguage(t *testing.T) {
	d, store := newStageTestDispatcher(t, stagepkg.NewSnapshot([]types.StageInfo{
		{ID: types.StageBacklog, Label: i18n.T("en", "stage.labels.backlog"), Position: 10, Initial: true, AutoAdvance: true},
		{ID: types.StageInProgress, Label: i18n.T("en", "stage.labels.in_progress"), Position: 20},
		{ID: types.StageDone, Label: i18n.T("en", "stage.labels.done"), Position: 30, Terminal: true},
	}))
	other := &Task{WorkspaceID: testWS, Title: "other", StageAssignments: map[Stage]string{types.StageInProgress: "dev"}}
	if err := store.Create(t.Context(), other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: types.StageBacklog}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	d.autoAdvanceStage(i18n.WithLocale(context.Background(), "es"), tk, tk.ID, types.StageBacklog, 0, "")

	for _, locale := range []string{"es", "en"} {
		c := lastCommentIn(t, store, tk.ID, locale)
		want := "**" + i18n.T(locale, "stage.labels.in_progress") + "**"
		if !strings.Contains(c, want) {
			t.Errorf("%s comment = %q, want it to name the column as %s", locale, c, want)
		}
	}
	if c := lastCommentIn(t, store, tk.ID, "es"); strings.Contains(c, "In progress") {
		t.Errorf("es comment = %q names the column in English", c)
	}
}

func TestNoAgentColumnStaysQuiet(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "build"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	d.autoAdvanceStage(context.Background(), tk, tk.ID, "build", 0, "")

	after, _ := store.Get(t.Context(), tk.ID)
	if after.Stage != "verify" {
		t.Fatalf("stage after auto-advance = %q, want verify", after.Stage)
	}
	if c := lastComment(t, store, tk.ID); c != "" {
		t.Errorf("a column nobody runs agents in got a comment: %q", c)
	}
}

func TestBoardStageHasAgent(t *testing.T) {
	_, store := newStageTestDispatcher(t, customBoard())
	ctx := t.Context()
	self := &Task{WorkspaceID: testWS, Title: "self", StageAssignments: map[Stage]string{"verify": "checker"}}
	if err := store.Create(ctx, self); err != nil {
		t.Fatalf("create: %v", err)
	}
	if has, err := store.BoardStageHasAgent(ctx, testWS, self.BoardID, "verify", self.ID); err != nil || has {
		t.Errorf("own assignment counted: has=%v err=%v", has, err)
	}
	empty := &Task{WorkspaceID: testWS, Title: "empty", StageAssignments: map[Stage]string{"verify": ""}}
	if err := store.Create(ctx, empty); err != nil {
		t.Fatalf("create: %v", err)
	}
	if has, _ := store.BoardStageHasAgent(ctx, testWS, self.BoardID, "verify", empty.ID); !has {
		t.Error("another task's agent for verify not found")
	}
	if has, _ := store.BoardStageHasAgent(ctx, "other-ws", self.BoardID, "verify", empty.ID); has {
		t.Error("an assignment leaked across workspaces")
	}
	if has, _ := store.BoardStageHasAgent(ctx, testWS, self.BoardID, "shipped", empty.ID); has {
		t.Error("an assignment for verify answered for shipped")
	}
}

func TestAutoAdvanceIntoTerminalCleansUp(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())

	blocked := &Task{WorkspaceID: testWS, Title: "downstream"}
	if err := store.Create(t.Context(), blocked); err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "verify"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.AddDependency(t.Context(), blocked.ID, tk.ID, DepHard); err != nil {
		t.Fatalf("add dep: %v", err)
	}
	if isBlocked, _, _ := store.IsBlocked(t.Context(), blocked.ID); !isBlocked {
		t.Fatal("downstream should be blocked while dep is in verify")
	}

	d.autoAdvanceStage(context.Background(), tk, tk.ID, "verify", 0, "")

	after, _ := store.Get(t.Context(), tk.ID)
	if after.Stage != "shipped" {
		t.Fatalf("stage = %q, want shipped", after.Stage)
	}
	if isBlocked, blockers, _ := store.IsBlocked(t.Context(), blocked.ID); isBlocked {
		t.Errorf("downstream still blocked by %v after dep reached custom terminal", blockers)
	}
	events, _ := store.ListEvents(t.Context(), blocked.ID)
	saw := false
	for _, ev := range events {
		if ev.Kind == EventDepResolved {
			saw = true
		}
	}
	if !saw {
		t.Error("no EventDepResolved on the dependent after custom-terminal entry")
	}
}

func TestTaskInDeletedStageIsInert(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := &Task{WorkspaceID: testWS, Title: "zombie", Stage: "ghost_stage"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	d.autoAdvanceStage(context.Background(), tk, tk.ID, "ghost_stage", 0, "")
	after, _ := store.Get(t.Context(), tk.ID)
	if after.Stage != "ghost_stage" {
		t.Errorf("unknown stage advanced to %q, want inert", after.Stage)
	}
	if after.Status != StatusIdle {
		t.Errorf("status = %q, want idle after clean run in unknown stage", after.Status)
	}

	d.HandleStageEntered(context.Background(), after, "build")
	final, _ := store.Get(t.Context(), tk.ID)
	if final.Stage != "ghost_stage" {
		t.Errorf("HandleStageEntered moved the task to %q", final.Stage)
	}
}

func TestKickbackFallsBackToCustomDefault(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "verify"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}

	handled := d.applyAuditorVerdict(context.Background(), tk, tk.ID, "verify",
		auditorVerdict{Verdict: "kick_back", KickBackTo: "no_such_stage"})
	if !handled {
		t.Fatal("kick_back verdict should end the post-run flow")
	}
	after, _ := store.Get(t.Context(), tk.ID)
	if after.Stage != "verify" {
		t.Errorf("kick_back target = %q, want custom default 'verify'", after.Stage)
	}
	evs, err := store.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var moved map[string]string
	for _, e := range evs {
		if e.Kind == EventStageEntered {
			_ = json.Unmarshal(e.Payload, &moved)
		}
	}
	if moved["from"] != "verify" || moved["to"] != "verify" || moved["via"] != "kick_back" {
		t.Errorf("kick_back event payload = %v, want from, to and via", moved)
	}
}

func TestLaunchRefusesMalformedStageID(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "Bad-Stage"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	d.launchStage(context.Background(), tk, tk.Stage, "whoever", &types.AgentType{Name: "whoever"}, launchOpts{})
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "not a valid slug") {
		t.Errorf("expected slug-guard comment, got %q", c)
	}
}

func TestAuditorVerdictReadsUploadedFeedback(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "verify"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	const instance = "dev-task-abc-verify-1"
	body := "---\nverdict: kick_back\nkick_back_to: build\nscore: 3/10\n---\n\n# Notes\nno.\n"
	if _, err := store.AddReport(t.Context(), tk.ID, SourceAuditor, "auditor",
		ReportKindAuditorFeedback, "", body, instance); err != nil {
		t.Fatalf("AddReport: %v", err)
	}

	got := d.auditorVerdict(t.Context(), tk.ID, instance, "")
	if got.Verdict != "kick_back" {
		t.Errorf("verdict = %q, want kick_back", got.Verdict)
	}
	if got.KickBackTo != "build" {
		t.Errorf("kick_back_to = %q, want build", got.KickBackTo)
	}

	if other := d.auditorVerdict(t.Context(), tk.ID, "dev-task-abc-verify-2", ""); other.Verdict != "" {
		t.Errorf("verdict leaked across instances: %+v", other)
	}
}
