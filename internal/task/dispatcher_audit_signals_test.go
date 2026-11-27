package task

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func signalsDispatcher(t *testing.T) (*Dispatcher, *Store, *convLauncher, *Task) {
	t.Helper()
	d, store, l, tk := auditedDispatcher(t)
	l.agent.Auditor.Mode = types.AuditorModeSignals
	return d, store, l, tk
}

// An output dir with the run's report, so the run does not read as one that wrote nothing.
func reportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeIn(t, dir, "_index.md", "# Done\nchanged the button")
	return dir
}

func eventPayloads(t *testing.T, store *Store, taskID string, kind EventKind) []json.RawMessage {
	t.Helper()
	events, err := store.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []json.RawMessage
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e.Payload)
		}
	}
	return out
}

// The signals recorded on the one auditor run of stage.
func auditorSignalsOf(t *testing.T, store *Store, taskID string, stage Stage) []string {
	t.Helper()
	var found []auditedRun
	for _, raw := range eventPayloads(t, store, taskID, EventAuditorRunStarted) {
		var p struct {
			Stage   string     `json:"stage"`
			Audited auditedRun `json:"audited"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p.Stage == string(stage) {
			found = append(found, p.Audited)
		}
	}
	if len(found) != 1 {
		t.Fatalf("auditor_run_started events for %s = %d, want 1", stage, len(found))
	}
	return found[0].Signals
}

func TestSignalsModeSkipsTheAuditorOfACleanRun(t *testing.T) {
	d, store, l, tk := signalsDispatcher(t)
	ctx := context.Background()

	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exitIn(t, "ctr-a", reportDir(t))

	if n := len(l.launches()); n != 1 {
		t.Fatalf("launches = %d, want no auditor after a clean run", n)
	}
	raw := eventPayloads(t, store, tk.ID, EventAuditorSkipped)
	if len(raw) != 1 {
		t.Fatalf("auditor_skipped events = %d, want 1", len(raw))
	}
	var p struct {
		Stage  string       `json:"stage"`
		Agent  string       `json:"agent"`
		Checks []AuditCheck `json:"checks"`
	}
	if err := json.Unmarshal(raw[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.Stage != string(types.StageInProgress) || p.Agent != "developer" || len(p.Checks) == 0 {
		t.Errorf("payload = %+v", p)
	}
	for _, c := range p.Checks {
		if c.State == AuditSignal {
			t.Errorf("check %+v is a signal in a skipped audit", c)
		}
	}
	got, _ := store.Get(ctx, tk.ID)
	if got.Status == StatusBlocked || got.Status == StatusRunning {
		t.Errorf("status = %q, want the stage settled as after a continue", got.Status)
	}
	if hasEvent(t, store, tk.ID, EventAuditorVerdict) {
		t.Error("a skipped audit recorded a verdict")
	}
}

func TestSignalsModeAuditsARunThatWroteNothing(t *testing.T) {
	d, store, l, tk := signalsDispatcher(t)
	ctx := context.Background()

	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exitIn(t, "ctr-a", t.TempDir())

	if n := len(l.launches()); n != 2 {
		t.Fatalf("launches = %d, want the auditor for a run with no report", n)
	}
	if got := auditorSignalsOf(t, store, tk.ID, types.StageInProgress); !slices.Equal(got, []string{AuditCheckNoOutput}) {
		t.Errorf("signals = %v, want [no_output]", got)
	}
	if hasEvent(t, store, tk.ID, EventAuditorSkipped) {
		t.Error("auditor_skipped recorded for an audited run")
	}
}

func TestSignalsModeAuditsAHighRiskTask(t *testing.T) {
	d, store, l, tk := signalsDispatcher(t)
	ctx := context.Background()
	if err := store.RecordEvent(ctx, tk.ID, EventFlowFlagsSet, "", "agent:refiner", "",
		map[string]any{"risk": "high", "size": "M"}); err != nil {
		t.Fatal(err)
	}

	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exitIn(t, "ctr-a", reportDir(t))

	if n := len(l.launches()); n != 2 {
		t.Fatalf("launches = %d, want the auditor on a high-risk task", n)
	}
	if got := auditorSignalsOf(t, store, tk.ID, types.StageInProgress); !slices.Equal(got, []string{AuditCheckRiskHigh}) {
		t.Errorf("signals = %v, want [risk_high]", got)
	}
}

// The first run after a person relaunched the stage with a reason is audited.
func TestSignalsModeAuditsTheRunAPersonRelaunched(t *testing.T) {
	d, store, l, tk := signalsDispatcher(t)
	ctx := context.Background()

	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	// The relaunch handler records its event once the run has launched.
	if err := store.RecordEvent(ctx, tk.ID, EventRelaunchRequested, "", "you@example.com", "",
		map[string]any{"stage": string(types.StageInProgress), "turns": 20, "comment_id": "c-1"}); err != nil {
		t.Fatal(err)
	}
	l.exitIn(t, "ctr-a", reportDir(t))

	if got := auditorSignalsOf(t, store, tk.ID, types.StageInProgress); !slices.Equal(got, []string{AuditCheckPersonRelaunch}) {
		t.Errorf("signals = %v, want [person_relaunch]", got)
	}
}

// In always mode nothing changes: the auditor runs after a clean run and no skip is recorded.
func TestAlwaysModeAuditsEveryRun(t *testing.T) {
	d, store, l, tk := auditedDispatcher(t)
	ctx := context.Background()

	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exitIn(t, "ctr-a", reportDir(t))

	if n := len(l.launches()); n != 2 {
		t.Fatalf("launches = %d, want the auditor", n)
	}
	if got := auditorSignalsOf(t, store, tk.ID, types.StageInProgress); len(got) != 0 {
		t.Errorf("signals = %v, want none recorded in always mode", got)
	}
	if hasEvent(t, store, tk.ID, EventAuditorSkipped) {
		t.Error("auditor_skipped recorded in always mode")
	}
}

func TestRelaunchedWithReason(t *testing.T) {
	_, store, _, tk := auditedDispatcher(t)
	ctx := context.Background()
	stage := types.StageInProgress
	record := func(kind EventKind, ref string, payload map[string]any) {
		t.Helper()
		if err := store.RecordEvent(ctx, tk.ID, kind, ref, "x", "", payload); err != nil {
			t.Fatal(err)
		}
	}
	if store.RelaunchedWithReason(ctx, tk.ID, stage, "run-1") {
		t.Error("true with no relaunch at all")
	}
	record(EventAgentRunFinished, "run-0", map[string]any{"stage": string(stage)})
	record(EventRelaunchRequested, "", map[string]any{"stage": string(stage), "turns": 20})
	if store.RelaunchedWithReason(ctx, tk.ID, stage, "run-1") {
		t.Error("true for a relaunch without a reason")
	}
	record(EventRelaunchRequested, "", map[string]any{"stage": "review", "comment_id": "c-0"})
	if store.RelaunchedWithReason(ctx, tk.ID, stage, "run-1") {
		t.Error("true for a relaunch of another stage")
	}
	record(EventRelaunchRequested, "", map[string]any{"stage": string(stage), "comment_id": "c-1"})
	// The run's own finish is recorded before the auditor decides; it does not hide the relaunch.
	record(EventAgentRunFinished, "run-1", map[string]any{"stage": string(stage)})
	if !store.RelaunchedWithReason(ctx, tk.ID, stage, "run-1") {
		t.Error("false for the first run after a relaunch with a reason")
	}
	if store.RelaunchedWithReason(ctx, tk.ID, stage, "run-2") {
		t.Error("true for the second run after the relaunch")
	}
}

// The diff checks read the task branch on the daemon host: gate config, suppressions and size.
func TestGatherAuditFactsReadsTheBranchDiff(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	r.commit(t, map[string]string{
		"src/app.ts":      "// eslint-disable-next-line no-console\nconsole.log(1)\nconst a = 2\n",
		".eslintrc.json":  "{}\n",
		"src/app.spec.ts": "it('works', () => {})\n",
	})
	f := auditFacts{Artifacts: 1}
	r.d.gatherAuditFacts(ctx, r.tk, "code", &auditedRun{Agent: "dev", Instance: "inst-1"}, &types.AuditorSpec{MaxDiffLines: 3}, &f)
	if f.Diff == nil || f.Diff.Unread != "" {
		t.Fatalf("diff = %+v, want it read", f.Diff)
	}
	if f.Diff.MaxLines != 3 || f.Diff.MaxFiles != types.DefaultAuditMaxDiffFiles {
		t.Errorf("budget = %d/%d, want 3 lines and the default files", f.Diff.MaxLines, f.Diff.MaxFiles)
	}
	if !slices.Contains(f.Diff.Suppressions, "src/app.ts") {
		t.Errorf("suppressions = %v, want src/app.ts", f.Diff.Suppressions)
	}
	checks := auditSignals(f)
	scope := checkState(t, checks, AuditCheckScope)
	if scope.State != AuditSignal || !strings.Contains(scope.Detail, ".eslintrc.json") || !strings.Contains(scope.Detail, "src/app.ts") {
		t.Errorf("scope = %+v", scope)
	}
	// src/app.ts (3) and .eslintrc.json (1) are production lines; the spec is a test.
	if size := checkState(t, checks, AuditCheckDiffSize); size.State != AuditSignal || size.Detail != "4/3 2/20" {
		t.Errorf("diff_size = %+v, want 4/3 2/20", size)
	}
}

// The flow_flags_set the daemon records when the diff adds "ui" has no risk; the spec's high risk
// still stands after it.
func TestLatestFlowSpecPassesOverTheUIDiffEvent(t *testing.T) {
	_, store, _, tk := auditedDispatcher(t)
	ctx := context.Background()
	if err := store.RecordEvent(ctx, tk.ID, EventFlowFlagsSet, "", "agent:refiner", "",
		map[string]any{"risk": "high", "size": "M", "flow_flags": []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(ctx, tk.ID, EventFlowFlagsSet, "", "octopus", "",
		map[string]any{"flow_flags": []string{"api", "ui"}, "from_diff": []string{"a.tsx"}}); err != nil {
		t.Fatal(err)
	}
	if risk, size := store.LatestFlowSpec(ctx, tk.ID); risk != "high" || size != "M" {
		t.Errorf("LatestFlowSpec = %q, %q, want high, M", risk, size)
	}
}

// Only the skips the run's own agent set call its auditor: a person's skip, or one another agent set
// in an earlier stage, was checked when it was set.
func TestGatherAuditFactsTakesTheRunAgentsSkips(t *testing.T) {
	d, store, _, _ := auditedDispatcher(t)
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, Title: "skips", Stage: "refinement", StageSkips: []StageSkip{
		{Stage: "testing", Reason: "docs only", By: "agent:thulu-refiner"},
		{Stage: "readiness", Reason: "no deploy", By: "ana@x"},
		{Stage: "test_design", Reason: "copy change", By: "agent:thulu-developer"},
	}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	f := auditFacts{Artifacts: 1}
	d.gatherAuditFacts(ctx, tk, "refinement", &auditedRun{Agent: "thulu-refiner", Instance: "inst-1"}, nil, &f)
	if !slices.Equal(f.AgentSkips, []Stage{"testing"}) {
		t.Fatalf("agent skips = %v, want only the refiner's testing", f.AgentSkips)
	}
	if c := checkState(t, auditSignals(f), AuditCheckSkips); c.State != AuditSignal || c.Detail != "testing" {
		t.Errorf("skips = %+v, want a signal naming testing", c)
	}
}
