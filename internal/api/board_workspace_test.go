package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
)

func boardListIDs(bs []stagepkg.BoardInfo) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ID
	}
	return out
}

func createBoardAuth(t *testing.T, h http.Handler, token, id string) {
	t.Helper()
	w := postJSONAuth(t, h, "/boards", token, map[string]any{
		"id": id, "label": strings.ToUpper(id), "stages": twoStageFlow(id),
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /boards as %s = %d: %s", id, w.Code, w.Body.String())
	}
}

func TestIntegration_WorkspaceIsolation_Boards(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "board-a@test.com", "BoardA")
	tokenB, _ := mintWorkspaceUser(t, srv, "board-b@test.com", "BoardB")

	createBoardAuth(t, h, tokenA, "aflow")

	got := decodeBoards(t, getJSON(t, h, "/boards", tokenA))
	ids := boardListIDs(got.Boards)
	if !slices.Contains(ids, "aflow") {
		t.Fatalf("A's boards = %v, want its own flow among them", ids)
	}
	if slices.Contains(ids, stagepkg.DefaultBoardID) {
		t.Errorf("A's boards = %v, leaked the install-wide default board", ids)
	}
	if got.Default == "" || got.Default == stagepkg.DefaultBoardID {
		t.Errorf("A's default board = %q, want one of its own", got.Default)
	}

	bList := decodeBoards(t, getJSON(t, h, "/boards", tokenB))
	if slices.Contains(boardListIDs(bList.Boards), "aflow") {
		t.Errorf("B's board list leaked A's flow: %v", boardListIDs(bList.Boards))
	}
	if bList.Default == got.Default {
		t.Errorf("both workspaces fall back to %q — the default board is not scoped", bList.Default)
	}

	stages := decodeBoard(t, getJSON(t, h, "/stages", tokenB))
	for _, st := range stages {
		if strings.HasPrefix(string(st.ID), "aflow_") {
			t.Errorf("GET /stages leaked A's column %q (label %q) to B", st.ID, st.Label)
		}
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/boards/aflow/stages"},
		{http.MethodGet, "/boards/aflow/types"},
		{http.MethodPut, "/boards/aflow/types"},
		{http.MethodPatch, "/boards/aflow"},
		{http.MethodDelete, "/boards/aflow"},
		{http.MethodGet, "/stages?board=aflow"},
		{http.MethodPost, "/stages?board=aflow"},
		{http.MethodPut, "/stages/order?board=aflow"},
		{http.MethodPatch, "/stages/aflow_open?board=aflow"},
		{http.MethodDelete, "/stages/aflow_open?board=aflow"},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokenB)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("B %s %s = %d, want 404: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	cw := postJSONAuth(t, h, "/boards", tokenB, map[string]any{
		"id": "stolen", "label": "Stolen", "clone_from": "aflow",
	})
	if cw.Code != http.StatusNotFound {
		t.Errorf("B cloning A's board = %d, want 404: %s", cw.Code, cw.Body.String())
	}

	if w := getJSON(t, h, "/boards/aflow/stages", tokenA); w.Code != http.StatusOK {
		t.Errorf("A GET own board stages = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_WorkspaceIsolation_TaskBoardPlacement(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "place-a@test.com", "PlaceA")
	tokenB, wsB := mintWorkspaceUser(t, srv, "place-b@test.com", "PlaceB")

	createBoardAuth(t, h, tokenA, "aonly")

	cw := postJSONAuth(t, h, "/tasks", tokenB, map[string]any{
		"title": "smuggled in", "board_id": "aonly",
	})
	if cw.Code == http.StatusCreated {
		t.Fatalf("B created a task on A's board: %s", cw.Body.String())
	}

	ow := postJSONAuth(t, h, "/tasks", tokenB, map[string]any{"title": "mine"})
	if ow.Code != http.StatusCreated {
		t.Fatalf("B POST /tasks = %d: %s", ow.Code, ow.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(ow.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	boardID, _ := created["board_id"].(string)
	if boardID == "aonly" || boardID == stagepkg.DefaultBoardID {
		t.Fatalf("B's task landed on board %q, which is not B's", boardID)
	}
	if got := created["workspace_id"]; got != wsB {
		t.Errorf("task workspace_id = %v, want %q", got, wsB)
	}
	if !boardVisibleTo(t, h, tokenB, boardID) {
		t.Errorf("B's task is on board %q, which B cannot even list", boardID)
	}

	mw := patchJSONAuth(t, h, "/tasks/"+created["id"].(string), tokenB,
		map[string]any{"board_id": "aonly"})
	if mw.Code == http.StatusOK {
		t.Errorf("B moved its task onto A's board: %s", mw.Body.String())
	}
}

func boardVisibleTo(t *testing.T, h http.Handler, token, boardID string) bool {
	t.Helper()
	return slices.Contains(
		boardListIDs(decodeBoards(t, getJSON(t, h, "/boards", token)).Boards), boardID)
}

func TestIntegration_WorkspaceIsolation_TaskTypes(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "type-a@test.com", "TypeA")
	tokenB, _ := mintWorkspaceUser(t, srv, "type-b@test.com", "TypeB")

	createBoardAuth(t, h, tokenA, "tflow")
	pw := putJSON(t, h, "/boards/tflow/types", map[string]any{
		"types": []map[string]any{
			{"id": "hotfix", "label": "Client hotfix", "skips": []string{}},
		},
	}, tokenA)
	if pw.Code != http.StatusOK {
		t.Fatalf("A PUT /boards/tflow/types = %d: %s", pw.Code, pw.Body.String())
	}

	var catalog struct {
		TaskTypes []stagepkg.TaskTypeInfo `json:"task_types"`
	}
	w := getJSON(t, h, "/task-types", tokenB)
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode task types: %v (%s)", err, w.Body.String())
	}
	for _, tt := range catalog.TaskTypes {
		if tt.ID == "hotfix" {
			t.Errorf("GET /task-types leaked A's type to B: %+v", tt)
		}
	}
	w = getJSON(t, h, "/task-types", tokenA)
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tt := range catalog.TaskTypes {
		if tt.ID == "hotfix" {
			found = true
		}
	}
	if !found {
		t.Errorf("A's own task type went missing from its catalog: %+v", catalog.TaskTypes)
	}
}

func TestIntegration_WorkspaceGetsItsOwnDefaultBoard(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "seed@test.com", "Seed")

	got := decodeBoards(t, getJSON(t, h, "/boards", token))
	if len(got.Boards) != 1 {
		t.Fatalf("a fresh workspace sees %d boards, want exactly its own: %v",
			len(got.Boards), boardListIDs(got.Boards))
	}
	seeded := got.Boards[0]
	if seeded.ID != got.Default || !seeded.Default {
		t.Errorf("seeded board %+v is not the workspace's default (%q)", seeded, got.Default)
	}
	stages := decodeBoard(t, getJSON(t, h, "/boards/"+seeded.ID+"/stages", token))
	if len(stages) != len(stagetest.Flow()) {
		t.Errorf("seeded board has %d columns, want the install's %d", len(stages), len(stagetest.Flow()))
	}
	catalog := decodeBoard(t, getJSON(t, h, "/stages", token))
	if len(catalog) != len(stages) {
		t.Errorf("GET /stages returned %d stages against %d on the only board it can see",
			len(catalog), len(stages))
	}
}

func TestIntegration_UnscopedCallerStillSeesEveryBoard(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "wide-a@test.com", "WideA")
	createBoardAuth(t, h, tokenA, "hidden")

	ids := boardListIDs(decodeBoards(t, getJSON(t, h, "/boards", "")).Boards)
	for _, want := range []string{stagepkg.DefaultBoardID, "hidden"} {
		if !slices.Contains(ids, want) {
			t.Errorf("unscoped caller's boards = %v, missing %q", ids, want)
		}
	}
}

func TestIntegration_CopiesStayOnTheSourceBoard(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "copy-board@test.com", "CopyBoard")

	createBoardAuth(t, h, token, "bugs")
	if w := putJSON(t, h, "/boards/bugs/types", map[string]any{
		"types": []map[string]any{{"id": "bug", "label": "Bug", "skips": []string{}}},
	}, token); w.Code != http.StatusOK {
		t.Fatalf("PUT /boards/bugs/types = %d: %s", w.Code, w.Body.String())
	}
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "on bugs", "board_id": "bugs", "type_id": "bug",
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d: %s", cw.Code, cw.Body.String())
	}
	var src map[string]any
	if err := json.Unmarshal(cw.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	srcID := src["id"].(string)
	if mw := patchJSONAuth(t, h, "/tasks/"+srcID, token, map[string]any{"stage": "bugs_shut"}); mw.Code != http.StatusOK {
		t.Fatalf("move source = %d: %s", mw.Code, mw.Body.String())
	}

	for _, tc := range []struct{ op, stage string }{
		{"clone", "bugs_open"},
		{"fork", "bugs_shut"},
	} {
		w := postJSONAuth(t, h, "/tasks/"+srcID+"/"+tc.op, token, nil)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST %s = %d: %s", tc.op, w.Code, w.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["board_id"] != "bugs" || got["type_id"] != "bug" || got["stage"] != tc.stage {
			t.Errorf("%s: board=%v type=%v stage=%v, want bugs/bug/%s",
				tc.op, got["board_id"], got["type_id"], got["stage"], tc.stage)
		}
	}
}
