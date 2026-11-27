package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	rbBoard   = "rb"
	rbBacklog = types.Stage("rb_backlog")
	rbReview  = types.Stage("rb_review")
	rbRelease = types.Stage("rb_release")
	rbDone    = types.Stage("rb_done")
	rbAgent   = "thulu-release"
)

type batchEnv struct {
	srv   *Server
	h     http.Handler
	token string
	ws    string
}

// newBatchEnv is a workspace with a board whose review stage asks for a merge approval, so a batch
// that jumps it has an approval to skip.
func newBatchEnv(t *testing.T) batchEnv {
	t.Helper()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "release@test.com", "Release")
	err := srv.stageAdmin.CreateBoard(t.Context(), stagepkg.BoardInfo{ID: rbBoard, Label: "Release flow", WorkspaceID: ws}, []types.StageInfo{
		{ID: rbBacklog, Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: rbReview, Label: "Review", Color: "#bb86fc", OpenPR: true,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "merge", Label: "Merge"}}},
		{ID: rbRelease, Label: "Release", Color: "#6fcf97", MergeTarget: true},
		{ID: rbDone, Label: "Done", Color: "#3aa667", Terminal: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return batchEnv{srv: srv, h: h, token: token, ws: ws}
}

func (e batchEnv) task(t *testing.T, title string, stage types.Stage, projects ...string) *task.Task {
	t.Helper()
	tk := &task.Task{Title: title, WorkspaceID: e.ws, BoardID: rbBoard, Stage: stage, ProjectIDs: projects}
	if err := e.srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func (e batchEnv) stage(t *testing.T, id string) types.Stage {
	t.Helper()
	got, err := e.srv.taskStore.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Stage
}

func decodeBatch(t *testing.T, body []byte) batchMoveResp {
	t.Helper()
	var resp batchMoveResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return resp
}

func (e batchEnv) move(t *testing.T, body map[string]any) (int, batchMoveResp) {
	t.Helper()
	w := postJSONAuth(t, e.h, "/tasks/batch-move", e.token, body)
	if w.Code != http.StatusOK {
		return w.Code, batchMoveResp{}
	}
	return w.Code, decodeBatch(t, w.Body.Bytes())
}

func skipReasons(resp batchMoveResp) map[string]string {
	out := map[string]string{}
	for _, s := range resp.Skipped {
		out[s.ID] = s.Reason
	}
	return out
}

func movedIDs(resp batchMoveResp) []string {
	var out []string
	for _, m := range resp.Moved {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	return out
}

func TestBatchMove_MovesForwardThroughTheGate(t *testing.T) {
	e := newBatchEnv(t)
	ctx := t.Context()
	a := e.task(t, "a", rbRelease)
	b := e.task(t, "b", rbRelease)
	held := e.task(t, "held", rbRelease)
	if err := e.srv.taskStore.AddApproval(ctx, &task.Approval{TaskID: held.ID, Key: "deploy_prod", Label: "Deploy to production",
		Stage: rbRelease, Source: task.ApprovalFromAgent, Status: task.ApprovalPending, CreatedBy: "agent"}); err != nil {
		t.Fatal(err)
	}
	early := e.task(t, "still in review", rbReview)
	foreign := &task.Task{Title: "theirs", WorkspaceID: "ws-other", BoardID: rbBoard, Stage: rbRelease}
	if err := e.srv.taskStore.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}

	code, resp := e.move(t, map[string]any{
		"board_id": rbBoard, "task_ids": []string{a.ID, b.ID, held.ID, early.ID, foreign.ID, a.ID, "nope"},
		"from_stage": rbRelease, "to_stage": rbDone, "note": "Deploy verified",
	})
	if code != http.StatusOK {
		t.Fatalf("POST = %d", code)
	}
	exp := []string{a.ID, b.ID}
	slices.Sort(exp)
	if got := movedIDs(resp); !slices.Equal(got, exp) {
		t.Errorf("moved %v, want %v", got, exp)
	}
	if len(resp.Gated) != 1 || resp.Gated[0].ID != held.ID || resp.Gated[0].Approvals[0] != "Deploy to production" {
		t.Errorf("gated = %+v, want the task the deploy approval holds", resp.Gated)
	}
	reasons := skipReasons(resp)
	want := map[string]string{early.ID: moveSkipWrongStage, foreign.ID: moveSkipNotFound, "nope": moveSkipNotFound}
	for id, reason := range want {
		if reasons[id] != reason {
			t.Errorf("skip %s = %q, want %q", id, reasons[id], reason)
		}
	}
	if e.stage(t, a.ID) != rbDone || e.stage(t, held.ID) != rbRelease || e.stage(t, early.ID) != rbReview {
		t.Error("stages after the batch are wrong")
	}
	entered, err := e.srv.taskStore.ListEvents(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range entered {
		if ev.Kind == task.EventStageEntered && strings.Contains(string(ev.Payload), `"via":"release_batch"`) &&
			strings.Contains(string(ev.Payload), `"note":"Deploy verified"`) {
			found = true
		}
	}
	if !found {
		t.Error("no stage_entered event with via release_batch and the note")
	}
	if n := (mergeEnv{srv: e.srv}).notes(t, a.ID, "api.release_batch.comments.moved"); n != 1 {
		t.Errorf("batch comments on a = %d, want 1", n)
	}
}

func TestBatchMove_RefusesWhatItMustNot(t *testing.T) {
	e := newBatchEnv(t)
	backlog := e.task(t, "backlog", rbBacklog)
	release := e.task(t, "release", rbRelease)
	many := make([]string, batchMoveMax+1)
	for i := range many {
		many[i] = release.ID + strings.Repeat("x", i)
	}
	for name, body := range map[string]map[string]any{
		"backward":         {"board_id": rbBoard, "task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": rbReview, "note": "n"},
		"same stage":       {"board_id": rbBoard, "task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": rbRelease, "note": "n"},
		"unknown stage":    {"board_id": rbBoard, "task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": "shipped", "note": "n"},
		"no note":          {"board_id": rbBoard, "task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": rbDone, "note": "  "},
		"no tasks":         {"board_id": rbBoard, "task_ids": []string{}, "from_stage": rbRelease, "to_stage": rbDone, "note": "n"},
		"over the cap":     {"board_id": rbBoard, "task_ids": many, "from_stage": rbRelease, "to_stage": rbDone, "note": "n"},
		"no board":         {"task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": rbDone, "note": "n"},
		"bad sha":          {"board_id": rbBoard, "task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": rbDone, "note": "n", "deployed_sha": "main"},
		"included, no sha": {"board_id": rbBoard, "task_ids": []string{release.ID}, "from_stage": rbRelease, "to_stage": rbDone, "note": "n", "require_included": true},
	} {
		if code, _ := e.move(t, body); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if code, _ := e.move(t, map[string]any{"board_id": "elsewhere", "task_ids": []string{release.ID},
		"from_stage": rbRelease, "to_stage": rbDone, "note": "n"}); code != http.StatusNotFound {
		t.Errorf("unknown board = %d, want 404", code)
	}

	// Backlog to Release jumps Review, whose Merge approval would never open: refused per task.
	code, resp := e.move(t, map[string]any{"board_id": rbBoard, "task_ids": []string{backlog.ID},
		"from_stage": rbBacklog, "to_stage": rbRelease, "note": "n"})
	if code != http.StatusOK || skipReasons(resp)[backlog.ID] != moveSkipJumpsApprovals || resp.Skipped[0].Detail != "Merge" {
		t.Fatalf("jump over review = %d %+v, want skipped jumps_approvals", code, resp)
	}
	if e.stage(t, backlog.ID) != rbBacklog {
		t.Error("the task jumped the approval")
	}
}

// grantRun records the octopus-api grant a run launched with and mints its key.
func (e batchEnv) grantRun(t *testing.T, taskID string, stage types.Stage, grant string) string {
	t.Helper()
	if e.srv.mcp.registry == nil {
		reg, err := mcp.NewRegistry(e.srv.taskStore.DB())
		if err != nil {
			t.Fatal(err)
		}
		e.srv.SetMCPRegistry(reg)
	}
	if err := e.srv.mcp.registry.RecordRunGrant(t.Context(), taskID, rbAgent, string(stage), mcp.RunGrant{Tools: grant}); err != nil {
		t.Fatal(err)
	}
	raw, _, err := e.srv.authStore.MintRunKey(t.Context(), auth.RunScope{TaskID: taskID, Agent: rbAgent, Stage: string(stage)}, e.ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBatchMove_RunKeyNeedsTheNamedToolAndItsBoard(t *testing.T) {
	e := newBatchEnv(t)
	ctx := t.Context()
	releaseTask := e.task(t, "Deploy acme/shop@9f2c1e7", rbRelease)
	waiting := e.task(t, "merged work", rbRelease)
	body := map[string]any{"board_id": rbBoard, "task_ids": []string{waiting.ID, releaseTask.ID},
		"from_stage": rbRelease, "to_stage": rbDone, "note": "Deploy verified"}

	for grant, wantCode := range map[string]string{
		"octopus-api@read":                   "api.scope.errors.run_tool_not_named",
		"octopus-api@write":                  "api.scope.errors.run_tool_not_named",
		"octopus-api@read+stage_tasks_list":  "api.scope.errors.run_tool_not_named",
		"thulu-github@read+tasks_move_batch": "api.scope.errors.run_tool_not_named",
	} {
		key := e.grantRun(t, releaseTask.ID, rbRelease, grant)
		w := keyDo(t, e.h, http.MethodPost, "/tasks/batch-move", key, body)
		if w.Code != http.StatusForbidden || errCode(t, w) != wantCode {
			t.Errorf("grant %q = %d %s, want 403 %s", grant, w.Code, w.Body, wantCode)
		}
	}

	key := e.grantRun(t, releaseTask.ID, rbRelease, "octopus-api@read+stage_tasks_list+tasks_move_batch")
	if err := e.srv.stageAdmin.CreateBoard(ctx, stagepkg.BoardInfo{ID: "rb_other", Label: "Other", WorkspaceID: e.ws},
		[]types.StageInfo{{ID: "rbo_todo", Label: "Todo", Color: "#5d6477", Initial: true}, {ID: "rbo_done", Label: "Done", Color: "#3aa667", Terminal: true}}); err != nil {
		t.Fatal(err)
	}
	other := &task.Task{Title: "other board", WorkspaceID: e.ws, BoardID: "rb_other", Stage: "rbo_todo"}
	if err := e.srv.taskStore.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherKey := e.grantRun(t, other.ID, "rbo_todo", "octopus-api@read+tasks_move_batch")
	if w := keyDo(t, e.h, http.MethodPost, "/tasks/batch-move", otherKey, body); w.Code != http.StatusForbidden ||
		errCode(t, w) != "api.scope.errors.run_own_board_only" {
		t.Errorf("a run on another board = %d %s, want 403 run_own_board_only", w.Code, w.Body)
	}

	w := keyDo(t, e.h, http.MethodPost, "/tasks/batch-move", key, body)
	if w.Code != http.StatusOK {
		t.Fatalf("named grant = %d %s", w.Code, w.Body)
	}
	resp := decodeBatch(t, w.Body.Bytes())
	if got := movedIDs(resp); !slices.Equal(got, []string{waiting.ID}) {
		t.Errorf("moved %v, want only the waiting task", got)
	}
	if skipReasons(resp)[releaseTask.ID] != moveSkipOwnTask {
		t.Errorf("own release task = %+v, want skipped own_task", resp.Skipped)
	}
}

// With require_included the daemon checks each task's merge commit against the deployed one in the
// project's history: in, not in yet, reverted, never merged, or touching another repository too.
func TestBatchMove_RequireIncluded(t *testing.T) {
	e := newBatchEnv(t)
	projID := createProject(t, e.h, e.token, "shop")
	proj, err := e.srv.projectStore.Get(t.Context(), projID)
	if err != nil {
		t.Fatal(err)
	}
	push := newPusher(t, proj.RepoURL)
	shaA := push.commit("a.txt", "task a")
	shaB := push.commit("b.txt", "task b")
	deployed := push.revert(shaB)
	shaC := push.commit("c.txt", "task c, after the deploy")

	merged := func(title, sha string, projects ...string) *task.Task {
		t.Helper()
		tk := e.task(t, title, rbRelease, append([]string{projID}, projects...)...)
		if sha != "" {
			if _, _, err := e.srv.taskStore.SetMergeSHA(t.Context(), tk.ID, projID, sha); err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	a := merged("a", shaA)
	b := merged("b", shaB)
	c := merged("c", shaC)
	none := merged("never merged", "")
	two := merged("two repos", shaA, "other-project")

	code, resp := e.move(t, map[string]any{
		"board_id": rbBoard, "task_ids": []string{a.ID, b.ID, c.ID, none.ID, two.ID},
		"from_stage": rbRelease, "to_stage": rbDone, "note": "Deployed, health pass",
		"deployed_sha": deployed, "project_id": projID, "require_included": true,
	})
	if code != http.StatusOK {
		t.Fatalf("POST = %d", code)
	}
	if got := movedIDs(resp); !slices.Equal(got, []string{a.ID}) {
		t.Errorf("moved %v, want only a", got)
	}
	reasons := skipReasons(resp)
	for id, want := range map[string]string{
		b.ID: moveSkipReverted, c.ID: moveSkipNotInDeploy, none.ID: moveSkipNoMergeSHA, two.ID: moveSkipOtherProjects,
	} {
		if reasons[id] != want {
			t.Errorf("skip %s = %q, want %q", id, reasons[id], want)
		}
	}
	if n := (mergeEnv{srv: e.srv}).notes(t, a.ID, "api.release_batch.comments.moved_deploy"); n != 1 {
		t.Errorf("deploy comments on a = %d, want 1", n)
	}
}

// stage_tasks_list reads GET /tasks with board, stage and project together.
func TestListTasks_ByBoardStageAndProject(t *testing.T) {
	e := newBatchEnv(t)
	inRelease := e.task(t, "shop in release", rbRelease, "shop")
	e.task(t, "api in release", rbRelease, "api")
	e.task(t, "shop in review", rbReview, "shop")
	w := getJSON(t, e.h, "/tasks?board="+rbBoard+"&stage="+string(rbRelease)+"&project=shop", e.token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", w.Code, w.Body)
	}
	var got []task.Task
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != inRelease.ID {
		t.Fatalf("listed %d tasks, want only %s", len(got), inRelease.ID)
	}
}

// A run's batch starts where merged work waits: earlier columns hold the stages that check the
// work (their agents, their verdicts), and a batch from there would carry tasks past them.
func TestBatchMove_RunStartsAtTheMergeTarget(t *testing.T) {
	e := newBatchEnv(t)
	releaseTask := e.task(t, "Deploy acme/shop@9f2c1e7", rbRelease)
	early := e.task(t, "still in backlog", rbBacklog)
	key := e.grantRun(t, releaseTask.ID, rbRelease, "octopus-api@read+tasks_move_batch")

	w := keyDo(t, e.h, http.MethodPost, "/tasks/batch-move", key, map[string]any{"board_id": rbBoard,
		"task_ids": []string{early.ID}, "from_stage": rbBacklog, "to_stage": rbReview, "note": "skip ahead"})
	if w.Code != http.StatusForbidden || errCode(t, w) != "api.release_batch.errors.run_before_merge_target" {
		t.Fatalf("run batch from backlog = %d %s, want 403 run_before_merge_target", w.Code, w.Body)
	}
	if got := e.stage(t, early.ID); got != rbBacklog {
		t.Errorf("the backlog task moved to %s", got)
	}
	// A person keeps the whole board.
	if code, resp := e.move(t, map[string]any{"board_id": rbBoard, "task_ids": []string{early.ID},
		"from_stage": rbBacklog, "to_stage": rbReview, "note": "by hand"}); code != http.StatusOK || len(resp.Moved) != 1 {
		t.Errorf("person batch from backlog = %d %+v, want one moved", code, resp)
	}

	// A board without a merge_target gives a run nowhere to start.
	if err := e.srv.stageAdmin.CreateBoard(t.Context(), stagepkg.BoardInfo{ID: "rb_plain", Label: "Plain", WorkspaceID: e.ws},
		[]types.StageInfo{{ID: "rbp_todo", Label: "Todo", Color: "#5d6477", Initial: true},
			{ID: "rbp_doing", Label: "Doing", Color: "#f2994a"},
			{ID: "rbp_done", Label: "Done", Color: "#3aa667", Terminal: true}}); err != nil {
		t.Fatal(err)
	}
	plain := &task.Task{Title: "plain run", WorkspaceID: e.ws, BoardID: "rb_plain", Stage: "rbp_doing"}
	if err := e.srv.taskStore.Create(t.Context(), plain); err != nil {
		t.Fatal(err)
	}
	other := &task.Task{Title: "plain work", WorkspaceID: e.ws, BoardID: "rb_plain", Stage: "rbp_doing"}
	if err := e.srv.taskStore.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	plainKey := e.grantRun(t, plain.ID, "rbp_doing", "octopus-api@read+tasks_move_batch")
	w = keyDo(t, e.h, http.MethodPost, "/tasks/batch-move", plainKey, map[string]any{"board_id": "rb_plain",
		"task_ids": []string{other.ID}, "from_stage": "rbp_doing", "to_stage": "rbp_done", "note": "close"})
	if w.Code != http.StatusForbidden || errCode(t, w) != "api.release_batch.errors.run_before_merge_target" {
		t.Errorf("run batch on a board without merge_target = %d %s, want 403", w.Code, w.Body)
	}
}
