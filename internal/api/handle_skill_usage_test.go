package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestSkillGrantsListsStagesAndAuditorStages(t *testing.T) {
	t.Parallel()
	agents := []agentstore.Agent{
		{AgentType: types.AgentType{Name: "developer", SkillsByStage: map[string][]string{
			"review": {"notes"}, "in_progress": {"other", " notes"}, "done": {"other"},
		}}},
		{AgentType: types.AgentType{Name: "reviewer", Auditor: &types.AuditorSpec{
			SkillsByStage: map[string][]string{"review": {"notes"}},
		}}},
		{AgentType: types.AgentType{Name: "idle", SkillsByStage: map[string][]string{"review": {"other"}}}},
	}
	got := skillGrants(agents, "notes")
	want := []SkillGrant{
		{Agent: "developer", Stages: []string{"in_progress", "review"}},
		{Agent: "reviewer", AuditorStages: []string{"review"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skillGrants = %+v, want %+v", got, want)
	}
	if got := skillGrants(agents, "missing"); got == nil || len(got) != 0 {
		t.Fatalf("no grants must be an empty list, got %#v", got)
	}
}

func TestRevokeSkillDropsEveryGrant(t *testing.T) {
	t.Parallel()
	at := types.AgentType{
		SkillsByStage: map[string][]string{"review": {"notes", "other"}, "done": {"notes"}},
		Auditor:       &types.AuditorSpec{SkillsByStage: map[string][]string{"review": {"notes"}}},
	}
	if !revokeSkill(&at, "notes") {
		t.Fatal("revokeSkill reported no change")
	}
	if !reflect.DeepEqual(at.SkillsByStage, map[string][]string{"review": {"other"}, "done": {}}) {
		t.Fatalf("skills_by_stage = %#v", at.SkillsByStage)
	}
	if len(at.Auditor.SkillsByStage["review"]) != 0 {
		t.Fatalf("auditor kept the grant: %#v", at.Auditor.SkillsByStage)
	}
	if revokeSkill(&at, "notes") {
		t.Fatal("a second revoke reported a change")
	}
}

func skillLifecycleServer(t *testing.T) (*Server, *agentstore.Store) {
	t.Helper()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	for path, body := range map[string]string{
		"prompt.md":  "write release notes",
		"skill.yaml": "name: notes\n# what the model reads first\ndescription: release notes\ntype: knowledge\n",
	} {
		if _, err := store.PutFile(ctx, defaultWorkspaceID, agentstore.KindSkill, "notes", path, []byte(body), "test"); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	if _, err := store.CreateAgent(ctx, defaultWorkspaceID, agentstore.Agent{AgentType: types.AgentType{
		Name: "developer", ValidStages: []types.Stage{"review"},
		SkillsByStage: map[string][]string{"review": {"notes", "other"}},
	}}, "test"); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir()}
	srv.SetAgentStore(store)
	return srv, store
}

func skillRequest(method, target, body string) *http.Request {
	r := asDevOperator(httptest.NewRequest(method, target, strings.NewReader(body)))
	r.SetPathValue("name", "notes")
	return r
}

func TestSkillUsageNamesTheGrantingAgents(t *testing.T) {
	t.Parallel()
	srv, _ := skillLifecycleServer(t)
	w := httptest.NewRecorder()
	srv.handleSkillUsage(w, skillRequest(http.MethodGet, "/skills/notes/usage", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got []SkillGrant
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Agent != "developer" || !reflect.DeepEqual(got[0].Stages, []string{"review"}) {
		t.Fatalf("usage = %+v", got)
	}
}

func TestDeleteSkillWithRevokeTakesTheGrantsAndStaysRestorable(t *testing.T) {
	t.Parallel()
	srv, store := skillLifecycleServer(t)
	ctx := context.Background()

	w := httptest.NewRecorder()
	srv.handleDeleteSkill(w, skillRequest(http.MethodDelete, "/skills/notes?revoke=1", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revoked":1`) {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	a, err := store.GetAgent(ctx, defaultWorkspaceID, "developer")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if !reflect.DeepEqual(a.SkillsByStage["review"], []string{"other"}) {
		t.Fatalf("grant survived the revoke: %#v", a.SkillsByStage)
	}

	w = httptest.NewRecorder()
	srv.handleListDeletedSkills(w, skillRequest(http.MethodGet, "/skills/deleted", ""))
	if !strings.Contains(w.Body.String(), `"name":"notes"`) {
		t.Fatalf("deleted skills = %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.handleListSkillFiles(w, skillRequest(http.MethodGet, "/skills/notes/files?deleted=1", ""))
	var files []agentFileEntry
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil || len(files) != 2 || !files[0].Deleted {
		t.Fatalf("files of the deleted skill = %s (%v)", w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	srv.handleListSkillFiles(w, skillRequest(http.MethodGet, "/skills/notes/files", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("plain file list of a deleted skill = %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	srv.handleRestoreSkill(w, skillRequest(http.MethodPost, "/skills/notes/restore", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"description":"release notes"`) {
		t.Fatalf("restore = %d: %s", w.Code, w.Body.String())
	}
	if _, err := store.ReadFile(ctx, defaultWorkspaceID, agentstore.KindSkill, "notes", "prompt.md"); err != nil {
		t.Fatalf("prompt.md not restored: %v", err)
	}
	w = httptest.NewRecorder()
	srv.handleRestoreSkill(w, skillRequest(http.MethodPost, "/skills/notes/restore", ""))
	if w.Code != http.StatusConflict {
		t.Fatalf("restoring a live skill = %d, want 409", w.Code)
	}
}

func TestDeleteSkillWithoutRevokeKeepsTheGrants(t *testing.T) {
	t.Parallel()
	srv, store := skillLifecycleServer(t)
	w := httptest.NewRecorder()
	srv.handleDeleteSkill(w, skillRequest(http.MethodDelete, "/skills/notes", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	a, err := store.GetAgent(context.Background(), defaultWorkspaceID, "developer")
	if err != nil || !reflect.DeepEqual(a.SkillsByStage["review"], []string{"notes", "other"}) {
		t.Fatalf("grants after a plain delete = %#v, %v", a.SkillsByStage, err)
	}
}

func TestPatchSkillRewritesOnlyTheKeysItSets(t *testing.T) {
	t.Parallel()
	srv, store := skillLifecycleServer(t)
	w := httptest.NewRecorder()
	srv.handlePatchSkill(w, skillRequest(http.MethodPatch, "/skills/notes",
		`{"description":"Use when: writing\nrelease notes","enforcement":"always"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", w.Code, w.Body.String())
	}
	var def SkillDef
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if def.Description != "Use when: writing release notes" || def.Enforcement != "always" || def.Type != "knowledge" {
		t.Fatalf("skill after patch = %+v", def)
	}
	f, err := store.ReadFile(context.Background(), defaultWorkspaceID, agentstore.KindSkill, "notes", "skill.yaml")
	if err != nil {
		t.Fatalf("read skill.yaml: %v", err)
	}
	if !strings.Contains(string(f.Body), "# what the model reads first") {
		t.Fatalf("the comment was lost:\n%s", f.Body)
	}

	for _, body := range []string{`{"enforcement":"sometimes"}`, `{"description":"` + strings.Repeat("x", skillDescriptionMax+1) + `"}`} {
		w = httptest.NewRecorder()
		srv.handlePatchSkill(w, skillRequest(http.MethodPatch, "/skills/notes", body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("patch %.40s… = %d, want 400", body, w.Code)
		}
	}
}

func TestSetSkillYAML(t *testing.T) {
	t.Parallel()
	out, err := setSkillYAML(nil, "notes", map[string]string{"description": "a: b"})
	if err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if string(out) != "name: notes\ndescription: 'a: b'\n" {
		t.Fatalf("from an empty file:\n%s", out)
	}
	out, err = setSkillYAML([]byte("name: notes\nrequires: [x]\n"), "notes", map[string]string{"enforcement": ""})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !strings.Contains(string(out), "requires: [x]") || !strings.Contains(string(out), `enforcement: ""`) {
		t.Fatalf("appending a key:\n%s", out)
	}
	in := "name: notes\n# who needs what\nrequires:\n  - x\n  - y\nenforcement: \"\" # default\n"
	out, err = setSkillYAML([]byte(in), "notes", map[string]string{"enforcement": "always"})
	if err != nil {
		t.Fatalf("keep indent: %v", err)
	}
	if want := "name: notes\n# who needs what\nrequires:\n  - x\n  - y\nenforcement: always # default\n"; string(out) != want {
		t.Fatalf("a PATCH of enforcement rewrote the file:\n%s\nwant:\n%s", out, want)
	}
	if _, err := setSkillYAML([]byte("- a\n- b\n"), "notes", map[string]string{"description": "x"}); err == nil {
		t.Fatal("a list is not a skill.yaml")
	}
	if _, err := setSkillYAML([]byte("name: [unclosed\n"), "notes", nil); err == nil {
		t.Fatal("broken YAML must fail")
	}
}
