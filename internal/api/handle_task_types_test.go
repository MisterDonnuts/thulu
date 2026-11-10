package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type typesResponse struct {
	TaskTypes []stagepkg.TaskTypeInfo `json:"task_types"`
	Untyped   int                     `json:"untyped"`
	Moved     int                     `json:"moved_tasks"`
}

func decodeTypes(t *testing.T, w *httptest.ResponseRecorder) typesResponse {
	t.Helper()
	var out typesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode task types: %v (body: %s)", err, w.Body.String())
	}
	return out
}

func bugfixSkippingRefinement() map[string]any {
	return map[string]any{
		"id": "bugfix", "label": "Bugfix", "icon": "bug", "color": "#e5484d",
		"skips": []string{"refinement", "review"},
	}
}

func setTypes(t *testing.T, h http.Handler, board, query string, tt ...map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return putJSON(t, h, "/boards/"+board+"/types"+query, map[string]any{"types": tt}, "")
}

func TestTaskTypesSeedIsEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	got := decodeTypes(t, getJSON(t, h, "/boards/default/types", ""))
	if len(got.TaskTypes) != 0 {
		t.Errorf("fresh instance offers %d types, want none — the migration seeds nothing", len(got.TaskTypes))
	}
}

func TestTaskTypesCreateAndList(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := setTypes(t, h, "default", "", bugfixSkippingRefinement(),
		map[string]any{"id": "feature", "label": "Feature", "icon": "feature", "default": true})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT types = %d, body %s", w.Code, w.Body.String())
	}
	got := decodeTypes(t, getJSON(t, h, "/boards/default/types", ""))
	if len(got.TaskTypes) != 2 {
		t.Fatalf("types = %+v, want 2", got.TaskTypes)
	}
	if got.TaskTypes[0].ID != "bugfix" || got.TaskTypes[0].Icon != "bug" {
		t.Errorf("first type = %+v, want the bugfix we wrote", got.TaskTypes[0])
	}
	if len(got.TaskTypes[0].Skips) != 2 {
		t.Errorf("skips = %v, want refinement and review", got.TaskTypes[0].Skips)
	}
	cat := decodeTypes(t, getJSON(t, h, "/task-types", ""))
	if len(cat.TaskTypes) != 2 {
		t.Errorf("catalog = %+v, want both types", cat.TaskTypes)
	}
}

func TestTaskTypeIconsAreServed(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	var got struct {
		Icons []string `json:"icons"`
	}
	w := getJSON(t, h, "/task-types/icons", "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Icons) != len(stagepkg.TaskTypeIcons) {
		t.Errorf("icons = %d, want the whole allowlist (%d)", len(got.Icons), len(stagepkg.TaskTypeIcons))
	}
}

func TestBoardStagesFilterByType(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement())

	full := decodeBoard(t, getJSON(t, h, "/boards/default/stages", ""))
	typed := decodeBoard(t, getJSON(t, h, "/boards/default/stages?type=bugfix", ""))
	if len(typed) != len(full)-2 {
		t.Errorf("typed flow = %v, want the board minus two columns", boardIDs(typed))
	}
	for _, st := range typed {
		if st.ID == types.StageRefinement {
			t.Error("refinement is in the bugfix flow, but the type skips it")
		}
	}
	if got := getJSON(t, h, "/boards/default/stages?type=ghost", ""); got.Code != http.StatusNotFound {
		t.Errorf("unknown type = %d, want 404", got.Code)
	}
}

func TestCreateTaskWithType(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement())

	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "crash on login", "type_id": "bugfix"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, body %s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	if created["type_id"] != "bugfix" {
		t.Errorf("type_id = %v, want bugfix", created["type_id"])
	}

	bad := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "x", "type_id": "ghost"})
	if bad.Code != http.StatusBadRequest {
		t.Errorf("unknown type = %d, want 400 — a typo must not silently run the full flow", bad.Code)
	}
}

func TestCreateTaskUsesBoardDefaultType(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "",
		bugfixSkippingRefinement(),
		map[string]any{"id": "feature", "label": "Feature", "default": true})

	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "no type given"})
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	if created["type_id"] != "feature" {
		t.Errorf("type_id = %v, want the board's default type", created["type_id"])
	}
}

func TestMoveToSkippedStageIsRefused(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement())
	id := createTypedTask(t, h, "bugfix")

	w := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"stage": "refinement"})
	if w.Code != http.StatusConflict {
		t.Fatalf("move to skipped stage = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["available_stages"] == nil {
		t.Error("409 must hand back the stages the type does have")
	}
	other := createTypedTask(t, h, "")
	if ok := patchJSONAuth(t, h, "/tasks/"+other, "", map[string]any{"stage": "refinement"}); ok.Code != http.StatusOK {
		t.Errorf("untyped task refused refinement: %d %s", ok.Code, ok.Body.String())
	}
}

func TestRetypeOutOfASkippedStage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement())
	id := createTypedTask(t, h, "")
	if w := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"stage": "refinement"}); w.Code != http.StatusOK {
		t.Fatalf("setup move to refinement: %d %s", w.Code, w.Body.String())
	}

	w := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"type_id": "bugfix"})
	if w.Code != http.StatusConflict {
		t.Fatalf("re-type = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	ok := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"type_id": "bugfix", "stage": "in_progress"})
	if ok.Code != http.StatusOK {
		t.Fatalf("re-type with a stage = %d, body %s", ok.Code, ok.Body.String())
	}
	var updated map[string]any
	json.Unmarshal(ok.Body.Bytes(), &updated)
	if updated["type_id"] != "bugfix" || updated["stage"] != "in_progress" {
		t.Errorf("after re-type: type=%v stage=%v", updated["type_id"], updated["stage"])
	}
}

func TestRetypeToUnknownType(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement())
	id := createTypedTask(t, h, "bugfix")
	if w := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"type_id": "ghost"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown type = %d, want 400", w.Code)
	}
}

func TestBoardMoveDropsAnUnofferedType(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement())
	if w := createBoard(t, h, map[string]any{
		"id": "other", "label": "Other", "clone_from": stagepkg.DefaultBoardID,
	}); w.Code != http.StatusCreated {
		t.Fatalf("create board: %d %s", w.Code, w.Body.String())
	}
	id := createTypedTask(t, h, "bugfix")

	w := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"board_id": "other"})
	if w.Code != http.StatusOK {
		t.Fatalf("board move = %d, body %s", w.Code, w.Body.String())
	}
	var updated map[string]any
	json.Unmarshal(w.Body.Bytes(), &updated)
	if updated["type_id"] != nil && updated["type_id"] != "" {
		t.Errorf("type_id = %v after moving to a board without it, want untyped", updated["type_id"])
	}
}

func TestRemovingAPopulatedTypeNeedsMoveTo(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", bugfixSkippingRefinement(),
		map[string]any{"id": "feature", "label": "Feature"})
	id := createTypedTask(t, h, "bugfix")

	w := setTypes(t, h, "default", "", map[string]any{"id": "feature", "label": "Feature"})
	if w.Code != http.StatusConflict {
		t.Fatalf("drop populated type = %d, want 409 (body %s)", w.Code, w.Body.String())
	}

	ok := setTypes(t, h, "default", "?move_to=", map[string]any{"id": "feature", "label": "Feature"})
	if ok.Code != http.StatusOK {
		t.Fatalf("drop with move_to = %d, body %s", ok.Code, ok.Body.String())
	}
	if got := decodeTypes(t, ok); got.Moved != 1 {
		t.Errorf("moved_tasks = %d, want 1", got.Moved)
	}
	after := getJSON(t, h, "/tasks/"+id, "")
	var task map[string]any
	json.Unmarshal(after.Body.Bytes(), &task)
	if task["type_id"] != nil && task["type_id"] != "" {
		t.Errorf("task type = %v, want untyped after ?move_to=", task["type_id"])
	}
}

func TestSkippingAPopulatedStageNeedsMoveToStage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	setTypes(t, h, "default", "", map[string]any{"id": "bugfix", "label": "Bugfix"})
	id := createTypedTask(t, h, "bugfix")
	if w := patchJSONAuth(t, h, "/tasks/"+id, "", map[string]any{"stage": "refinement"}); w.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}

	w := setTypes(t, h, "default", "", bugfixSkippingRefinement())
	if w.Code != http.StatusConflict {
		t.Fatalf("skip a populated stage = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	ok := setTypes(t, h, "default", "?move_to_stage=in_progress", bugfixSkippingRefinement())
	if ok.Code != http.StatusOK {
		t.Fatalf("skip with move_to_stage = %d, body %s", ok.Code, ok.Body.String())
	}
	after := getJSON(t, h, "/tasks/"+id, "")
	var task map[string]any
	json.Unmarshal(after.Body.Bytes(), &task)
	if task["stage"] != "in_progress" {
		t.Errorf("stage = %v, want the task relocated out of the skipped column", task["stage"])
	}
}

func TestTaskTypeValidationAtTheEdge(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"skip the initial stage", map[string]any{"id": "x", "label": "X", "skips": []string{"backlog"}}},
		{"skip a terminal stage", map[string]any{"id": "x", "label": "X", "skips": []string{"done"}}},
		{"skip a stage of another board", map[string]any{"id": "x", "label": "X", "skips": []string{"nope"}}},
		{"icon off the allowlist", map[string]any{"id": "x", "label": "X", "icon": "skull"}},
		{"bad id", map[string]any{"id": "X-1", "label": "X"}},
		{"no label", map[string]any{"id": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := setTypes(t, h, "default", "", tc.body); w.Code != http.StatusBadRequest {
				t.Errorf("PUT types = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestTaskTypesUnknownBoard(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := getJSON(t, h, "/boards/ghost/types", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET = %d, want 404", w.Code)
	}
	if w := setTypes(t, h, "ghost", "", bugfixSkippingRefinement()); w.Code != http.StatusNotFound {
		t.Errorf("PUT = %d, want 404", w.Code)
	}
}

func TestDeletingABoardDropsItsTypes(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "hotfix", "label": "Hotfix", "stages": twoStageFlow("hot"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create board: %d %s", w.Code, w.Body.String())
	}
	setTypes(t, h, "hotfix", "", map[string]any{"id": "urgent", "label": "Urgent"})

	r := httptest.NewRequest(http.MethodDelete, "/boards/hotfix", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("delete board = %d, body %s", w.Code, w.Body.String())
	}
	if cat := decodeTypes(t, getJSON(t, h, "/task-types", "")); len(cat.TaskTypes) != 0 {
		t.Errorf("catalog = %+v, want empty: the type's only board is gone", cat.TaskTypes)
	}
}

func createTypedTask(t *testing.T, h http.Handler, typeID string) string {
	t.Helper()
	body := map[string]any{"title": "t"}
	if typeID != "" {
		body["type_id"] = typeID
	}
	w := postJSON(t, h, "/tasks?workspace=ws-test", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	return created["id"].(string)
}
