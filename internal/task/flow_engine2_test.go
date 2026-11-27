package task

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// QA (testing) runs only for ui; perf only for perf. Every stage auto-advances so a test walks it.
func skipBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true, AutoAdvance: true},
		{ID: "verify", Label: "Verify", Position: 20, AutoAdvance: true,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "qa", Label: "QA"}}},
		{ID: "testing", Label: "Testing", Position: 30, AutoAdvance: true, SkipUnless: []string{"ui"}},
		{ID: "perf", Label: "Perf", Position: 40, AutoAdvance: true, SkipUnless: []string{"perf"}},
		{ID: "review", Label: "Review", Position: 50},
		{ID: "shipped", Label: "Shipped", Position: 60, Terminal: true},
	})
}

func TestNextStageForSkipsConditionalStages(t *testing.T) {
	reg := skipBoard()
	cases := []struct {
		name    string
		flags   []string
		next    Stage
		skipped []Stage
	}{
		{"NULL skips nothing", nil, "testing", nil},
		{"ui runs QA", []string{"ui"}, "testing", nil},
		{"perf passes over QA", []string{"perf"}, "perf", []Stage{"testing"}},
		{"api passes over both", []string{"api"}, "review", []Stage{"testing", "perf"}},
		{"set but empty passes over both", []string{}, "review", []Stage{"testing", "perf"}},
	}
	for _, c := range cases {
		next, skipped := nextStageFor(reg, &Task{FlowFlags: c.flags}, "verify")
		if next != c.next || !reflect.DeepEqual(skipped, c.skipped) {
			t.Errorf("%s: next %q skipped %v, want %q %v", c.name, next, skipped, c.next, c.skipped)
		}
	}
	if next, _ := nextStageFor(reg, &Task{FlowFlags: []string{}}, "review"); next != "shipped" {
		t.Errorf("from review = %q: a terminal stage is never skipped", next)
	}
}

func TestAutoAdvancePassesOverASkippedStageWithoutEnteringIt(t *testing.T) {
	d, store := newStageTestDispatcher(t, skipBoard())
	ctx := context.Background()
	tk := createIn(t, store, "build", "")
	flags := []string{"api"}
	if _, err := store.Update(ctx, tk.ID, Patch{FlowFlags: &flags}); err != nil {
		t.Fatal(err)
	}
	// The caller's copy predates the flags, as after a run whose flags.yaml just set them.
	d.autoAdvanceStage(ctx, tk, tk.ID, "build", 0, "")
	at, _ := store.Get(ctx, tk.ID)
	if at.Stage != "verify" {
		t.Fatalf("stage = %q, want verify", at.Stage)
	}
	// The QA approval holds it; deciding it lets ApprovalsSettled move on.
	d.autoAdvanceStage(ctx, at, at.ID, "verify", 0, "")
	if _, err := d.DecideApproval(ctx, at, approvalKeyed(t, store, tk.ID, "qa").ID, ApprovalApproved, "ana@x", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	after, _ := store.Get(ctx, tk.ID)
	if after.Stage != "review" {
		t.Fatalf("stage = %q, want review: testing and perf run only for ui and perf", after.Stage)
	}
	for _, ev := range eventsOf(t, store, tk.ID, EventStageEntered) {
		if ev["to"] == "testing" || ev["to"] == "perf" {
			t.Errorf("entered a skipped stage: %v", ev)
		}
	}
	skipped := eventsOf(t, store, tk.ID, EventStageSkipped)
	if len(skipped) != 2 || skipped[0]["stage"] != "testing" || skipped[1]["stage"] != "perf" {
		t.Errorf("stage_skipped events = %v", skipped)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "Perf") || !strings.Contains(c, "api") {
		t.Errorf("last note = %q, want why perf was skipped", c)
	}
}

func TestAutoAdvanceWithoutFlowFlagsRunsEveryStage(t *testing.T) {
	d, store := newStageTestDispatcher(t, skipBoard())
	ctx := context.Background()
	tk := createIn(t, store, "verify", "")
	d.HandleStageEntered(ctx, tk, "build")
	d.autoAdvanceStage(ctx, tk, tk.ID, "verify", 0, "")
	if _, err := d.DecideApproval(ctx, tk, approvalKeyed(t, store, tk.ID, "qa").ID, ApprovalWaived, "ana@x", ""); err != nil {
		t.Fatalf("waive: %v", err)
	}
	if after, _ := store.Get(ctx, tk.ID); after.Stage != "testing" {
		t.Errorf("stage = %q, want testing: NULL flow flags skip nothing", after.Stage)
	}
	if got := eventsOf(t, store, tk.ID, EventStageSkipped); len(got) != 0 {
		t.Errorf("stage_skipped events with NULL flags: %v", got)
	}
}

func TestParseAndAddFlagsStoresTheSurface(t *testing.T) {
	_, store := newStageTestDispatcher(t, skipBoard())
	ctx := context.Background()
	tk := createIn(t, store, "build", "")

	res, err := ParseAndAddFlags(ctx, store, tk.ID, SkipActor{By: "agent:refiner", Agent: true}, []byte("surface: [UI, migration]\nrisk: medium\nsize: L\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.FlowFlags, []string{"ui", "migration"}) || res.Risk != "medium" || res.Size != "L" {
		t.Errorf("import = %+v", res)
	}
	got, _ := store.GetRow(ctx, tk.ID)
	if !reflect.DeepEqual(got.FlowFlags, []string{"ui", "migration"}) {
		t.Errorf("stored = %v", got.FlowFlags)
	}
	if ev := eventsOf(t, store, tk.ID, EventFlowFlagsSet); len(ev) != 1 || ev[0]["risk"] != "medium" || ev[0]["size"] != "L" {
		t.Errorf("flow_flags_set events = %v", ev)
	}

	// One bad value leaves the surface as it was: a typo must not skip QA.
	res, err = ParseAndAddFlags(ctx, store, tk.ID, SkipActor{By: "agent:refiner", Agent: true}, []byte("surface: [ui, \"no good\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Invalid) != 1 {
		t.Errorf("invalid = %v", res.Invalid)
	}
	if got, _ := store.GetRow(ctx, tk.ID); !reflect.DeepEqual(got.FlowFlags, []string{"ui", "migration"}) {
		t.Errorf("an invalid surface changed the stored one to %v", got.FlowFlags)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "no good") {
		t.Errorf("note = %q, want the refused value", c)
	}

	// No surface key: the flags stay.
	if _, err := ParseAndAddFlags(ctx, store, tk.ID, SkipActor{By: "agent:refiner", Agent: true}, []byte("flags: []\n")); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetRow(ctx, tk.ID); !reflect.DeepEqual(got.FlowFlags, []string{"ui", "migration"}) {
		t.Errorf("a flags.yaml without surface changed it to %v", got.FlowFlags)
	}
}

// The refiner of a real run wrote its findings as bare strings. They become custom flags, and the
// surface, risk and size next to them still land.
func TestParseAndAddFlagsTakesBareStrings(t *testing.T) {
	_, store := newStageTestDispatcher(t, skipBoard())
	ctx := context.Background()
	tk := createIn(t, store, "build", "")

	doc := "surface: [ui, auth]\nrisk: high\nsize: M\nflags:\n  - \"specs pin the old /auth-main behavior\"\n" +
		"  - kind: custom\n    label: proposed follow-up task\n  - \"\"\n"
	res, err := ParseAndAddFlags(ctx, store, tk.ID, SkipActor{By: "agent:refiner", Agent: true}, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || !reflect.DeepEqual(res.FlowFlags, []string{"ui", "auth"}) || res.Risk != "high" || res.Size != "M" {
		t.Errorf("import = %+v", res)
	}
	got, err := store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, f := range got.Flags {
		if f.Kind == FlagCustom {
			labels = append(labels, f.Label)
		}
	}
	if !reflect.DeepEqual(labels, []string{"specs pin the old /auth-main behavior", "proposed follow-up task"}) {
		t.Errorf("custom flags = %v", labels)
	}
}

type countingReleaser struct {
	mu  sync.Mutex
	ids []string
}

func (r *countingReleaser) Release(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

func (r *countingReleaser) released() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func TestReleaseStageFreesTheGroupOnceAllAreThere(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	rel := &countingReleaser{}
	d.sandbox = rel
	ctx := context.Background()

	root := createIn(t, store, "review", "")
	child := &Task{WorkspaceID: testWS, Title: "child", ParentID: root.ID, Stage: "code"}
	if err := store.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"p1": "/data/projects/x--task"}
	for _, id := range []string{root.ID, child.ID} {
		if _, err := store.Update(ctx, id, Patch{WorktreePaths: paths}); err != nil {
			t.Fatal(err)
		}
	}

	root = moveTo(t, store, root.ID, "release")
	d.HandleStageEntered(ctx, root, "review")
	if got := rel.released(); len(got) != 0 {
		t.Fatalf("released %v while a subtask is still coding", got)
	}
	if ok, _ := store.AllInGroupReleased(ctx, root.ID); ok {
		t.Error("AllInGroupReleased with a subtask in code")
	}

	childNow := moveTo(t, store, child.ID, "release")
	d.HandleStageEntered(ctx, childNow, "code")
	if got := rel.released(); !reflect.DeepEqual(got, []string{root.ID}) {
		t.Fatalf("released %v, want the root's sandbox once", got)
	}
	if !store.InReleaseStage(childNow) {
		t.Error("InReleaseStage(release) = false")
	}
	for _, id := range []string{root.ID, child.ID} {
		got, _ := store.Get(ctx, id)
		if len(got.WorktreePaths) != 0 {
			t.Errorf("%s still points at worktrees %v", id, got.WorktreePaths)
		}
	}
	if ev := eventsOf(t, store, child.ID, EventResourcesReleased); len(ev) != 1 {
		t.Errorf("resources_released events = %v", ev)
	}
	if d.needsWorktree(childNow, "release") {
		t.Error("a release_resources stage asks for a worktree")
	}
}

// The root waits in release; its subtask skips release and lands in the terminal stage last. Nobody
// enters release again, so reaching Done has to free the group.
func TestDoneFreesAGroupWaitingInARelease(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	rel := &countingReleaser{}
	d.sandbox = rel
	ctx := context.Background()

	root := createIn(t, store, "review", "")
	child := &Task{WorkspaceID: testWS, Title: "child", ParentID: root.ID, Stage: "code"}
	if err := store.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	root = moveTo(t, store, root.ID, "release")
	d.HandleStageEntered(ctx, root, "review")
	if got := rel.released(); len(got) != 0 {
		t.Fatalf("released %v while a subtask is still coding", got)
	}
	childNow := moveTo(t, store, child.ID, "shipped")
	d.HandleStageEntered(ctx, childNow, "code")
	if got := rel.released(); !reflect.DeepEqual(got, []string{root.ID}) {
		t.Fatalf("released %v, want the root's sandbox once the last task is done", got)
	}
	if ev := eventsOf(t, store, child.ID, EventResourcesReleased); len(ev) != 1 {
		t.Errorf("resources_released events = %v", ev)
	}
}

// A restart reconciles a group the daemon died before releasing, and leaves alone one it already
// released (its release agent may be waiting on an approval with its session on disk).
func TestReconcileReleasesOnlyWhatWasNotReleased(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	rel := &countingReleaser{}
	d.sandbox = rel
	ctx := context.Background()

	released := createIn(t, store, "review", "")
	released = moveTo(t, store, released.ID, "release")
	d.HandleStageEntered(ctx, released, "review")

	orphan := createIn(t, store, "release", "")
	if _, err := store.Update(ctx, orphan.ID, Patch{WorktreePaths: map[string]string{"p1": "/data/projects/x--task"}}); err != nil {
		t.Fatal(err)
	}
	if got := rel.released(); !reflect.DeepEqual(got, []string{released.ID}) {
		t.Fatalf("before reconcile released %v", got)
	}
	d.ReconcileDone(ctx)
	if got := rel.released(); !reflect.DeepEqual(got, []string{released.ID, orphan.ID}) {
		t.Errorf("after reconcile released %v, want the orphan once and the released group not again", got)
	}
}

func TestKickBackFromAReleaseStageIsRefused(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "release", "")
	d.kickBack(context.Background(), tk, kickBackReq{From: "release", Target: "code", Via: ViaChangesRequested, Actor: "ana@x"})
	after, _ := store.Get(t.Context(), tk.ID)
	if after.Stage != "release" || after.Status != StatusBlocked {
		t.Errorf("after a kick back from release: %q/%q, want release/blocked", after.Stage, after.Status)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "new task") {
		t.Errorf("note = %q", c)
	}
	if ev := eventsOf(t, store, tk.ID, EventStageEntered); len(ev) != 0 {
		t.Errorf("the refused kick back moved the task: %v", ev)
	}
}

type fixedToken struct{}

func (fixedToken) GetGithubToken(_ context.Context, projectID string) (*githubapp.Token, error) {
	return &githubapp.Token{ProjectID: projectID, Token: "tok"}, nil
}

type prFake struct {
	mu    sync.Mutex
	calls []string
	draft string
}

func newPRDispatcher(t *testing.T, fake *prFake) (*Dispatcher, *Store, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		fake.calls = append(fake.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/pulls":
			if strings.Contains(string(b), `"draft":true`) {
				fake.draft = "true"
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"number":7,"html_url":"https://github.com/acme/widgets/pull/7"}`))
		case r.URL.Path == "/repos/acme/widgets/pulls/7":
			w.Write([]byte(`{"number":7,"node_id":"PR_7","state":"open","draft":true}`))
		case r.URL.Path == "/graphql":
			w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", srv.URL)

	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	const pid = "p1widgets0000001"
	if err := projects.ImportProject(t.Context(), project.Project{
		ID: pid, Name: "widgets", Path: filepath.Join(t.TempDir(), "widgets"),
		RepoURL: "https://github.com/acme/widgets", DefaultBranch: "main", WorkspaceID: testWS,
	}); err != nil {
		t.Fatal(err)
	}
	return NewDispatcher(store, nil, projects, fixedToken{}, nil), store, pid
}

func TestPRDraftThenReady(t *testing.T) {
	fake := &prFake{}
	d, store, pid := newPRDispatcher(t, fake)
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, Title: "widgets", ProjectIDs: []string{pid}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}

	tk = d.stagePullRequests(ctx, tk, types.StageInfo{ID: "in_progress", Label: "In progress", PRDraft: true})
	if fake.draft != "true" || tk.PRURLs[pid] != "https://github.com/acme/widgets/pull/7" {
		t.Fatalf("draft = %q, PRs %v", fake.draft, tk.PRURLs)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "draft") {
		t.Errorf("note = %q", c)
	}
	stored, _ := store.Get(ctx, tk.ID)
	if stored.PRURLs[pid] == "" {
		t.Fatal("the draft PR was not stored on the task")
	}

	// A second pr_draft stage does not open another one.
	before := len(fake.calls)
	d.stagePullRequests(ctx, stored, types.StageInfo{ID: "verification", PRDraft: true})
	if len(fake.calls) != before {
		t.Errorf("an existing PR was opened again: %v", fake.calls[before:])
	}

	d.stagePullRequests(ctx, stored, types.StageInfo{ID: "review", PRReady: true})
	if !strings.Contains(strings.Join(fake.calls, ","), "POST /graphql") {
		t.Errorf("calls = %v, want the GraphQL mutation", fake.calls)
	}
	if c := lastComment(t, store, tk.ID); !strings.Contains(c, "ready for review") {
		t.Errorf("note = %q", c)
	}
	if ev := eventsOf(t, store, tk.ID, EventPRReady); len(ev) != 1 {
		t.Errorf("pr_ready events = %v", ev)
	}

	// A subtask shares the root's PR and does nothing.
	child := &Task{WorkspaceID: testWS, Title: "child", ParentID: tk.ID, ProjectIDs: []string{pid}}
	if err := store.Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	before = len(fake.calls)
	d.stagePullRequests(ctx, child, types.StageInfo{ID: "in_progress", PRDraft: true})
	if len(fake.calls) != before {
		t.Errorf("a subtask called GitHub: %v", fake.calls[before:])
	}
}
