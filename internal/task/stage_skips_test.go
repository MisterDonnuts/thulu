package task

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A board with each kind of column a skip meets: refine reads flags.yaml, design and dev may be
// skipped, qa waits on "ui", review is marked no_skip, gate asks for an approval, ship frees the
// worktree.
func taskSkipBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "backlog", Label: "Backlog", Position: 10, Initial: true},
		{ID: "refine", Label: "Refine", Position: 20, AutoAdvance: true, IngestFlags: true},
		{ID: "design", Label: "Design", Position: 30, AutoAdvance: true},
		{ID: "dev", Label: "Dev", Position: 40, AutoAdvance: true, WipGroup: types.WipGroupWork},
		{ID: "qa", Label: "QA", Position: 50, AutoAdvance: true, SkipUnless: []string{"ui"}},
		{ID: "review", Label: "Review", Position: 60, AutoAdvance: true, NoSkip: true},
		{ID: "gate", Label: "Gate", Position: 70, RequiredApprovals: []types.ApprovalRequirement{{Key: "merge", Label: "Merge"}}},
		{ID: "ship", Label: "Ship", Position: 80, ReleaseResources: true},
		{ID: "done", Label: "Done", Position: 90, Terminal: true},
	})
}

var person = SkipActor{By: "ana@x"}

func refinerOf(tk *Task) SkipActor {
	return SkipActor{By: "agent:refiner", Agent: true, RunStage: "refine", RunTask: tk.ID}
}

func addSkip(t *testing.T, store *Store, tk *Task, a SkipActor, stage Stage, reason string) SkipsEdit {
	t.Helper()
	res, err := store.UpsertStageSkips(t.Context(), tk.ID, a, []StageSkip{{Stage: stage, Reason: reason}}, SkipSourceAPI)
	if err != nil {
		t.Fatalf("add skip %s: %v", stage, err)
	}
	if len(res.Refused) > 0 {
		t.Fatalf("add skip %s refused: %v", stage, res.Refused[0].Err)
	}
	return res
}

func skipStages(skips []StageSkip) []Stage {
	out := []Stage{}
	for _, sk := range skips {
		out = append(out, sk.Stage)
	}
	return out
}

func rowOf(t *testing.T, store *Store, id string) *Task {
	t.Helper()
	got, err := store.GetRow(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStageSkipsRoundTrip(t *testing.T) {
	_, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "backlog", "")
	if tk.StageSkips == nil || len(tk.StageSkips) != 0 {
		t.Fatalf("a new task's skips = %#v, want []", tk.StageSkips)
	}
	raw, err := json.Marshal(tk)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"stage_skips":[]`) {
		t.Errorf("task JSON lacks stage_skips: []: %s", raw)
	}

	addSkip(t, store, tk, person, "design", "copy change, nothing to test")
	got := rowOf(t, store, tk.ID)
	if len(got.StageSkips) != 1 || got.StageSkips[0].Stage != "design" || got.StageSkips[0].By != "ana@x" ||
		got.StageSkips[0].Reason != "copy change, nothing to test" || got.StageSkips[0].At.IsZero() {
		t.Fatalf("stored skips = %+v", got.StageSkips)
	}
	full, err := store.Get(ctx, tk.ID)
	if err != nil || !reflect.DeepEqual(skipStages(full.StageSkips), []Stage{"design"}) {
		t.Fatalf("Get skips = %+v (%v)", full, err)
	}
	cards, err := store.List(ctx)
	if err != nil || len(cards) != 1 || !reflect.DeepEqual(skipStages(cards[0].StageSkips), []Stage{"design"}) {
		t.Fatalf("card list skips = %+v (%v)", cards, err)
	}
	if n := got.SkipsAhead(taskSkipBoard()); n != 1 {
		t.Errorf("skips ahead = %d, want 1", n)
	}

	// Create keeps the skips it is given: an export bundle's import goes through it.
	imported := &Task{WorkspaceID: testWS, Title: "imported", Stage: "backlog",
		StageSkips: []StageSkip{{Stage: "dev", Reason: "carried over", By: "ana@x"}}}
	if err := store.Create(ctx, imported); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, imported.ID); !reflect.DeepEqual(skipStages(got.StageSkips), []Stage{"dev"}) {
		t.Errorf("imported skips = %+v", got.StageSkips)
	}
	ev := eventsOf(t, store, tk.ID, EventStageSkipAdded)
	if len(ev) != 1 || ev[0]["stage"] != "design" || ev[0]["by"] != "ana@x" || ev[0]["source"] != SkipSourceAPI {
		t.Errorf("stage_skip_added events = %v", ev)
	}
}

func TestMayAddSkips(t *testing.T) {
	reg := taskSkipBoard()
	inRefine := &Task{ID: "t1", Stage: "refine"}
	cases := []struct {
		name  string
		t     *Task
		actor SkipActor
		ok    bool
	}{
		{"a person", inRefine, person, true},
		{"an operator key", inRefine, SkipActor{By: "key:ci"}, true},
		{"the refiner in refinement", inRefine, refinerOf(inRefine), true},
		{"the developer's run key", inRefine, SkipActor{By: "agent:dev", Agent: true, RunStage: "dev", RunTask: "t1"}, false},
		{"the auditor of refinement", inRefine, SkipActor{By: "agent:auditor", Agent: true, Auditor: true, RunStage: "refine", RunTask: "t1"}, false},
		{"the refiner after the task moved on", &Task{ID: "t1", Stage: "design"}, refinerOf(inRefine), false},
		{"the refiner of another task", &Task{ID: "t2", Stage: "refine"}, refinerOf(inRefine), false},
		{"a delegated chat", inRefine, SkipActor{By: "agent:chat", Agent: true}, false},
	}
	for _, c := range cases {
		err := MayAddSkips(reg, c.t, c.actor)
		if c.ok != (err == nil) {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && apperr.Code(err) != "task.errors.skip_add_refiner_only" {
			t.Errorf("%s: code = %q", c.name, apperr.Code(err))
		}
	}
}

func TestMayRemoveSkips(t *testing.T) {
	tk := &Task{ID: "t1", Stage: "dev"}
	cases := []struct {
		name  string
		actor SkipActor
		ok    bool
	}{
		{"a person", person, true},
		{"the task's own run", SkipActor{By: "agent:dev", Agent: true, RunStage: "dev", RunTask: "t1"}, true},
		{"a mention on the task", SkipActor{By: "agent:x", Agent: true, RunStage: "mention", RunTask: "t1"}, true},
		{"a run of a subtask", SkipActor{By: "agent:dev", Agent: true, RunStage: "dev", RunTask: "t1a"}, false},
		{"a chat with no task", SkipActor{By: "agent:chat", Agent: true}, false},
		{"a delegated chat", SkipActor{By: "agent:chat", Agent: true, Delegated: true}, true},
	}
	for _, c := range cases {
		err := MayRemoveSkips(tk, c.actor)
		if c.ok != (err == nil) {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && apperr.Code(err) != "task.errors.skip_remove_own_task" {
			t.Errorf("%s: code = %q", c.name, apperr.Code(err))
		}
	}
}

func TestAddStageSkipRefusals(t *testing.T) {
	_, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "dev", "")
	long := strings.Repeat("é", MaxSkipReason+1)
	cases := []struct {
		name   string
		stage  Stage
		reason string
		code   string
	}{
		{"empty reason", "qa", "", "task.errors.skip_reason_required"},
		{"blank reason", "qa", "  \n ", "task.errors.skip_reason_required"},
		{"reason too long", "qa", long, "task.errors.skip_reason_too_long"},
		{"unknown column", "nowhere", "x", "task.errors.skip_stage_unknown"},
		{"the current column", "dev", "x", "task.errors.skip_not_ahead"},
		{"an earlier column", "design", "x", "task.errors.skip_not_ahead"},
		{"a no_skip column", "review", "x", "task.errors.skip_no_skip"},
		{"a gate column", "gate", "x", "task.errors.skip_not_skippable"},
		{"a release column", "ship", "x", "task.errors.skip_not_skippable"},
		{"the terminal column", "done", "x", "task.errors.skip_not_skippable"},
		{"the backlog", "backlog", "x", "task.errors.skip_not_skippable"},
		{"a virtual stage", "mention", "x", "task.errors.skip_not_skippable"},
	}
	for _, c := range cases {
		res, err := store.UpsertStageSkips(ctx, tk.ID, person, []StageSkip{{Stage: c.stage, Reason: c.reason}}, SkipSourceAPI)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(res.Refused) != 1 || apperr.Code(res.Refused[0].Err) != c.code || len(res.Added) != 0 {
			t.Errorf("%s: refused = %+v, want %s", c.name, res.Refused, c.code)
		}
	}
	if got := rowOf(t, store, tk.ID); len(got.StageSkips) != 0 {
		t.Fatalf("refused adds stored %+v", got.StageSkips)
	}
	if ev := eventsOf(t, store, tk.ID, EventStageSkipAdded); len(ev) != 0 {
		t.Errorf("refused adds recorded %v", ev)
	}

	// An agent outside refinement adds nothing at all, whatever it names.
	_, err := store.UpsertStageSkips(ctx, tk.ID, SkipActor{By: "agent:dev", Agent: true, RunStage: "dev", RunTask: tk.ID},
		[]StageSkip{{Stage: "qa", Reason: "I say so"}}, SkipSourceAPI)
	if apperr.KindOf(err) != apperr.KindForbidden {
		t.Errorf("developer add: err = %v, want Forbidden", err)
	}

	// Same column again: the reason is replaced.
	addSkip(t, store, tk, person, "qa", "first")
	addSkip(t, store, tk, SkipActor{By: "bo@x"}, "qa", "second")
	if got := rowOf(t, store, tk.ID); len(got.StageSkips) != 1 || got.StageSkips[0].Reason != "second" || got.StageSkips[0].By != "bo@x" {
		t.Errorf("upsert = %+v", got.StageSkips)
	}

	// A finished task has nothing left to skip.
	done := createIn(t, store, "done", "")
	res, _ := store.UpsertStageSkips(ctx, done.ID, person, []StageSkip{{Stage: "qa", Reason: "x"}}, SkipSourceAPI)
	if len(res.Refused) != 1 || apperr.Code(res.Refused[0].Err) != "task.errors.skip_task_finished" {
		t.Errorf("finished task: %+v", res.Refused)
	}
}

func TestAddStageSkipTooMany(t *testing.T) {
	stages := []types.StageInfo{{ID: "backlog", Label: "Backlog", Position: 1, Initial: true}}
	for i := range MaxStageSkips + 1 {
		stages = append(stages, types.StageInfo{ID: Stage("c" + string(rune('a'+i))), Label: "C", Position: 10 + i})
	}
	stages = append(stages, types.StageInfo{ID: "done", Label: "Done", Position: 100, Terminal: true})
	reg := stagepkg.NewSnapshot(stages)
	_, store := newStageTestDispatcher(t, reg)
	tk := createIn(t, store, "backlog", "")
	var in []StageSkip
	for _, st := range stages[1 : len(stages)-1] {
		in = append(in, StageSkip{Stage: st.ID, Reason: "not needed"})
	}
	res, err := store.UpsertStageSkips(t.Context(), tk.ID, person, in, SkipSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != MaxStageSkips || len(res.Refused) != 1 || apperr.Code(res.Refused[0].Err) != "task.errors.skip_too_many" {
		t.Errorf("added %d, refused %+v", len(res.Added), res.Refused)
	}
}

func TestNextStageForHonorsTaskSkips(t *testing.T) {
	reg := taskSkipBoard()
	skip := func(ids ...Stage) []StageSkip {
		var out []StageSkip
		for _, id := range ids {
			out = append(out, StageSkip{Stage: id, Reason: "r"})
		}
		return out
	}
	cases := []struct {
		name    string
		t       *Task
		from    Stage
		next    Stage
		skipped []Stage
	}{
		{"no skips", &Task{}, "refine", "design", nil},
		{"one skip", &Task{StageSkips: skip("design")}, "refine", "dev", []Stage{"design"}},
		{"two in a row", &Task{StageSkips: skip("design", "dev")}, "refine", "qa", []Stage{"design", "dev"}},
		{"with skip_unless", &Task{FlowFlags: []string{"api"}, StageSkips: skip("design")}, "refine", "dev", []Stage{"design"}},
		{"skip_unless after a skip", &Task{FlowFlags: []string{"api"}, StageSkips: skip("dev")}, "design", "review", []Stage{"dev", "qa"}},
		// A board edit made review no_skip after the task skipped it: the skip is ignored.
		{"skip on a no_skip column", &Task{FlowFlags: []string{"ui"}, StageSkips: skip("review")}, "qa", "review", nil},
	}
	for _, c := range cases {
		next, skipped := nextStageFor(reg, c.t, c.from)
		if next != c.next || !reflect.DeepEqual(skipped, c.skipped) {
			t.Errorf("%s: next %q skipped %v, want %q %v", c.name, next, skipped, c.next, c.skipped)
		}
	}
}

func TestAutoAdvancePassesOverATaskSkip(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "refine", "")
	addSkip(t, store, tk, refinerOf(tk), "design", "a config flag, no behavior to test")
	// The caller's copy predates the skip, as after a run whose flags.yaml just wrote it.
	d.autoAdvanceStage(ctx, tk, tk.ID, "refine", 0, "")
	if got := rowOf(t, store, tk.ID); got.Stage != "dev" {
		t.Fatalf("stage = %q, want dev", got.Stage)
	}
	for _, ev := range eventsOf(t, store, tk.ID, EventStageEntered) {
		if ev["to"] == "design" {
			t.Errorf("entered the skipped column: %v", ev)
		}
	}
	ev := eventsOf(t, store, tk.ID, EventStageSkipped)
	if len(ev) != 1 || ev[0]["stage"] != "design" || ev[0]["task_skip"] != true ||
		ev[0]["reason"] != "a config flag, no behavior to test" || ev[0]["by"] != "agent:refiner" {
		t.Errorf("stage_skipped events = %v", ev)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "Design") || !strings.Contains(c, "a config flag") {
		t.Errorf("note = %q, want the column and the reason", c)
	}
}

// Nobody refined the task: every column runs, those with skip_unless included. A person's skip still
// applies (D5).
func TestUnrefinedTaskRunsEverythingButAPersonsSkip(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "dev", "")
	d.autoAdvanceStage(ctx, tk, tk.ID, "dev", 0, "")
	if got := rowOf(t, store, tk.ID); got.Stage != "qa" || got.FlowFlags != nil {
		t.Fatalf("task = %s flags %v, want qa with nil flags", got.Stage, got.FlowFlags)
	}

	other := createIn(t, store, "dev", "")
	addSkip(t, store, other, person, "qa", "a backend change, nothing on screen")
	d.autoAdvanceStage(ctx, other, other.ID, "dev", 0, "")
	if got := rowOf(t, store, other.ID); got.Stage != "review" {
		t.Fatalf("stage = %q, want review: the person's skip holds without flow flags", got.Stage)
	}
}

func TestParseAndAddFlagsSkips(t *testing.T) {
	_, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "refine", "")
	doc := "surface: [api]\nrisk: low\nsize: S\nskip:\n" +
		"  - stage: design\n    reason: renames a field, no behavior to test\n" +
		"  - stage: review\n    reason: trivial\n" +
		"  - dev\n"
	res, err := ParseAndAddFlags(ctx, store, tk.ID, refinerOf(tk), []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skipStages(res.StageSkips), []Stage{"design"}) {
		t.Errorf("import skips = %+v", res.StageSkips)
	}
	refused := map[Stage]string{}
	for _, r := range res.SkipsRefused {
		refused[r.Stage] = apperr.Code(r.Err)
	}
	if refused["review"] != "task.errors.skip_no_skip" || refused["dev"] != "task.errors.skip_reason_required" {
		t.Errorf("refused = %v", refused)
	}
	got := rowOf(t, store, tk.ID)
	if len(got.StageSkips) != 1 || got.StageSkips[0].By != "agent:refiner" || !slices.Equal(got.FlowFlags, []string{"api"}) {
		t.Fatalf("stored = %+v flags %v", got.StageSkips, got.FlowFlags)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "`review`") || !strings.Contains(c, "`dev`") {
		t.Errorf("refusal note = %q", c)
	}
	if ev := eventsOf(t, store, tk.ID, EventStageSkipAdded); len(ev) != 1 || ev[0]["source"] != SkipSourceFlagsYAML {
		t.Errorf("stage_skip_added = %v", ev)
	}

	// A person's skip survives the refiner's next file; the refiner's own entry it leaves out goes.
	addSkip(t, store, tk, person, "qa", "no screens")
	if _, err := ParseAndAddFlags(ctx, store, tk.ID, refinerOf(tk), []byte("skip: []\n")); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, tk.ID); !reflect.DeepEqual(skipStages(got.StageSkips), []Stage{"qa"}) {
		t.Errorf("after an empty skip list = %+v, want the person's qa", got.StageSkips)
	}
	removed := eventsOf(t, store, tk.ID, EventStageSkipRemoved)
	if len(removed) != 1 || removed[0]["stage"] != "design" || removed[0]["cause"] != SkipCauseRefinementDropped {
		t.Errorf("stage_skip_removed = %v", removed)
	}

	// No skip key: the skips stay.
	if _, err := ParseAndAddFlags(ctx, store, tk.ID, refinerOf(tk), []byte("risk: medium\n")); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, tk.ID); !reflect.DeepEqual(skipStages(got.StageSkips), []Stage{"qa"}) {
		t.Errorf("a file without skip changed them to %+v", got.StageSkips)
	}
}

// The refiner naming a column a person already skips leaves the person's entry as it is, so its
// next flags.yaml without that column does not drop it as one of its own.
func TestRefinerDoesNotTakeOverAPersonsSkip(t *testing.T) {
	_, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "refine", "")
	addSkip(t, store, tk, person, "dev", "a person decided: config only")

	doc := "skip:\n  - stage: dev\n    reason: the refiner agrees\n"
	res, err := ParseAndAddFlags(ctx, store, tk.ID, refinerOf(tk), []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SkipsRefused) != 0 || len(res.StageSkips) != 1 || res.StageSkips[0].By != "ana@x" {
		t.Fatalf("import = %+v", res)
	}
	via, err := store.UpsertStageSkips(ctx, tk.ID, refinerOf(tk), []StageSkip{{Stage: "dev", Reason: "again"}}, SkipSourceAPI)
	if err != nil || len(via.Added) != 0 || via.Task == nil {
		t.Fatalf("API add over a person's skip = %+v (%v)", via, err)
	}
	if _, err := ParseAndAddFlags(ctx, store, tk.ID, refinerOf(tk), []byte("skip: []\n")); err != nil {
		t.Fatal(err)
	}
	got := rowOf(t, store, tk.ID)
	if len(got.StageSkips) != 1 || got.StageSkips[0].By != "ana@x" || got.StageSkips[0].Reason != "a person decided: config only" {
		t.Errorf("skips = %+v, want the person's dev untouched", got.StageSkips)
	}
	if ev := eventsOf(t, store, tk.ID, EventStageSkipAdded); len(ev) != 1 {
		t.Errorf("stage_skip_added = %v, want only the person's", ev)
	}
	if ev := eventsOf(t, store, tk.ID, EventStageSkipRemoved); len(ev) != 0 {
		t.Errorf("stage_skip_removed = %v, want none", ev)
	}

	// A rerun that writes the same skip again changes nothing and records nothing.
	again := "skip:\n  - stage: design\n    reason: renames a field\n"
	for range 2 {
		if _, err := ParseAndAddFlags(ctx, store, tk.ID, refinerOf(tk), []byte(again)); err != nil {
			t.Fatal(err)
		}
	}
	design := 0
	for _, ev := range eventsOf(t, store, tk.ID, EventStageSkipAdded) {
		if ev["stage"] == "design" {
			design++
		}
	}
	if design != 1 {
		t.Errorf("stage_skip_added for design = %d, want 1", design)
	}
}

// A flags.yaml from a stage that does not read it (here the developer's) adds no skip.
func TestParseAndAddFlagsSkipsFromALaterStageAreRefused(t *testing.T) {
	_, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "dev", "")
	dev := SkipActor{By: "agent:dev", Agent: true, RunStage: "dev", RunTask: tk.ID}
	res, err := ParseAndAddFlags(ctx, store, tk.ID, dev, []byte("skip:\n  - stage: qa\n    reason: I would rather not\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, tk.ID); len(got.StageSkips) != 0 {
		t.Fatalf("the developer's flags.yaml stored %+v", got.StageSkips)
	}
	if len(res.SkipsRefused) != 1 || apperr.Code(res.SkipsRefused[0].Err) != "task.errors.skip_add_refiner_only" {
		t.Errorf("refused = %+v", res.SkipsRefused)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "`qa`") {
		t.Errorf("note = %q, want the refused column", c)
	}
}

func TestMovingIntoASkippedColumnDropsTheSkip(t *testing.T) {
	_, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "refine", "")
	addSkip(t, store, tk, person, "design", "r1")
	addSkip(t, store, tk, person, "qa", "r2")
	design := Stage("design")
	if _, err := store.Update(ctx, tk.ID, Patch{Stage: &design, By: "bo@x"}); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, tk.ID); !reflect.DeepEqual(skipStages(got.StageSkips), []Stage{"qa"}) {
		t.Fatalf("skips = %+v, want only qa", got.StageSkips)
	}
	evs, err := store.ListEvents(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Kind != EventStageSkipRemoved {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(e.Payload, &p)
		found = p["stage"] == "design" && p["cause"] == SkipCauseEntered && e.Actor == "bo@x" &&
			strings.Contains(p["reason"].(string), "bo@x")
	}
	if !found {
		t.Errorf("no stage_skip_removed for design by bo@x: %+v", evs)
	}

	other := "other"
	if _, err := store.Update(ctx, tk.ID, Patch{BoardID: &other}); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, tk.ID); len(got.StageSkips) != 0 {
		t.Errorf("a board change kept %+v", got.StageSkips)
	}
	removed := eventsOf(t, store, tk.ID, EventStageSkipRemoved)
	if last := removed[len(removed)-1]; last["stage"] != "qa" || last["cause"] != SkipCauseBoardChanged {
		t.Errorf("last removal = %v", last)
	}
}

func TestRemovingASkipAheadRunsTheColumnWhenReached(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := createIn(t, store, "design", "")
	addSkip(t, store, tk, person, "dev", "nothing to build")
	res, err := d.RemoveStageSkip(ctx, tk, "dev", SkipActor{By: "agent:designer", Agent: true, RunStage: "design", RunTask: tk.ID}, "the tests need new code")
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.MovedBack || res.Deferred || rowOf(t, store, tk.ID).Stage != "design" {
		t.Fatalf("removal = %+v, want no move", res)
	}
	d.autoAdvanceStage(ctx, rowOf(t, store, tk.ID), tk.ID, "design", 0, "")
	if got := rowOf(t, store, tk.ID); got.Stage != "dev" {
		t.Errorf("stage = %q, want dev", got.Stage)
	}
	if _, err := d.RemoveStageSkip(ctx, tk, "dev", person, "again"); apperr.Code(err) != "task.errors.skip_not_skipped" {
		t.Errorf("second removal: %v", err)
	}
	if _, err := d.RemoveStageSkip(ctx, tk, "qa", person, " "); apperr.Code(err) != "task.errors.skip_reason_required" {
		t.Errorf("removal without reason: %v", err)
	}
}

// The skip of a column the task passed: removing it sends the task back there. Set up by adding
// the skip in refine and moving the task past the column by hand without entering it.
func passedSkipTask(t *testing.T, store *Store, at Stage) *Task {
	t.Helper()
	tk := createIn(t, store, "refine", "")
	addSkip(t, store, tk, person, "design", "no tests to write")
	moveTo(t, store, tk.ID, at)
	return rowOf(t, store, tk.ID)
}

func TestPersonRemovingAPassedSkipSendsTheTaskBack(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := passedSkipTask(t, store, "qa")
	roundBefore := store.TaskRound(ctx, tk.ID)
	res, err := d.RemoveStageSkip(ctx, tk, "design", person, "the change touches pricing")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || !res.MovedBack || res.Deferred {
		t.Fatalf("removal = %+v, want moved back", res)
	}
	got := rowOf(t, store, tk.ID)
	if got.Stage != "design" || len(got.StageSkips) != 0 {
		t.Fatalf("task = %s skips %+v, want design with none", got.Stage, got.StageSkips)
	}
	entered := eventsOf(t, store, tk.ID, EventStageEntered)
	if last := entered[len(entered)-1]; last["to"] != "design" || last["via"] != ViaSkipRemoved {
		t.Errorf("last stage_entered = %v", last)
	}
	if r := store.TaskRound(ctx, tk.ID); r != roundBefore+1 {
		t.Errorf("round = %d, want %d", r, roundBefore+1)
	}
	if n := store.CountKickBacks(ctx, tk.ID); n != 0 {
		t.Errorf("CountKickBacks = %d: a return for a removed skip is not a send-back", n)
	}
	if !hasComment(t, store, tk.ID, "the change touches pricing") {
		t.Error("no note gives the reason the task went back")
	}
	removed := eventsOf(t, store, tk.ID, EventStageSkipRemoved)
	if len(removed) != 1 || removed[0]["passed"] != true || removed[0]["cause"] != SkipCauseRemoved || removed[0]["by"] != "ana@x" {
		t.Errorf("stage_skip_removed = %v", removed)
	}

	// The column ran; the task goes forward with nothing skipped again.
	d.autoAdvanceStage(ctx, got, tk.ID, "design", 0, "")
	if got := rowOf(t, store, tk.ID); got.Stage != "dev" {
		t.Errorf("after design: %s, want dev", got.Stage)
	}
}

func TestSkipReturnIgnoresTheKickBackCap(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	d.WithBoards(cappedBoards{Boards: stagepkg.SingleBoard(taskSkipBoard()), max: 1})
	ctx := t.Context()
	tk := passedSkipTask(t, store, "qa")
	if err := store.RecordEvent(ctx, tk.ID, EventStageEntered, "", "auditor", "qa → dev",
		map[string]string{"from": "qa", "to": "dev", "via": ViaKickBack}); err != nil {
		t.Fatal(err)
	}
	if n := store.CountKickBacks(ctx, tk.ID); n != 1 {
		t.Fatalf("setup: CountKickBacks = %d", n)
	}
	if _, err := d.RemoveStageSkip(ctx, tk, "design", person, "needs tests"); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, store, tk.ID); got.Stage != "design" || got.Status == StatusBlocked {
		t.Fatalf("task = %s/%s, want design: the cap does not hold a skip return", got.Stage, got.Status)
	}
}

func TestAgentRemovingAPassedSkipWaitsForItsRun(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := passedSkipTask(t, store, "qa")
	running := StatusRunning
	if _, err := store.Update(ctx, tk.ID, Patch{Status: &running}); err != nil {
		t.Fatal(err)
	}
	tester := SkipActor{By: "agent:tester", Agent: true, RunStage: "qa", RunTask: tk.ID}
	res, err := d.RemoveStageSkip(ctx, rowOf(t, store, tk.ID), "design", tester, "found an untested branch")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || !res.Deferred || res.MovedBack || rowOf(t, store, tk.ID).Stage != "qa" {
		t.Fatalf("removal = %+v, stage %s; want deferred in qa", res, rowOf(t, store, tk.ID).Stage)
	}
	if !hasComment(t, store, tk.ID, "when this run ends") {
		t.Error("no note says the task goes back when the run ends")
	}

	// A failed run does not move it; the return waits for the next clean end.
	d.finishStageRun(ctx, tk.ID, rowOf(t, store, tk.ID), "qa", auditedRun{Exit: 1}, stageVerdict{})
	if got := rowOf(t, store, tk.ID); got.Stage != "qa" {
		t.Fatalf("after a failed run: %s, want qa", got.Stage)
	}
	d.finishStageRun(ctx, tk.ID, rowOf(t, store, tk.ID), "qa", auditedRun{Exit: 0}, stageVerdict{})
	got := rowOf(t, store, tk.ID)
	if got.Stage != "design" {
		t.Fatalf("after the clean end: %s, want design", got.Stage)
	}
	entered := eventsOf(t, store, tk.ID, EventStageEntered)
	if last := entered[len(entered)-1]; last["via"] != ViaSkipRemoved || last["from"] != "qa" {
		t.Errorf("last stage_entered = %v", last)
	}
	// Back in qa later, the old removal no longer asks for a return.
	d.autoAdvanceStage(ctx, got, tk.ID, "design", 0, "")
	d.autoAdvanceStage(ctx, rowOf(t, store, tk.ID), tk.ID, "dev", 0, "")
	if got := rowOf(t, store, tk.ID); got.Stage != "qa" {
		t.Fatalf("forward again: %s, want qa", got.Stage)
	}
	if back := d.skipReturnTarget(ctx, rowOf(t, store, tk.ID), "qa"); back != "" {
		t.Errorf("stale return target %q", back)
	}
}

// The run that removed the skip ends with a send-back to a column after the pending one: the task
// goes to the earlier one.
func TestVerdictKickBackKeepsAnEarlierSkipReturn(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	tk := passedSkipTask(t, store, "qa")
	running := StatusRunning
	if _, err := store.Update(ctx, tk.ID, Patch{Status: &running}); err != nil {
		t.Fatal(err)
	}
	tester := SkipActor{By: "agent:tester", Agent: true, RunStage: "qa", RunTask: tk.ID}
	if _, err := d.RemoveStageSkip(ctx, rowOf(t, store, tk.ID), "design", tester, "missing tests"); err != nil {
		t.Fatal(err)
	}
	d.finishStageRun(ctx, tk.ID, rowOf(t, store, tk.ID), "qa", auditedRun{Agent: "tester", Exit: 0},
		stageVerdict{Verdict: verdictKickBack, KickBackTo: "dev", Source: verdictFromAgent, Agent: "tester", Reason: "bug_found"})
	if got := rowOf(t, store, tk.ID); got.Stage != "design" {
		t.Errorf("stage = %s, want design: the pending return is earlier than dev", got.Stage)
	}
}

func TestRemovingAPassedSkipOfAFinishedTaskIsRefused(t *testing.T) {
	d, store := newStageTestDispatcher(t, taskSkipBoard())
	ctx := t.Context()
	for _, at := range []Stage{"done", "ship"} {
		tk := passedSkipTask(t, store, at)
		_, err := d.RemoveStageSkip(ctx, tk, "design", person, "too late")
		if apperr.Code(err) != "task.errors.skip_return_released" || apperr.KindOf(err) != apperr.KindConflict {
			t.Errorf("%s: err = %v", at, err)
		}
		if got := rowOf(t, store, tk.ID); got.Stage != at || len(got.StageSkips) != 1 {
			t.Errorf("%s: task = %s skips %+v, want unchanged", at, got.Stage, got.StageSkips)
		}
	}
}

func TestSkipsAheadCountsOnlyColumnsStillToRun(t *testing.T) {
	reg := taskSkipBoard()
	tk := &Task{Stage: "dev", StageSkips: []StageSkip{{Stage: "design"}, {Stage: "qa"}, {Stage: "review"}}}
	if n := tk.SkipsAhead(reg); n != 1 {
		t.Errorf("skips ahead = %d, want 1 (design passed, review no_skip)", n)
	}
	if _, ok := activeSkip(tk, mustStage(t, reg, "review")); ok {
		t.Error("a skip on a no_skip column is active")
	}
}

func mustStage(t *testing.T, reg stagepkg.Registry, id Stage) types.StageInfo {
	t.Helper()
	info, ok := reg.Get(id)
	if !ok {
		t.Fatalf("no stage %s", id)
	}
	return info
}
