package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// No stage auto-advances, so a test drives every move itself.
func kickBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "intake", Label: "Intake", Position: 10, Initial: true},
		{ID: "spec", Label: "Spec", Position: 15},
		{ID: "code", Label: "Code", Position: 20, WipGroup: types.WipGroupWork},
		{ID: "verify", Label: "Verify", Position: 30, AgentVerdict: true},
		{ID: "review", Label: "Review", Position: 40,
			RequiredApprovals:  []types.ApprovalRequirement{{Key: "qa", Label: "QA sign-off", ResetOnChange: true}},
			OnChangesRequested: types.ChangesKickBack, ChangesKickBackTo: "code"},
		{ID: "release", Label: "Release", Position: 50, ReleaseResources: true, AgentVerdict: true},
		{ID: "shipped", Label: "Shipped", Position: 60, Terminal: true},
	})
}

// The board settings the dispatcher reads (max_kick_backs) come from BoardList.
type cappedBoards struct {
	stagepkg.Boards
	max int
}

func (c cappedBoards) BoardList() []stagepkg.BoardInfo {
	list := c.Boards.BoardList()
	for i := range list {
		list[i].MaxKickBacks = c.max
	}
	return list
}

func newKickDispatcher(t *testing.T, max int) (*Dispatcher, *Store) {
	t.Helper()
	d, store := newStageTestDispatcher(t, kickBoard())
	d.WithBoards(cappedBoards{Boards: stagepkg.SingleBoard(kickBoard()), max: max})
	return d, store
}

func moveTo(t *testing.T, store *Store, taskID string, stage Stage) *Task {
	t.Helper()
	if _, err := store.Update(t.Context(), taskID, Patch{Stage: &stage}); err != nil {
		t.Fatalf("move to %s: %v", stage, err)
	}
	got, err := store.Get(t.Context(), taskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return got
}

func auditorKickBack(t *testing.T, d *Dispatcher, store *Store, tk *Task, to Stage) *Task {
	t.Helper()
	if !d.applyVerdict(context.Background(), tk, tk.ID, tk.Stage, stageVerdict{Verdict: verdictKickBack, KickBackTo: string(to)}) {
		t.Fatal("a kick_back verdict must end the post-run flow")
	}
	got, _ := store.Get(t.Context(), tk.ID)
	return got
}

func eventsOf(t *testing.T, store *Store, taskID string, kind EventKind) []map[string]any {
	t.Helper()
	evs, err := store.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []map[string]any
	for _, e := range evs {
		if e.Kind == kind {
			var p map[string]any
			_ = json.Unmarshal(e.Payload, &p)
			out = append(out, p)
		}
	}
	return out
}

func TestKickBackTarget(t *testing.T) {
	reg := kickBoard()
	cases := []struct {
		from, requested, want Stage
	}{
		{"verify", "spec", "spec"},
		{"verify", "intake", "intake"}, // an explicit request may name the initial stage
		{"verify", "review", "code"},   // forward: nearest earlier work stage
		{"verify", "verify", "code"},   // itself
		{"verify", "shipped", "code"},  // terminal
		{"verify", "nowhere", "code"},  // unknown
		{"review", "", "code"},         // nearest work stage, not the nearest stage
		{"code", "", "spec"},           // no work stage behind: KickbackDefault
		{"spec", "", ""},               // only the initial stage behind
		{"intake", "", ""},             // nothing behind
		{"ghost", "code", ""},          // unknown origin
	}
	for _, c := range cases {
		if got := kickBackTarget(reg, c.from, c.requested); got != c.want {
			t.Errorf("kickBackTarget(%s, %q) = %q, want %q", c.from, c.requested, got, c.want)
		}
	}

	// A stage that may be skipped is never a fallback.
	skippy := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "intake", Label: "Intake", Position: 10, Initial: true},
		{ID: "code", Label: "Code", Position: 20},
		{ID: "ui", Label: "UI", Position: 30, WipGroup: types.WipGroupWork, SkipUnless: []string{"ui"}},
		{ID: "verify", Label: "Verify", Position: 40},
		{ID: "shipped", Label: "Shipped", Position: 50, Terminal: true},
	})
	if got := kickBackTarget(skippy, "verify", "ui"); got != "code" {
		t.Errorf("skippable target = %q, want code", got)
	}
}

func TestAuditorKickBackNeverForward(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")

	after := auditorKickBack(t, d, store, tk, "review")
	if after.Stage != "code" {
		t.Fatalf("forward kick_back landed in %q, want code", after.Stage)
	}
	moved := eventsOf(t, store, tk.ID, EventStageEntered)
	if len(moved) != 1 || moved[0]["from"] != "verify" || moved[0]["to"] != "code" || moved[0]["via"] != ViaKickBack {
		t.Errorf("stage_entered = %v", moved)
	}
}

func TestKickBackLimitBlocksAtMax(t *testing.T) {
	d, store := newKickDispatcher(t, 2)
	tk := createIn(t, store, "verify", "")

	for i := 1; i <= 2; i++ {
		after := auditorKickBack(t, d, store, tk, "code")
		if after.Stage != "code" {
			t.Fatalf("kick_back %d left the task in %q", i, after.Stage)
		}
		tk = moveTo(t, store, tk.ID, "verify")
	}
	after := auditorKickBack(t, d, store, tk, "code")
	if after.Stage != "verify" || after.Status != StatusBlocked {
		t.Fatalf("third kick_back: stage/status = %q/%q, want verify/blocked", after.Stage, after.Status)
	}
	c := lastComment(t, store, tk.ID)
	if !strings.Contains(c, "2 times") || !strings.Contains(c, "allows 2") || !strings.Contains(c, "the auditor") {
		t.Errorf("limit comment = %q", c)
	}
	limits := eventsOf(t, store, tk.ID, EventKickBackLimit)
	if len(limits) != 1 || limits[0]["to"] != "code" || limits[0]["via"] != ViaKickBack {
		t.Errorf("kick_back_limit events = %v", limits)
	}
}

func TestKickBackLimitCountsChangesRequested(t *testing.T) {
	d, store := newKickDispatcher(t, 2)
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")

	auditorKickBack(t, d, store, tk, "code")
	tk = moveTo(t, store, tk.ID, "review")
	if _, err := d.DecideApproval(ctx, tk, approvalKeyed(t, store, tk.ID, "qa").ID, ApprovalChangesRequested, "ana@x", "redo it"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if after, _ := store.Get(ctx, tk.ID); after.Stage != "code" {
		t.Fatalf("changes requested left the task in %q, want code", after.Stage)
	}
	tk = moveTo(t, store, tk.ID, "verify")
	after := auditorKickBack(t, d, store, tk, "code")
	if after.Stage != "verify" || after.Status != StatusBlocked {
		t.Errorf("auditor after one auditor and one change request: %q/%q, want verify/blocked", after.Stage, after.Status)
	}

	// Over the cap, a change request blocks in place too.
	d2, store2 := newKickDispatcher(t, 1)
	tk2 := createIn(t, store2, "verify", "")
	auditorKickBack(t, d2, store2, tk2, "code")
	tk2 = moveTo(t, store2, tk2.ID, "review")
	if _, err := d2.DecideApproval(ctx, tk2, approvalKeyed(t, store2, tk2.ID, "qa").ID, ApprovalChangesRequested, "ana@x", "again"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	after2, _ := store2.Get(ctx, tk2.ID)
	if after2.Stage != "review" || after2.Status != StatusBlocked {
		t.Errorf("change request over the cap: %q/%q, want review/blocked", after2.Stage, after2.Status)
	}
	if c := lastComment(t, store2, tk2.ID); !strings.Contains(c, "a change request") || !strings.Contains(c, "again") {
		t.Errorf("limit comment = %q, want the source and the note", c)
	}
}

func TestKickBackLimitZeroIsUnlimited(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	for i := 1; i <= 5; i++ {
		if after := auditorKickBack(t, d, store, tk, "code"); after.Stage != "code" {
			t.Fatalf("kick_back %d with no cap left the task in %q", i, after.Stage)
		}
		tk = moveTo(t, store, tk.ID, "verify")
	}
}

func TestPersonMoveResetsKickBacks(t *testing.T) {
	for name, reset := range map[string]func(store *Store, taskID string){
		"manual move": func(store *Store, taskID string) {
			_ = store.RecordEvent(context.Background(), taskID, EventStageEntered, "", "user", "",
				map[string]string{"from": "code", "to": "verify", "via": ViaManual, "by_person": "true"})
		},
		"relaunch out of blocked": func(store *Store, taskID string) {
			_ = store.RecordEvent(context.Background(), taskID, EventKickBackReset, "", "ana@x", "", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, store := newKickDispatcher(t, 1)
			tk := createIn(t, store, "verify", "")
			auditorKickBack(t, d, store, tk, "code")
			tk = moveTo(t, store, tk.ID, "verify")
			reset(store, tk.ID)
			if after := auditorKickBack(t, d, store, tk, "code"); after.Stage != "code" {
				t.Errorf("after the reset the kick_back left the task in %q/%q", after.Stage, after.Status)
			}
		})
	}
}

func TestAgentMoveDoesNotReset(t *testing.T) {
	d, store := newKickDispatcher(t, 1)
	tk := createIn(t, store, "verify", "")
	auditorKickBack(t, d, store, tk, "code")
	tk = moveTo(t, store, tk.ID, "verify")
	_ = store.RecordEvent(context.Background(), tk.ID, EventStageEntered, "", "agent:coder", "",
		map[string]string{"from": "code", "to": "verify", "via": ViaManual, "by_person": "false"})
	if after := auditorKickBack(t, d, store, tk, "code"); after.Stage != "verify" || after.Status != StatusBlocked {
		t.Errorf("an agent's own move reset the count: %q/%q", after.Stage, after.Status)
	}
}

func writeVerdict(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "verdict.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const verdictKickBackBody = "---\nverdict: kick_back\nreason: gates_failed\n---\nTC-2 fails: the total is off by one.\nSemgrep: 1 high in pay.go.\n"

func runVerdict(t *testing.T, d *Dispatcher, store *Store, tk *Task, run auditedRun) *Task {
	t.Helper()
	ctx := context.Background()
	if v, ok := d.takeStageVerdict(ctx, tk, tk.Stage, run); ok {
		d.finishStageRun(ctx, tk.ID, tk, tk.Stage, run, v)
	}
	got, _ := store.Get(t.Context(), tk.ID)
	return got
}

func TestStageVerdictKickBackSkipsAuditorAndCounts(t *testing.T) {
	d, store := newKickDispatcher(t, 1)
	tk := createIn(t, store, "verify", "")
	run := auditedRun{Agent: "qa", Instance: "qa-task-" + tk.ID + "-verify-1", OutputDir: writeVerdict(t, verdictKickBackBody)}

	after := runVerdict(t, d, store, tk, run)
	if after.Stage != "code" {
		t.Fatalf("verdict kick_back without kick_back_to landed in %q, want code (nearest work stage)", after.Stage)
	}
	moved := eventsOf(t, store, tk.ID, EventStageEntered)
	if len(moved) != 1 || moved[0]["via"] != ViaVerdict {
		t.Errorf("stage_entered = %v, want one via verdict", moved)
	}
	verdicts := eventsOf(t, store, tk.ID, EventStageVerdict)
	if len(verdicts) != 1 || verdicts[0]["verdict"] != "kick_back" || verdicts[0]["reason"] != "gates_failed" {
		t.Errorf("stage_verdict events = %v", verdicts)
	}
	if n := len(eventsOf(t, store, tk.ID, EventAuditorVerdict)); n != 0 {
		t.Errorf("%d auditor_verdict events, want none", n)
	}
	// One line for people; the target stage reads the body in its brief.
	if c := lastComment(t, store, tk.ID); strings.Contains(c, "TC-2 fails") || !strings.Contains(c, "gates_failed") {
		t.Errorf("kick-back note = %q, want the reason without the quoted body", c)
	}

	// The verdict's send-back counts against max_kick_backs.
	tk = moveTo(t, store, tk.ID, "verify")
	run.Instance = "qa-task-" + tk.ID + "-verify-2"
	if again := runVerdict(t, d, store, tk, run); again.Stage != "verify" || again.Status != StatusBlocked {
		t.Errorf("second verdict over a cap of 1: %q/%q, want verify/blocked", again.Stage, again.Status)
	}
}

// A single-round reason (branch_behind) sends the task back once; the same stage sending it back
// again for the same reason blocks it there instead, under a board cap that would allow it. Another
// reason, and gates_failed twice, still move. A person's relaunch resets the count.
func TestStageVerdictSameSingleRoundReasonTwiceBlocks(t *testing.T) {
	d, store := newKickDispatcher(t, 5)
	tk := createIn(t, store, "verify", "")
	behind := "---\nverdict: kick_back\nkick_back_to: code\nreason: branch_behind\n---\n12 commits behind main.\n"
	run := func(n int, body string) *Task {
		tk = moveTo(t, store, tk.ID, "verify")
		return runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: fmt.Sprintf("qa-task-%s-verify-%d", tk.ID, n),
			OutputDir: writeVerdict(t, body)})
	}
	if got := run(1, behind); got.Stage != "code" {
		t.Fatalf("first branch_behind: %s/%s, want code", got.Stage, got.Status)
	}
	if got := run(2, verdictKickBackBody); got.Stage != "code" {
		t.Fatalf("gates_failed after branch_behind: %s/%s, want code", got.Stage, got.Status)
	}
	if got := run(3, verdictKickBackBody); got.Stage != "code" {
		t.Fatalf("gates_failed twice: %s/%s, want code (only the board cap applies)", got.Stage, got.Status)
	}
	got := run(4, behind)
	if got.Stage != "verify" || got.Status != StatusBlocked {
		t.Fatalf("second branch_behind: %s/%s, want verify/blocked", got.Stage, got.Status)
	}
	want := BlockInfo{Stage: "verify", Agent: "qa", Cause: BlockKickBackRepeated, Reason: "branch_behind"}
	if got.Block == nil || *got.Block != want {
		t.Errorf("block = %+v, want %+v", got.Block, want)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "branch_behind") || !strings.Contains(c, "12 commits behind") {
		t.Errorf("block comment %q", c)
	}
	if n := store.CountVerdictKickBacks(t.Context(), tk.ID, "verify", "branch_behind"); n != 2 {
		t.Errorf("CountVerdictKickBacks = %d, want 2 (the refused one included)", n)
	}

	if err := store.RecordEvent(t.Context(), tk.ID, EventKickBackReset, "", "ana@x", "reset",
		map[string]string{"stage": "verify"}); err != nil {
		t.Fatal(err)
	}
	if n := store.CountVerdictKickBacks(t.Context(), tk.ID, "verify", "branch_behind"); n != 0 {
		t.Errorf("after a reset CountVerdictKickBacks = %d, want 0", n)
	}
	if got := run(5, behind); got.Stage != "code" {
		t.Errorf("branch_behind after a reset: %s/%s, want code", got.Stage, got.Status)
	}
}

// The block note is one line; the body stays in the stage_verdict report and the brief.
func TestStageVerdictBlockNamesTheReason(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	dir := writeVerdict(t, "---\nverdict: block\nreason: spec_test_conflict\n---\nThe spec says 10%, the locked test says 15%.\n")
	after := runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: "qa-task-" + tk.ID + "-verify-1", OutputDir: dir})
	if after.Stage != "verify" || after.Status != StatusBlocked {
		t.Fatalf("block verdict: %q/%q", after.Stage, after.Status)
	}
	got, _ := store.Get(t.Context(), tk.ID)
	// The system's block note names the reason; the stop thread carries what the agent asks (here,
	// with no ask, the verdict's first sentence), written as the agent.
	var note, stop *Comment
	for i := range got.Comments {
		switch c := &got.Comments[i]; {
		case c.Source == SourceSystem && strings.Contains(c.Body, "spec_test_conflict"):
			note = c
		case c.Kind == CommentBlock:
			stop = c
		}
	}
	if note == nil || strings.Contains(note.Body, "The spec says 10%") {
		t.Errorf("block note = %+v, want the reason without the quoted body", note)
	}
	if stop == nil || stop.Author != "qa" || stop.Body != "The spec says 10%, the locked test says 15%." {
		t.Errorf("stop = %+v, want the verdict's first sentence from qa", stop)
	}
	want := BlockInfo{Stage: "verify", Agent: "qa", Cause: BlockVerdict, Reason: "spec_test_conflict"}
	if got.Block == nil || *got.Block != want {
		t.Errorf("block = %+v, want %+v", got.Block, want)
	}
}

func TestStageVerdictContinueAndIgnored(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	ctx := context.Background()

	tk := createIn(t, store, "verify", "")
	cont := auditedRun{Agent: "qa", Instance: "i1", OutputDir: writeVerdict(t, "---\nverdict: continue\n---\nall green\n")}
	if _, ok := d.takeStageVerdict(ctx, tk, "verify", cont); ok {
		t.Error("continue must leave the run to the auditor and the advance")
	}
	if v := eventsOf(t, store, tk.ID, EventStageVerdict); len(v) != 1 || v[0]["verdict"] != "continue" {
		t.Errorf("continue not recorded: %v", v)
	}

	// A failed run, a continuation and a stage without agent_verdict ignore the file.
	failed := auditedRun{Agent: "qa", Instance: "i4", Exit: 1, OutputDir: writeVerdict(t, verdictKickBackBody)}
	cont2 := auditedRun{Agent: "qa", Instance: "i5", NoAdvance: true, OutputDir: failed.OutputDir}
	for _, run := range []auditedRun{failed, cont2} {
		if _, ok := d.takeStageVerdict(ctx, tk, "verify", run); ok {
			t.Errorf("run %s acted on its verdict", run.Instance)
		}
	}
	other := createIn(t, store, "code", "")
	before := len(eventsOf(t, store, other.ID, EventStageVerdict))
	if _, ok := d.takeStageVerdict(ctx, other, "code", auditedRun{Agent: "dev", Instance: "i6", OutputDir: failed.OutputDir}); ok {
		t.Error("a stage without agent_verdict acted on verdict.md")
	}
	if after := len(eventsOf(t, store, other.ID, EventStageVerdict)); after != before {
		t.Error("a stage without agent_verdict recorded a verdict")
	}
}

// A clean exit with no verdict, an unreadable one or a verdict a stage agent may not give (asking for
// more turns) blocks the task where it is: in a stage whose agent decides, no decision is not a pass.
func TestStageVerdictMissingOrMalformedBlocks(t *testing.T) {
	cases := map[string]string{
		"missing":          "",
		"no front matter":  "verdict: kick_back\nThe tests fail on mobile.\n",
		"needs more turns": "---\nverdict: needs_more_turns\n---\n",
		"empty verdict":    "---\nreason: qa_failed\n---\nTC-3 fails\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			d, store := newKickDispatcher(t, 0)
			tk := createIn(t, store, "verify", "")
			dir := t.TempDir()
			if body != "" {
				dir = writeVerdict(t, body)
			}
			after := runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: "qa-task-" + tk.ID + "-verify-1", OutputDir: dir})
			if after.Stage != "verify" || after.Status != StatusBlocked {
				t.Fatalf("no readable verdict left the task %q/%q, want verify/blocked", after.Stage, after.Status)
			}
			want := BlockInfo{Stage: "verify", Agent: "qa", Cause: BlockVerdictMissing}
			if after.Block == nil || *after.Block != want {
				t.Errorf("block = %+v, want %+v", after.Block, want)
			}
			if c := lastComment(t, store, tk.ID); !strings.Contains(c, "without a `verdict.md` that reads") || !strings.Contains(c, "@qa") {
				t.Errorf("missing verdict comment = %q", c)
			}
			if n := len(eventsOf(t, store, tk.ID, EventStageEntered)); n != 0 {
				t.Errorf("%d stage moves, want none", n)
			}
		})
	}
}

// On a real task the runner's outputs upload failed, so verdict.md never reached the daemon. The block
// says the verdict was lost in delivery instead of blaming the agent for not writing it.
func TestStageVerdictLostInDeliveryBlocksWithoutBlamingTheAgent(t *testing.T) {
	for name, rep := range map[string]RunnerReport{
		"upload failed":  {OutputsError: "413 Request Entity Too Large"},
		"files left out": {OutputsSkipped: []string{"videos/tc-1.mp4"}},
	} {
		t.Run(name, func(t *testing.T) {
			d, store := newKickDispatcher(t, 0)
			tk := createIn(t, store, "verify", "")
			instance := "qa-task-" + tk.ID + "-verify-1"
			rep.Runner, rep.RunID, rep.Instance = "mac", "runner-j1", instance
			d.RecordRunnerReport(t.Context(), tk.ID, rep)
			warnings := eventsOf(t, store, tk.ID, EventRunnerWarning)
			if len(warnings) != 1 || warnings[0]["run_id"] != "runner-j1" || warnings[0]["message"] == "" {
				t.Fatalf("runner_warning events = %v, want one with run_id and message", warnings)
			}
			after := runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: instance, OutputDir: t.TempDir()})
			want := BlockInfo{Stage: "verify", Agent: "qa", Cause: BlockVerdictUndelivered}
			if after.Status != StatusBlocked || after.Block == nil || *after.Block != want {
				t.Fatalf("task %s, block %+v; want blocked with %+v", after.Status, after.Block, want)
			}
			if c := lastComment(t, store, tk.ID); strings.Contains(c, "without writing its verdict") || !strings.Contains(c, "did not reach Thulu") {
				t.Errorf("lost verdict comment = %q", c)
			}
		})
	}
	// Another run's delivery problem does not excuse this one.
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	d.RecordRunnerReport(t.Context(), tk.ID, RunnerReport{Runner: "mac", RunID: "runner-j0", Instance: "older", OutputsError: "boom"})
	after := runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: "qa-task-" + tk.ID + "-verify-2", OutputDir: t.TempDir()})
	if after.Block == nil || after.Block.Cause != BlockVerdictMissing {
		t.Errorf("block = %+v, want verdict_missing", after.Block)
	}
}

// Through postStageRun: the auditor does not run and the stage does not advance.
func TestStageVerdictMissingDoesNotAdvance(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "intake", Label: "Intake", Position: 10, Initial: true},
		{ID: "verify", Label: "Verify", Position: 20, AgentVerdict: true, AutoAdvance: true},
		{ID: "review", Label: "Review", Position: 30},
		{ID: "shipped", Label: "Shipped", Position: 40, Terminal: true},
	})
	d, store := newStageTestDispatcher(t, reg)
	tk := createIn(t, store, "verify", "")
	d.postStageRun(t.Context(), tk.ID, "verify", "qa", "ctr-1", "qa-task-"+tk.ID+"-verify-1", t.TempDir(), 0, postRunOpts{})
	got, _ := store.Get(t.Context(), tk.ID)
	if got.Stage != "verify" || got.Status != StatusBlocked {
		t.Fatalf("clean exit without verdict.md: %q/%q, want verify/blocked", got.Stage, got.Status)
	}
}

func TestStageVerdictFromUploadedReport(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	instance := "qa-task-" + tk.ID + "-verify-1"
	if _, err := store.AddReport(t.Context(), tk.ID, SourceAgent, "qa", ReportKindStageVerdict, "",
		"---\nverdict: kick_back\nkick_back_to: spec\nreason: qa_failed\n---\nthe form loses the draft\n", instance); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddReport(t.Context(), tk.ID, SourceAgent, "qa", ReportKindStageVerdict, "",
		"---\nverdict: block\n---\nanother run\n", "qa-task-"+tk.ID+"-verify-9"); err != nil {
		t.Fatal(err)
	}
	after := runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: instance})
	if after.Stage != "spec" {
		t.Errorf("uploaded verdict landed the task in %q/%q, want spec", after.Stage, after.Status)
	}
}

func TestStageVerdictKickBackAfterReleaseBlocks(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "release", "")
	dir := writeVerdict(t, "---\nverdict: kick_back\nkick_back_to: code\nreason: gates_failed\n---\nthe deploy failed its smoke test\n")
	after := runVerdict(t, d, store, tk, auditedRun{Agent: "ops", Instance: "ops-task-" + tk.ID + "-release-1", OutputDir: dir})
	if after.Stage != "release" || after.Status != StatusBlocked {
		t.Errorf("kick_back from a release_resources stage: %q/%q, want release/blocked", after.Stage, after.Status)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "new task") {
		t.Errorf("comment = %q", c)
	}
}

func TestIsStageInstanceFor(t *testing.T) {
	cases := []struct {
		instance, task, stage string
		want                  bool
	}{
		{"qa-task-abc123-verify-17", "abc123", "verify", true},
		{"qa-task-abc123-verify-17", "abc123", "review", false},
		{"qa-task-abc123-verify-17", "zzz", "verify", false},
		{"auditor-audit-abc123-verify-17", "abc123", "verify", false},
		{"", "abc123", "verify", false},
	}
	for _, c := range cases {
		if got := IsStageInstanceFor(c.instance, c.task, c.stage); got != c.want {
			t.Errorf("IsStageInstanceFor(%q, %q, %q) = %v", c.instance, c.task, c.stage, got)
		}
	}
}

func TestNeedsMoreTurnsCountsPerStageEntry(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	started := map[string]string{"stage": "verify", "agent": "qa"}
	for i := 0; i < 3; i++ {
		_ = store.RecordEvent(ctx, tk.ID, EventAgentRunStarted, "r", "agent:qa", "", started)
	}
	d.applyVerdict(ctx, tk, tk.ID, "verify", stageVerdict{Verdict: "needs_more_turns"})
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "limit (3)") {
		t.Fatalf("three runs in this entry should hit the limit: %q", c)
	}

	// Sent back and returned: the stage gets its relaunches again.
	_ = store.RecordEvent(ctx, tk.ID, EventStageEntered, "", "auditor", "",
		map[string]string{"from": "review", "to": "verify", "via": ViaKickBack})
	_ = store.RecordEvent(ctx, tk.ID, EventAgentRunStarted, "r", "agent:qa", "", started)
	d.applyVerdict(ctx, tk, tk.ID, "verify", stageVerdict{Verdict: "needs_more_turns"})
	full, _ := store.Get(ctx, tk.ID)
	saw := false
	for _, c := range full.Comments {
		if strings.Contains(c.Body, "(2/3)") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("after re-entering, the relaunch should be 2/3; comments: %q", lastComment(t, store, tk.ID))
	}
}

func TestPostVerdictArtifactStoresReportOnce(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	dir := writeVerdict(t, verdictKickBackBody)
	d.postVerdictArtifact(context.Background(), tk.ID, "qa", "i1", dir)

	posted := writeVerdict(t, verdictKickBackBody)
	if err := os.WriteFile(filepath.Join(posted, "verdict.posted"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.postVerdictArtifact(context.Background(), tk.ID, "qa", "i2", posted)

	reports, _ := store.ListReports(t.Context(), tk.ID)
	n := 0
	for _, r := range reports {
		if r.Kind == ReportKindStageVerdict {
			n++
			if r.InstanceID != "i1" || r.Source != SourceAgent || r.Author != "qa" {
				t.Errorf("verdict report = %+v", r)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d verdict reports, want 1 (the uploaded one is skipped)", n)
	}
}
