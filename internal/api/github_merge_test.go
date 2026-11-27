package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/workshop"
)

// The merge flow board: backlog → work → review (opens the PR, needs a sign-off) → release
// (merge_target) → done.
const (
	mtBoard   = "mt"
	mtBacklog = types.Stage("mt_backlog")
	mtWork    = types.Stage("mt_work")
	mtReview  = types.Stage("mt_review")
	mtRelease = types.Stage("mt_release")
	mtDone    = types.Stage("mt_done")
	mergeWS   = "ws-a"
)

type mergeEnv struct {
	srv *Server
	h   http.Handler
}

func newMergeEnv(t *testing.T) mergeEnv {
	t.Helper()
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	ctx := t.Context()
	err := srv.stageAdmin.CreateBoard(ctx, stagepkg.BoardInfo{ID: mtBoard, Label: "Merge flow", WorkspaceID: mergeWS}, []types.StageInfo{
		{ID: mtBacklog, Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: mtWork, Label: "Work", Color: "#f2994a"},
		{ID: mtReview, Label: "Review", Color: "#bb86fc", OpenPR: true,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "signoff", Label: "Sign-off"}}},
		{ID: mtRelease, Label: "Release", Color: "#6fcf97", MergeTarget: true},
		{ID: mtDone, Label: "Done", Color: "#3aa667", Terminal: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []githubapp.Installation{
		{InstallationID: 100, WorkspaceID: mergeWS, AccountLogin: "acme"},
		{InstallationID: 200, WorkspaceID: "ws-b", AccountLogin: "other"},
	} {
		if err := srv.gh.store.UpsertInstallation(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}
	return mergeEnv{srv: srv, h: h}
}

func (e mergeEnv) project(t *testing.T, name string) *project.Project {
	t.Helper()
	p, err := e.srv.projectStore.Create(t.Context(), name, testgit.BareRepo(t), project.CreateOpts{WorkspaceID: mergeWS})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// task creates a task on board in stage, with one PR per project (prs maps project id to URL).
func (e mergeEnv) task(t *testing.T, board string, stage types.Stage, prs map[string]string) *task.Task {
	t.Helper()
	ctx := t.Context()
	var ids []string
	for pid := range prs {
		ids = append(ids, pid)
	}
	tk := &task.Task{Title: "merge me", WorkspaceID: mergeWS, BoardID: board, Stage: stage, ProjectIDs: ids}
	if err := e.srv.taskStore.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	branch := "b"
	if _, err := e.srv.taskStore.Update(ctx, tk.ID, task.Patch{PRURLs: prs, BranchName: &branch}); err != nil {
		t.Fatal(err)
	}
	return e.get(t, tk.ID)
}

func (e mergeEnv) get(t *testing.T, id string) *task.Task {
	t.Helper()
	got, err := e.srv.taskStore.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mergedPayload(installation int64, url, sha, base string) map[string]any {
	return map[string]any{
		"action":       "closed",
		"installation": map[string]any{"id": installation},
		"repository":   map[string]any{"full_name": "acme/app"},
		"sender":       map[string]any{"login": "ana"},
		"pull_request": map[string]any{
			"number": 1, "html_url": url, "merged": sha != "", "merge_commit_sha": sha,
			"base": map[string]any{"ref": base}, "merged_by": map[string]any{"login": "ana"},
		},
	}
}

func (e mergeEnv) deliver(t *testing.T, payload map[string]any) {
	t.Helper()
	if code := deliverWebhook(t, e.h, "pull_request", payload); code != http.StatusNoContent {
		t.Fatalf("delivery = %d, want 204", code)
	}
}

func (e mergeEnv) notes(t *testing.T, taskID, key string) int {
	t.Helper()
	cs, err := e.srv.taskStore.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range cs {
		if cs[i].Key == key {
			n++
		}
	}
	return n
}

func (e mergeEnv) events(t *testing.T, taskID string, kind task.EventKind) []task.WorkflowEvent {
	t.Helper()
	all, err := e.srv.taskStore.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []task.WorkflowEvent
	for i := range all {
		if all[i].Kind == kind {
			out = append(out, all[i])
		}
	}
	return out
}

func sha40(c byte) string { return strings.Repeat(string(c), 40) }

func TestPRMerge_MovesToMergeTarget(t *testing.T) {
	e := newMergeEnv(t)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/1"
	tk := e.task(t, mtBoard, mtReview, map[string]string{p.ID: url})
	info, _ := e.srv.stagesFor(tk).Get(mtReview)
	if _, err := e.srv.taskStore.EnsureStageApprovals(t.Context(), tk, info); err != nil {
		t.Fatal(err)
	}

	merged := mergedPayload(100, url, sha40('a'), p.DefaultBranch)
	merged["pull_request"].(map[string]any)["head"] = map[string]any{"ref": project.TaskBranchName(tk.ID)}
	e.deliver(t, merged)
	got := e.get(t, tk.ID)
	if got.Stage != mtRelease || got.MergeSHAs[p.ID] != sha40('a') {
		t.Fatalf("after the merge: stage %s merge_shas %v, want %s and the sha", got.Stage, got.MergeSHAs, mtRelease)
	}
	entered := e.events(t, tk.ID, task.EventStageEntered)
	if len(entered) != 1 || entered[0].Actor != "github:ana" || !strings.Contains(string(entered[0].Payload), `"via":"merge"`) {
		t.Errorf("stage_entered = %+v, want one by github:ana via merge", entered)
	}
	if n := len(e.events(t, tk.ID, task.EventBranchMerged)); n != 1 {
		t.Errorf("branch_merged events = %d, want 1", n)
	}
	aps, err := e.srv.taskStore.ListApprovals(t.Context(), tk.ID)
	if err != nil || len(aps) != 1 || aps[0].Status != task.ApprovalWaived || aps[0].DecidedBy != "github:ana" ||
		!strings.Contains(aps[0].DecidedNote, "ana") {
		t.Errorf("approvals = %+v, %v; want the sign-off waived by the merger", aps, err)
	}

	// GitHub redelivers, and the reconciler may report the same merge: nothing repeats.
	e.deliver(t, mergedPayload(100, url, sha40('a'), p.DefaultBranch))
	if n := e.notes(t, tk.ID, "api.merges.comments.moved"); n != 1 {
		t.Errorf("moved comments = %d, want 1", n)
	}
	if n := len(e.events(t, tk.ID, task.EventStageEntered)); n != 1 {
		t.Errorf("stage_entered after a redelivery = %d, want 1", n)
	}
}

// installThulu writes the thulu flow into ws as POST /workshop/apply would: agents and skills, then
// the board.
func (e mergeEnv) installThulu(t *testing.T, ws string) {
	t.Helper()
	ctx := t.Context()
	agents, err := agentstore.New(e.srv.stageAdmin.DB())
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetAgentStore(agents)
	cat, err := workshop.LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatal(err)
	}
	set, err := cat.Select([]workshop.Ref{{Kind: workshop.KindFlow, Name: "thulu"}})
	if err != nil {
		t.Fatal(err)
	}
	st := workshop.StoreState{Store: agents, Tenant: ws}
	plan, err := workshop.MakePlan(ctx, st, set, workshop.SourceWorkshop, workshop.Choices{},
		workshop.Env{Locale: "en", Boards: e.srv.flowBoards(ws)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := workshop.Apply(ctx, agents, ws, set, plan, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := workshop.ApplyFlows(ctx, e.srv.stageAdmin, ws, set, plan, &res); err != nil {
		t.Fatal(err)
	}
}

// On the thulu board a merge in human review lands in Release with the merge SHA, waives the Merge
// and UX review approvals in the merger's name and frees the task's worktrees.
func TestPRMerge_ThuluFlowReviewToRelease(t *testing.T) {
	e := newMergeEnv(t)
	e.installThulu(t, mergeWS)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/11"
	tk := &task.Task{Title: "cart total", WorkspaceID: mergeWS, BoardID: "thulu", TypeID: "feature",
		Stage: "review", ProjectIDs: []string{p.ID}}
	if err := e.srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{PRURLs: map[string]string{p.ID: url}}); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.taskStore.AddApproval(t.Context(), &task.Approval{TaskID: tk.ID, Key: "ux_review", Label: "UX review",
		Stage: "review", Source: task.ApprovalFromAgent, Status: task.ApprovalPending, CreatedBy: "agent:thulu-agentic-tester"}); err != nil {
		t.Fatal(err)
	}

	e.deliver(t, mergedPayload(100, url, sha40('f'), p.DefaultBranch))
	got := e.get(t, tk.ID)
	if got.Stage != "release" || got.MergeSHAs[p.ID] != sha40('f') {
		t.Fatalf("after the merge: stage %s merge_shas %v, want release and the sha", got.Stage, got.MergeSHAs)
	}
	aps, err := e.srv.taskStore.ListApprovals(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"merge", "ux_review"} {
		var found bool
		for i := range aps {
			if aps[i].Key == key {
				found = true
				if aps[i].Status != task.ApprovalWaived || aps[i].DecidedBy != "github:ana" {
					t.Errorf("%s after the merge = %s by %q, want waived by github:ana", key, aps[i].Status, aps[i].DecidedBy)
				}
			}
		}
		if !found {
			t.Errorf("no %s approval on the task (%+v)", key, aps)
		}
	}
	if n := len(e.events(t, tk.ID, task.EventResourcesReleased)); n != 1 {
		t.Errorf("resources_released events = %d, want 1", n)
	}
	if n := e.notes(t, tk.ID, "api.merges.comments.moved"); n != 1 {
		t.Errorf("moved comments = %d, want 1", n)
	}
}

func TestPRMerge_BoardWithoutMergeTarget(t *testing.T) {
	e := newMergeEnv(t)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/2"
	tk := e.task(t, e.srv.boards.DefaultBoardID(), types.StageHandoff, map[string]string{p.ID: url})
	e.deliver(t, mergedPayload(100, url, sha40('b'), p.DefaultBranch))
	got := e.get(t, tk.ID)
	if got.Stage != types.StageHandoff {
		t.Errorf("stage = %s, want it unchanged on a board without merge_target", got.Stage)
	}
	if got.MergeSHAs[p.ID] != sha40('b') {
		t.Errorf("merge_shas = %v, want the sha recorded anyway", got.MergeSHAs)
	}
	if n := len(e.events(t, tk.ID, task.EventStageEntered)); n != 0 {
		t.Errorf("stage_entered = %d, want 0", n)
	}
}

func TestPRMerge_MultiRepoWaitsForEvery(t *testing.T) {
	e := newMergeEnv(t)
	api, web := e.project(t, "api"), e.project(t, "web")
	const urlAPI, urlWeb = "https://github.com/acme/api/pull/3", "https://github.com/acme/web/pull/4"
	tk := e.task(t, mtBoard, mtReview, map[string]string{api.ID: urlAPI, web.ID: urlWeb})

	e.deliver(t, mergedPayload(100, urlAPI, sha40('c'), api.DefaultBranch))
	got := e.get(t, tk.ID)
	if got.Stage != mtReview || got.MergeSHAs[api.ID] != sha40('c') {
		t.Fatalf("after one of two merges: stage %s shas %v", got.Stage, got.MergeSHAs)
	}
	if n := e.notes(t, tk.ID, "api.merges.comments.waiting"); n != 1 {
		t.Errorf("waiting comments = %d, want 1", n)
	}

	e.deliver(t, mergedPayload(100, urlWeb, sha40('d'), web.DefaultBranch))
	got = e.get(t, tk.ID)
	if got.Stage != mtRelease || len(got.MergeSHAs) != 2 {
		t.Fatalf("after both merges: stage %s shas %v, want %s and two shas", got.Stage, got.MergeSHAs, mtRelease)
	}
}

func TestPRMerge_StaysPut(t *testing.T) {
	cases := []struct {
		name    string
		stage   types.Stage
		install int64
		sha     string
		base    string
		head    string
		running bool
		wantSHA bool
		note    string
	}{
		{name: "out of order", stage: mtWork, install: 100, sha: sha40('e'), wantSHA: true, note: "api.merges.comments.out_of_order"},
		{name: "already past", stage: mtDone, install: 100, sha: sha40('e'), wantSHA: true},
		{name: "closed unmerged", stage: mtReview, install: 100, note: "api.merges.comments.closed_unmerged"},
		{name: "other branch", stage: mtReview, install: 100, sha: sha40('e'), base: "develop", note: "api.merges.comments.other_branch"},
		// A run recorded someone else's merged PR on its task (RecordAgentPR takes any PR of the repo).
		{name: "other head", stage: mtReview, install: 100, sha: sha40('e'), head: "feature-x", note: "api.merges.comments.other_head"},
		{name: "other workspace's installation", stage: mtReview, install: 200, sha: sha40('e')},
		{name: "running", stage: mtReview, install: 100, sha: sha40('e'), running: true, wantSHA: true, note: "api.merges.comments.deferred"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newMergeEnv(t)
			p := e.project(t, "app")
			url := "https://github.com/acme/app/pull/9"
			tk := e.task(t, mtBoard, c.stage, map[string]string{p.ID: url})
			if c.running {
				running := task.StatusRunning
				if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{Status: &running}); err != nil {
					t.Fatal(err)
				}
			}
			base := c.base
			if base == "" {
				base = p.DefaultBranch
			}
			payload := mergedPayload(c.install, url, c.sha, base)
			if c.head != "" {
				payload["pull_request"].(map[string]any)["head"] = map[string]any{"ref": c.head}
			}
			e.deliver(t, payload)
			got := e.get(t, tk.ID)
			if got.Stage != c.stage {
				t.Errorf("stage = %s, want %s", got.Stage, c.stage)
			}
			if (got.MergeSHAs[p.ID] != "") != c.wantSHA {
				t.Errorf("merge_shas = %v, want recorded=%v", got.MergeSHAs, c.wantSHA)
			}
			if c.note != "" && e.notes(t, tk.ID, c.note) != 1 {
				t.Errorf("no %s comment", c.note)
			}
		})
	}
}

// A merge seen while an agent worked on the task moves it once the run is over, on the
// reconciler's next round, without asking GitHub again.
func TestPRReconcile_MovesDeferredMerge(t *testing.T) {
	e := newMergeEnv(t)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/5"
	tk := e.task(t, mtBoard, mtReview, map[string]string{p.ID: url})
	running, idle := task.StatusRunning, task.StatusIdle
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{Status: &running}); err != nil {
		t.Fatal(err)
	}
	e.deliver(t, mergedPayload(100, url, sha40('f'), p.DefaultBranch))
	if st := e.srv.reconcileMergedPRs(t.Context(), time.Now()); st.Moved != 0 {
		t.Fatalf("moved %d while the run went on", st.Moved)
	}
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{Status: &idle}); err != nil {
		t.Fatal(err)
	}
	st := e.srv.reconcileMergedPRs(t.Context(), time.Now())
	if st.Moved != 1 || st.Read != 0 {
		t.Fatalf("reconcile = %+v, want 1 moved and no GitHub read", st)
	}
	got := e.get(t, tk.ID)
	if got.Stage != mtRelease {
		t.Fatalf("stage = %s, want %s", got.Stage, mtRelease)
	}
	if entered := e.events(t, tk.ID, task.EventStageEntered); len(entered) != 1 || entered[0].Actor != "github:ana" {
		t.Errorf("stage_entered = %+v, want one credited to github:ana", entered)
	}

	// A person moves it back on purpose: the reconciler leaves it there.
	back := mtReview
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{Stage: &back}); err != nil {
		t.Fatal(err)
	}
	e.srv.recordEvent(t.Context(), tk.ID, task.EventStageEntered, "", "user", "back", map[string]any{"via": task.ViaManual})
	if st := e.srv.reconcileMergedPRs(t.Context(), time.Now()); st.Moved != 0 {
		t.Errorf("moved a task a person sent back after the merge")
	}
}

// The merge flow board with auto-advancing stages: work → verify (opens the PR) → check → ship
// (merge_target).
const (
	mtaBoard  = "mt_auto"
	mtaWork   = types.Stage("mta_work")
	mtaVerify = types.Stage("mta_verify")
	mtaCheck  = types.Stage("mta_check")
	mtaShip   = types.Stage("mta_ship")
)

func (e mergeEnv) autoBoard(t *testing.T) {
	t.Helper()
	err := e.srv.stageAdmin.CreateBoard(t.Context(), stagepkg.BoardInfo{ID: mtaBoard, Label: "Auto", WorkspaceID: mergeWS}, []types.StageInfo{
		{ID: "mta_backlog", Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: mtaWork, Label: "Work", Color: "#f2994a", AutoAdvance: true},
		{ID: mtaVerify, Label: "Verify", Color: "#2d9cdb", OpenPR: true, AutoAdvance: true},
		{ID: mtaCheck, Label: "Check", Color: "#f2c94c"},
		{ID: mtaShip, Label: "Ship", Color: "#6fcf97", MergeTarget: true},
		{ID: "mta_done", Label: "Done", Color: "#3aa667", Terminal: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e mergeEnv) setStatus(t *testing.T, id string, st task.RunStatus) {
	t.Helper()
	if _, err := e.srv.taskStore.Update(t.Context(), id, task.Patch{Status: &st}); err != nil {
		t.Fatal(err)
	}
}

// The run in a stage ends and the board moves the task on by itself, through the dispatcher's
// auto-advance.
func (e mergeEnv) autoAdvance(t *testing.T, id string, want types.Stage) {
	t.Helper()
	e.setStatus(t, id, task.StatusAwaitingApproval)
	e.srv.taskDispatcher.ApprovalsSettled(t.Context(), id)
	if got := e.get(t, id); got.Stage != want {
		t.Fatalf("auto-advance left the task in %s, want %s", got.Stage, want)
	}
}

// A merge seen while the run of an auto-advancing stage worked on the task: the run ends by moving
// the task to the next stage, and that move must not swallow the merge. Once the task rests, the
// reconciler takes it to the merge_target stage.
func TestPRReconcile_DeferredMergeSurvivesAutoAdvance(t *testing.T) {
	e := newMergeEnv(t)
	e.autoBoard(t)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/11"
	tk := e.task(t, mtaBoard, mtaVerify, map[string]string{p.ID: url})
	e.setStatus(t, tk.ID, task.StatusRunning)
	e.deliver(t, mergedPayload(100, url, sha40('a'), p.DefaultBranch))
	if got := e.get(t, tk.ID); got.Stage != mtaVerify {
		t.Fatalf("stage = %s while the run went on, want %s", got.Stage, mtaVerify)
	}
	e.autoAdvance(t, tk.ID, mtaCheck)

	st := e.srv.reconcileMergedPRs(t.Context(), time.Now())
	if st.Moved != 1 {
		t.Fatalf("reconcile = %+v, want the merged task moved", st)
	}
	if got := e.get(t, tk.ID); got.Stage != mtaShip {
		t.Fatalf("stage = %s, want %s", got.Stage, mtaShip)
	}
}

// A merge ahead of the flow (before the stage that opens the PR) stays recorded only, also once the
// board has moved the task on into the stages after the PR.
func TestPRReconcile_OutOfOrderMergeStaysAfterAutoAdvance(t *testing.T) {
	e := newMergeEnv(t)
	e.autoBoard(t)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/12"
	tk := e.task(t, mtaBoard, mtaWork, map[string]string{p.ID: url})
	e.deliver(t, mergedPayload(100, url, sha40('b'), p.DefaultBranch))
	if e.notes(t, tk.ID, "api.merges.comments.out_of_order") != 1 {
		t.Fatal("no out_of_order comment")
	}
	e.autoAdvance(t, tk.ID, mtaVerify)
	e.setStatus(t, tk.ID, task.StatusIdle)
	if st := e.srv.reconcileMergedPRs(t.Context(), time.Now()); st.Moved != 0 {
		t.Fatalf("reconcile = %+v, moved a task merged ahead of the flow", st)
	}
	if got := e.get(t, tk.ID); got.Stage != mtaVerify {
		t.Fatalf("stage = %s, want %s", got.Stage, mtaVerify)
	}
}

type reconcileGitHub struct {
	reads atomic.Int32
	state string
	// The PR's head branch; "b" (the branch mergeEnv's tasks carry) when empty.
	head string
}

func (g *reconcileGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.reads.Add(1)
	var n int
	if _, err := fmt.Sscanf(r.URL.Path, "/repos/acme/app/pulls/%d", &n); err != nil {
		http.NotFound(w, r)
		return
	}
	merged := g.state == "merged"
	state := "open"
	if g.state != "open" {
		state = "closed"
	}
	pr := map[string]any{
		"number": n, "html_url": fmt.Sprintf("https://github.com/acme/app/pull/%d", n), "state": state,
		"merged": merged, "base": map[string]any{"ref": "main"}, "head": map[string]any{"ref": cmp.Or(g.head, "b"), "sha": sha40('9')},
		"merged_by": map[string]any{"login": "bea"},
	}
	if merged {
		pr["merge_commit_sha"] = sha40('7')
	}
	_ = json.NewEncoder(w).Encode(pr)
}

func TestPRReconcile_ReadsSilentRepos(t *testing.T) {
	gh := &reconcileGitHub{state: "open"}
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	e := newMergeEnv(t)
	e.srv.gh.tokens = &sourcedTokens{source: githubapp.SourceStored}
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/6"
	tk := e.task(t, mtBoard, mtReview, map[string]string{p.ID: url})
	other := e.task(t, mtBoard, mtWork, map[string]string{p.ID: "https://github.com/acme/app/pull/60"})
	now := time.Now()

	if st := e.srv.reconcileMergedPRs(t.Context(), now); st.Read != 1 || st.Merged != 0 {
		t.Fatalf("open PR round = %+v, want one read (the task before the PR stage is skipped)", st)
	}

	// The repository's webhooks arrive: the reconciler leaves it to them.
	e.srv.prMerges.sawHook("acme/app", now)
	gh.state = "merged"
	if st := e.srv.reconcileMergedPRs(t.Context(), now); st.Read != 0 {
		t.Fatalf("hooked repo read %d times", st.Read)
	}

	later := now.Add(prReconcileHookQuiet + time.Minute)
	st := e.srv.reconcileMergedPRs(t.Context(), later)
	if st.Read != 1 || st.Merged != 1 {
		t.Fatalf("silent repo round = %+v, want the merge found", st)
	}
	got := e.get(t, tk.ID)
	if got.Stage != mtRelease || got.MergeSHAs[p.ID] != sha40('7') {
		t.Fatalf("after reconcile: stage %s shas %v", got.Stage, got.MergeSHAs)
	}
	if e.get(t, other.ID).Stage != mtWork {
		t.Error("the task before the PR stage moved")
	}

	before := gh.reads.Load()
	if st := e.srv.reconcileMergedPRs(t.Context(), later); st.Read != 0 || st.Moved != 0 {
		t.Errorf("second round = %+v, want nothing to do", st)
	}
	if gh.reads.Load() != before {
		t.Error("the second round asked GitHub again")
	}
}

func TestPRReconcile_ClosedPRGoesQuiet(t *testing.T) {
	gh := &reconcileGitHub{state: "closed"}
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	e := newMergeEnv(t)
	e.srv.gh.tokens = &sourcedTokens{source: githubapp.SourceStored}
	p := e.project(t, "app")
	e.task(t, mtBoard, mtReview, map[string]string{p.ID: "https://github.com/acme/app/pull/8"})
	now := time.Now()
	e.srv.reconcileMergedPRs(t.Context(), now)
	if st := e.srv.reconcileMergedPRs(t.Context(), now.Add(time.Hour)); st.Read != 0 {
		t.Errorf("a closed PR was read again an hour later")
	}
	if st := e.srv.reconcileMergedPRs(t.Context(), now.Add(prReconcileSilence+time.Hour)); st.Read != 1 {
		t.Errorf("a closed PR was never read again")
	}
}

// A merged PR whose head is another branch is not the task's merge: the reconciler records nothing,
// leaves the task where it is and stops reading the PR.
func TestPRReconcile_OtherHeadGoesQuiet(t *testing.T) {
	gh := &reconcileGitHub{state: "merged", head: "someone-elses-branch"}
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	e := newMergeEnv(t)
	e.srv.gh.tokens = &sourcedTokens{source: githubapp.SourceStored}
	p := e.project(t, "app")
	tk := e.task(t, mtBoard, mtReview, map[string]string{p.ID: "https://github.com/acme/app/pull/11"})
	now := time.Now()
	if st := e.srv.reconcileMergedPRs(t.Context(), now); st.Read != 1 || st.Moved != 0 {
		t.Fatalf("round = %+v, want one read and nothing moved", st)
	}
	got := e.get(t, tk.ID)
	if got.Stage != mtReview || got.MergeSHAs[p.ID] != "" {
		t.Fatalf("stage %s merge_shas %v, want %s and nothing recorded", got.Stage, got.MergeSHAs, mtReview)
	}
	if st := e.srv.reconcileMergedPRs(t.Context(), now.Add(time.Hour)); st.Read != 0 {
		t.Errorf("the PR was read again an hour later")
	}
}

// A merge a person confirms from a chat records the commit on the task that owns the PR.
func TestHeldMerge_RecordsMergeSHA(t *testing.T) {
	merge := sha40('8')
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/widgets/pulls/7/merge":
			fmt.Fprintf(w, `{"merged":true,"sha":%q}`, merge)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/7":
			fmt.Fprintf(w, `{"number":7,"html_url":"https://github.com/acme/widgets/pull/7","state":"closed","merged":true,`+
				`"merge_commit_sha":%q,"base":{"ref":"main"},"merged_by":{"login":"ana"}}`, merge)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	rig := newDelegatedGithubRig(t, "thulu-github@read+pr_merge")
	tk := &task.Task{Title: "held", WorkspaceID: rig.ws, Stage: types.StageHandoff, ProjectIDs: []string{rig.own}}
	if err := rig.srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{PRURLs: map[string]string{rig.own: "https://github.com/acme/widgets/pull/7"}}); err != nil {
		t.Fatal(err)
	}
	id := rig.held(t, "/internal/github/acme/widgets/pulls/7/merge", map[string]string{"merge_method": "squash"})
	rig.confirm(t, id)
	got, err := rig.srv.taskStore.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeSHAs[rig.own] != merge {
		t.Errorf("merge_shas = %v, want %s for the project", got.MergeSHAs, merge)
	}
}

// The gate stays closed for a person's move and opens for a merge, which waives what it skips.
func TestMoveTaskStage_Gates(t *testing.T) {
	e := newMergeEnv(t)
	tk := e.task(t, mtBoard, mtReview, nil)
	info, _ := e.srv.stagesFor(tk).Get(mtReview)
	if _, err := e.srv.taskStore.EnsureStageApprovals(t.Context(), tk, info); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	got, v, err := e.srv.moveTaskStage(ctx, tk, stageMove{To: mtRelease, Cause: task.GateRelease, Via: task.ViaReleaseBatch, Actor: "release"})
	if err != nil || v.Passed || got.Stage != mtReview {
		t.Fatalf("release past a pending sign-off: passed=%v stage=%s err=%v, want held", v.Passed, got.Stage, err)
	}
	if _, _, err := e.srv.moveTaskStage(ctx, tk, stageMove{To: "nope", Cause: task.GateMerge}); err == nil {
		t.Error("a stage not on the board went through")
	}
	got, v, err = e.srv.moveTaskStage(ctx, tk, stageMove{To: mtRelease, Cause: task.GateMerge, Via: task.ViaMerge, Actor: "github:ana"})
	if err != nil || !v.Passed || got.Stage != mtRelease {
		t.Fatalf("merge move: passed=%v stage=%s err=%v", v.Passed, got.Stage, err)
	}
	if n := len(e.events(t, tk.ID, task.EventApprovalWaived)); n != 1 {
		t.Errorf("approval_waived events = %d, want 1", n)
	}
	// tk still says mt_review: a move from a stale read must not land.
	if _, _, err := e.srv.moveTaskStage(ctx, tk, stageMove{To: mtDone, Cause: task.GateMerge}); !task.IsStageChanged(err) {
		t.Errorf("move from a stale read: err = %v, want stage changed", err)
	}
}
