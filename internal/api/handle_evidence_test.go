package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const evidenceTake = `{"version":1,"title":"Login","base_url":"https://app.example.com","steps":[{"kind":"navigate","url":"/"}]}`

func evidenceServer(t *testing.T) (*Server, http.Handler, *evidence.Store) {
	t.Helper()
	srv, h := newIntegrationServer(t)
	store, err := evidence.NewStore(srv.taskStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetEvidence(store, nil)
	return srv, h, store
}

func keyRequest(t *testing.T, h http.Handler, method, path, key string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func TestEvidenceLibraryAndRunnerFlow(t *testing.T) {
	srv, h, store := evidenceServer(t)
	token, ws := registerUserInWorkspace(t, h, "qa@example.com", "QA", "")

	w := postJSONAuth(t, h, "/evidence/environments", token, map[string]any{"name": "Staging", "base_url": "https://staging.example.com"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create env: %d %s", w.Code, w.Body)
	}
	env := decode[evidence.Environment](t, w)
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/evidence/environments/"+env.ID+"/secrets", strings.NewReader(`{"TEST_PASSWORD":"s3cret!"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("secrets: %d %s", w.Code, w.Body)
	}

	w = postJSONAuth(t, h, "/evidence/cases", token, map[string]any{"title": "Login works", "take": json.RawMessage(evidenceTake)})
	if w.Code != http.StatusCreated {
		t.Fatalf("create case: %d %s", w.Code, w.Body)
	}
	c := decode[evidence.Case](t, w)
	if c.Key != "EV-1" || c.Status != evidence.CaseReady {
		t.Fatalf("case: %+v", c)
	}
	if w = postJSONAuth(t, h, "/evidence/cases", token, map[string]any{"title": "bad", "take": map[string]any{"version": 9}}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad take: %d %s", w.Code, w.Body)
	}

	other, _ := registerUserInWorkspace(t, h, "eve@example.com", "Eve", "")
	if w = getJSON(t, h, "/evidence/cases/EV-1", other); w.Code != http.StatusNotFound {
		t.Fatalf("other workspace read the case: %d", w.Code)
	}
	if w = getJSON(t, h, "/evidence/environments", other); strings.Contains(w.Body.String(), env.ID) {
		t.Fatal("other workspace listed the environment")
	}

	w = postJSONAuth(t, h, "/evidence/runs", token, map[string]any{"environment_id": env.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("queue: %d %s", w.Code, w.Body)
	}
	batch := decode[evidence.Batch](t, w)
	if ok, err := store.StartBatch(t.Context(), batch.ID, "test-runner"); !ok || err != nil {
		t.Fatalf("start: %v %v", ok, err)
	}

	runnerKey, _, err := srv.authStore.MintRunKey(t.Context(), auth.RunScope{Agent: evidence.RunnerAgentPrefix + batch.ID, Stage: evidence.RunnerStage}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	strangerKey, _, _ := srv.authStore.MintRunKey(t.Context(), auth.RunScope{Agent: evidence.RunnerAgentPrefix + "other", Stage: evidence.RunnerStage}, ws, time.Hour)
	if w = keyRequest(t, h, http.MethodPost, "/evidence/jobs/"+batch.ID+"/next", strangerKey, []byte(`{}`), "application/json"); w.Code != http.StatusForbidden {
		t.Fatalf("runner of another batch: %d", w.Code)
	}
	if w = postJSONAuth(t, h, "/evidence/jobs/"+batch.ID+"/next", token, map[string]any{}); w.Code != http.StatusForbidden {
		t.Fatalf("a person called the runner route: %d", w.Code)
	}

	w = keyRequest(t, h, http.MethodPost, "/evidence/jobs/"+batch.ID+"/next", runnerKey, []byte(`{}`), "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("next: %d %s", w.Code, w.Body)
	}
	job := decode[evidence.Job](t, w)
	if job.Run.CaseKey != "EV-1" || job.BaseURL != "https://staging.example.com" || job.Secrets["TEST_PASSWORD"] != "s3cret!" || len(job.Take) == 0 {
		t.Fatalf("job: %+v", job)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("kind", "video")
	fw, _ := mw.CreateFormFile("file", "take.mp4")
	_, _ = fw.Write([]byte("fake video bytes"))
	_ = mw.Close()
	w = keyRequest(t, h, http.MethodPost, "/evidence/jobs/"+batch.ID+"/runs/"+job.Run.ID+"/artifacts", runnerKey, buf.Bytes(), mw.FormDataContentType())
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	video := decode[map[string]string](t, w)["id"]

	result, _ := json.Marshal(evidence.RunResult{Status: evidence.RunPass, VideoID: video, DurationMS: 3000,
		Checks: []evidence.Check{{Label: "Dashboard shows", Pass: true, Measured: true}}})
	if w = keyRequest(t, h, http.MethodPost, "/evidence/jobs/"+batch.ID+"/runs/"+job.Run.ID+"/result", runnerKey, result, "application/json"); w.Code != http.StatusOK {
		t.Fatalf("result: %d %s", w.Code, w.Body)
	}
	if w = keyRequest(t, h, http.MethodPost, "/evidence/jobs/"+batch.ID+"/next", runnerKey, []byte(`{}`), "application/json"); w.Code != http.StatusNoContent {
		t.Fatalf("drained batch: %d %s", w.Code, w.Body)
	}

	w = getJSON(t, h, "/evidence/runs/"+job.Run.ID, token)
	run := decode[evidence.Run](t, w)
	if run.Status != evidence.RunPass || len(run.Artifacts) != 1 {
		t.Fatalf("run: %+v", run)
	}
	w = getJSON(t, h, "/evidence/artifacts/"+video, token)
	if w.Code != http.StatusOK || w.Body.String() != "fake video bytes" || w.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("artifact: %d %q", w.Code, w.Body)
	}
	if w = getJSON(t, h, "/evidence/artifacts/"+video, other); w.Code != http.StatusNotFound {
		t.Fatalf("other workspace read the video: %d", w.Code)
	}
	w = getJSON(t, h, "/evidence/cases?status=pass", token)
	list := decode[struct {
		Cases []evidence.Case `json:"cases"`
	}](t, w)
	if len(list.Cases) != 1 || list.Cases[0].Latest[env.ID].Status != evidence.RunPass {
		t.Fatalf("cases: %s", w.Body)
	}
	w = getJSON(t, h, "/evidence/cases?environment_id="+env.ID+"&status=pass&priority=low,medium,high,critical", token)
	onEnv := decode[struct {
		Cases  []evidence.Case `json:"cases"`
		Counts map[string]int  `json:"counts"`
		Tags   []string        `json:"tags"`
	}](t, w)
	if len(onEnv.Cases) != 1 || onEnv.Counts[evidence.RunPass] != 1 || onEnv.Tags == nil {
		t.Fatalf("cases on the environment: %s", w.Body)
	}
	if w = getJSON(t, h, "/evidence/cases?tag=nope,other", token); len(decode[struct {
		Cases []evidence.Case `json:"cases"`
	}](t, w).Cases) != 0 {
		t.Fatalf("tags nobody has: %s", w.Body)
	}

	stored, err := store.GetArtifact(t.Context(), ws, video)
	if err != nil {
		t.Fatal(err)
	}
	path := stored.Path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("artifact file: %v", err)
	}
	if w = deleteRequest(t, h, "/evidence/cases/EV-1", token); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the case's files outlived it: %v", err)
	}
}

func TestEvidenceRecordingRoutes(t *testing.T) {
	srv, h, store := evidenceServer(t)
	token, ws := registerUserInWorkspace(t, h, "qa@example.com", "QA", "")
	env := decode[evidence.Environment](t, postJSONAuth(t, h, "/evidence/environments", token,
		map[string]any{"name": "Staging", "base_url": "https://staging.example.com"}))
	pw := "s3cret!"
	if _, err := store.SetSecrets(t.Context(), ws, env.ID, map[string]*string{"TEST_PASSWORD": &pw}); err != nil {
		t.Fatal(err)
	}
	c := decode[evidence.Case](t, postJSONAuth(t, h, "/evidence/cases", token, map[string]any{"title": "Login works", "steps": "Log in"}))
	if _, err := store.CreateRecording(t.Context(), ws, c.ID, env.ID, "chat-rec", "qa@example.com"); err != nil {
		t.Fatal(err)
	}
	chatKey, _, err := srv.authStore.MintRunKey(t.Context(), auth.RunScope{ChatID: "chat-rec", Agent: "developer", Stage: "chat"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	idleKey, _, _ := srv.authStore.MintRunKey(t.Context(), auth.RunScope{ChatID: "chat-idle", Agent: "developer", Stage: "chat"}, ws, time.Hour)

	w := keyRequest(t, h, http.MethodGet, "/evidence/recording", chatKey, nil, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "TEST_PASSWORD") || strings.Contains(w.Body.String(), pw) {
		t.Fatalf("recording: %d %s", w.Code, w.Body)
	}
	if w = keyRequest(t, h, http.MethodGet, "/evidence/recording", idleKey, nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("chat without recording: %d", w.Code)
	}
	w = keyRequest(t, h, http.MethodGet, "/evidence/recording/secrets/TEST_PASSWORD", chatKey, nil, "")
	if w.Code != http.StatusOK || decode[map[string]string](t, w)["value"] != pw {
		t.Fatalf("secret: %d %s", w.Code, w.Body)
	}
	if w = keyRequest(t, h, http.MethodGet, "/evidence/recording/secrets/TEST_PASSWORD", idleKey, nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("idle chat read a secret: %d", w.Code)
	}

	body, _ := json.Marshal(map[string]json.RawMessage{"take": json.RawMessage(evidenceTake)})
	w = keyRequest(t, h, http.MethodPut, "/evidence/recording/take", chatKey, body, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("save take: %d %s", w.Code, w.Body)
	}
	saved := decode[struct {
		Case  evidence.Case  `json:"case"`
		Batch evidence.Batch `json:"batch"`
	}](t, w)
	if saved.Case.TakeSource != evidence.SourceAgent || saved.Batch.Trigger != evidence.TriggerRecording || saved.Batch.Total != 1 {
		t.Fatalf("saved: %+v", saved)
	}
	if w = keyRequest(t, h, http.MethodPut, "/evidence/recording/take", chatKey, body, "application/json"); w.Code != http.StatusNotFound {
		t.Fatalf("second save: %d", w.Code)
	}

	if !store.IsRecordingChat(t.Context(), "chat-rec") {
		if _, err := store.CreateRecording(t.Context(), ws, c.ID, env.ID, "chat-rec2", "qa@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	at := srv.evidenceAugment(t.Context(), "chat-rec2", &typesAgentForTest)
	if at == &typesAgentForTest || at.DefaultMCP("chat") != "octopus-api,thulu-browser" {
		t.Fatalf("augment: %q", at.DefaultMCP("chat"))
	}
	if typesAgentForTest.DefaultMCP("chat") != "octopus-api" {
		t.Fatal("augment changed the catalog's agent")
	}
	if srv.evidenceAugment(t.Context(), "chat-idle", &typesAgentForTest) != &typesAgentForTest {
		t.Fatal("a chat without a recording was augmented")
	}
}

var typesAgentForTest = types.AgentType{Name: "developer", MCPByStage: map[string][]string{"chat": {"octopus-api"}}}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Code
}

func TestEvidenceEnvironmentPermissionsByRole(t *testing.T) {
	srv, h, store := evidenceServer(t)
	token, ws := registerUserInWorkspace(t, h, "owner@example.com", "Owner", "")
	pid := createProject(t, h, token, "shop")
	viewer := mintKey(t, srv, ws, auth.RoleViewer, auth.PermAll)
	dev := mintKey(t, srv, ws, auth.RoleDeveloper, auth.PermAll)
	admin := mintKey(t, srv, ws, auth.RoleAdmin, auth.PermAll)

	w := keyDo(t, h, http.MethodPost, "/evidence/environments", dev, map[string]any{"project_id": pid, "name": "staging",
		"display_name": "Staging", "base_url": "https://staging.example.com"})
	if w.Code != http.StatusCreated {
		t.Fatalf("developer creates a project environment: %d %s", w.Code, w.Body)
	}
	staging := decode[evidence.Environment](t, w)
	if staging.Kind != evidence.KindProject || staging.ProjectName != "shop" || !staging.Can[evidence.ActionEdit].Allowed ||
		staging.Can[evidence.ActionDelete].Reason != evidence.ReasonDeleteOnProject {
		t.Fatalf("staging: %+v", staging)
	}
	if pe, err := srv.projectStore.GetEnvironment(t.Context(), pid, staging.ProjectEnvironmentID); err != nil ||
		pe.DisplayName != "Staging" || pe.URL != "https://staging.example.com" {
		t.Fatalf("project environment: %+v %v", pe, err)
	}
	if w = keyDo(t, h, http.MethodPost, "/evidence/environments", dev, map[string]any{"name": "Prod",
		"base_url": "https://example.com", "is_production": true}); w.Code != http.StatusForbidden ||
		errCode(t, w) != "api.evidence.errors.forbidden_production" {
		t.Fatalf("developer creates production: %d %s", w.Code, w.Body)
	}
	if w = keyDo(t, h, http.MethodPost, "/evidence/environments", viewer, map[string]any{"name": "Site",
		"base_url": "https://example.com"}); w.Code != http.StatusForbidden {
		t.Fatalf("viewer creates: %d", w.Code)
	}
	w = keyDo(t, h, http.MethodPost, "/evidence/environments", admin, map[string]any{"name": "Prod",
		"base_url": "https://example.com", "is_production": true})
	if w.Code != http.StatusCreated {
		t.Fatalf("admin creates production: %d %s", w.Code, w.Body)
	}
	prod := decode[evidence.Environment](t, w)

	if w = keyDo(t, h, http.MethodPatch, "/evidence/environments/"+staging.ID, dev, map[string]any{"display_name": "Staging EU"}); w.Code != http.StatusOK {
		t.Fatalf("developer edits staging: %d %s", w.Code, w.Body)
	}
	if pe, _ := srv.projectStore.GetEnvironment(t.Context(), pid, staging.ProjectEnvironmentID); pe.DisplayName != "Staging EU" {
		t.Fatalf("display name not on the project: %+v", pe)
	}
	if w = keyDo(t, h, http.MethodPatch, "/evidence/environments/"+staging.ID, dev, map[string]any{"name": "x"}); w.Code != http.StatusBadRequest ||
		errCode(t, w) != "evidence.errors.field_not_editable" {
		t.Fatalf("slug of a project row: %d %s", w.Code, w.Body)
	}
	cron := "0 7 * * *"
	if w = keyDo(t, h, http.MethodPatch, "/evidence/environments/"+prod.ID, dev, map[string]any{"schedule": cron}); w.Code != http.StatusForbidden {
		t.Fatalf("developer edits production: %d %s", w.Code, w.Body)
	}
	if w = keyDo(t, h, http.MethodPatch, "/evidence/environments/"+prod.ID, admin, map[string]any{"schedule": cron}); w.Code != http.StatusOK {
		t.Fatalf("admin edits production: %d %s", w.Code, w.Body)
	}
	site := decode[evidence.Environment](t, keyDo(t, h, http.MethodPost, "/evidence/environments", dev,
		map[string]any{"name": "Site", "base_url": "https://site.example.com"}))
	if w = keyDo(t, h, http.MethodPatch, "/evidence/environments/"+site.ID, dev, map[string]any{"is_production": true}); w.Code != http.StatusForbidden {
		t.Fatalf("developer marks production: %d %s", w.Code, w.Body)
	}

	if w = keyDo(t, h, http.MethodPut, "/evidence/environments/"+prod.ID+"/secrets", dev, map[string]string{"QA_PASSWORD": "p"}); w.Code != http.StatusForbidden {
		t.Fatalf("developer replaces production logins: %d", w.Code)
	}
	w = keyDo(t, h, http.MethodPut, "/evidence/environments/"+staging.ID+"/secrets", dev, map[string]string{"QA_PASSWORD": "hunter22"})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "hunter22") {
		t.Fatalf("developer sets staging logins: %d %s", w.Code, w.Body)
	}

	if w = keyDo(t, h, http.MethodDelete, "/evidence/environments/"+site.ID, dev, nil); w.Code != http.StatusForbidden {
		t.Fatalf("developer deletes: %d", w.Code)
	}
	if w = keyDo(t, h, http.MethodDelete, "/evidence/environments/"+staging.ID, admin, nil); w.Code != http.StatusConflict ||
		errCode(t, w) != "api.evidence.errors.forbidden_delete_on_project" {
		t.Fatalf("admin deletes a project row: %d %s", w.Code, w.Body)
	}
	if w = keyDo(t, h, http.MethodDelete, "/evidence/environments/"+site.ID, admin, nil); w.Code != http.StatusNoContent {
		t.Fatalf("admin deletes a site: %d %s", w.Code, w.Body)
	}

	list := decode[struct {
		Environments []evidence.Environment `json:"environments"`
	}](t, keyDo(t, h, http.MethodGet, "/evidence/environments", viewer, nil))
	var sandbox *evidence.Environment
	for i := range list.Environments {
		e := &list.Environments[i]
		if e.Can[evidence.ActionRun].Reason != evidence.ReasonViewer {
			t.Errorf("viewer may run on %s: %+v", e.Name, e.Can)
		}
		if e.Kind == evidence.KindSandbox {
			sandbox = e
		}
	}
	if len(list.Environments) != 3 || sandbox == nil || sandbox.Sandbox == nil || sandbox.Sandbox.InitScriptURL != "/project/?id="+pid+"#repo" {
		t.Fatalf("environments: %s", keyDo(t, h, http.MethodGet, "/evidence/environments", viewer, nil).Body)
	}
	ctxBody := decode[struct {
		Role     string                         `json:"role"`
		Can      map[string]evidence.Permission `json:"can"`
		Projects []struct {
			ID           string                `json:"id"`
			Environments int                   `json:"environments"`
			Sandbox      *evidence.SandboxInfo `json:"sandbox"`
		} `json:"projects"`
		Standalone int  `json:"standalone_environments"`
		Picker     bool `json:"show_project_picker"`
	}](t, keyDo(t, h, http.MethodGet, "/evidence/context", dev, nil))
	if ctxBody.Role != "developer" || ctxBody.Can[evidence.ActionCreateProductionEnvironment].Reason != evidence.ReasonProduction ||
		len(ctxBody.Projects) != 1 || ctxBody.Projects[0].Environments != 1 || ctxBody.Projects[0].Sandbox == nil ||
		ctxBody.Standalone != 1 || ctxBody.Picker {
		t.Fatalf("context: %+v", ctxBody)
	}

	if _, err := store.CreateCase(t.Context(), ws, "owner", evidence.Case{Title: "Shop", ProjectID: pid,
		Take: json.RawMessage(evidenceTake), TakeSource: evidence.SourceUpload}); err != nil {
		t.Fatal(err)
	}
	if !sandbox.Sandbox.Available {
		if w = keyDo(t, h, http.MethodPost, "/evidence/runs", dev, map[string]any{"environment_id": sandbox.ID}); w.Code != http.StatusPreconditionFailed {
			t.Fatalf("run on an unavailable sandbox: %d %s", w.Code, w.Body)
		}
	}
	if w = keyDo(t, h, http.MethodPost, "/evidence/runs", dev, map[string]any{"environment_id": staging.ID, "branch": "main"}); w.Code != http.StatusBadRequest {
		t.Fatalf("branch on a project environment: %d %s", w.Code, w.Body)
	}
	if w = keyDo(t, h, http.MethodPost, "/evidence/runs", viewer, map[string]any{"environment_id": staging.ID}); w.Code != http.StatusForbidden {
		t.Fatalf("viewer runs: %d", w.Code)
	}

	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/projects/"+pid+"/environments/"+staging.ProjectEnvironmentID, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("delete project environment: %d %s", w.Code, w.Body)
	}
	got, err := store.GetEnvironment(t.Context(), ws, staging.ID)
	if err != nil || got.Kind != evidence.KindStandalone || got.Name != "Staging EU (shop)" || len(got.SecretNames) != 1 {
		t.Fatalf("detached: %+v %v", got, err)
	}

	pe := decode[map[string]any](t, postJSONAuth(t, h, "/projects/"+pid+"/environments", token,
		map[string]any{"name": "staging", "url": "https://staging.example.com"}))
	peID, _ := pe["id"].(string)
	if w = keyDo(t, h, http.MethodPost, "/evidence/environments/"+got.ID+"/link", dev, map[string]string{"project_environment_id": peID}); w.Code != http.StatusForbidden {
		t.Fatalf("developer links: %d", w.Code)
	}
	w = keyDo(t, h, http.MethodPost, "/evidence/environments/"+got.ID+"/link", admin, map[string]string{"project_environment_id": peID})
	if w.Code != http.StatusOK {
		t.Fatalf("admin links: %d %s", w.Code, w.Body)
	}
	if linked := decode[evidence.Environment](t, w); linked.Kind != evidence.KindProject || linked.ID != got.ID {
		t.Fatalf("linked: %+v", linked)
	}
}

func TestPickRecordingAgent(t *testing.T) {
	for name, tc := range map[string]struct {
		agents []recordingAgent
		want   string
	}{
		"none":                 {nil, ""},
		"ready with browser":   {[]recordingAgent{{Type: "a", Ready: true}, {Type: "b", Ready: true, Browser: true}}, "b"},
		"browser without key":  {[]recordingAgent{{Type: "a", Ready: true}, {Type: "b", Browser: true}}, "a"},
		"nobody has a key":     {[]recordingAgent{{Type: "a"}, {Type: "b", Browser: true}}, "a"},
		"only the first agent": {[]recordingAgent{{Type: "a", Ready: true}}, "a"},
		"browser without prompt": {[]recordingAgent{
			{Type: "a", Ready: true, Browser: true},
			{Type: "b", Ready: true, PromptReady: true},
		}, "b"},
		"prompt and browser": {[]recordingAgent{
			{Type: "a", Ready: true, PromptReady: true},
			{Type: "b", Ready: true, PromptReady: true, Browser: true},
		}, "b"},
	} {
		if got := pickRecordingAgent(tc.agents); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestRecordEvidence_AgentWithoutChatPrompt(t *testing.T) {
	srv, h, _ := evidenceServer(t)
	token, _ := registerUserInWorkspace(t, h, "rec-noprompt@example.com", "RN", "")
	name := setupChatAgent(t, srv, "mute")
	if err := os.Remove(filepath.Join(srv.agentsDir, "agents", name, "prompts", "main.md")); err != nil {
		t.Fatal(err)
	}
	w := postJSONAuth(t, h, "/llm-profiles", token, map[string]any{
		"name": "team", "engine": "claude-code", "key": "sk-ant-api-rec-1", "is_default": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create llm profile: %d %s", w.Code, w.Body)
	}
	w = postJSONAuth(t, h, "/evidence/environments", token, map[string]any{"name": "Staging", "base_url": "https://staging.example.com"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create env: %d %s", w.Code, w.Body)
	}
	env := decode[evidence.Environment](t, w)
	if w = postJSONAuth(t, h, "/evidence/cases", token, map[string]any{"title": "Logout"}); w.Code != http.StatusCreated {
		t.Fatalf("create case: %d %s", w.Code, w.Body)
	}

	agents := decode[struct {
		Agents []recordingAgent `json:"agents"`
	}](t, getJSON(t, h, "/evidence/recording-agents", token))
	if len(agents.Agents) != 1 || !agents.Agents[0].Ready || agents.Agents[0].PromptReady {
		t.Fatalf("recording agents = %+v, want mute ready but without a chat prompt", agents.Agents)
	}

	w = postJSONAuth(t, h, "/evidence/cases/EV-1/record", token, map[string]any{"environment_id": env.ID, "agent_type": name})
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "prompts/main.md") {
		t.Fatalf("record with no chat prompt = %d %s, want 412 naming the prompt", w.Code, w.Body)
	}
	if body := getJSON(t, h, "/chats?include_archived=true", token).Body.String(); strings.Contains(body, `"agent_type":"mute"`) {
		t.Errorf("a refused recording left a chat behind: %s", body)
	}
}
