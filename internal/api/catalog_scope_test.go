package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/settings"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func tenantRequest(method, path, wid string) *http.Request {
	return tenantRequestBody(method, path, wid, "body")
}

func tenantRequestBody(method, path, wid, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	return r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{
		UserID: "u-" + wid, Email: "admin@" + wid + ".test",
		WorkspaceID: wid, Role: auth.RoleAdmin,
	}))
}

func revisionFixture(t *testing.T, store *agentstore.Store, tenant string) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	ids := map[string]int64{}
	for _, f := range []struct{ kind, owner, path, body string }{
		{agentstore.KindAgent, "developer", "prompts/main.md", "developer prompt"},
		{agentstore.KindAgent, "auditor", "prompts/main.md", "auditor prompt"},
		{agentstore.KindSkill, "worktree-protocol", "prompt.md", "never commit to main"},
	} {
		if _, err := store.PutFile(ctx, tenant,
			f.kind, f.owner, f.path, []byte(f.body), "test"); err != nil {
			t.Fatalf("put %s/%s: %v", f.kind, f.owner, err)
		}
		revs, err := store.ListRevisions(ctx, tenant, f.kind, f.owner, "", 1)
		if err != nil || len(revs) == 0 {
			t.Fatalf("list revisions %s/%s: %v (%d rows)", f.kind, f.owner, err, len(revs))
		}
		ids[f.kind+"/"+f.owner] = revs[0].ID
	}
	return ids
}

func TestAgentRevisionBodyStaysInsideTheNamedAgent(t *testing.T) {
	t.Parallel()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ids := revisionFixture(t, store, defaultWorkspaceID)
	theirs := revisionFixture(t, store, "ws-other")
	srv := &Server{agentsDir: t.TempDir()}
	srv.SetAgentStore(store)

	body := func(agentName string, id int64) *httptest.ResponseRecorder {
		r := asDevOperator(httptest.NewRequest("GET", "/types/x/revisions/1/body", nil))
		r.SetPathValue("name", agentName)
		r.SetPathValue("id", strconv.FormatInt(id, 10))
		w := httptest.NewRecorder()
		srv.handleAgentRevisionBody(w, r)
		return w
	}

	own := body("developer", ids[agentstore.KindAgent+"/developer"])
	if own.Code != http.StatusOK || own.Body.String() != "developer prompt" {
		t.Fatalf("own revision = %d %q, want 200 with the prompt", own.Code, own.Body.String())
	}
	for _, tc := range []struct {
		name string
		id   int64
	}{
		{"another agent's prompt", ids[agentstore.KindAgent+"/auditor"]},
		{"a skill body", ids[agentstore.KindSkill+"/worktree-protocol"]},
		{"another workspace's prompt", theirs[agentstore.KindAgent+"/developer"]},
	} {
		w := body("developer", tc.id)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s via /types/developer/revisions = %d, want 404: %s", tc.name, w.Code, w.Body.String())
		}
	}
	if w := body("developer", 999999); w.Code != http.StatusNotFound {
		t.Errorf("unknown revision id = %d, want 404", w.Code)
	}
}

func TestRestoreRevisionStaysInsideTheNamedAgent(t *testing.T) {
	t.Parallel()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ids := revisionFixture(t, store, defaultWorkspaceID)
	srv := &Server{agentsDir: t.TempDir()}
	srv.SetAgentStore(store)

	r := asDevOperator(httptest.NewRequest("POST", "/types/developer/revisions/1/restore", nil))
	r.SetPathValue("name", "developer")
	r.SetPathValue("id", strconv.FormatInt(ids[agentstore.KindSkill+"/worktree-protocol"], 10))
	w := httptest.NewRecorder()
	srv.handleRestoreAgentRevision(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("restoring a skill revision through an agent path = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestCatalogIsPerWorkspace(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")

	do := func(h func(http.ResponseWriter, *http.Request), method, wid, body string, path map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := tenantRequestBody(method, "/types", wid, body)
		for k, v := range path {
			r.SetPathValue(k, v)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	dev := map[string]string{"name": "developer", "path": "prompts/main.md"}

	if w := do(srv.handleCreateAgent, http.MethodPost, "ws-a", `{"name":"developer"}`, nil); w.Code != http.StatusCreated {
		t.Fatalf("create in ws-a = %d: %s", w.Code, w.Body.String())
	}
	if w := do(srv.handleWriteAgentFile, http.MethodPut, "ws-a", "a's prompt", dev); w.Code != http.StatusOK {
		t.Fatalf("write prompt in ws-a = %d: %s", w.Code, w.Body.String())
	}
	settingsBody, _ := json.Marshal(AgentSettings{
		Name: "developer", ValidStages: []string{"in_progress"},
		MCPByStage: map[string][]string{"in_progress": {"thulu-github@write"}},
	})
	if w := do(srv.handlePutAgentSettings, http.MethodPut, "ws-a", string(settingsBody), dev); w.Code != http.StatusOK {
		t.Fatalf("settings in ws-a = %d: %s", w.Code, w.Body.String())
	}

	w := do(srv.handleListTypes, http.MethodGet, "ws-a", "", nil)
	if !strings.Contains(w.Body.String(), "thulu-github@write") {
		t.Fatalf("ws-a's GET /types lost the definition: %s", w.Body.String())
	}
	if w := do(srv.handleReadAgentFile, http.MethodGet, "ws-a", "", dev); w.Body.String() != "a's prompt" {
		t.Fatalf("ws-a reads %q", w.Body.String())
	}

	w = do(srv.handleListTypes, http.MethodGet, "ws-b", "", nil)
	var listed []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed) != 0 {
		t.Fatalf("ws-b's GET /types = %s (%v)", w.Body.String(), err)
	}
	for route, h := range map[string]func(http.ResponseWriter, *http.Request){
		"GET /types/{name}/files/{path...}": srv.handleReadAgentFile,
		"GET /types/{name}/settings":        srv.handleGetAgentSettings,
		"GET /types/{name}/prompts":         srv.handleListAgentPrompts,
		"PUT /types/{name}/files/{path...}": srv.handleWriteAgentFile,
		"DELETE /types/{name}":              srv.handleDeleteAgent,
	} {
		w := do(h, http.MethodGet, "ws-b", "b's prompt", dev)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s from ws-b = %d, want 404: %s", route, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "a's prompt") || strings.Contains(w.Body.String(), "thulu-github") {
			t.Errorf("%s leaked ws-a's catalog to ws-b: %s", route, w.Body.String())
		}
	}
	if f, _ := store.ReadFile(context.Background(), "ws-a", agentstore.KindAgent, "developer", "prompts/main.md"); string(f.Body) != "a's prompt" {
		t.Fatalf("ws-b changed ws-a's prompt to %q", f.Body)
	}

	if at := types.FindAgent(srv.agents("ws-b"), "developer"); at != nil {
		t.Fatal("a ws-b run resolved ws-a's agent")
	}
	if at := types.FindAgent(srv.agents("ws-a"), "developer"); at == nil {
		t.Fatal("a ws-a run did not resolve its own agent")
	}
}

func TestSystemSkillsAreTheOperatorsToWrite(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir()}
	srv.SetAgentStore(store)
	const opWS = "ws-operator"
	t.Setenv(OperatorWorkspaceEnv, opWS)

	files, _ := json.Marshal(map[string]any{"files": map[string]string{
		"prompt.md": "rules", "skill.yaml": "name: octopus-gh\nmcp: thulu-github\n",
	}})
	put := func(wid string) *httptest.ResponseRecorder {
		r := tenantRequestBody(http.MethodPut, "/system/skills/octopus-gh", wid, string(files))
		r.SetPathValue("name", "octopus-gh")
		w := httptest.NewRecorder()
		srv.handlePutSystemSkill(w, r)
		return w
	}
	w := put("ws-tenant")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), OperatorWorkspaceEnv) {
		t.Fatalf("tenant admin PUT /system/skills = %d: %s", w.Code, w.Body.String())
	}
	if w := put(opWS); w.Code != http.StatusOK {
		t.Fatalf("operator PUT /system/skills = %d: %s", w.Code, w.Body.String())
	}

	for _, name := range []string{"octopus-gh", "octopus-future"} {
		r := tenantRequestBody(http.MethodPut, "/skills/"+name+"/files/prompt.md", "ws-tenant", "mine")
		r.SetPathValue("name", name)
		r.SetPathValue("path", "prompt.md")
		w := httptest.NewRecorder()
		srv.handleWriteSkillFile(w, r)
		if w.Code != http.StatusConflict {
			t.Errorf("workspace skill named %s = %d, want 409: %s", name, w.Code, w.Body.String())
		}
	}

	r := tenantRequestBody(http.MethodPost, "/skills", "ws-tenant", `{"name":"notes","description":"team notes"}`)
	w = httptest.NewRecorder()
	srv.handleCreateSkill(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create workspace skill = %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.handleListSkills(w, tenantRequest(http.MethodGet, "/skills", "ws-tenant"))
	var listed []SkillDef
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&listed); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(listed) != 2 || listed[0].Name != "notes" || listed[0].System || !listed[1].System {
		t.Fatalf("GET /skills = %+v", listed)
	}
	w = httptest.NewRecorder()
	srv.handleListSkills(w, tenantRequest(http.MethodGet, "/skills", "ws-other"))
	if strings.Contains(w.Body.String(), `"notes"`) {
		t.Fatalf("another workspace lists ws-tenant's skill: %s", w.Body.String())
	}
}

func TestCatalogWriteAllowsAWorkspaceAdminKey(t *testing.T) {
	t.Setenv(OperatorWorkspaceEnv, "")
	srv, h := newIntegrationServer(t)
	rawKey, _, err := srv.authStore.CreateAPIKey(t.Context(), "admin", "test", auth.KeyGrant{
		WorkspaceID: "ws-tenant",
		Role:        auth.RoleOwner,
		Permissions: []auth.Permission{auth.PermAll},
	})
	if err != nil {
		t.Fatalf("mint key: %v", err)
	}

	r := httptest.NewRequest(http.MethodPut, "/types/developer/files/prompts/main.md",
		strings.NewReader("you are a developer"))
	r.Header.Set("X-API-Key", rawKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatalf("a workspace admin key was refused its own catalog: %s", w.Body.String())
	}
}

func TestPutSettingRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	store, err := settings.NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	srv := &Server{settingsStore: store}

	put := func(key, value string) *httptest.ResponseRecorder {
		r := asDevOperator(httptest.NewRequest(http.MethodPut, "/settings/"+key, strings.NewReader(value)))
		r.SetPathValue("key", key)
		w := httptest.NewRecorder()
		srv.handlePutSetting(w, r)
		return w
	}

	if w := put(settings.SandboxMaxKey, "4"); w.Code != http.StatusNoContent {
		t.Fatalf("PUT a well-known key = %d, want 204: %s", w.Code, w.Body.String())
	}
	for _, key := range []string{"attacker.note", "sandbox.maks", "chat.max_container"} {
		w := put(key, `"whatever"`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT /settings/%s = %d, want 400: %s", key, w.Code, w.Body.String())
		}
		if _, err := store.GetRaw(t.Context(), key); err == nil {
			t.Errorf("PUT /settings/%s stored a row anyway", key)
		}
	}
}

func TestPutSettingRejectsAnIngestURLThatWouldFailSilently(t *testing.T) {
	t.Parallel()
	store, err := settings.NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	srv := &Server{settingsStore: store}

	put := func(value string) *httptest.ResponseRecorder {
		r := asDevOperator(httptest.NewRequest(http.MethodPut,
			"/settings/"+settings.IngestPublicURLKey, strings.NewReader(value)))
		r.SetPathValue("key", settings.IngestPublicURLKey)
		w := httptest.NewRecorder()
		srv.handlePutSetting(w, r)
		return w
	}

	if w := put(`"https://thulu.example.com"`); w.Code != http.StatusNoContent {
		t.Fatalf("PUT a good URL = %d, want 204: %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`"10.0.0.5:8585"`, `"thulu.example.com"`, `8585`} {
		if w := put(bad); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400: %s", bad, w.Code, w.Body.String())
		}
	}
	if got, _ := store.IngestPublicURL(t.Context()); got != "https://thulu.example.com" {
		t.Errorf("IngestPublicURL = %q after refused writes, want the earlier value", got)
	}
}
