package api

import (
	"encoding/json"
	"net/http"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestMoveTasksToBoard_ReportsFailure(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)

	real := &task.Task{WorkspaceID: "ws-test", Title: "real", BoardID: stagepkg.DefaultBoardID, Stage: types.StageBacklog}
	if err := srv.taskStore.Create(t.Context(), real); err != nil {
		t.Fatalf("create task: %v", err)
	}
	ghost := task.Task{ID: "0000000000000000", Title: "vanished", BoardID: stagepkg.DefaultBoardID}

	dest := srv.boards.Board(stagepkg.DefaultBoardID)
	moved, err := srv.moveTasksToBoard(t.Context(),
		[]task.Task{*real, ghost}, "temp", stagepkg.DefaultBoardID, dest, types.StageBacklog)
	if err == nil {
		t.Fatal("relocating a task that cannot be updated reported success")
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1 (the one that did land)", moved)
	}
}

func TestDeleteBoard_LeavesNoTaskBehind(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "temp", "label": "Temp", "stages": twoStageFlow("tmp"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create board: %s", w.Body.String())
	}
	for _, title := range []string{"a", "b", "c"} {
		if w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": title, "board_id": "temp"}); w.Code != http.StatusCreated {
			t.Fatalf("create task %s: %s", title, w.Body.String())
		}
	}

	if w := deleteRequest(t, h, "/boards/temp?move_to=default", ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, body %s", w.Code, w.Body.String())
	}

	all, err := srv.taskStore.List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range all {
		if all[i].BoardID == "temp" {
			t.Errorf("task %s still points at the deleted board", all[i].ID)
		}
		if !srv.stageAdmin.HasBoard(all[i].BoardID) {
			t.Errorf("task %s points at unknown board %q", all[i].ID, all[i].BoardID)
		}
	}
}

func TestDeleteBoard_LateArrivalIsStillRelocated(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "temp", "label": "Temp", "stages": twoStageFlow("tmp"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create board: %s", w.Body.String())
	}
	latecomer := &task.Task{WorkspaceID: "ws-test", Title: "latecomer", BoardID: "temp", Stage: "tmp_open"}
	if err := srv.taskStore.Create(t.Context(), latecomer); err != nil {
		t.Fatalf("create task: %v", err)
	}

	w := deleteRequest(t, h, "/boards/temp?move_to=default", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, body %s", w.Code, w.Body.String())
	}
	got, err := srv.taskStore.Get(t.Context(), latecomer.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BoardID == "temp" {
		t.Errorf("task still on the deleted board")
	}
}

func TestSetBoardTypes_InvalidPayloadMovesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		replace map[string]any
	}{
		{
			name:    "icon outside the allowlist",
			replace: map[string]any{"id": "feature", "label": "Feature", "icon": "definitely-not-an-icon"},
		},
		{
			name:    "empty label",
			replace: map[string]any{"id": "feature", "label": ""},
		},
		{
			name:    "malformed id",
			replace: map[string]any{"id": "Not A Slug", "label": "Feature"},
		},
		{
			name:    "colour that is not a hex colour",
			replace: map[string]any{"id": "feature", "label": "Feature", "color": "rebeccapurple"},
		},
		{
			name: "skips a terminal stage",
			replace: map[string]any{
				"id": "feature", "label": "Feature", "skips": []string{"done"},
			},
		},
		{
			name: "skips the initial stage",
			replace: map[string]any{
				"id": "feature", "label": "Feature", "skips": []string{"backlog"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := newIntegrationServer(t)
			if w := setTypes(t, h, "default", "", bugfixSkippingRefinement()); w.Code != http.StatusOK {
				t.Fatalf("seed types: %s", w.Body.String())
			}
			id := createTypedTask(t, h, "bugfix")

			w := setTypes(t, h, "default", "?move_to=", tc.replace)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("PUT types = %d, want 400 (body %s)", w.Code, w.Body.String())
			}

			after, err := srv.taskStore.Get(t.Context(), id)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if after.TypeID != "bugfix" {
				t.Errorf("task was re-typed to %q by a request that was rejected", after.TypeID)
			}
			offered := decodeTypes(t, getJSON(t, h, "/boards/default/types", ""))
			if len(offered.TaskTypes) != 1 || offered.TaskTypes[0].ID != "bugfix" {
				t.Errorf("type table changed on a rejected write: %+v", offered.TaskTypes)
			}
		})
	}
}

func TestSetBoardTypes_ListInvariantsMoveNothing(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := setTypes(t, h, "default", "", bugfixSkippingRefinement()); w.Code != http.StatusOK {
		t.Fatalf("seed: %s", w.Body.String())
	}
	id := createTypedTask(t, h, "bugfix")

	dup := map[string]any{"id": "feature", "label": "Feature"}
	w := setTypes(t, h, "default", "?move_to=", dup, dup)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate ids = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	after, err := srv.taskStore.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.TypeID != "bugfix" {
		t.Errorf("task re-typed to %q by a rejected request", after.TypeID)
	}

	twoDefaults := []map[string]any{
		{"id": "one", "label": "One", "default": true},
		{"id": "two", "label": "Two", "default": true},
	}
	w = setTypes(t, h, "default", "?move_to=", twoDefaults...)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("two defaults = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if after, err = srv.taskStore.Get(t.Context(), id); err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.TypeID != "bugfix" {
		t.Errorf("task re-typed to %q by a rejected request", after.TypeID)
	}
}

func TestSetBoardTypes_TooManyTypesMovesNothing(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := setTypes(t, h, "default", "", bugfixSkippingRefinement()); w.Code != http.StatusOK {
		t.Fatalf("seed: %s", w.Body.String())
	}
	id := createTypedTask(t, h, "bugfix")

	many := make([]map[string]any, 0, stagepkg.MaxBoardTypes+1)
	for i := 0; i <= stagepkg.MaxBoardTypes; i++ {
		many = append(many, map[string]any{
			"id": "t" + string(rune('a'+i%26)) + string(rune('a'+i/26)), "label": "T",
		})
	}
	w := setTypes(t, h, "default", "?move_to=", many...)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	after, err := srv.taskStore.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.TypeID != "bugfix" {
		t.Errorf("task re-typed to %q by a rejected request", after.TypeID)
	}
}

func TestApplyTypeRelocations_ReportsFailure(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)

	real := &task.Task{WorkspaceID: "ws-test", Title: "real", BoardID: stagepkg.DefaultBoardID, Stage: types.StageBacklog, TypeID: "bugfix"}
	if err := srv.taskStore.Create(t.Context(), real); err != nil {
		t.Fatalf("create: %v", err)
	}
	ghost := task.Task{ID: "0000000000000000", Title: "vanished", BoardID: stagepkg.DefaultBoardID}

	moved, err := srv.applyTypeRelocations(t.Context(), stagepkg.DefaultBoardID, []typeRelocation{{
		tasks: []task.Task{*real, ghost}, toType: "", reason: "task_type_removed",
	}})
	if err == nil {
		t.Fatal("relocating a vanished task reported success")
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
}

func TestSetBoardTypes_ValidEditStillRelocates(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := setTypes(t, h, "default", "", bugfixSkippingRefinement()); w.Code != http.StatusOK {
		t.Fatalf("seed: %s", w.Body.String())
	}
	id := createTypedTask(t, h, "bugfix")

	w := setTypes(t, h, "default", "?move_to=", map[string]any{"id": "feature", "label": "Feature", "icon": "feature"})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Moved int `json:"moved_tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Moved != 1 {
		t.Errorf("moved_tasks = %d, want 1", resp.Moved)
	}
	after, err := srv.taskStore.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.TypeID != "" {
		t.Errorf("task type = %q, want untyped", after.TypeID)
	}
}
