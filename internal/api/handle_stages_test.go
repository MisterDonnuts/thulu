package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func decodeBoard(t *testing.T, w *httptest.ResponseRecorder) []types.StageInfo {
	t.Helper()
	var board []types.StageInfo
	if err := json.Unmarshal(w.Body.Bytes(), &board); err != nil {
		t.Fatalf("decode board: %v (body: %s)", err, w.Body.String())
	}
	return board
}

func boardIDs(board []types.StageInfo) []string {
	out := make([]string, len(board))
	for i, st := range board {
		out[i] = string(st.ID)
	}
	return out
}

func TestStagesCreate(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := postJSON(t, h, "/stages", map[string]any{
		"id": "security_review", "label": "Security Review", "color": "#ff0044",
		"auto_advance": true, "needs_agent": true, "wip_group": "review",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /stages = %d, body %s", w.Code, w.Body.String())
	}
	board := decodeBoard(t, w)
	if got := board[len(board)-2].ID; got != "security_review" {
		t.Errorf("new stage at %v, want second-to-last (before done)", boardIDs(board))
	}

	g := getJSON(t, h, "/stages", "")
	var listed []types.StageInfo
	json.Unmarshal(g.Body.Bytes(), &listed)
	found := false
	for _, st := range listed {
		if st.ID == "security_review" {
			found = true
			if !st.AutoAdvance || st.WipGroup != "review" || st.Color != "#ff0044" {
				t.Errorf("flags lost on round-trip: %+v", st)
			}
		}
	}
	if !found {
		t.Fatal("created stage missing from GET /stages")
	}

	if w := postJSON(t, h, "/stages", map[string]any{"id": "security_review", "label": "X"}); w.Code != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", w.Code)
	}
	for _, id := range []string{"mention", "chat", "archived", "queued", "With-Hyphen", ""} {
		if w := postJSON(t, h, "/stages", map[string]any{"id": id, "label": "X"}); w.Code < 400 {
			t.Errorf("create %q = %d, want error", id, w.Code)
		}
	}
}

func TestStagesCreateWithIndex(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	idx := 2
	w := postJSON(t, h, "/stages", map[string]any{
		"id": "triage", "label": "Triage", "index": idx,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}
	if board := decodeBoard(t, w); board[idx].ID != "triage" {
		t.Errorf("board = %v, want triage at index %d", boardIDs(board), idx)
	}
}

func TestStagesPatch(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := patchJSONAuth(t, h, "/stages/refinement", "", map[string]any{
		"label": "Analysis", "color": "#123abc",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", w.Code, w.Body.String())
	}
	for _, st := range decodeBoard(t, w) {
		if st.ID == types.StageRefinement && (st.Label != "Analysis" || st.Color != "#123abc" || !st.IngestFlags) {
			t.Errorf("patch lost fields: %+v", st)
		}
	}

	w = patchJSONAuth(t, h, "/stages/refinement", "", map[string]any{"initial": true})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH initial = %d: %s", w.Code, w.Body.String())
	}
	initials := 0
	for _, st := range decodeBoard(t, w) {
		if st.Initial {
			initials++
			if st.ID != types.StageRefinement {
				t.Errorf("initial moved to %s, want refinement", st.ID)
			}
		}
	}
	if initials != 1 {
		t.Errorf("initials = %d, want exactly 1", initials)
	}

	if w := patchJSONAuth(t, h, "/stages/done", "", map[string]any{"auto_advance": true}); w.Code != http.StatusBadRequest {
		t.Errorf("terminal+auto_advance = %d, want 400", w.Code)
	}
	if w := patchJSONAuth(t, h, "/stages/refinement", "", map[string]any{"color": "red"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad color = %d, want 400", w.Code)
	}
	if w := patchJSONAuth(t, h, "/stages/nope", "", map[string]any{"label": "X"}); w.Code != http.StatusNotFound {
		t.Errorf("unknown stage = %d, want 404", w.Code)
	}
}

func TestStagesReorder(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	current := stagetest.Flow()
	ids := make([]string, len(current))
	for i, st := range current {
		ids[i] = string(st.ID)
	}
	ids[2], ids[3] = ids[3], ids[2]

	w := putJSON(t, h, "/stages/order", map[string]any{"ids": ids}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /stages/order = %d: %s", w.Code, w.Body.String())
	}
	board := decodeBoard(t, w)
	if board[2].ID != types.StageInProgress || board[3].ID != types.StageReview {
		t.Errorf("order after swap = %v", boardIDs(board))
	}

	if w := putJSON(t, h, "/stages/order", map[string]any{"ids": ids[:len(ids)-1]}, ""); w.Code != http.StatusBadRequest {
		t.Errorf("short order = %d, want 400", w.Code)
	}
	bad := append(append([]string{}, ids[:len(ids)-1]...), "impostor")
	if w := putJSON(t, h, "/stages/order", map[string]any{"ids": bad}, ""); w.Code != http.StatusBadRequest {
		t.Errorf("unknown id in order = %d, want 400", w.Code)
	}
	dup := append(append([]string{}, ids[:len(ids)-1]...), ids[0])
	if w := putJSON(t, h, "/stages/order", map[string]any{"ids": dup}, ""); w.Code != http.StatusBadRequest {
		t.Errorf("dup id in order = %d, want 400", w.Code)
	}
}

func TestStagesDeleteEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := deleteRequest(t, h, "/stages/release", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}
	g := getJSON(t, h, "/stages", "")
	for _, st := range decodeBoard(t, g) {
		if st.ID == types.StageRelease {
			t.Error("release still on the board after delete")
		}
	}
}

func TestStagesDeleteGuards(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := deleteRequest(t, h, "/stages/backlog", ""); w.Code != http.StatusConflict {
		t.Errorf("delete initial = %d, want 409", w.Code)
	}
	if w := deleteRequest(t, h, "/stages/done", ""); w.Code != http.StatusConflict {
		t.Errorf("delete last terminal = %d, want 409", w.Code)
	}
	if w := deleteRequest(t, h, "/stages/never_was", ""); w.Code != http.StatusNotFound {
		t.Errorf("delete unknown = %d, want 404", w.Code)
	}
}

func TestStagesDeleteWithTasks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	for _, title := range []string{"t1", "t2"} {
		tw := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": title, "stage": "testing"})
		if tw.Code != http.StatusCreated {
			t.Fatalf("create task = %d: %s", tw.Code, tw.Body.String())
		}
	}
	prios, err := srv.settingsStore.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("read priorities: %v", err)
	}
	if _, ok := prios["testing"]; !ok {
		t.Fatal("expected seeded priority for testing")
	}

	w := deleteRequest(t, h, "/stages/testing", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with tasks = %d, want 409: %s", w.Code, w.Body.String())
	}
	var conflict struct {
		TaskCount int `json:"task_count"`
	}
	json.Unmarshal(w.Body.Bytes(), &conflict)
	if conflict.TaskCount != 2 {
		t.Errorf("task_count = %d, want 2", conflict.TaskCount)
	}

	for _, dest := range []string{"testing", "mention", "never_was"} {
		if w := deleteRequest(t, h, "/stages/testing?move_to="+dest, ""); w.Code != http.StatusBadRequest {
			t.Errorf("move_to=%s = %d, want 400", dest, w.Code)
		}
	}

	w = deleteRequest(t, h, "/stages/testing?move_to=backlog", "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete with move_to = %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		MovedTasks int    `json:"moved_tasks"`
		MovedTo    string `json:"moved_to"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.MovedTasks != 2 || res.MovedTo != "backlog" {
		t.Errorf("moved = %+v, want 2 → backlog", res)
	}

	tasks, err := srv.taskStore.ListFilter(t.Context(), "backlog")
	if err != nil {
		t.Fatalf("list backlog: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("backlog holds %d tasks, want 2", len(tasks))
	}
	for _, tk := range tasks {
		events, _ := srv.taskStore.ListEvents(t.Context(), tk.ID)
		sawMove := false
		for _, ev := range events {
			if ev.Kind == task.EventStageEntered {
				sawMove = true
			}
		}
		if !sawMove {
			t.Errorf("task %s: no stage_entered event after administrative move", tk.ID)
		}
	}

	prios, err = srv.settingsStore.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("re-read priorities: %v", err)
	}
	if _, ok := prios["testing"]; ok {
		t.Error("deleted stage still has a sandbox priority entry")
	}
}

func TestStagesReadOnlyWithoutStore(t *testing.T) {
	srv := &Server{boards: stagepkg.StaticBoards()}
	w := httptest.NewRecorder()
	srv.handleCreateStage(w, httptest.NewRequest(http.MethodPost, "/stages", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("create on read-only registry = %d, want 503", w.Code)
	}
}

func TestStagesCustomFlowEndToEnd(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	w := postJSON(t, h, "/stages", map[string]any{
		"id": "compliance", "label": "Compliance", "blocked_by_flags": true, "index": 3,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}

	tw := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "audit", "stage": "compliance"})
	if tw.Code != http.StatusCreated {
		t.Fatalf("task into custom stage = %d: %s", tw.Code, tw.Body.String())
	}
	var created task.Task
	json.Unmarshal(tw.Body.Bytes(), &created)

	if err := srv.taskStore.AddFlag(t.Context(), created.ID, &task.Flag{Kind: task.FlagCustom, Label: "open finding", CreatedBy: "test"}); err != nil {
		t.Fatalf("add flag: %v", err)
	}
	mw := patchJSONAuth(t, h, "/tasks/"+created.ID, "", map[string]any{"stage": "in_progress"})
	if mw.Code != http.StatusConflict {
		t.Errorf("leave gated custom stage with pending flag = %d, want 409: %s", mw.Code, mw.Body.String())
	}
}

func TestStagesWritesAnswerInReaderLanguage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	es := map[string]string{"Accept-Language": "es"}
	label := func(board []types.StageInfo, id types.Stage) string {
		for _, st := range board {
			if st.ID == id {
				return st.Label
			}
		}
		return ""
	}
	ids := make([]string, 0)
	for _, st := range stagetest.Flow() {
		ids = append(ids, string(st.ID))
	}
	ids[2], ids[3] = ids[3], ids[2]

	writes := []struct {
		name string
		w    *httptest.ResponseRecorder
	}{
		{"PUT /stages/order", putJSONWithHeaders(t, h, "/stages/order", map[string]any{"ids": ids}, es)},
		{"PATCH /stages/{id}", requestWithHeaders(t, h, http.MethodPatch, "/stages/refinement",
			map[string]any{"color": "#123abc"}, es)},
		{"POST /stages", requestWithHeaders(t, h, http.MethodPost, "/stages",
			map[string]any{"id": "triage", "label": "Triage"}, es)},
	}
	for _, wr := range writes {
		if wr.w.Code >= 300 {
			t.Fatalf("%s = %d: %s", wr.name, wr.w.Code, wr.w.Body.String())
		}
		if got := label(decodeBoard(t, wr.w), types.StageInProgress); got != "En curso" {
			t.Errorf("%s: in_progress = %q, want the Spanish seeded name", wr.name, got)
		}
	}
	if got := label(decodeBoard(t, writes[2].w), "triage"); got != "Triage" {
		t.Errorf("POST: a typed name came back as %q", got)
	}

	del := requestWithHeaders(t, h, http.MethodDelete, "/stages/release", nil, es)
	if del.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", del.Code, del.Body.String())
	}
	var deleted struct {
		Stages []types.StageInfo `json:"stages"`
	}
	if err := json.Unmarshal(del.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if got := label(deleted.Stages, types.StageDone); got != "Hecho" {
		t.Errorf("DELETE: done = %q, want the Spanish seeded name", got)
	}

	pb := requestWithHeaders(t, h, http.MethodPatch, "/boards/default", map[string]any{"description": "Equipo web"}, es)
	if pb.Code != http.StatusOK {
		t.Fatalf("PATCH /boards/default = %d: %s", pb.Code, pb.Body.String())
	}
	var boards []stagepkg.BoardInfo
	if err := json.Unmarshal(pb.Body.Bytes(), &boards); err != nil {
		t.Fatalf("decode boards: %v", err)
	}
	for _, b := range boards {
		if b.ID == "default" && b.Label != "Principal" {
			t.Errorf("PATCH /boards/default: label = %q, want the Spanish seeded name", b.Label)
		}
	}

	if got := label(decodeBoard(t, getJSON(t, h, "/boards/default/stages", "")), types.StageInProgress); got != "In progress" {
		t.Errorf("English GET after Spanish writes: in_progress = %q", got)
	}
}

func requestWithHeaders(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
