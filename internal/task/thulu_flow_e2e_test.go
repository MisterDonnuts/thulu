package task

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/workshop"
)

// The Thulu flow end to end: the Workshop installs the flow the way POST /workshop/apply does, and
// tasks walk the board through the real dispatcher. Agent runs are fake: the test plays each
// container, writes into the task's worktree what the agent would commit and into the run's output
// dir what it would leave (flags.yaml, verdict.md, locked-tests.txt, feedback.md), then fires the
// exit the launcher would report.

const (
	thuluBoardID = "thulu"
	thuluPRURL   = "https://github.com/acme/widgets/pull/7"
)

type e2eRun struct {
	cid   string
	inst  string
	agent string
	stage Stage
	audit bool
	// Comment bodies on the task when the run started: what the agent reads in the discussion.
	discussion []string
}

// Starts nothing: it records each launch and hands its exit to the test.
type e2eLauncher struct {
	mu     sync.Mutex
	store  *Store
	agents *agentstore.Store
	runs   []*e2eRun
	exits  map[string]func(int, string, string)
	alive  map[string]bool
}

func (l *e2eLauncher) LaunchForStage(
	ctx context.Context, t *Task, stage Stage, _ *types.AgentType,
	env map[string]string, _, _ map[string]string, _ bool,
) (string, string, string, error) {
	var discussion []string
	if full, err := l.store.Get(ctx, t.ID); err == nil {
		for _, c := range full.Comments {
			discussion = append(discussion, c.Body)
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	run := &e2eRun{
		cid: fmt.Sprintf("ctr-%d", len(l.runs)+1), inst: env["INSTANCE_ID"], agent: env["AGENT_NAME"],
		stage: stage, audit: env["AUDIT_ONLY"] == "1", discussion: discussion,
	}
	l.runs = append(l.runs, run)
	l.alive[run.cid] = true
	return run.cid, run.inst, "", nil
}

func (l *e2eLauncher) OnExit(_ context.Context, containerID string, cb func(int, string, string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exits[containerID] = cb
}

func (l *e2eLauncher) FindAgentType(workspaceID, name string) *types.AgentType {
	list, err := l.agents.ListAgents(context.Background(), workspaceID)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Name == name && !list[i].Disabled() {
			at := list[i].AgentType
			return &at
		}
	}
	return nil
}

func (l *e2eLauncher) ContainerAlive(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.alive[id]
}

func (l *e2eLauncher) ScanExitedTaskAgents(context.Context) ([]ExitedTaskAgent, error) {
	return nil, nil
}

// The run at index i once its exit is wired, or nil.
func (l *e2eLauncher) ready(i int) (*e2eRun, func(int, string, string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i >= len(l.runs) {
		return nil, nil
	}
	run := l.runs[i]
	return run, l.exits[run.cid]
}

func (l *e2eLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.runs)
}

func (l *e2eLauncher) exited(cid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.alive, cid)
	delete(l.exits, cid)
}

// GitHub as the flow sees it: one repository, acme/widgets, whose PR 7 opens as a draft.
type ghFake struct {
	mu    sync.Mutex
	calls []string
	draft bool
	ready bool
}

func newGHFake(t *testing.T) *ghFake {
	t.Helper()
	gh := &ghFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gh.mu.Lock()
		defer gh.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		gh.calls = append(gh.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/pulls":
			gh.draft = strings.Contains(string(b), `"draft":true`)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":7,"html_url":"` + thuluPRURL + `"}`))
		case r.URL.Path == "/repos/acme/widgets/pulls/7":
			_, _ = fmt.Fprintf(w, `{"number":7,"node_id":"PR_7","state":"open","draft":%v}`, !gh.ready)
		case r.URL.Path == "/graphql":
			gh.ready = true
			_, _ = w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", srv.URL)
	return gh
}

func (g *ghFake) state() (draft, ready bool, calls []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.draft, g.ready, slices.Clone(g.calls)
}

type thuluRig struct {
	d        *Dispatcher
	store    *Store
	boards   *stagepkg.Store
	projects *project.Store
	l        *e2eLauncher
	rel      *countingReleaser
	gh       *ghFake
	pid      string
	// Runs the test already played.
	played int
}

func newThuluRig(t *testing.T) *thuluRig {
	t.Helper()
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	ctx := t.Context()
	gh := newGHFake(t)
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	boards, err := stagepkg.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := agentstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	installThuluFlow(t, agents, boards)
	store.WithBoards(boards)

	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := projects.Create(ctx, "widgets", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	// The clone pushes to its local origin; the PR calls go to GitHub, which needs a github.com remote.
	if _, err := db.ExecContext(ctx, store.rb(`UPDATE projects SET repo_url = ? WHERE id = ?`),
		"https://github.com/acme/widgets", proj.ID); err != nil {
		t.Fatal(err)
	}

	l := &e2eLauncher{store: store, agents: agents, exits: map[string]func(int, string, string){}, alive: map[string]bool{}}
	rel := &countingReleaser{}
	d := NewDispatcher(store, l, projects, fixedToken{}, rel).WithBoards(boards)
	return &thuluRig{d: d, store: store, boards: boards, projects: projects, l: l, rel: rel, gh: gh, pid: proj.ID}
}

// The apply POST /workshop/apply runs for {"items":[{"kind":"flow","name":"thulu"}]}: the plan, then
// the agents and skills, then the board.
func installThuluFlow(t *testing.T, agents *agentstore.Store, boards *stagepkg.Store) {
	t.Helper()
	ctx := t.Context()
	cat, err := workshop.LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatal(err)
	}
	set, err := cat.Select([]workshop.Ref{{Kind: workshop.KindFlow, Name: "thulu"}})
	if err != nil {
		t.Fatal(err)
	}
	st := workshop.StoreState{Store: agents, Tenant: testWS}
	env := workshop.Env{Locale: "en", Boards: workshop.StageBoards{Store: boards, Tenant: testWS}}
	plan, err := workshop.MakePlan(ctx, st, set, workshop.SourceWorkshop, workshop.Choices{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked {
		t.Fatalf("the thulu flow plan is blocked: %+v", plan.Notices)
	}
	res, err := workshop.Apply(ctx, agents, testWS, set, plan, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := workshop.ApplyFlows(ctx, boards, testWS, set, plan, &res); err != nil {
		t.Fatal(err)
	}
	if _, ok := stagepkg.BoardInfoOf(boards, thuluBoardID); !ok {
		t.Fatal("the apply left no thulu board")
	}
}

// Who runs each stage of a new task, as the API layers it: the column's default agent, then the
// task type's own agents on the board (an empty name clears the column).
func thuluAssignments(boards stagepkg.Boards, typeID string) map[Stage]string {
	columns, byType := stagepkg.BoardAgents(boards, thuluBoardID, typeID)
	out := maps.Clone(columns)
	for st, agent := range byType {
		if agent == "" {
			delete(out, st)
		} else {
			out[st] = agent
		}
	}
	return out
}

func (r *thuluRig) newTask(t *testing.T, typeID string) *Task {
	t.Helper()
	tk := &Task{
		WorkspaceID: testWS, Title: typeID + " for the cart", BoardID: thuluBoardID, TypeID: typeID,
		ProjectIDs: []string{r.pid}, StageAssignments: thuluAssignments(r.boards, typeID),
	}
	if err := r.store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create %s: %v", typeID, err)
	}
	if tk.Stage != "backlog" {
		t.Fatalf("a new %s starts in %q, want backlog", typeID, tk.Stage)
	}
	return r.get(t, tk.ID)
}

func (r *thuluRig) get(t *testing.T, id string) *Task {
	t.Helper()
	got, err := r.store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A person drags the card, as moveTaskStage does for PATCH /tasks/{id} with a stage: the exit gate,
// the write, the timeline, the next stage's launch.
func (r *thuluRig) personMove(t *testing.T, tk *Task, to Stage) GateVerdict {
	t.Helper()
	return r.adminMove(t, tk, to, GateManual, ViaManual, "ana@x")
}

func (r *thuluRig) adminMove(t *testing.T, tk *Task, to Stage, cause GateCause, via, actor string) GateVerdict {
	t.Helper()
	ctx := t.Context()
	cur := r.get(t, tk.ID)
	from := cur.Stage
	v, err := r.store.StageExitGate(ctx, cur, from, to, cause)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Passed {
		return v
	}
	idle := StatusIdle
	updated, err := r.store.UpdateIfStage(ctx, cur.ID, from, Patch{Stage: &to, Status: &idle})
	if err != nil {
		t.Fatalf("move %s → %s: %v", from, to, err)
	}
	if err := r.store.RecordEvent(ctx, cur.ID, EventStageEntered, "", actor, string(from)+" → "+string(to),
		map[string]string{"from": string(from), "to": string(to), "via": via, "by_person": "true"}); err != nil {
		t.Fatal(err)
	}
	r.d.HandleStageEntered(ctx, updated, from)
	return v
}

// GitHub reports the PR merged: what onPRMerged does for the pull_request webhook (save the merge
// SHA, move to the merge_target stage past whatever the merge decided).
func (r *thuluRig) merged(t *testing.T, tk *Task, sha string) {
	t.Helper()
	if _, _, err := r.store.SetMergeSHA(t.Context(), tk.ID, r.pid, sha); err != nil {
		t.Fatal(err)
	}
	r.adminMove(t, tk, "release", GateMerge, ViaMerge, "github:ana")
}

// The next launch the test has not played yet, waiting for the dispatcher's goroutine.
func (r *thuluRig) next(t *testing.T) (*e2eRun, func(int, string, string)) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if run, exit := r.l.ready(r.played); run != nil && exit != nil {
			r.played++
			return run, exit
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no launch #%d (runs so far: %d)", r.played+1, r.l.count())
	return nil, nil
}

// What one fake container does before it exits 0: files in the task's worktree (committed by the
// daemon after the run) and files in its output dir.
type runOutput struct {
	code map[string]string
	out  map[string]string
}

// play takes the next launch, checks it is the stage and agent the flow expects there, and ends it.
func (r *thuluRig) play(t *testing.T, tk *Task, stage Stage, agent string, o runOutput) *e2eRun {
	t.Helper()
	run, exit := r.next(t)
	if run.stage != stage || run.agent != agent || run.audit {
		t.Fatalf("launch #%d = %s/%s audit=%v, want %s/%s", r.played, run.stage, run.agent, run.audit, stage, agent)
	}
	r.finish(t, tk, run, exit, o)
	return run
}

// audit takes the next launch, which must be the auditor of stage, and ends it with feedback.md.
func (r *thuluRig) audit(t *testing.T, tk *Task, stage Stage, feedback, comment string) *e2eRun {
	t.Helper()
	run, exit := r.next(t)
	if run.stage != stage || !run.audit {
		t.Fatalf("launch #%d = %s/%s audit=%v, want the auditor of %s", r.played, run.stage, run.agent, run.audit, stage)
	}
	out := map[string]string{"feedback.md": feedback}
	if comment != "" {
		out["auditor-comment.md"] = comment
	}
	r.finish(t, tk, run, exit, runOutput{out: out})
	return run
}

func (r *thuluRig) finish(t *testing.T, tk *Task, run *e2eRun, exit func(int, string, string), o runOutput) {
	t.Helper()
	if len(o.code) > 0 {
		wt := r.get(t, tk.ID).WorktreePaths[r.pid]
		if wt == "" {
			t.Fatalf("%s run on %s has no worktree to write", run.stage, tk.ID)
		}
		for rel, body := range o.code {
			writeIn(t, wt, rel, body)
		}
	}
	dir := t.TempDir()
	for name, body := range o.out {
		writeIn(t, dir, name, body)
	}
	r.l.exited(run.cid)
	exit(0, dir, "")
}

func (r *thuluRig) noMoreLaunches(t *testing.T, why string) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if n := r.l.count(); n != r.played {
		run, _ := r.l.ready(r.played)
		t.Fatalf("%s: unexpected launch %s/%s (audit=%v)", why, run.stage, run.agent, run.audit)
	}
}

func (r *thuluRig) wantAt(t *testing.T, tk *Task, stage Stage, status RunStatus) *Task {
	t.Helper()
	got := r.get(t, tk.ID)
	if got.Stage != stage || got.Status != status {
		t.Fatalf("task is %s/%s, want %s/%s\nlast comment: %s", got.Stage, got.Status, stage, status,
			lastComment(t, r.store, tk.ID))
	}
	return got
}

// Only the stage: a stage that launches an agent does it on its own goroutine, so the status may
// still be idle.
func (r *thuluRig) wantStage(t *testing.T, tk *Task, stage Stage) {
	t.Helper()
	if got := r.get(t, tk.ID); got.Stage != stage {
		t.Fatalf("task is in %s, want %s\nlast comment: %s", got.Stage, stage, lastComment(t, r.store, tk.ID))
	}
}

func approvalOf(t *testing.T, store *Store, taskID, key string) *Approval {
	t.Helper()
	list, err := store.ListApprovals(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Key == key {
			return &list[i]
		}
	}
	return nil
}

func (r *thuluRig) approve(t *testing.T, tk *Task, key string) {
	t.Helper()
	a := approvalOf(t, r.store, tk.ID, key)
	if a == nil {
		t.Fatalf("no %s approval on the task", key)
	}
	if _, err := r.d.DecideApproval(t.Context(), r.get(t, tk.ID), a.ID, ApprovalApproved, "ana@x", ""); err != nil {
		t.Fatalf("approve %s: %v", key, err)
	}
}

const (
	auditPass      = "---\nverdict: continue\nscore: 10/10\n---\n# Notes\nevery gate check passes\n"
	verdictPass    = "---\nverdict: continue\n---\nall green\n"
	cartTest       = "package cart\n\nimport \"testing\"\n\nfunc TestTotal(t *testing.T) {\n\tif Total(2, 3) != 5 {\n\t\tt.Fatal(\"total\")\n\t}\n}\n"
	cartTestStrong = cartTest + "\nfunc TestTotalZero(t *testing.T) {\n\tif Total(0, 0) != 0 {\n\t\tt.Fatal(\"zero\")\n\t}\n}\n"
	cartCode       = "package cart\n\nfunc Total(a, b int) int { return a + b }\n"
)

func refinementOut(surface string) runOutput {
	return runOutput{out: map[string]string{
		"flags.yaml": "surface: [" + surface + "]\nrisk: low\nsize: S\n",
		"comment.md": "Spec written: Total(a, b) returns the sum.",
	}}
}

func testDesignOut(test string) runOutput {
	return runOutput{
		code: map[string]string{"cart/cart_test.go": test},
		out:  map[string]string{lockedTestsFile: "cart/cart_test.go\n"},
	}
}

func verdictOut(body string) runOutput {
	return runOutput{out: map[string]string{"verdict.md": body}}
}

// Refinement has no auditor in the catalog; a feature then waits for "Definition ready".
func (r *thuluRig) refine(t *testing.T, tk *Task, surface string) {
	t.Helper()
	r.play(t, tk, "refinement", "thulu-refiner", refinementOut(surface))
}

// Test design writes a test and locks it; it has no auditor either.
func (r *thuluRig) designTests(t *testing.T, tk *Task, test string) {
	t.Helper()
	r.play(t, tk, "test_design", "thulu-test-designer", testDesignOut(test))
}

// Development makes the test pass and says continue. Its auditor runs on every run (mode always),
// a clean low-risk one included.
func (r *thuluRig) develop(t *testing.T, tk *Task, code map[string]string) {
	t.Helper()
	o := verdictOut(verdictPass)
	o.code = code
	r.play(t, tk, "in_progress", "thulu-developer", o)
	r.audit(t, tk, "in_progress", auditPass, "")
}

// Agentic testing says continue and its auditor, which runs on every run, agrees.
func (r *thuluRig) test(t *testing.T, tk *Task, o runOutput) {
	t.Helper()
	r.play(t, tk, "testing", "thulu-agentic-tester", o)
	r.audit(t, tk, "testing", auditPass, "")
}

func (r *thuluRig) lockedBlob(t *testing.T, tk *Task, path string) string {
	t.Helper()
	files, err := r.store.ListLockedFiles(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f.BlobSHA
		}
	}
	return ""
}

func stagesEntered(t *testing.T, store *Store, taskID string) []string {
	t.Helper()
	var out []string
	for _, ev := range eventsOf(t, store, taskID, EventStageEntered) {
		to, _ := ev["to"].(string)
		out = append(out, to)
	}
	return out
}

// The verdict body travels in the stage_verdict report, which the next brief carries.
func hasVerdictReport(t *testing.T, store *Store, taskID, part string) bool {
	t.Helper()
	reports, err := store.ListReports(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(reports, func(r Report) bool {
		return r.Kind == ReportKindStageVerdict && strings.Contains(r.Body, part)
	})
}

func commentByID(comments []Comment, id string) Comment {
	for i := range comments {
		if comments[i].ID == id {
			return comments[i]
		}
	}
	return Comment{}
}

func hasComment(t *testing.T, store *Store, taskID, part string) bool {
	t.Helper()
	full, err := store.Get(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(full.Comments, func(c Comment) bool { return strings.Contains(c.Body, part) })
}

// (a) A feature with UI walks every column: refinement and its approval, locked tests, development,
// a draft PR at verification, AI review, agentic testing asking for a UX review decided later,
// human review with the PR ready, the merge, the release and done.
func TestThuluFlowE2E_FeatureWithUI(t *testing.T) {
	r := newThuluRig(t)
	ctx := t.Context()
	tk := r.newTask(t, "feature")

	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "ui")
	if got := r.wantAt(t, tk, "refinement", StatusAwaitingApproval); !slices.Equal(got.FlowFlags, []string{"ui"}) {
		t.Fatalf("flow flags = %v, want [ui]", got.FlowFlags)
	}
	r.noMoreLaunches(t, "definition_ready pending")
	r.approve(t, tk, "definition_ready")

	r.designTests(t, tk, cartTest)
	if r.lockedBlob(t, tk, "cart/cart_test.go") == "" {
		t.Fatal("test design left cart/cart_test.go unlocked")
	}

	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	if draft, _, _ := r.gh.state(); !draft || r.get(t, tk.ID).PRURLs[r.pid] != thuluPRURL {
		t.Fatalf("entering verification: draft=%v PRs=%v, want the draft PR", draft, r.get(t, tk.ID).PRURLs)
	}

	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))

	// The tester asks a person for a UX review in human review; testing itself is not held.
	run, exit := r.next(t)
	if run.stage != "testing" || run.agent != "thulu-agentic-tester" {
		t.Fatalf("launch = %s/%s, want testing/thulu-agentic-tester", run.stage, run.agent)
	}
	if err := r.store.AddApproval(ctx, &Approval{TaskID: tk.ID, Key: "ux_review", Label: "UX review", Stage: "review",
		Source: ApprovalFromAgent, Status: ApprovalPending, CreatedBy: "agent:thulu-agentic-tester"}); err != nil {
		t.Fatal(err)
	}
	r.finish(t, tk, run, exit, verdictOut(verdictPass))
	r.audit(t, tk, "testing", auditPass, "")

	r.ready(t, tk)
	at := r.wantAt(t, tk, "review", StatusIdle)
	if _, ready, _ := r.gh.state(); !ready {
		t.Error("entering human review did not mark the PR ready")
	}
	if ev := eventsOf(t, r.store, tk.ID, EventPRReady); len(ev) != 1 {
		t.Errorf("pr_ready events = %v", ev)
	}
	v := r.personMove(t, at, "release")
	var holding []string
	for _, a := range v.Blocking {
		holding = append(holding, a.Key)
	}
	slices.Sort(holding)
	if v.Passed || !slices.Equal(holding, []string{"merge", "ux_review"}) {
		t.Fatalf("leaving review by hand: passed=%v holding %v, want merge and ux_review", v.Passed, holding)
	}
	r.approve(t, tk, "ux_review")
	r.approve(t, tk, "merge")
	r.wantAt(t, tk, "review", StatusIdle)

	sha := strings.Repeat("a", 40)
	r.merged(t, tk, sha)
	rel := r.wantAt(t, tk, "release", StatusIdle)
	if rel.MergeSHAs[r.pid] != sha {
		t.Errorf("merge_shas = %v, want %s", rel.MergeSHAs, sha)
	}
	if got := r.rel.released(); !slices.Equal(got, []string{tk.ID}) || len(rel.WorktreePaths) != 0 {
		t.Errorf("release: sandboxes released %v, worktrees %v; want the task's freed", got, rel.WorktreePaths)
	}
	if ev := eventsOf(t, r.store, tk.ID, EventResourcesReleased); len(ev) != 1 {
		t.Errorf("resources_released events = %v", ev)
	}
	r.noMoreLaunches(t, "a feature runs no agent in release")

	r.personMove(t, rel, "done")
	r.wantAt(t, tk, "done", StatusIdle)
	want := []string{"refinement", "test_design", "in_progress", "verification", "ai_review", "testing", "readiness", "review", "release", "done"}
	if got := stagesEntered(t, r.store, tk.ID); !slices.Equal(got, want) {
		t.Errorf("stages entered = %v, want %v", got, want)
	}
	r.noMoreLaunches(t, "done")
}

// Up to AI review for a task that needs no approval to leave refinement.
func (r *thuluRig) toAIReview(t *testing.T, tk *Task, surface string) {
	t.Helper()
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, surface)
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
}

// (b) A feature that touches no UI still runs agentic testing: the column has no skip_unless, and
// the tester reads the diff, finds no screen and continues. Which work a change needs is the
// agent's call, not the column's.
func TestThuluFlowE2E_FeatureWithoutUIStillRunsTheTester(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "feature")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.approve(t, tk, "definition_ready")
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)

	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)
	r.noMoreLaunches(t, "human review")
	if ev := eventsOf(t, r.store, tk.ID, EventStageSkipped); len(ev) != 0 {
		t.Errorf("stage_skipped events = %v, want none", ev)
	}
}

// Production readiness finds nothing and hands the task to human review.
func (r *thuluRig) ready(t *testing.T, tk *Task) {
	t.Helper()
	r.play(t, tk, "readiness", "thulu-readiness", verdictOut("---\nverdict: continue\n---\nReady: 0 behind main, 9 checks pass.\n"))
}

// The tester on a change with no screen: it reads the diff and continues.
func (r *thuluRig) testNothingToSee(t *testing.T, tk *Task) {
	t.Helper()
	r.test(t, tk, verdictOut("---\nverdict: continue\n---\nThe diff touches no screen.\n"))
}

// A board whose owner made agentic testing conditional again, the way the column editor's
// "Only run for" does.
func (r *thuluRig) testingOnlyForUI(t *testing.T) {
	t.Helper()
	stages := r.boards.Board(thuluBoardID).List()
	for i := range stages {
		if stages[i].ID == "testing" {
			stages[i].SkipUnless = []string{"ui"}
		}
	}
	if err := r.boards.SaveBoard(t.Context(), thuluBoardID, stages); err != nil {
		t.Fatal(err)
	}
}

// (b2) On a board with skip_unless [ui] on testing, the refiner says the change touches no UI and
// then the developer edits a component: the daemon reads the branch diff before passing over the
// column, adds "ui" and runs it.
func TestSkipUnlessUI_DeveloperTouchesUITheSpecDidNotMark(t *testing.T) {
	r := newThuluRig(t)
	r.testingOnlyForUI(t)
	tk := r.newTask(t, "feature")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.approve(t, tk, "definition_ready")
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{
		"cart/cart.go":                     cartCode,
		"web/src/components/CartTotal.tsx": "export const CartTotal = () => null;\n",
	})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))

	r.test(t, tk, verdictOut(verdictPass))
	r.ready(t, tk)
	got := r.wantAt(t, tk, "review", StatusIdle)
	if !slices.Equal(got.FlowFlags, []string{"api", "ui"}) {
		t.Errorf("flow flags = %v, want [api ui]: the diff's UI file is saved on the task", got.FlowFlags)
	}
	if ev := eventsOf(t, r.store, tk.ID, EventStageSkipped); len(ev) != 0 {
		t.Errorf("stage_skipped events = %v, want none", ev)
	}
	var fromDiff []any
	for _, ev := range eventsOf(t, r.store, tk.ID, EventFlowFlagsSet) {
		if files, ok := ev["from_diff"].([]any); ok {
			fromDiff = files
		}
	}
	if len(fromDiff) != 1 || fromDiff[0] != "web/src/components/CartTotal.tsx" {
		t.Errorf("flow_flags_set from_diff = %v, want the component", fromDiff)
	}
	if !hasComment(t, r.store, tk.ID, "web/src/components/CartTotal.tsx") {
		t.Error("no note names the UI file that made agentic testing run")
	}
}

// (b3) Same board: a diff the daemon cannot read runs the column instead of trusting the spec's
// surface.
func TestSkipUnlessUI_UnreadableDiffRunsTheStage(t *testing.T) {
	r := newThuluRig(t)
	r.testingOnlyForUI(t)
	ctx := t.Context()
	tk := r.newTask(t, "feature")
	// Straight into AI review with no worktree: there is no branch to diff.
	flags := []string{"api"}
	ai := Stage("ai_review")
	if _, err := r.store.Update(ctx, tk.ID, Patch{Stage: &ai, FlowFlags: &flags}); err != nil {
		t.Fatal(err)
	}
	r.d.autoAdvanceStage(ctx, r.get(t, tk.ID), tk.ID, "ai_review", 0, "")
	r.wantStage(t, tk, "testing")
	if got := r.get(t, tk.ID); !slices.Equal(got.FlowFlags, []string{"api"}) {
		t.Errorf("flow flags = %v: nothing confirmed UI, so the saved flags stay", got.FlowFlags)
	}
	if !hasComment(t, r.store, tk.ID, "Could not read the branch diff") {
		t.Errorf("last note = %q, want why testing runs", lastComment(t, r.store, tk.ID))
	}
}

// (b4) Same board: a Go-only diff confirms the spec, and the column is passed over unopened.
func TestSkipUnlessUI_GoOnlyDiffSkipsTheStage(t *testing.T) {
	r := newThuluRig(t)
	r.testingOnlyForUI(t)
	tk := r.newTask(t, "feature")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.approve(t, tk, "definition_ready")
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))

	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)
	r.noMoreLaunches(t, "testing is skipped for an api-only diff")
	if ev := eventsOf(t, r.store, tk.ID, EventStageSkipped); len(ev) != 1 || ev[0]["stage"] != "testing" {
		t.Errorf("stage_skipped events = %v, want testing", ev)
	}
	if hasComment(t, r.store, tk.ID, "UI files") {
		t.Error("a Go-only diff was read as UI")
	}
}

// (c) A chore has no test design and no agentic testing: its type skips them.
func TestThuluFlowE2E_ChoreSkipsTestDesignAndTesting(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "chore")
	if _, ok := tk.StageAssignments["test_design"]; ok {
		t.Errorf("a chore got a test designer: %v", tk.StageAssignments)
	}
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "ui")
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))

	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)
	r.noMoreLaunches(t, "a chore in human review")
	want := []string{"refinement", "in_progress", "verification", "ai_review", "readiness", "review"}
	if got := stagesEntered(t, r.store, tk.ID); !slices.Equal(got, want) {
		t.Errorf("stages entered = %v, want %v", got, want)
	}
	if a := approvalOf(t, r.store, tk.ID, "definition_ready"); a != nil {
		t.Errorf("a chore was asked for Definition ready: %+v", a)
	}
}

// (d) A release task goes from the backlog straight to Release, where thulu-release runs it; a
// verified deploy takes the release task to Done.
func TestThuluFlowE2E_ReleaseTask(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "release")
	if !maps.Equal(tk.StageAssignments, map[Stage]string{"release": "thulu-release"}) {
		t.Fatalf("release task assignments = %v, want only thulu-release in release", tk.StageAssignments)
	}
	if next, _ := r.d.nextStageFor(tk, "backlog"); next != "release" {
		t.Fatalf("after the backlog a release task goes to %q, want release", next)
	}
	r.personMove(t, tk, "release")
	r.play(t, tk, "release", "thulu-release", runOutput{out: map[string]string{
		"verdict.md": "---\nverdict: continue\nreason: other\n---\nDeployed 9f2c1e7: healthy, 0 tasks to Done.\n",
		"comment.md": "Deployed 9f2c1e7: healthy.",
	}})
	r.wantAt(t, tk, "done", StatusIdle)
	if got := stagesEntered(t, r.store, tk.ID); !slices.Equal(got, []string{"release", "done"}) {
		t.Errorf("stages entered = %v", got)
	}

	// A deploy a person has to judge holds the release task in Release.
	held := r.newTask(t, "release")
	r.personMove(t, held, "release")
	r.play(t, held, "release", "thulu-release", runOutput{out: map[string]string{
		"verdict.md": "---\nverdict: block\nreason: other\n---\nHealth inconclusive after 30 min: 210 requests. The error rate doubled at 14:05.\n",
	}})
	r.wantAt(t, held, "release", StatusBlocked)
	want := BlockInfo{Stage: "release", Agent: "thulu-release", Cause: BlockVerdict, Reason: "other"}
	if b := r.get(t, held.ID).Block; b == nil || *b != want {
		t.Errorf("block = %+v, want %+v", b, want)
	}
	// With no ask, the stop shows the verdict's first sentence; the rest stays in the report.
	if !hasVerdictReport(t, r.store, held.ID, "error rate doubled") || hasComment(t, r.store, held.ID, "error rate doubled") {
		t.Error("the block verdict's body is not in its report, or it is quoted in the discussion")
	}
	stop := r.get(t, held.ID)
	if id := OpenStopThreadOf(stop.Comments); id == "" {
		t.Error("the block verdict opened no stop thread")
	} else if c := commentByID(stop.Comments, id); c.Body != "Health inconclusive after 30 min: 210 requests." ||
		c.Source != SourceAgent || c.Author != "thulu-release" {
		t.Errorf("stop = %+v, want the verdict's first sentence written by thulu-release", c)
	}
	r.noMoreLaunches(t, "release tasks settled")
}

// (e) Development edits a test that test design locked: a person is asked, and the task waits in
// Development until the change is approved.
func TestThuluFlowE2E_BugEditingALockedTestWaitsForAPerson(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	if a := approvalOf(t, r.store, tk.ID, "definition_ready"); a != nil {
		t.Errorf("a bug was asked for Definition ready: %+v", a)
	}
	r.designTests(t, tk, cartTest)
	locked := r.lockedBlob(t, tk, "cart/cart_test.go")

	weakened := "package cart\n\nimport \"testing\"\n\nfunc TestTotal(t *testing.T) {}\n"
	o := verdictOut(verdictPass)
	o.code = map[string]string{"cart/cart.go": cartCode, "cart/cart_test.go": weakened}
	r.play(t, tk, "in_progress", "thulu-developer", o)
	r.audit(t, tk, "in_progress", auditPass, "")
	r.wantAt(t, tk, "in_progress", StatusAwaitingApproval)
	a := approvalOf(t, r.store, tk.ID, ApprovalKeyTestChange)
	if a == nil || a.Status != ApprovalPending || a.Stage != "in_progress" || a.Source != ApprovalFromSystem {
		t.Fatalf("test_change approval = %+v", a)
	}
	r.noMoreLaunches(t, "test_change pending")

	r.approve(t, tk, ApprovalKeyTestChange)
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	if now := r.lockedBlob(t, tk, "cart/cart_test.go"); now == "" || now == locked {
		t.Errorf("after the approval the lock is %q, want the approved edit as baseline (was %q)", now, locked)
	}
}

const verificationKickBack = "---\nverdict: kick_back\nreason: gates_failed\nkick_back_to: in_progress\n---\nTC-1 fails: Total(2, 3) returns 6.\n"

// (f) Verification sends the task back to Development twice; the third time the board's cap of two
// returns blocks it where it is, with a comment saying who sent it back and why.
func TestThuluFlowE2E_KickBackLimitBlocksOnTheThirdReturn(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})

	for i := 1; i <= 2; i++ {
		r.play(t, tk, "verification", "thulu-verifier", verdictOut(verificationKickBack))
		r.wantStage(t, tk, "in_progress")
		r.develop(t, tk, map[string]string{"cart/cart.go": cartCode + fmt.Sprintf("\n// fix %d\n", i)})
	}
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verificationKickBack))
	r.wantAt(t, tk, "verification", StatusBlocked)
	c := lastComment(t, r.store, tk.ID)
	for _, part := range []string{"2 times", "allows 2", "Development", "TC-1 fails"} {
		if !strings.Contains(c, part) {
			t.Errorf("escalation comment %q lacks %q", c, part)
		}
	}
	if ev := eventsOf(t, r.store, tk.ID, EventKickBackLimit); len(ev) != 1 || ev[0]["to"] != "in_progress" {
		t.Errorf("kick_back_limit events = %v", ev)
	}
	want := BlockInfo{Stage: "verification", Agent: "thulu-verifier", Cause: BlockKickBackLimit}
	if b := r.get(t, tk.ID).Block; b == nil || *b != want {
		t.Errorf("block = %+v, want %+v", b, want)
	}
	r.noMoreLaunches(t, "blocked at the cap")
}

// (g) Mutants survive: verification sends the task to test design, not to Development, and the
// stronger test test design writes there becomes the locked baseline.
func TestThuluFlowE2E_MutantsSurvivedGoBackToTestDesign(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.designTests(t, tk, cartTest)
	first := r.lockedBlob(t, tk, "cart/cart_test.go")
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})

	r.play(t, tk, "verification", "thulu-verifier", verdictOut(
		"---\nverdict: kick_back\nreason: mutants_survived\nkick_back_to: test_design\n---\n3 mutants survive in cart.go: a+b → a-b with 0.\n"))
	r.wantStage(t, tk, "test_design")
	if !hasComment(t, r.store, tk.ID, "mutants_survived") {
		t.Error("no comment carries the reason to test design")
	}
	r.designTests(t, tk, cartTestStrong)
	second := r.lockedBlob(t, tk, "cart/cart_test.go")
	if second == "" || second == first {
		t.Fatalf("lock after the second test design = %q, want a new baseline (first %q)", second, first)
	}
	r.develop(t, tk, nil)
	if a := approvalOf(t, r.store, tk.ID, ApprovalKeyTestChange); a != nil {
		t.Errorf("test design's own rewrite asked a person: %+v", a)
	}
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)
	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)
}

// (h) The Development auditor sends the run back to its own stage; the next developer run starts
// with the auditor's notes in the discussion.
func TestThuluFlowE2E_DevelopmentAuditorRedoesItsStage(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.designTests(t, tk, cartTest)
	o := verdictOut(verdictPass)
	o.code = map[string]string{"cart/cart.go": cartCode}
	r.play(t, tk, "in_progress", "thulu-developer", o)
	const note = "Total ignores negative amounts; TC-1 only covers positives."
	r.audit(t, tk, "in_progress",
		"---\nverdict: kick_back\nkick_back_to: in_progress\nscore: 6/10\n---\n# Notes\nQ2 FAIL negative amounts\n", note)

	o.code = map[string]string{"cart/cart.go": cartCode + "\n// negatives\n"}
	run := r.play(t, tk, "in_progress", "thulu-developer", o)
	if !slices.ContainsFunc(run.discussion, func(c string) bool { return strings.Contains(c, note) }) {
		t.Fatalf("the second developer run started without the auditor's note; discussion: %q", run.discussion)
	}
	r.audit(t, tk, "in_progress", auditPass, "")
	r.wantStage(t, tk, "verification")
	var redo map[string]any
	for _, m := range eventsOf(t, r.store, tk.ID, EventStageEntered) {
		if m["from"] == "in_progress" && m["to"] == "in_progress" {
			redo = m
		}
	}
	if redo == nil || redo["via"] != ViaKickBack {
		t.Errorf("stage_entered in_progress → in_progress = %v, want one via kick_back", redo)
	}
	if n := r.store.CountKickBacks(t.Context(), tk.ID); n != 1 {
		t.Errorf("kick backs = %d, want the redo counted once", n)
	}
}

// the discussion, walks the flow again on the PR it already has, and the Merge approval asks again.
func TestThuluFlowE2E_ChangesRequestedInReviewGoBackToDevelopment(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.toAIReview(t, tk, "api")
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)
	r.ready(t, tk)
	at := r.wantAt(t, tk, "review", StatusIdle)

	const note = "Round the total to cents before showing it."
	merge := approvalOf(t, r.store, tk.ID, "merge")
	if _, err := r.d.DecideApproval(t.Context(), at, merge.ID, ApprovalChangesRequested, "ana@x", note); err != nil {
		t.Fatalf("request changes: %v", err)
	}
	run := r.play(t, tk, "in_progress", "thulu-developer", runOutput{
		code: map[string]string{"cart/cart.go": cartCode + "\n// cents\n"},
		out:  map[string]string{"verdict.md": verdictPass},
	})
	if !slices.ContainsFunc(run.discussion, func(c string) bool { return strings.Contains(c, note) }) {
		t.Errorf("Development restarted without the reviewer's note; discussion: %q", run.discussion)
	}
	r.audit(t, tk, "in_progress", auditPass, "")
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)
	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)

	if a := approvalOf(t, r.store, tk.ID, "merge"); a == nil || a.Status != ApprovalPending {
		t.Errorf("merge approval back in review = %+v, want pending again", a)
	}
	_, _, calls := r.gh.state()
	opened := 0
	for _, c := range calls {
		if c == "POST /repos/acme/widgets/pulls" {
			opened++
		}
	}
	if opened != 1 {
		t.Errorf("PRs opened = %d, want the one PR reused (calls %v)", opened, calls)
	}
	if n := r.store.CountKickBacks(t.Context(), tk.ID); n != 1 {
		t.Errorf("kick backs = %d, want the change request counted", n)
	}
}

// Up to production readiness for a bug that touches no screen.
func (r *thuluRig) toReadiness(t *testing.T, tk *Task) {
	t.Helper()
	r.toAIReview(t, tk, "api")
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)
}

func readinessKickBack(to Stage, reason, body string) runOutput {
	return verdictOut("---\nverdict: kick_back\nkick_back_to: " + string(to) + "\nreason: " + reason + "\n---\n" + body + "\n")
}

// (i) Production readiness sends the task back to any earlier column it names; the task walks the
// flow from there, through readiness again, to human review.
func TestThuluFlowE2E_ReadinessSendsBackToEachColumn(t *testing.T) {
	cases := []struct {
		to     Stage
		reason string
		redo   func(r *thuluRig, t *testing.T, tk *Task)
	}{
		{"in_progress", "branch_behind", func(r *thuluRig, t *testing.T, tk *Task) {
			r.develop(t, tk, map[string]string{"cart/cart.go": cartCode + "\n// rebased\n"})
			r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
			r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
			r.testNothingToSee(t, tk)
		}},
		{"verification", "reports_inconsistent", func(r *thuluRig, t *testing.T, tk *Task) {
			r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
			r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
			r.testNothingToSee(t, tk)
		}},
		{"ai_review", "review_shallow", func(r *thuluRig, t *testing.T, tk *Task) {
			r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
			r.testNothingToSee(t, tk)
		}},
		{"testing", "evidence_missing", func(r *thuluRig, t *testing.T, tk *Task) {
			r.testNothingToSee(t, tk)
		}},
	}
	for _, c := range cases {
		t.Run(string(c.to), func(t *testing.T) {
			r := newThuluRig(t)
			tk := r.newTask(t, "bug")
			r.toReadiness(t, tk)
			r.play(t, tk, "readiness", "thulu-readiness", readinessKickBack(c.to, c.reason, "finding for "+string(c.to)))
			r.wantStage(t, tk, c.to)
			if !hasVerdictReport(t, r.store, tk.ID, "finding for "+string(c.to)) || !hasComment(t, r.store, tk.ID, c.reason) {
				t.Errorf("the target column gets no feedback; last comment %q", lastComment(t, r.store, tk.ID))
			}
			moves := eventsOf(t, r.store, tk.ID, EventStageEntered)
			if last := moves[len(moves)-1]; last["from"] != "readiness" || last["to"] != string(c.to) || last["via"] != ViaVerdict {
				t.Errorf("send-back = %v, want readiness → %s via verdict", last, c.to)
			}
			c.redo(r, t, tk)
			r.ready(t, tk)
			r.wantAt(t, tk, "review", StatusIdle)
			if n := r.store.CountKickBacks(t.Context(), tk.ID); n != 1 {
				t.Errorf("kick backs = %d, want 1", n)
			}
		})
	}
}

// (j) The loop guard: readiness sends the task back to rebase, the next round comes back still
// behind, and the second send-back for the same reason blocks the task in readiness instead of
// moving it, under the board's cap of two.
func TestThuluFlowE2E_ReadinessBlocksOnTheSameReasonTwice(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.toReadiness(t, tk)
	behind := readinessKickBack("in_progress", "branch_behind", "66 commits behind main; conflicts in cart/cart.go.")
	r.play(t, tk, "readiness", "thulu-readiness", behind)
	r.wantStage(t, tk, "in_progress")
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode + "\n// round 2\n"})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)

	r.play(t, tk, "readiness", "thulu-readiness", behind)
	r.wantAt(t, tk, "readiness", StatusBlocked)
	want := BlockInfo{Stage: "readiness", Agent: "thulu-readiness", Cause: BlockKickBackRepeated, Reason: "branch_behind"}
	if b := r.get(t, tk.ID).Block; b == nil || *b != want {
		t.Errorf("block = %+v, want %+v", b, want)
	}
	c := lastComment(t, r.store, tk.ID)
	for _, part := range []string{"same reason", "branch_behind", "66 commits behind"} {
		if !strings.Contains(c, part) {
			t.Errorf("block comment %q lacks %q", c, part)
		}
	}
	if n := r.store.CountKickBacks(t.Context(), tk.ID); n != 1 {
		t.Errorf("kick backs = %d, want the refused one not counted", n)
	}
	r.noMoreLaunches(t, "blocked on a repeated send-back")

	// A person relaunching the stage resets the count: the same reason may send it back again.
	if err := r.store.RecordEvent(t.Context(), tk.ID, EventKickBackReset, "", "ana@x", "reset",
		map[string]string{"stage": "readiness"}); err != nil {
		t.Fatal(err)
	}
	if err := r.d.RelaunchStage(t.Context(), r.get(t, tk.ID), 0); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	r.play(t, tk, "readiness", "thulu-readiness", behind)
	r.wantStage(t, tk, "in_progress")
}

// (k) A reason a second round may fail for anew (failing gates) keeps only the board's cap.
func TestThuluFlowE2E_GatesFailedTwiceStillMoves(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.personMove(t, tk, "refinement")
	r.refine(t, tk, "api")
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	for i := 1; i <= 2; i++ {
		r.play(t, tk, "verification", "thulu-verifier", verdictOut(verificationKickBack))
		r.wantStage(t, tk, "in_progress")
		r.develop(t, tk, map[string]string{"cart/cart.go": cartCode + fmt.Sprintf("\n// fix %d\n", i)})
	}
}

// (l) Agentic testing exits cleanly with its kick_back written only in the report: no verdict.md,
// so the task is blocked in testing instead of reaching human review.
func TestThuluFlowE2E_TestingWithoutVerdictBlocks(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.toAIReview(t, tk, "api")
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.play(t, tk, "testing", "thulu-agentic-tester", runOutput{out: map[string]string{
		"_index.md":  "# Testing\n\nverdict: kick_back\nkick_back_to: in_progress\n\nTC-2 fails on mobile.\n",
		"comment.md": "Back to Development: TC-2 fails on mobile.",
	}})
	r.wantAt(t, tk, "testing", StatusBlocked)
	want := BlockInfo{Stage: "testing", Agent: "thulu-agentic-tester", Cause: BlockVerdictMissing}
	if b := r.get(t, tk.ID).Block; b == nil || *b != want {
		t.Errorf("block = %+v, want %+v", b, want)
	}
	r.noMoreLaunches(t, "blocked without a verdict")
}

func refinementSkipping(surface, stage, reason string) runOutput {
	o := refinementOut(surface)
	o.out["flags.yaml"] += "skip:\n  - stage: " + stage + "\n    reason: " + reason + "\n"
	return o
}

// (s1) The refiner skips test design for a bug; development comes next. In AI review a person decides
// the tests are needed after all: the task goes back to test design, the test designer runs, and the
// task walks forward again to human review.
func TestThuluFlowE2E_RefinerSkipsTestDesignAndAPersonBringsItBack(t *testing.T) {
	r := newThuluRig(t)
	ctx := t.Context()
	tk := r.newTask(t, "bug")
	r.personMove(t, tk, "refinement")
	r.play(t, tk, "refinement", "thulu-refiner", refinementSkipping("api", "test_design", "a log message changes, nothing to test"))

	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode})
	if added := eventsOf(t, r.store, tk.ID, EventStageSkipAdded); len(added) != 1 ||
		added[0]["stage"] != "test_design" || added[0]["by"] != "agent:thulu-refiner" || added[0]["source"] != SkipSourceFlagsYAML {
		t.Errorf("stage_skip_added = %v", added)
	}
	if skipped := eventsOf(t, r.store, tk.ID, EventStageSkipped); len(skipped) != 1 ||
		skipped[0]["stage"] != "test_design" || skipped[0]["task_skip"] != true {
		t.Errorf("stage_skipped = %v", skipped)
	}
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))

	// The reviewer is running when the person decides; its run outlives the move.
	reviewer, exit := r.next(t)
	if reviewer.stage != "ai_review" {
		t.Fatalf("launch = %s, want ai_review", reviewer.stage)
	}
	res, err := r.d.RemoveStageSkip(ctx, r.get(t, tk.ID), "test_design", SkipActor{By: "ana@x"},
		"the fix changes how totals round; it needs a locked test")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || !res.MovedBack {
		t.Fatalf("removal = %+v, want the task back in test design", res)
	}
	r.wantStage(t, tk, "test_design")
	r.finish(t, tk, reviewer, exit, verdictOut(verdictPass))

	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{"cart/cart.go": cartCode + "\n// rounding\n"})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.testNothingToSee(t, tk)
	r.ready(t, tk)
	got := r.wantAt(t, tk, "review", StatusIdle)
	if len(got.StageSkips) != 0 {
		t.Errorf("skips = %+v, want none", got.StageSkips)
	}
	want := []string{"refinement", "in_progress", "verification", "ai_review", "test_design", "in_progress",
		"verification", "ai_review", "testing", "readiness", "review"}
	if entered := stagesEntered(t, r.store, tk.ID); !slices.Equal(entered, want) {
		t.Errorf("stages entered = %v, want %v", entered, want)
	}
	removed := eventsOf(t, r.store, tk.ID, EventStageSkipRemoved)
	if len(removed) != 1 || removed[0]["cause"] != SkipCauseRemoved || removed[0]["passed"] != true {
		t.Errorf("stage_skip_removed = %v", removed)
	}
	if r.lockedBlob(t, tk, "cart/cart_test.go") == "" {
		t.Error("test design ran but left the test unlocked")
	}
}

// (s2) Testing waits on "ui" and the refiner skipped it, but the developer edits a component: the
// daemon removes the skip itself and testing runs, without a second "ui" in the flags.
func TestThuluFlowE2E_UIDiffRemovesATaskSkip(t *testing.T) {
	r := newThuluRig(t)
	r.testingOnlyForUI(t)
	tk := r.newTask(t, "feature")
	r.personMove(t, tk, "refinement")
	r.play(t, tk, "refinement", "thulu-refiner", refinementSkipping("ui", "testing", "only the wording of a label changes"))
	r.approve(t, tk, "definition_ready")
	r.designTests(t, tk, cartTest)
	r.develop(t, tk, map[string]string{
		"cart/cart.go":                     cartCode,
		"web/src/components/CartTotal.tsx": "export const CartTotal = () => null;\n",
	})
	r.play(t, tk, "verification", "thulu-verifier", verdictOut(verdictPass))
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	r.test(t, tk, verdictOut(verdictPass))
	got := r.get(t, tk.ID)
	if !slices.Equal(got.FlowFlags, []string{"ui"}) {
		t.Errorf("flow flags = %v, want [ui] once", got.FlowFlags)
	}
	if len(got.StageSkips) != 0 {
		t.Errorf("skips = %+v, want none", got.StageSkips)
	}
	removed := eventsOf(t, r.store, tk.ID, EventStageSkipRemoved)
	if len(removed) != 1 || removed[0]["stage"] != "testing" || removed[0]["cause"] != SkipCauseUIDiff ||
		!strings.Contains(removed[0]["reason"].(string), "CartTotal.tsx") {
		t.Errorf("stage_skip_removed = %v", removed)
	}
}

// (s3) Same board, a person skipped testing and the diff cannot be read: the skip goes and testing
// runs.
func TestThuluFlowE2E_UnreadableDiffRemovesATaskSkip(t *testing.T) {
	r := newThuluRig(t)
	r.testingOnlyForUI(t)
	ctx := t.Context()
	tk := r.newTask(t, "feature")
	if _, err := r.store.UpsertStageSkips(ctx, tk.ID, SkipActor{By: "ana@x"},
		[]StageSkip{{Stage: "testing", Reason: "backend only"}}, SkipSourceAPI); err != nil {
		t.Fatal(err)
	}
	flags := []string{"ui"}
	ai := Stage("ai_review")
	if _, err := r.store.Update(ctx, tk.ID, Patch{Stage: &ai, FlowFlags: &flags}); err != nil {
		t.Fatal(err)
	}
	r.d.autoAdvanceStage(ctx, r.get(t, tk.ID), tk.ID, "ai_review", 0, "")
	r.wantStage(t, tk, "testing")
	removed := eventsOf(t, r.store, tk.ID, EventStageSkipRemoved)
	if len(removed) != 1 || removed[0]["cause"] != SkipCauseDiffUnreadable {
		t.Errorf("stage_skip_removed = %v", removed)
	}
}

// The catalog audits Development and Testing on every run, whatever the risk, and no other column:
// play fails on an auditor launch where none is expected, so walking the flow checks both halves.
func TestThuluFlowE2E_AuditorsRunOnDevelopmentAndTestingOnly(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.toReadiness(t, tk)
	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)
	for _, e := range eventsOf(t, r.store, tk.ID, EventAuditorSkipped) {
		t.Errorf("an audit was skipped: %v", e)
	}
}

// Testing stops on two cases only a person can run; waiving both moves the task to readiness and
// starts its agent. The waiver comes from an HTTP request whose context ends when the answer goes
// out, so the launch must not die with it.
func TestThuluFlowE2E_WaivingTheNeedsPersonCasesStartsReadiness(t *testing.T) {
	r := newThuluRig(t)
	tk := r.newTask(t, "bug")
	r.toAIReview(t, tk, "api")
	r.play(t, tk, "ai_review", "thulu-reviewer", verdictOut(verdictPass))
	if _, err := r.store.AddTestCases(t.Context(), tk.ID, "agent:thulu-test-designer", []TestCase{
		{Title: "Signs in with an emailed code"}, {Title: "Deletes the account"},
	}); err != nil {
		t.Fatal(err)
	}
	r.play(t, tk, "testing", "thulu-agentic-tester", verdictOut(`---
verdict: block
reason: needs_decision
needs_person:
  - {ref: TC-1, ask: Needs the emailed code of a dev account, why: real_account}
  - {ref: TC-2, ask: Needs a disposable account, why: real_account}
---
Two cases need a person.
`))
	r.wantAt(t, tk, "testing", StatusBlocked)

	req, cancel := context.WithCancel(t.Context())
	res, err := r.d.WaiveTestCases(req, r.get(t, tk.ID), []string{"TC-1", "TC-2"}, "ana@x", "no dev inbox")
	cancel()
	if err != nil || !res.Advanced || res.Stage != "readiness" {
		t.Fatalf("waive = %+v, %v; want the task moved to readiness", res, err)
	}
	r.ready(t, tk)
	r.wantAt(t, tk, "review", StatusIdle)
}
