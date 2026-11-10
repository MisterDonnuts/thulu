package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func auditedDispatcher(t *testing.T) (*Dispatcher, *Store, *convLauncher, *Task) {
	t.Helper()
	d, store, l := newConvDispatcher(t)
	l.agent.Auditor = &types.AuditorSpec{
		Stages:       []string{string(types.StageInProgress)},
		TurnsByStage: map[string]string{string(types.StageInProgress): "4"},
	}
	l.agent.MCPByStage = map[string][]string{string(types.StageInProgress): {"thulu-github"}}
	tk := &Task{WorkspaceID: testWS, Title: "audited", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return d, store, l, full
}

func hasEvent(t *testing.T, store *Store, taskID string, kind EventKind) bool {
	t.Helper()
	events, err := store.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func TestAuditorRunsInItsOwnContainer(t *testing.T) {
	d, store, l, tk := auditedDispatcher(t)
	ctx := context.Background()

	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	launches := l.launches()
	if len(launches) != 1 {
		t.Fatalf("launches = %d, want the agent's", len(launches))
	}
	agentEnv := launches[0]
	for _, k := range []string{"AUDITOR_TURNS", "AUDITOR_MODEL", "AUDIT_ONLY"} {
		if v, ok := agentEnv[k]; ok {
			t.Errorf("agent container carries %s=%q", k, v)
		}
	}

	l.exit(t, "ctr-a")
	launches = l.launches()
	if len(launches) != 2 {
		t.Fatalf("launches after the agent exited = %d, want the auditor too", len(launches))
	}
	audit := launches[1]
	if audit["AUDIT_ONLY"] != "1" || audit["AUDITED_INSTANCE"] != agentEnv["INSTANCE_ID"] ||
		audit["AUDITED_AGENT"] != "developer" || audit["AUDITOR_TURNS"] != "4" {
		t.Errorf("auditor env = %v", audit)
	}
	if _, ok := audit["MCP_TOOLS"]; ok {
		t.Error("the auditor got the agent's tools")
	}
	gotTask, gotStage, ok := parseAuditInstanceID(audit["INSTANCE_ID"])
	if !ok || gotTask != tk.ID || gotStage != types.StageInProgress {
		t.Errorf("auditor instance %q parses as %q/%q/%v", audit["INSTANCE_ID"], gotTask, gotStage, ok)
	}

	mid, _ := store.Get(ctx, tk.ID)
	if mid.CurrentRunID != "ctr-b" || mid.Stage != types.StageInProgress {
		t.Errorf("mid-audit task: run %q stage %q", mid.CurrentRunID, mid.Stage)
	}

	if _, err := store.AddReport(ctx, tk.ID, SourceAuditor, auth.AuditorAgent, ReportKindAuditorFeedback, "",
		"---\nverdict: block\n---\n# Notes\nfalta el test", audit["INSTANCE_ID"]); err != nil {
		t.Fatalf("add report: %v", err)
	}
	l.exit(t, "ctr-b")
	done, _ := store.Get(ctx, tk.ID)
	if done.Status != StatusBlocked {
		t.Errorf("status = %q, want blocked by the verdict", done.Status)
	}
	if !store.HasAuditFinishEventForInstance(ctx, audit["INSTANCE_ID"]) {
		t.Error("no auditor_run_finished event")
	}
}

func TestAgentCannotWriteItsOwnVerdict(t *testing.T) {
	d, store, _, tk := auditedDispatcher(t)
	d.launcher.(*convLauncher).agent.Auditor = nil

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "feedback.md"),
		[]byte("---\nverdict: block\n---\nyo me apruebo"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-x",
		"developer-task-"+tk.ID+"-in_progress-1", dir, 0, postRunOpts{})

	if hasEvent(t, store, tk.ID, EventAuditorVerdict) {
		t.Error("a verdict was read out of the agent's own output dir")
	}
	reports, _ := store.ListReports(t.Context(), tk.ID)
	for _, r := range reports {
		if r.Kind == ReportKindAuditorFeedback {
			t.Errorf("the agent's feedback.md was filed as the auditor's: %+v", r)
		}
	}
	if got, _ := store.Get(t.Context(), tk.ID); got.Status == StatusBlocked {
		t.Error("the agent blocked its own task")
	}
}

func TestContinuationIsNotAudited(t *testing.T) {
	d, _, l, tk := auditedDispatcher(t)
	d.launchStage(context.Background(), tk, types.StageInProgress, "developer", l.agent,
		launchOpts{Session: "sess-1", Message: "what about the test?", NoAdvance: true})
	l.exit(t, "ctr-a")
	if n := len(l.launches()); n != 1 {
		t.Errorf("launches = %d, want no auditor after a continuation", n)
	}
}

func TestParseStageInstanceIDs(t *testing.T) {
	cases := []struct {
		id       string
		task     string
		stage    Stage
		stageRun bool
		auditRun bool
	}{
		{"developer-task-abc123-in_progress-17", "abc123", "in_progress", true, false},
		{"developer-audit-abc123-in_progress-17", "abc123", "in_progress", false, true},
		{"my-task-bot-audit-abc123-review-17", "abc123", "review", false, true},
		{"x-audit-y-task-abc123-review-17", "abc123", "review", true, false},
		{"developer-mention-abc123-c1-17", "", "", false, false},
	}
	for _, c := range cases {
		task, stage, ok := parseTaskInstanceID(c.id)
		if ok != c.stageRun || (ok && (task != c.task || stage != c.stage)) {
			t.Errorf("parseTaskInstanceID(%q) = %q, %q, %v", c.id, task, stage, ok)
		}
		task, stage, ok = parseAuditInstanceID(c.id)
		if ok != c.auditRun || (ok && (task != c.task || stage != c.stage)) {
			t.Errorf("parseAuditInstanceID(%q) = %q, %q, %v", c.id, task, stage, ok)
		}
		if c.stageRun || c.auditRun {
			if got, ok := InstanceTaskID(c.id); !ok || got != c.task {
				t.Errorf("InstanceTaskID(%q) = %q, %v", c.id, got, ok)
			}
		}
	}
}

func TestStoppedAuditDoesNotAdvance(t *testing.T) {
	d, store, l, tk := auditedDispatcher(t)
	ctx := context.Background()
	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exit(t, "ctr-a")

	l.mu.Lock()
	stop := l.exits["ctr-b"]
	l.mu.Unlock()
	if stop == nil {
		t.Fatal("no auditor run to stop")
	}
	stop(143, "", "")

	got, _ := store.Get(ctx, tk.ID)
	if got.Stage != types.StageInProgress {
		t.Errorf("stage = %q after a stopped audit, want it to stay", got.Stage)
	}
	if got.Status == StatusRunning || got.Status == StatusIdle {
		t.Errorf("status = %q, want the stop recorded", got.Status)
	}
}
