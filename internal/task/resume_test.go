package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type logLauncher struct {
	*convLauncher
	logs   map[string]string
	exited []ExitedTaskAgent
}

func (l *logLauncher) RunLogs(runID string) string { return l.logs[runID] }

func (l *logLauncher) ScanExitedTaskAgents(context.Context) ([]ExitedTaskAgent, error) {
	return l.exited, nil
}

func TestResumeStageResumesTheStagesSessionAndAudits(t *testing.T) {
	d, store, l, tk := auditedDispatcher(t)
	full := withSession(t, store, tk.ID, "sess-7", "developer", types.StageInProgress)

	if err := d.ResumeStage(context.Background(), full, "use Postgres", LaunchAnswerResume); err != nil {
		t.Fatalf("ResumeStage: %v", err)
	}
	waitFor(t, func() bool { return len(l.launches()) == 1 })
	env := l.launches()[0]
	if env["SESSION_ID"] != "sess-7" || env["RESUME_MESSAGE"] != "use Postgres" {
		t.Errorf("env = %v, want the stage's session and the answer", env)
	}
	if env[EnvLaunchKind] != string(LaunchAnswerResume) {
		t.Errorf("%s = %q, want %q", EnvLaunchKind, env[EnvLaunchKind], LaunchAnswerResume)
	}
	waitFor(t, func() bool {
		l.recordingLauncher.mu.Lock()
		defer l.recordingLauncher.mu.Unlock()
		return l.exits["ctr-a"] != nil
	})
	l.exit(t, "ctr-a")
	if n := len(l.launches()); n != 2 {
		t.Errorf("launches = %d, want the auditor after a resume", n)
	}
}

func TestResumeStageStartsFreshWithoutTheStagesSession(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{
		WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress,
		StageAssignments: map[Stage]string{types.StageInProgress: "developer"},
	}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full := withSession(t, store, tk.ID, "sess-old", "developer", types.StageBacklog)

	if err := d.ResumeStage(context.Background(), full, "rename it", LaunchChangesResume); err != nil {
		t.Fatalf("ResumeStage: %v", err)
	}
	waitFor(t, func() bool { return len(l.launches()) == 1 })
	env := l.launches()[0]
	if sid, ok := env["SESSION_ID"]; ok {
		t.Errorf("SESSION_ID = %q, want none: the session belongs to another stage", sid)
	}
	if env["RESUME_MESSAGE"] != "rename it" {
		t.Errorf("RESUME_MESSAGE = %q, want the request even without a session", env["RESUME_MESSAGE"])
	}
	if env[EnvLaunchKind] != string(LaunchChangesResume) {
		t.Errorf("%s = %q", EnvLaunchKind, env[EnvLaunchKind])
	}
}

func TestResumeStageRefuses(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	ctx := context.Background()

	if err := d.ResumeStage(ctx, full, "hola", LaunchContinuation); apperr.KindOf(err) != apperr.KindInvalid {
		t.Errorf("continuation kind: %v, want Invalid", err)
	}
	if err := d.ResumeStage(ctx, full, "  ", LaunchAnswerResume); apperr.KindOf(err) != apperr.KindInvalid {
		t.Errorf("empty message: %v, want Invalid", err)
	}
	if err := d.ResumeStage(ctx, full, "hola", LaunchAnswerResume); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("no session and no assigned agent: %v, want Precondition", err)
	}
	running := liveRun(t, store, l, tk.ID, "ctr-live")
	if err := d.ResumeStage(ctx, running, "hola", LaunchAnswerResume); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("run alive: %v, want Conflict", err)
	}
	if n := len(l.launches()); n != 0 {
		t.Errorf("launches = %d after refusals, want 0", n)
	}
}

func TestCaptureSessionPrefersTheAdaptersExport(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".engine-session"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".engine-session", "id"), []byte("sess-adapter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".session_id"), []byte("sess-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.captureSession(t.Context(), tk.ID, "", types.StageInProgress, "developer", dir, "ctr-x")
	if got, _ := store.Get(t.Context(), tk.ID); got.SessionID != "sess-adapter" {
		t.Errorf("SessionID = %q, want the adapter's", got.SessionID)
	}

	if err := os.WriteFile(filepath.Join(dir, ".engine-session", "id"), []byte("../../etc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.captureSession(t.Context(), tk.ID, "", types.StageInProgress, "developer", dir, "ctr-x")
	if got, _ := store.Get(t.Context(), tk.ID); got.SessionID != "sess-legacy" {
		t.Errorf("SessionID = %q, want the fallback file over a malformed export", got.SessionID)
	}
}

func TestCaptureSessionFallsBackToTheRunLog(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	ll := &logLauncher{convLauncher: l, logs: map[string]string{
		"ctr-k": `{"type":"system","subtype":"init","session_id":"sess-init","model":"m"}
{"type":"result","subtype":"success","result":"ok","session_id":"sess-final"}
`,
	}}
	d.launcher = ll
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	d.captureSession(t.Context(), tk.ID, "", types.StageInProgress, "developer", "", "ctr-k")
	if got, _ := store.Get(t.Context(), tk.ID); got.SessionID != "sess-final" {
		t.Errorf("SessionID = %q, want the last one the stream reported", got.SessionID)
	}
}

func TestReconcileRebuildsTheRunsKindFromItsLabel(t *testing.T) {
	for _, tc := range []struct {
		kind     LaunchKind
		audited  bool
		describe string
	}{
		{LaunchContinuation, false, "continuation"},
		{LaunchAnswerResume, true, "answer resume"},
		{"", true, "run from before the label"},
	} {
		t.Run(tc.describe, func(t *testing.T) {
			d, store, l, tk := auditedDispatcher(t)
			instance := "developer-task-" + tk.ID + "-" + string(types.StageInProgress) + "-1700000000000"
			d.launcher = &logLauncher{convLauncher: l, exited: []ExitedTaskAgent{{
				ContainerID: "ctr-orphan", InstanceID: instance, AgentType: "developer", LaunchKind: tc.kind,
			}}}
			d.Reconcile(context.Background())

			if !store.HasFinishEventForInstance(t.Context(), instance) {
				t.Fatal("the orphan was not replayed")
			}
			if got := len(l.launches()) == 1; got != tc.audited {
				t.Errorf("auditor launched = %v, want %v", got, tc.audited)
			}
		})
	}
}

func TestLaunchKindPostRunOpts(t *testing.T) {
	if o := ParseLaunchKind("continuation").postRunOpts(); !o.NoAdvance || !o.NoAudit {
		t.Errorf("continuation opts = %+v, want neither advance nor audit", o)
	}
	for _, k := range []string{"stage", "answer_resume", "changes_resume", "", "bogus"} {
		if o := ParseLaunchKind(k).postRunOpts(); o.NoAdvance || o.NoAudit {
			t.Errorf("%q opts = %+v, want a stage run's", k, o)
		}
	}
	if got := ParseLaunchKind("bogus"); got != LaunchStage {
		t.Errorf("ParseLaunchKind(bogus) = %q, want stage", got)
	}
}
