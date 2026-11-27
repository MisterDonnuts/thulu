package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/workshop"
)

func newFlowServer(t *testing.T) (srv *Server, h http.Handler, token string) {
	t.Helper()
	srv, h = newIntegrationServer(t)
	store, err := agentstore.New(srv.stageAdmin.DB())
	if err != nil {
		t.Fatalf("agent store: %v", err)
	}
	srv.SetAgentStore(store)
	token, _ = registerUserInWorkspace(t, h, "flow@example.com", "Flow", "")
	return srv, h, token
}

func decodeAs[T any](t *testing.T, w *httptest.ResponseRecorder, what string) T {
	t.Helper()
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("%s = %d: %s", what, w.Code, w.Body.String())
	}
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v (%s)", what, err, w.Body.String())
	}
	return out
}

func applyThulu(t *testing.T, h http.Handler, token string) workshopApplyResponse {
	t.Helper()
	w := postJSONAuth(t, h, "/workshop/apply", token, map[string]any{
		"items": []map[string]string{{"kind": workshop.KindFlow, "name": "thulu"}},
	})
	return decodeAs[workshopApplyResponse](t, w, "POST /workshop/apply")
}

func columnIDs(st []types.StageInfo) []types.Stage {
	out := make([]types.Stage, len(st))
	for i := range st {
		out[i] = st[i].ID
	}
	return out
}

type flowTask struct {
	ID               string                 `json:"id"`
	Stage            types.Stage            `json:"stage"`
	BoardID          string                 `json:"board_id"`
	TypeID           string                 `json:"type_id"`
	StageAssignments map[types.Stage]string `json:"stage_assignments"`
}

func createFlowTask(t *testing.T, h http.Handler, token, board, typeID string, assigns map[string]string) flowTask {
	t.Helper()
	body := map[string]any{"title": typeID + " task", "board_id": board, "type_id": typeID}
	if assigns != nil {
		body["stage_assignments"] = assigns
	}
	return decodeAs[flowTask](t, postJSONAuth(t, h, "/tasks", token, body), "POST /tasks "+typeID)
}

// The thulu flow on an empty workspace: one apply leaves an eleven-column board, its agents and its
// task types; tasks get their agents by type; a second apply changes nothing.
func TestIntegration_ThuluFlowAppliesToAnEmptyWorkspace(t *testing.T) {
	t.Parallel()
	_, h, token := newFlowServer(t)

	res := applyThulu(t, h, token)
	if res.Plan.Blocked || !slices.Contains(res.Result.Installed, workshop.Ref{Kind: workshop.KindFlow, Name: "thulu"}) {
		t.Fatalf("apply: blocked=%v installed=%+v notices=%+v", res.Plan.Blocked, res.Result.Installed, res.Plan.Notices)
	}

	boards := decodeBoards(t, getJSON(t, h, "/boards", token))
	var board stagepkg.BoardInfo
	for _, b := range boards.Boards {
		if b.Flow != nil && b.Flow.Name == "thulu" {
			board = b
		}
	}
	if board.ID != "thulu" || board.MaxKickBacks != 2 || board.Flow.Revision != 5 {
		t.Fatalf("flow board = %+v (all: %+v)", board, boards.Boards)
	}

	stages := decodeAs[[]types.StageInfo](t, getJSON(t, h, "/boards/thulu/stages", token), "stages")
	want := []types.Stage{"backlog", "refinement", "test_design", "in_progress", "verification",
		"ai_review", "testing", "readiness", "review", "release", "done"}
	if got := columnIDs(stages); !slices.Equal(got, want) {
		t.Fatalf("columns = %v", got)
	}
	if stages[3].DefaultAgent != "thulu-developer" || stages[7].DefaultAgent != "thulu-readiness" || stages[9].DefaultAgent != "" {
		t.Errorf("in_progress agent %q, readiness agent %q, release agent %q",
			stages[3].DefaultAgent, stages[7].DefaultAgent, stages[9].DefaultAgent)
	}
	if !stages[5].NoSkip || !stages[8].NoSkip || stages[2].NoSkip {
		t.Errorf("no_skip: ai_review %v, review %v, test_design %v; the installed board keeps the flow's never-skipped reviews",
			stages[5].NoSkip, stages[8].NoSkip, stages[2].NoSkip)
	}

	agents := decodeAs[[]types.AgentType](t, getJSON(t, h, "/types", token), "GET /types")
	names := make([]string, len(agents))
	for i := range agents {
		names[i] = agents[i].Name
	}
	for _, name := range []string{"thulu-refiner", "thulu-test-designer", "thulu-developer", "thulu-verifier",
		"thulu-reviewer", "thulu-agentic-tester", "thulu-readiness", "thulu-release"} {
		if !slices.Contains(names, name) {
			t.Errorf("agent %s not installed (have %v)", name, names)
		}
	}

	chore := decodeAs[[]types.StageInfo](t, getJSON(t, h, "/boards/thulu/stages?type=chore", token), "chore stages")
	if ids := columnIDs(chore); slices.Contains(ids, "test_design") || slices.Contains(ids, "testing") || len(ids) != 9 {
		t.Errorf("chore columns = %v", ids)
	}
	release := decodeAs[[]types.StageInfo](t, getJSON(t, h, "/boards/thulu/stages?type=release", token), "release stages")
	if ids := columnIDs(release); !slices.Equal(ids, []types.Stage{"backlog", "release", "done"}) {
		t.Errorf("release columns = %v", ids)
	}

	choreTask := createFlowTask(t, h, token, "thulu", "chore", nil)
	if _, ok := choreTask.StageAssignments["test_design"]; ok || choreTask.StageAssignments["in_progress"] != "thulu-developer" {
		t.Errorf("chore assignments = %v", choreTask.StageAssignments)
	}
	releaseTask := createFlowTask(t, h, token, "thulu", "release", nil)
	if releaseTask.Stage != "backlog" || releaseTask.StageAssignments["release"] != "thulu-release" ||
		len(releaseTask.StageAssignments) != 1 {
		t.Errorf("release task = %+v", releaseTask)
	}
	feature := createFlowTask(t, h, token, "thulu", "feature", nil)
	if _, ok := feature.StageAssignments["release"]; ok || feature.StageAssignments["refinement"] != "thulu-refiner" {
		t.Errorf("feature assignments = %v", feature.StageAssignments)
	}

	again := applyThulu(t, h, token)
	if again.Plan.Writes != 0 {
		t.Errorf("reapply writes %d", again.Plan.Writes)
	}
	for _, it := range again.Plan.Items {
		if it.Action != workshop.ActionSame {
			t.Errorf("reapply: %s/%s is %s", it.Kind, it.Name, it.Action)
		}
	}
	r := again.Result
	if len(r.Installed)+len(r.Updated)+len(r.Replaced)+len(r.Copied) != 0 {
		t.Errorf("reapply wrote %+v", r)
	}
	if n := len(decodeBoards(t, getJSON(t, h, "/boards", token)).Boards); n != len(boards.Boards) {
		t.Errorf("boards after reapply = %d, want %d", n, len(boards.Boards))
	}
}

// The setup's minimal start: thulu init applies the thulu-minimal flow with flow_default. It
// installs as a board of its own and becomes the default; the board the workspace already had, and
// the task on it, stay as they were; a new task lands on the flow's board with thulu-agent in In
// progress.
func TestIntegration_MinimalFlowBecomesTheDefaultBoard(t *testing.T) {
	t.Parallel()
	_, h, token := newFlowServer(t)
	seeded := decodeBoards(t, getJSON(t, h, "/boards", token)).Default
	old := decodeAs[flowTask](t, postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "already here"}), "POST /tasks")
	oldColumns := decodeAs[[]types.StageInfo](t, getJSON(t, h, "/boards/"+old.BoardID+"/stages", token), "seeded stages")

	w := postJSONAuth(t, h, "/workshop/apply", token, map[string]any{
		"items":        []map[string]string{{"kind": workshop.KindFlow, "name": "thulu-minimal"}},
		"flow_default": true,
	})
	res := decodeAs[workshopApplyResponse](t, w, "POST /workshop/apply")
	if res.Plan.Blocked || !slices.Contains(res.Result.Installed, workshop.Ref{Kind: workshop.KindFlow, Name: "thulu_minimal"}) {
		t.Fatalf("apply: blocked=%v installed=%+v notices=%+v", res.Plan.Blocked, res.Result.Installed, res.Plan.Notices)
	}

	boards := decodeBoards(t, getJSON(t, h, "/boards", token))
	if boards.Default != "thulu_minimal" {
		t.Errorf("default board = %q, want thulu_minimal (was %q)", boards.Default, seeded)
	}
	stages := decodeAs[[]types.StageInfo](t, getJSON(t, h, "/boards/thulu_minimal/stages", token), "stages")
	if got := columnIDs(stages); !slices.Equal(got, []types.Stage{"backlog", "in_progress", "review", "done"}) {
		t.Fatalf("columns = %v", got)
	}
	if stages[1].DefaultAgent != "thulu-agent" || stages[2].DefaultAgent != "" {
		t.Errorf("in_progress agent %q, review agent %q", stages[1].DefaultAgent, stages[2].DefaultAgent)
	}
	again := decodeAs[[]types.StageInfo](t, getJSON(t, h, "/boards/"+old.BoardID+"/stages", token), "seeded stages")
	if !slices.EqualFunc(again, oldColumns, func(a, b types.StageInfo) bool {
		return a.ID == b.ID && a.DefaultAgent == b.DefaultAgent
	}) {
		t.Errorf("the board the workspace had changed: %v -> %v", columnIDs(oldColumns), columnIDs(again))
	}
	if moved := decodeAs[flowTask](t, getJSON(t, h, "/tasks/"+old.ID, token), "GET /tasks"); moved.BoardID != old.BoardID {
		t.Errorf("the existing task moved from %s to %s", old.BoardID, moved.BoardID)
	}

	fresh := decodeAs[flowTask](t, postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "new"}), "POST /tasks")
	if fresh.BoardID != "thulu_minimal" || fresh.StageAssignments["in_progress"] != "thulu-agent" {
		t.Errorf("new task: board %q, assignments %v", fresh.BoardID, fresh.StageAssignments)
	}
}

// Precedence column < type < request, a retype that follows the new type, and the checks on the
// agents a column or a type names.
func TestIntegration_FlowAssignmentsPrecedenceAndChecks(t *testing.T) {
	t.Parallel()
	_, h, token := newFlowServer(t)
	applyThulu(t, h, token)

	explicit := createFlowTask(t, h, token, "thulu", "release", map[string]string{"release": "", "in_progress": "thulu-developer"})
	if explicit.StageAssignments["release"] != "" {
		t.Errorf("the request lost to the type: %v", explicit.StageAssignments)
	}

	feature := createFlowTask(t, h, token, "thulu", "feature", nil)
	w := patchJSONAuth(t, h, "/tasks/"+feature.ID, token, map[string]any{"type_id": "release"})
	retyped := decodeAs[flowTask](t, w, "PATCH /tasks retype")
	if retyped.StageAssignments["release"] != "thulu-release" {
		t.Errorf("retyped to release, assignments = %v", retyped.StageAssignments)
	}

	boardTypes := decodeAs[struct {
		TaskTypes []stagepkg.TaskTypeInfo `json:"task_types"`
	}](t, getJSON(t, h, "/boards/thulu/types", token), "types").TaskTypes
	for _, c := range []struct{ agent, code string }{
		{"ghost", "api.boards.errors.agent_unknown"},
		{"thulu-developer", "api.boards.errors.agent_wrong_stage"},
		{"thulu-release", ""},
	} {
		bad := slices.Clone(boardTypes)
		for i := range bad {
			if bad[i].ID == "bug" {
				bad[i].Agents = map[types.Stage]string{"release": c.agent}
			}
		}
		w := putJSON(t, h, "/boards/thulu/types", map[string]any{"types": bad}, token)
		switch {
		case c.code == "" && w.Code != http.StatusOK:
			t.Errorf("type agent %s = %d: %s", c.agent, w.Code, w.Body.String())
		case c.code != "" && (w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.code)):
			t.Errorf("type agent %s = %d %s, want 400 %s", c.agent, w.Code, w.Body.String(), c.code)
		}
	}
	if task := createFlowTask(t, h, token, "thulu", "bug", nil); task.StageAssignments["release"] != "thulu-release" {
		t.Errorf("the bug type's agent did not reach a bug: %v", task.StageAssignments)
	}

	if w := patchJSONAuth(t, h, "/stages/release?board=thulu", token, map[string]any{"default_agent": "thulu-developer"}); w.Code != http.StatusBadRequest {
		t.Errorf("column agent outside its stages = %d: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, h, "/stages/release?board=thulu", token, map[string]any{"default_agent": "thulu-release"}); w.Code != http.StatusOK {
		t.Errorf("valid column agent = %d: %s", w.Code, w.Body.String())
	}
	if task := createFlowTask(t, h, token, "thulu", "feature", nil); task.StageAssignments["release"] != "thulu-release" {
		t.Errorf("the column default did not reach a feature: %v", task.StageAssignments)
	}
}

type assignBoards struct {
	stagepkg.Boards
	reg   stagepkg.Registry
	types []stagepkg.TaskTypeInfo
}

func (b assignBoards) Board(string) stagepkg.Registry { return b.reg }
func (b assignBoards) BoardFor(_, typeID string) stagepkg.Registry {
	for i := range b.types {
		if b.types[i].ID == typeID {
			return stagepkg.NewSnapshot(stagepkg.FilterStages(b.reg.List(), b.types[i].Skips))
		}
	}
	return b.reg
}
func (b assignBoards) BoardTypes(string) []stagepkg.TaskTypeInfo { return b.types }

func TestResolveAssignmentsPrecedence(t *testing.T) {
	b := assignBoards{
		reg: stagepkg.NewSnapshot([]types.StageInfo{
			{ID: "backlog", Label: "Backlog", Position: 10, Initial: true},
			{ID: "dev", Label: "Dev", Position: 20, DefaultAgent: "coder"},
			{ID: "qa", Label: "QA", Position: 30, DefaultAgent: "tester"},
			{ID: "ship", Label: "Ship", Position: 40},
			{ID: "done", Label: "Done", Position: 50, Terminal: true},
		}),
		types: []stagepkg.TaskTypeInfo{
			{ID: "feature", Label: "Feature", Default: true, Agents: map[types.Stage]string{"ship": ""}},
			{ID: "release", Label: "Release", Skips: []types.Stage{"dev", "qa"}, Agents: map[types.Stage]string{"ship": "shipper"}},
		},
	}
	templates := map[types.Stage]string{"dev": "project-coder", "ship": "project-shipper", "qa": ""}
	cases := []struct {
		name    string
		typeID  string
		request map[types.Stage]string
		want    map[types.Stage]string
	}{
		{"template beats the column, an empty template keeps it, the type clears ship", "feature", nil,
			map[types.Stage]string{"dev": "project-coder", "qa": "tester"}},
		{"the type beats the template", "release", nil,
			map[types.Stage]string{"dev": "project-coder", "ship": "shipper"}},
		{"the request beats the type", "release", map[types.Stage]string{"ship": "", "qa": "me"},
			map[types.Stage]string{"dev": "project-coder", "ship": "", "qa": "me"}},
	}
	for _, c := range cases {
		got := resolveAssignments(b, "board", c.typeID, templates, c.request)
		if !maps.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// thulu-sandbox runs inside the daemon, so no container or registration lists it: the plan must
// still count it as installed when the daemon has a sandbox manager, and as missing without one.
func TestIntegration_WorkshopEnvKnowsTheDaemonServedSandbox(t *testing.T) {
	srv, _, _ := newFlowServer(t)
	env, err := srv.workshopEnv(t.Context())
	if err != nil {
		t.Fatalf("workshopEnv: %v", err)
	}
	if env.KnownMCP[mcp.SandboxServer] {
		t.Fatalf("without a sandbox manager %s counts as installed", mcp.SandboxServer)
	}
	withSandboxManager(t, srv)
	env, err = srv.workshopEnv(t.Context())
	if err != nil {
		t.Fatalf("workshopEnv: %v", err)
	}
	if !env.KnownMCP[mcp.SandboxServer] {
		t.Fatalf("with a sandbox manager %s counts as missing: %v", mcp.SandboxServer, env.KnownMCP)
	}
}
