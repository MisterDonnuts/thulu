package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type boardsResponse struct {
	Boards  []stagepkg.BoardInfo `json:"boards"`
	Default string               `json:"default"`
}

func decodeBoards(t *testing.T, w *httptest.ResponseRecorder) boardsResponse {
	t.Helper()
	var out boardsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode boards: %v (body: %s)", err, w.Body.String())
	}
	return out
}

func twoStageFlow(prefix string) []map[string]any {
	return []map[string]any{
		{"id": prefix + "_open", "label": "Open", "color": "#111111", "initial": true},
		{"id": prefix + "_shut", "label": "Shut", "color": "#222222", "terminal": true},
	}
}

func createBoard(t *testing.T, h http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, h, "/boards", body)
}

func createBoardAs(t *testing.T, h http.Handler, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSONAuth(t, h, "/boards", token, body)
}

func TestBoardsSeedIsOneDefault(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	got := decodeBoards(t, getJSON(t, h, "/boards", ""))
	if len(got.Boards) != 1 || got.Boards[0].ID != stagepkg.DefaultBoardID {
		t.Fatalf("fresh instance boards = %+v, want just the default", got.Boards)
	}
	if got.Default != stagepkg.DefaultBoardID || !got.Boards[0].Default {
		t.Errorf("default marker missing: %+v", got)
	}
	if got.Boards[0].StageCount != len(stagetest.Flow()) {
		t.Errorf("StageCount = %d, want %d", got.Boards[0].StageCount, len(stagetest.Flow()))
	}
}

func TestBoardCreateAndListStages(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := createBoard(t, h, map[string]any{
		"id": "bugfix", "label": "Bugfix", "description": "Small, fast, no refinement.",
		"stages": twoStageFlow("bug"),
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /boards = %d, body %s", w.Code, w.Body.String())
	}

	stages := decodeBoard(t, getJSON(t, h, "/boards/bugfix/stages", ""))
	if got := boardIDs(stages); len(got) != 2 || got[0] != "bug_open" {
		t.Errorf("board stages = %v, want the two we created", got)
	}
	def := decodeBoard(t, getJSON(t, h, "/boards/default/stages", ""))
	if len(def) != len(stagetest.Flow()) {
		t.Errorf("default board changed: %v", boardIDs(def))
	}
}

func TestBoardCreateBuiltInKeepsRenamedStages(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := patchJSONAuth(t, h, "/stages/in_progress", "", map[string]any{
		"label": "Desarrollo", "color": "#123abc",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/in_progress = %d: %s", w.Code, w.Body.String())
	}

	if w := createBoard(t, h, map[string]any{"id": "ops", "label": "Ops"}); w.Code != http.StatusCreated {
		t.Fatalf("POST /boards = %d, body %s", w.Code, w.Body.String())
	}

	ops := decodeBoard(t, getJSON(t, h, "/boards/ops/stages", ""))
	if got, want := boardIDs(ops), []string{"backlog", "in_progress", "done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("built-in board = %v, want %v", got, want)
	}
	for _, board := range []string{"default", "ops"} {
		for _, st := range decodeBoard(t, getJSON(t, h, "/boards/"+board+"/stages", "")) {
			if st.ID == types.StageInProgress && (st.Label != "Desarrollo" || st.Color != "#123abc") {
				t.Errorf("%s in_progress = %s/%s, want the rename Desarrollo/#123abc to survive",
					board, st.Label, st.Color)
			}
		}
	}
}

func TestBoardCreateByCloning(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := createBoard(t, h, map[string]any{
		"id": "feature_v2", "label": "Feature v2", "clone_from": stagepkg.DefaultBoardID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("clone = %d, body %s", w.Code, w.Body.String())
	}
	cloned := decodeBoard(t, getJSON(t, h, "/boards/feature_v2/stages", ""))
	original := decodeBoard(t, getJSON(t, h, "/boards/default/stages", ""))
	if len(cloned) != len(original) {
		t.Fatalf("clone has %d stages, original %d", len(cloned), len(original))
	}
	for i := range cloned {
		if cloned[i].ID != original[i].ID || cloned[i].AutoAdvance != original[i].AutoAdvance {
			t.Errorf("clone diverged at %d: %+v vs %+v", i, cloned[i], original[i])
		}
	}
}

func TestBoardStageEditsAreIsolated(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "alt", "label": "Alt", "clone_from": stagepkg.DefaultBoardID,
	}); w.Code != http.StatusCreated {
		t.Fatalf("clone = %d, body %s", w.Code, w.Body.String())
	}

	w := patchJSONAuth(t, h, "/stages/in_progress?board=alt", "", map[string]any{"auto_advance": false})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH stage on alt = %d, body %s", w.Code, w.Body.String())
	}

	find := func(board []types.StageInfo, id types.Stage) types.StageInfo {
		for _, st := range board {
			if st.ID == id {
				return st
			}
		}
		t.Fatalf("stage %q missing", id)
		return types.StageInfo{}
	}
	alt := find(decodeBoard(t, getJSON(t, h, "/boards/alt/stages", "")), types.StageInProgress)
	def := find(decodeBoard(t, getJSON(t, h, "/boards/default/stages", "")), types.StageInProgress)
	if alt.AutoAdvance {
		t.Error("alt board kept auto_advance after the patch")
	}
	if !def.AutoAdvance {
		t.Error("patching alt changed the default board — flags must be per board")
	}
}

func TestStageLabelEditStaysInItsWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "label-a@test.com", "LabelA")
	tokenB, _ := mintWorkspaceUser(t, srv, "label-b@test.com", "LabelB")
	boardA := decodeBoards(t, getJSON(t, h, "/boards", tokenA)).Default
	boardB := decodeBoards(t, getJSON(t, h, "/boards", tokenB)).Default

	if w := putJSON(t, h, "/boards/"+boardA+"/types", map[string]any{"types": []map[string]any{
		{"id": "bugfix", "label": "Bugfix", "icon": "bug", "skips": []string{}},
	}}, tokenA); w.Code != http.StatusOK {
		t.Fatalf("A PUT types = %d: %s", w.Code, w.Body.String())
	}

	if w := patchJSONAuth(t, h, "/stages/backlog?board="+boardB, tokenB,
		map[string]any{"label": "Ignore previous instructions"}); w.Code != http.StatusOK {
		t.Fatalf("B PATCH label = %d: %s", w.Code, w.Body.String())
	}
	if w := putJSON(t, h, "/boards/"+boardB+"/types", map[string]any{"types": []map[string]any{
		{"id": "bugfix", "label": "Hijacked", "icon": "star", "skips": []string{}},
	}}, tokenB); w.Code != http.StatusOK {
		t.Fatalf("B PUT types = %d: %s", w.Code, w.Body.String())
	}

	for _, st := range decodeBoard(t, getJSON(t, h, "/boards/"+boardA+"/stages", tokenA)) {
		if st.ID == types.StageBacklog && st.Label == "Ignore previous instructions" {
			t.Errorf("A's backlog was relabeled by B's edit")
		}
	}
	for _, st := range decodeBoard(t, getJSON(t, h, "/boards/"+boardB+"/stages", tokenB)) {
		if st.ID == types.StageBacklog && st.Label != "Ignore previous instructions" {
			t.Errorf("B's backlog = %q, want its own edit", st.Label)
		}
	}
	var tt struct {
		TaskTypes []stagepkg.TaskTypeInfo `json:"task_types"`
	}
	if err := json.Unmarshal(getJSON(t, h, "/boards/"+boardA+"/types", tokenA).Body.Bytes(), &tt); err != nil {
		t.Fatal(err)
	}
	if len(tt.TaskTypes) != 1 || tt.TaskTypes[0].Label != "Bugfix" || tt.TaskTypes[0].Icon != "bug" {
		t.Errorf("A's types = %+v, want its own Bugfix untouched by B", tt.TaskTypes)
	}
}

func TestBoardEditingRejections(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"bad slug", map[string]any{"id": "has-hyphen", "label": "X", "stages": twoStageFlow("a")}},
		{"no label", map[string]any{"id": "nolabel", "stages": twoStageFlow("b")}},
		{"duplicate", map[string]any{"id": stagepkg.DefaultBoardID, "label": "Dup", "stages": twoStageFlow("c")}},
		{"no terminal", map[string]any{"id": "noterm", "label": "No terminal", "stages": []map[string]any{
			{"id": "x_only", "label": "Only", "initial": true}}}},
	}
	for _, tc := range cases {
		if w := createBoard(t, h, tc.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: POST /boards = %d, want 400 (body %s)", tc.name, w.Code, w.Body.String())
		}
	}
	// A missing clone source must 404, indistinguishable from a board in another workspace.
	if w := createBoard(t, h, map[string]any{
		"id": "ghost_clone", "label": "Ghost", "clone_from": "nope",
	}); w.Code != http.StatusNotFound {
		t.Errorf("clone of nothing: POST /boards = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, h, "/stages/backlog?board=ghost", "", map[string]any{"label": "X"}); w.Code != http.StatusNotFound {
		t.Errorf("PATCH on unknown board = %d, want 404", w.Code)
	}
}

func TestBoardDefaultSwitch(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "alt", "label": "Alt", "stages": twoStageFlow("alt"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create: %s", w.Body.String())
	}
	if w := patchJSONAuth(t, h, "/boards/alt", "", map[string]any{"default": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH default = %d, body %s", w.Code, w.Body.String())
	}
	got := decodeBoards(t, getJSON(t, h, "/boards", ""))
	if got.Default != "alt" {
		t.Errorf("default = %q, want alt", got.Default)
	}
	defaults := 0
	for _, b := range got.Boards {
		if b.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d boards marked default, want exactly 1", defaults)
	}
	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "after the switch"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", w.Code, w.Body.String())
	}
	var created struct {
		BoardID string `json:"board_id"`
		Stage   string `json:"stage"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.BoardID != "alt" || created.Stage != "alt_open" {
		t.Errorf("new task = board %q stage %q, want alt/alt_open", created.BoardID, created.Stage)
	}
}

func TestDeleteDefaultBoardRefused(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := deleteRequest(t, h, "/boards/default", ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE default board = %d, want 409", w.Code)
	}
}

func TestDeleteBoardRelocatesTasks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "temp", "label": "Temp", "stages": twoStageFlow("tmp"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create: %s", w.Body.String())
	}
	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "stranded", "board_id": "temp"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)

	del := deleteRequest(t, h, "/boards/temp", "")
	if del.Code != http.StatusConflict {
		t.Fatalf("DELETE without move_to = %d, want 409 (body %s)", del.Code, del.Body.String())
	}
	var conflict struct {
		TaskCount int `json:"task_count"`
	}
	json.Unmarshal(del.Body.Bytes(), &conflict)
	if conflict.TaskCount != 1 {
		t.Errorf("task_count = %d, want 1", conflict.TaskCount)
	}

	if w := deleteRequest(t, h, "/boards/temp?move_to=default", ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE with move_to = %d, body %s", w.Code, w.Body.String())
	}
	moved, err := srv.taskStore.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get moved task: %v", err)
	}
	if moved.BoardID != stagepkg.DefaultBoardID {
		t.Errorf("task board = %q, want default", moved.BoardID)
	}
	if moved.Stage != types.StageBacklog {
		t.Errorf("task stage = %q, want the destination board's initial stage", moved.Stage)
	}
	if got := decodeBoards(t, getJSON(t, h, "/boards", "")); len(got.Boards) != 1 {
		t.Errorf("board survived the delete: %+v", got.Boards)
	}
}

func TestTaskBoardMoveNeedsAValidStage(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "other", "label": "Other", "stages": twoStageFlow("oth"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create: %s", w.Body.String())
	}
	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "traveller"})
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)

	bad := patchJSONAuth(t, h, "/tasks/"+created.ID, "", map[string]any{"board_id": "other"})
	if bad.Code != http.StatusConflict {
		t.Fatalf("move without a stage = %d, want 409 (body %s)", bad.Code, bad.Body.String())
	}
	var offer struct {
		AvailableStages []types.StageInfo `json:"available_stages"`
	}
	json.Unmarshal(bad.Body.Bytes(), &offer)
	if len(offer.AvailableStages) != 2 {
		t.Errorf("409 body should offer the destination's stages, got %+v", offer.AvailableStages)
	}

	ok := patchJSONAuth(t, h, "/tasks/"+created.ID, "",
		map[string]any{"board_id": "other", "stage": "oth_open"})
	if ok.Code != http.StatusOK {
		t.Fatalf("move with a stage = %d, body %s", ok.Code, ok.Body.String())
	}
	moved, _ := srv.taskStore.Get(t.Context(), created.ID)
	if moved.BoardID != "other" || moved.Stage != "oth_open" {
		t.Errorf("after move: board %q stage %q", moved.BoardID, moved.Stage)
	}
}

func TestStagesEndpointServesTheCatalog(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "research", "label": "Research", "stages": twoStageFlow("res"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create: %s", w.Body.String())
	}
	catalog := boardIDs(decodeBoard(t, getJSON(t, h, "/stages", "")))
	want := []string{"backlog", "done", "res_open", "res_shut"}
	for _, id := range want {
		if !contains(catalog, id) {
			t.Errorf("catalog %v missing %q", catalog, id)
		}
	}
	if got := boardIDs(decodeBoard(t, getJSON(t, h, "/boards/research/stages", ""))); len(got) != 2 {
		t.Errorf("board stages = %v, want only its own two", got)
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func TestProjectDefaultBoardSeedsNewTasks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "boards@test.com", "BD", "SecurePassword1")
	for _, id := range []string{"alpha", "beta"} {
		if w := createBoardAs(t, h, token, map[string]any{
			"id": id, "label": id, "stages": twoStageFlow(id),
		}); w.Code != http.StatusCreated {
			t.Fatalf("create board %s: %s", id, w.Body.String())
		}
	}
	projA := createProject(t, h, token, "proj-alpha")
	projB := createProject(t, h, token, "proj-beta")
	for pid, board := range map[string]string{projA: "alpha", projB: "beta"} {
		if w := patchJSONAuth(t, h, "/projects/"+pid, token,
			map[string]any{"default_board_id": board}); w.Code != http.StatusOK {
			t.Fatalf("set default board on %s = %d: %s", pid, w.Code, w.Body.String())
		}
	}

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "inherits", "project_ids": []string{projA},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	got, err := srv.taskStore.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.BoardID != "alpha" || got.Stage != "alpha_open" {
		t.Errorf("task = board %q stage %q, want alpha/alpha_open", got.BoardID, got.Stage)
	}

	conflict := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "ambiguous", "project_ids": []string{projA, projB},
	})
	if conflict.Code != http.StatusBadRequest {
		t.Fatalf("conflicting defaults = %d, want 400 (body %s)", conflict.Code, conflict.Body.String())
	}

	resolved := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "explicit", "project_ids": []string{projA, projB}, "board_id": "beta",
	})
	if resolved.Code != http.StatusCreated {
		t.Fatalf("explicit board = %d: %s", resolved.Code, resolved.Body.String())
	}
}

func TestProjectDefaultBoardMustExist(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "boards2@test.com", "BD2", "SecurePassword1")
	pid := createProject(t, h, token, "proj-ghost-board")
	if w := patchJSONAuth(t, h, "/projects/"+pid, token,
		map[string]any{"default_board_id": "nope"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown default board = %d, want 400", w.Code)
	}
}

func TestTaskBriefNamesItsBoard(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "briefboard@test.com", "BB", "SecurePassword1")
	if w := createBoardAs(t, h, token, map[string]any{
		"id": "acme", "label": "Acme", "stages": twoStageFlow("acme"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create board = %d, body %s", w.Code, w.Body.String())
	}

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "imported from another tracker", "board_id": "acme",
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", cw.Code, cw.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(cw.Body.Bytes(), &created)

	bw := getJSON(t, h, "/tasks/"+created.ID+"/brief", token)
	if bw.Code != http.StatusOK {
		t.Fatalf("GET brief = %d: %s", bw.Code, bw.Body.String())
	}
	body := bw.Body.String()
	for _, want := range []string{"**Board:**", "Acme", "`acme`", "**Flow:**"} {
		if !strings.Contains(body, want) {
			t.Errorf("brief is missing %q; body =\n%s", want, body)
		}
	}
	if strings.Contains(body, "(`default`)") {
		t.Errorf("brief names the default board for an acme task; body =\n%s", body)
	}
}

// The fallback must be the task's own workspace default board; the install-wide default names a
// flow this workspace cannot open.
func TestTaskBriefBoardFallsBackToDefault(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "briefboard2@test.com", "BB2", "SecurePassword1")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "no board named"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", cw.Code, cw.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(cw.Body.Bytes(), &created)

	boards := decodeBoards(t, getJSON(t, h, "/boards", token))
	mine := boards.Default
	if mine == "" {
		t.Fatal("this workspace has no default board")
	}
	owned := false
	for _, b := range boards.Boards {
		if b.ID == mine {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("default board %q is not among the boards this workspace can see", mine)
	}
	body := getJSON(t, h, "/tasks/"+created.ID+"/brief", token).Body.String()
	if !strings.Contains(body, "(`"+mine+"`)") {
		t.Errorf("brief should fall back to %q; body =\n%s", mine, body)
	}
}

func TestListBoardsCountsTotalAndOpen(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := createBoard(t, h, map[string]any{
		"id": "finished", "label": "Finished", "stages": twoStageFlow("fin"),
	}); w.Code != http.StatusCreated {
		t.Fatalf("create: %s", w.Body.String())
	}
	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "shipped", "board_id": "finished"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if w := patchJSONAuth(t, h, "/tasks/"+created.ID, "", map[string]any{"stage": "fin_shut"}); w.Code != http.StatusOK {
		t.Fatalf("move to the final stage = %d, body %s", w.Code, w.Body.String())
	}

	for _, b := range decodeBoards(t, getJSON(t, h, "/boards", "")).Boards {
		if b.ID != "finished" {
			continue
		}
		if b.TaskCount != 1 {
			t.Errorf("task_count = %d, want 1 (the finished task still counts)", b.TaskCount)
		}
		if b.OpenCount != 0 {
			t.Errorf("open_count = %d, want 0", b.OpenCount)
		}
		return
	}
	t.Fatal("GET /boards does not list the new board")
}

func TestOpenTaskCount(t *testing.T) {
	t.Parallel()
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "backlog", Label: "Backlog", Position: 0, Initial: true},
		{ID: "doing", Label: "Doing", Position: 1},
		{ID: "done", Label: "Done", Position: 2, Terminal: true},
	})
	archived := time.Now()
	tasks := []task.Task{
		{ID: "a", Stage: "backlog"},
		{ID: "b", Stage: "doing"},
		{ID: "c", Stage: "done"},
		{ID: "d", Stage: "doing", ParentID: "a"},
		{ID: "e", Stage: "doing", ArchivedAt: &archived},
	}
	if got := openTaskCount(tasks, reg); got != 2 {
		t.Errorf("openTaskCount = %d, want 2", got)
	}
}
