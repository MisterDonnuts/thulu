package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func withSkipDispatcher(rig *runScopeRig) {
	l := &acceptingLauncher{agent: &types.AgentType{Name: "developer"}}
	rig.srv.taskDispatcher = task.NewDispatcher(rig.srv.taskStore, l, rig.srv.projectStore, nil, nil).WithBoards(rig.srv.boards)
}

func putStage(t *testing.T, rig *runScopeRig, id string, stage task.Stage) {
	t.Helper()
	if _, err := rig.srv.taskStore.Update(t.Context(), id, task.Patch{Stage: &stage}); err != nil {
		t.Fatal(err)
	}
}

func skipsOf(t *testing.T, rig *runScopeRig, id string) []task.StageSkip {
	t.Helper()
	got, err := rig.srv.taskStore.GetRow(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.StageSkips
}

func skipBody(stage, reason string) map[string]any {
	return map[string]any{"stage": stage, "reason": reason}
}

func TestStageSkips_PersonAddsAndRemoves(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	withSkipDispatcher(rig)
	add := "/tasks/" + rig.a + "/stage-skips"

	w := postJSONAuth(t, rig.h, add, rig.token, skipBody("testing", "a backend fix with no screen"))
	if w.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Skip       task.StageSkip   `json:"skip"`
		StageSkips []task.StageSkip `json:"stage_skips"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Skip.Stage != "testing" || res.Skip.By != "run-scope@test.com" || len(res.StageSkips) != 1 {
		t.Errorf("add response = %+v", res)
	}
	g := getJSON(t, rig.h, "/tasks/"+rig.a, rig.token)
	if g.Code != http.StatusOK || !strings.Contains(g.Body.String(), `"stage_skips":[{"stage":"testing"`) {
		t.Errorf("GET task = %d, lacks the skip: %s", g.Code, g.Body.String())
	}

	if w := postJSONAuth(t, rig.h, add, rig.token, skipBody("testing", "")); w.Code != http.StatusBadRequest {
		t.Errorf("no reason = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := postJSONAuth(t, rig.h, add, rig.token, skipBody("handoff", "x")); w.Code != http.StatusConflict {
		t.Errorf("a pull request column = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, rig.h, "/stages/ai_review", "", map[string]any{"no_skip": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH no_skip = %d: %s", w.Code, w.Body.String())
	}
	st := getJSON(t, rig.h, "/stages", rig.token)
	if !strings.Contains(st.Body.String(), `"no_skip":true`) {
		t.Errorf("GET /stages lacks no_skip: %s", st.Body.String())
	}
	if w := postJSONAuth(t, rig.h, add, rig.token, skipBody("ai_review", "x")); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "skip_no_skip") {
		t.Errorf("a no_skip column = %d, want 409: %s", w.Code, w.Body.String())
	}

	rm := "/tasks/" + rig.a + "/stage-skips/testing/remove"
	if w := postJSONAuth(t, rig.h, rm, rig.token, map[string]any{"reason": ""}); w.Code != http.StatusBadRequest {
		t.Errorf("remove without reason = %d, want 400", w.Code)
	}
	w = postJSONAuth(t, rig.h, rm, rig.token, map[string]any{"reason": "it shows a total after all"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"moved_back":false`) {
		t.Fatalf("remove = %d: %s", w.Code, w.Body.String())
	}
	if got := skipsOf(t, rig, rig.a); len(got) != 0 {
		t.Errorf("skips after remove = %+v", got)
	}
	if w := postJSONAuth(t, rig.h, rm, rig.token, map[string]any{"reason": "again"}); w.Code != http.StatusNotFound {
		t.Errorf("remove twice = %d, want 404", w.Code)
	}
}

// Only the refinement stage's agent adds skips; any agent on the task removes them.
func TestStageSkips_AgentKeys(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	withSkipDispatcher(rig)
	add := "/tasks/" + rig.a + "/stage-skips"
	putStage(t, rig, rig.a, "refinement")

	if w := keyDo(t, rig.h, http.MethodPost, add, rig.keyA, skipBody("testing", "x")); w.Code != http.StatusForbidden {
		t.Errorf("the developer's key = %d, want 403: %s", w.Code, w.Body.String())
	}
	auditor := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "refinement"})
	if w := keyDo(t, rig.h, http.MethodPost, add, auditor, skipBody("testing", "x")); w.Code != http.StatusForbidden {
		t.Errorf("the auditor = %d, want 403: %s", w.Code, w.Body.String())
	}
	refiner := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "refiner", Stage: "refinement"})
	if w := keyDo(t, rig.h, http.MethodPost, add, refiner, skipBody("testing", "a log line changes")); w.Code != http.StatusOK {
		t.Fatalf("the refiner = %d: %s", w.Code, w.Body.String())
	}
	if got := skipsOf(t, rig, rig.a); len(got) != 1 || got[0].By != "agent:refiner" {
		t.Errorf("skips = %+v", got)
	}
	// A column a person already skips stays theirs when the refiner names it too.
	if w := postJSONAuth(t, rig.h, add, rig.token, skipBody("ai_review", "a person's call")); w.Code != http.StatusOK {
		t.Fatalf("person add = %d: %s", w.Code, w.Body.String())
	}
	w := keyDo(t, rig.h, http.MethodPost, add, refiner, skipBody("ai_review", "the refiner's call"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"by":"run-scope@test.com"`) {
		t.Errorf("the refiner over a person's skip = %d: %s", w.Code, w.Body.String())
	}
	viewer := mintKey(t, rig.srv, rig.ws, auth.RoleViewer, auth.PermTasksRead, auth.PermTasksWrite)
	if w := keyDo(t, rig.h, http.MethodPost, add, viewer, skipBody("ai_review", "x")); w.Code != http.StatusForbidden {
		t.Errorf("a viewer = %d, want 403", w.Code)
	}

	putStage(t, rig, rig.a, "in_progress")
	rm := "/tasks/" + rig.a + "/stage-skips/testing/remove"
	if w := keyDo(t, rig.h, http.MethodPost, rm, viewer, map[string]any{"reason": "x"}); w.Code != http.StatusForbidden {
		t.Errorf("a viewer removing = %d, want 403", w.Code)
	}
	// A subtask's run reaches its parent's other writes, but a removal could send the parent back.
	if w := keyDo(t, rig.h, http.MethodPost, rm, rig.keyA1, map[string]any{"reason": "x"}); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "skip_remove_own_task") {
		t.Errorf("a subtask's run removing = %d, want 403: %s", w.Code, w.Body.String())
	}
	if got := skipsOf(t, rig, rig.a); len(got) != 2 {
		t.Fatalf("skips after a refused removal = %+v", got)
	}
	if w := keyDo(t, rig.h, http.MethodPost, rm, rig.keyA, map[string]any{"reason": "I added a component"}); w.Code != http.StatusOK {
		t.Fatalf("the developer removing = %d: %s", w.Code, w.Body.String())
	}
	evs, err := rig.srv.taskStore.ListEvents(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	var by string
	for _, e := range evs {
		if e.Kind == task.EventStageSkipRemoved {
			by = e.Actor
		}
	}
	if by != "agent:developer" {
		t.Errorf("stage_skip_removed actor = %q, want agent:developer", by)
	}
}

func TestStageSkips_RemovingAPassedSkipSendsTheTaskBack(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	withSkipDispatcher(rig)
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/stage-skips", rig.token, skipBody("ai_review", "trivial")); w.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", w.Code, w.Body.String())
	}
	putStage(t, rig, rig.a, "testing")
	w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/stage-skips/ai_review/remove", rig.token,
		map[string]any{"reason": "it touches auth"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"moved_back":true`) {
		t.Fatalf("remove = %d: %s", w.Code, w.Body.String())
	}
	got, err := rig.srv.taskStore.GetRow(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != "ai_review" {
		t.Errorf("stage = %s, want ai_review", got.Stage)
	}
}

func TestStageSkips_PatchRefusesTheList(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	w := patchJSONAuth(t, rig.h, "/tasks/"+rig.a, rig.token, map[string]any{
		"stage_skips": []map[string]string{{"stage": "testing", "reason": "x"}},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "stage_skips_patch") {
		t.Errorf("PATCH stage_skips = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A person dragging the task into a skipped column runs it: the skip goes, and the reason names them.
func TestStageSkips_MovingIntoTheColumnDropsTheSkip(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/stage-skips", rig.token, skipBody("ai_review", "trivial")); w.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", w.Code, w.Body.String())
	}
	if w := moveTask(t, rig.h, rig.token, rig.a, "ai_review"); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	if got := skipsOf(t, rig, rig.a); len(got) != 0 {
		t.Fatalf("skips after the move = %+v", got)
	}
	evs, err := rig.srv.taskStore.ListEvents(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Kind == task.EventStageSkipRemoved && e.Actor == "run-scope@test.com" &&
			strings.Contains(string(e.Payload), "run-scope@test.com") && strings.Contains(string(e.Payload), task.SkipCauseEntered) {
			found = true
		}
	}
	if !found {
		t.Errorf("no stage_skip_removed by the person: %+v", evs)
	}
}

func TestStageSkips_FlagsImport(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	putStage(t, rig, rig.a, "refinement")
	refiner := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "refiner", Stage: "refinement"})
	body := "surface: [api]\nskip:\n  - stage: testing\n    reason: no screen\n  - stage: handoff\n    reason: x\n"
	w := flowRawDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/flags/import", "application/x-yaml", refiner, "", body)
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		StageSkips   []task.StageSkip   `json:"stage_skips"`
		SkipsRefused []task.SkipRefusal `json:"skips_refused"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.StageSkips) != 1 || res.StageSkips[0].Stage != "testing" ||
		len(res.SkipsRefused) != 1 || res.SkipsRefused[0].Stage != "handoff" || res.SkipsRefused[0].Why == "" {
		t.Errorf("import response = %s", w.Body.String())
	}
}

func TestStageSkips_Brief(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	brief := func(q string) string {
		t.Helper()
		w := getJSON(t, rig.h, "/tasks/"+rig.a+"/brief"+q, rig.token)
		if w.Code != http.StatusOK {
			t.Fatalf("brief = %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	putStage(t, rig, rig.a, "refinement")
	b := brief("")
	if !strings.Contains(b, "may skip later columns") || !strings.Contains(b, "`testing`") || !strings.Contains(b, "`handoff`") {
		t.Errorf("the refinement brief does not list the skippable columns:\n%s", b)
	}
	if strings.Contains(b, "Columns this task skips") {
		t.Error("a task with no skips gets the skips section")
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/stage-skips", rig.token, skipBody("testing", "a backend-only fix")); w.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", w.Code, w.Body.String())
	}
	if b := brief("?audit=refiner-task-x-refinement-1"); !strings.Contains(b, "a backend-only fix") || !strings.Contains(b, "You audit the refinement") {
		t.Errorf("the auditor's brief lacks the skip or the audit line:\n%s", b)
	}
	putStage(t, rig, rig.a, "in_progress")
	b = brief("")
	if !strings.Contains(b, "`testing`") || !strings.Contains(b, "task_skip_remove") || strings.Contains(b, "may skip later columns") {
		t.Errorf("a later stage's brief:\n%s", b)
	}
}
