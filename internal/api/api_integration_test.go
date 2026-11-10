package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/schema/schematest"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/webhook"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func integrationServerOf(t *testing.T, h http.Handler) *Server {
	t.Helper()
	b, ok := h.(*devBrowser)
	if !ok || b.srv == nil {
		t.Fatalf("no integration server behind handler %T: pass the handler newIntegrationServer returned", h)
	}
	return b.srv
}

// Tests that touch process-wide state (t.Setenv, package vars, Docker, wall-clock waits) must stay
// serial.
// Handlers read os.Getenv per request, so a test that sets env stays serial even with its own
// server.
func newIntegrationServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()

	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("newIntegrationServer: %s: %v", what, err)
		}
	}

	db := schematest.NewBootedDB(t, stagetest.Flow())

	usageStore, err := usage.NewStore(db, dir)
	must(err, "usage")

	projStore, err := project.NewStore(db, filepath.Join(dir, "projects"))
	must(err, "project")

	taskStore, err := task.NewStore(db, filepath.Join(dir, "attachments"))
	must(err, "task")

	settStore, err := settings.NewStore(db)
	must(err, "settings")

	sbStore, err := sandbox.NewStore(db)
	must(err, "sandbox")

	secStore, err := secrets.NewStore(db)
	must(err, "secrets")

	chatStore, err := chat.NewStore(db)
	must(err, "chat")

	chatRTStore, err := chatruntime.NewStore(db)
	must(err, "chatruntime")

	chatBus := chat.NewBus()
	chatRT := chatruntime.NewManager(chatRTStore, settStore, nil, chatruntime.ManagerConfig{DataDir: dir})
	chatSvc := chatruntime.NewService(chatStore, chatRT, chatBus, dir, nil)

	authStore, err := auth.NewUserStore(db)
	must(err, "auth")

	auditStore, err := audit.NewStore(db)
	must(err, "audit")

	wh, err := webhook.NewStore(db)
	must(err, "webhook")

	reg := registry.New()

	srv := New(t.Context(), Config{
		Addr:      ":0",
		AgentsDir: dir,
		RunsDir:   filepath.Join(dir, "runs"),

		UsageStore:    usageStore,
		ProjectStore:  projStore,
		TaskStore:     taskStore,
		SettingsStore: settStore,
		SandboxStore:  sbStore,
		SecretsStore:  secStore,
		AuthStore:     authStore,
		AuditStore:    auditStore,
		WebhookStore:  wh,

		Docker:         nil,
		Kube:           nil,
		SandboxManager: nil,
		ChatService:    chatSvc,
		Registry:       reg,
		Watcher:        nil,

		ChatBus:      chatBus,
		MCPForwarder: nil,
	})

	credStore, err := credentials.NewStore(db)
	must(err, "credentials")
	srv.SetCredentials(credStore)

	ghStore, err := githubapp.NewStore(db)
	must(err, "githubapp")
	srv.SetGitHubApp(githubapp.NewService(ghStore), ghStore)

	stageStore, err := stagepkg.NewStore(db)
	must(err, "stages")
	if got, want := len(stageStore.Catalog()), len(stagetest.Flow()); got != want {
		t.Fatalf("newIntegrationServer: template board has %d stages, want the %d-stage reference flow", got, want)
	}
	srv.SetStageStore(stageStore)
	settStore.SetStageBoard(stageStore.Catalog)
	must(settStore.SeedSandboxPriorities(t.Context()), "seed sandbox priorities")

	envMetricStore, err := envmetrics.NewStore(db)
	must(err, "envmetrics")
	srv.SetEnvMetrics(envMetricStore)

	ingestStore, err := ingest.NewStore(db)
	must(err, "ingest")
	srv.SetIngest(ingestStore, nil)

	srv.srv.Handler = asDevBrowser(srv, srv.srv.Handler)
	return srv, srv.srv.Handler
}

const (
	testDevToken = "integration-suite-dev-token-0123456789abcdef"
	testDevCSRF  = "integration-suite-csrf"
)

// The dev token is process-wide; every test sets the same constant, so parallel tests agree on it.
func asDevBrowser(srv *Server, next http.Handler) http.Handler {
	auth.SetDevToken(testDevToken)
	return &devBrowser{srv: srv, next: next}
}

type devBrowser struct {
	srv  *Server
	next http.Handler
}

func (b *devBrowser) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" && r.Header.Get("X-API-Key") == "" && !hasCookie(r, auth.CookieAccess) {
		r.AddCookie(&http.Cookie{Name: auth.CookieDevSession, Value: testDevToken})
		r.AddCookie(&http.Cookie{Name: auth.CookieCSRF, Value: testDevCSRF})
		r.Header.Set("X-CSRF-Token", testDevCSRF)
	}
	b.next.ServeHTTP(w, r)
}

func asDevOperator(r *http.Request) *http.Request {
	auth.SetDevToken(testDevToken)
	r.AddCookie(&http.Cookie{Name: auth.CookieDevSession, Value: testDevToken})
	out := r
	auth.DevSessionMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, in *http.Request) {
		out = in
	})).ServeHTTP(httptest.NewRecorder(), r)
	return out
}

func hasCookie(r *http.Request, name string) bool {
	c, err := r.Cookie(name)
	return err == nil && c.Value != ""
}

func setupChatAgent(t *testing.T, srv *Server, name string) string {
	t.Helper()
	agentDir := filepath.Join(srv.agentsDir, "agents", name)
	if err := os.MkdirAll(filepath.Join(agentDir, "prompts"), 0755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	os.WriteFile(filepath.Join(agentDir, "prompts", "main.md"), []byte("# Chat Agent\n"), 0644)
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "` + name + `"
      thulu.stages: "chat"
      thulu.model: "opus"
`
	if err := os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
	return name
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func postJSONAuth(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func getJSON(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIntegration_HealthLive(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/health/live", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /health/live = %d, want 200", w.Code)
	}
}

func TestIntegration_HealthReady(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/health/ready", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /health/ready = %d, want 200", w.Code)
	}
}

func TestIntegration_HealthFull(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/health", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] == nil {
		t.Error("missing status field")
	}
}

func TestIntegration_OpenAPI(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/openapi.json", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /openapi.json = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestIntegration_AuthConfig_Public(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/auth/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /auth/config = %d, want 200", w.Code)
	}
	var cfg map[string]any
	if err := json.NewDecoder(w.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"has_users", "jwt_configured", "api_key_configured"} {
		if _, ok := cfg[key]; !ok {
			t.Errorf("missing field %q in /auth/config response", key)
		}
	}
	if hasUsers, _ := cfg["has_users"].(bool); hasUsers {
		t.Error("has_users should be false before anyone signs in")
	}
}

func TestIntegration_AuthConfig_AfterFirstUser(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	registerUser(t, h, "admin@test.com", "Admin", "SecurePassword1")

	w := getJSON(t, h, "/auth/config", "")
	var cfg map[string]any
	json.NewDecoder(w.Body).Decode(&cfg)
	if hasUsers, _ := cfg["has_users"].(bool); !hasUsers {
		t.Error("has_users should be true once an account exists")
	}
}

func registerUser(t *testing.T, h http.Handler, email, name, password string) string {
	t.Helper()
	token, _ := registerUserInWorkspace(t, h, email, name, password)
	return token
}

func registerCatalogOperator(t *testing.T, h http.Handler, email, name, password string) string {
	t.Helper()
	token, wid := registerUserInWorkspace(t, h, email, name, password)
	t.Setenv(OperatorWorkspaceEnv, wid)
	return token
}

func registerUserInWorkspace(t *testing.T, h http.Handler, email, name, _ string) (token, workspaceID string) {
	t.Helper()
	return mintWorkspaceUserAs(t, integrationServerOf(t, h), email, name, auth.RoleOwner)
}

func TestIntegration_RefreshToken_Invalid(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := postJSON(t, h, "/auth/refresh", map[string]string{"refresh_token": "not-a-real-token"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid refresh token should yield 401, got %d", w.Code)
	}
}

func TestIntegration_TasksCRUD(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "bob@example.com", "Bob", "SecurePassword1")

	w := getJSON(t, h, "/tasks", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks = %d, want 200", w.Code)
	}
	var list []any
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("expected empty task list, got %d", len(list))
	}

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "Test task",
		"body":        "do things",
		"project_ids": []string{},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d, want 201: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("POST /tasks: no id in response: %+v", created)
	}

	gw := getJSON(t, h, "/tasks/"+id, token)
	if gw.Code != http.StatusOK {
		t.Errorf("GET /tasks/%s = %d, want 200", id, gw.Code)
	}
	var got map[string]any
	json.NewDecoder(gw.Body).Decode(&got)
	if got["title"] != "Test task" {
		t.Errorf("title = %q, want Test task", got["title"])
	}

	w2 := getJSON(t, h, "/tasks", token)
	var list2 []any
	json.NewDecoder(w2.Body).Decode(&list2)
	if len(list2) != 1 {
		t.Errorf("expected 1 task, got %d", len(list2))
	}
}

func TestIntegration_MeRequiresAuth(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/auth/me", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /auth/me without token = %d, want 401", w.Code)
	}
}

func TestIntegration_ProjectsListEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "carol@example.com", "Carol", "SecurePassword1")

	w := getJSON(t, h, "/projects", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /projects = %d, want 200", w.Code)
	}
	var list []any
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("expected empty project list, got %d", len(list))
	}
}

func TestIntegration_ProjectsCreateValidation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := postJSON(t, h, "/projects", map[string]any{"name": "OnlyName"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing repo_url should yield 400, got %d", w.Code)
	}

	w2 := postJSON(t, h, "/projects", map[string]any{"repo_url": "git@example.com:foo.git"})
	if w2.Code != http.StatusBadRequest {
		t.Errorf("missing name should yield 400, got %d", w2.Code)
	}
}

func createChat(t *testing.T, h http.Handler, token, agentType string) string {
	t.Helper()
	w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": agentType})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /chats = %d: %s", w.Code, w.Body.String())
	}
	var c map[string]any
	json.NewDecoder(w.Body).Decode(&c)
	id, _ := c["id"].(string)
	if id == "" {
		t.Fatal("chat id missing in create response")
	}
	return id
}

func TestIntegration_Chats_CreateListGet(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "dave@example.com", "Dave", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")

	lw := getJSON(t, h, "/chats", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /chats = %d", lw.Code)
	}
	if body := strings.TrimSpace(lw.Body.String()); body == "null" {
		t.Error("GET /chats must return [], got null")
	}

	id := createChat(t, h, token, "chatty")

	gw := getJSON(t, h, "/chats/"+id, token)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET /chats/{id} = %d", gw.Code)
	}
	var c map[string]any
	json.NewDecoder(gw.Body).Decode(&c)
	if c["agent_type"] != "chatty" || c["status"] != "idle" {
		t.Errorf("chat shape wrong: %v", c)
	}

	lw2 := getJSON(t, h, "/chats", token)
	var chats []map[string]any
	json.NewDecoder(lw2.Body).Decode(&chats)
	if len(chats) != 1 {
		t.Errorf("expected 1 chat in list, got %d", len(chats))
	}

	if w := getJSON(t, h, "/chats/nope", token); w.Code != http.StatusNotFound {
		t.Errorf("GET unknown chat = %d, want 404", w.Code)
	}
}

func TestIntegration_Chats_CreateValidation(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatval@test.com", "CV", "SecurePassword1")

	if w := postJSONAuth(t, h, "/chats", token, map[string]string{}); w.Code != http.StatusBadRequest {
		t.Errorf("POST /chats without agent_type = %d, want 400", w.Code)
	}

	setupAgent(t, srv, "no-chat-agent")
	w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "no-chat-agent"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /chats with non-chat agent = %d, want 400: %s", w.Code, w.Body.String())
	}

	if w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "ghost"}); w.Code != http.StatusBadRequest {
		t.Errorf("POST /chats with unknown agent = %d, want 400", w.Code)
	}

	setupChatAgent(t, srv, "chatty2")
	w = postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "chatty2", "task_id": "t-missing"})
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /chats with missing task = %d, want 404", w.Code)
	}
}

func TestIntegration_Chats_PatchRenameArchiveDelete(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatpatch@test.com", "CP", "SecurePassword1")
	setupChatAgent(t, srv, "chatty3")
	id := createChat(t, h, token, "chatty3")

	b, _ := json.Marshal(map[string]any{"title": "Sprint planning", "archived": true})
	r := httptest.NewRequest(http.MethodPatch, "/chats/"+id, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /chats/{id} = %d: %s", w.Code, w.Body.String())
	}
	var c map[string]any
	json.NewDecoder(w.Body).Decode(&c)
	if c["title"] != "Sprint planning" || c["archived"] != true {
		t.Errorf("patched chat wrong: %v", c)
	}

	pw := postJSONAuth(t, h, "/chats/"+id+"/messages", token, map[string]string{"text": "hola"})
	if pw.Code != http.StatusConflict {
		t.Errorf("POST message to archived chat = %d, want 409", pw.Code)
	}

	dr := httptest.NewRequest(http.MethodDelete, "/chats/"+id, nil)
	dr.Header.Set("Authorization", "Bearer "+token)
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, dr)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /chats/{id} = %d", dw.Code)
	}
	if w := getJSON(t, h, "/chats/"+id, token); w.Code != http.StatusNotFound {
		t.Errorf("GET deleted chat = %d, want 404", w.Code)
	}
}

func TestIntegration_Chats_PostMessage(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatpost@test.com", "CM", "SecurePassword1")
	setupChatAgent(t, srv, "chatty4")
	id := createChat(t, h, token, "chatty4")

	if w := postJSONAuth(t, h, "/chats/"+id+"/messages", token, map[string]string{"text": " "}); w.Code != http.StatusBadRequest {
		t.Errorf("POST empty message = %d, want 400", w.Code)
	}

	if w := postJSONAuth(t, h, "/chats/missing/messages", token, map[string]string{"text": "hola"}); w.Code != http.StatusNotFound {
		t.Errorf("POST to unknown chat = %d, want 404", w.Code)
	}

	w := postJSONAuth(t, h, "/chats/"+id+"/messages", token, map[string]string{"text": "hola agente"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST message = %d, want 202: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Message map[string]any `json:"message"`
		TurnID  string         `json:"turn_id"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Message["text"] != "hola agente" || resp.TurnID == "" {
		t.Errorf("202 body wrong: %+v", resp)
	}

	mw := getJSON(t, h, "/chats/"+id+"/messages", token)
	if mw.Code != http.StatusOK {
		t.Fatalf("GET messages = %d", mw.Code)
	}
	var msgs []map[string]any
	json.NewDecoder(mw.Body).Decode(&msgs)
	if len(msgs) == 0 || msgs[0]["text"] != "hola agente" {
		t.Errorf("messages list wrong: %v", msgs)
	}
}

func TestIntegration_Chats_PostMessage_TurnInFlight(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatbusy@test.com", "CB", "SecurePassword1")
	setupChatAgent(t, srv, "chatty5")
	id := createChat(t, h, token, "chatty5")

	if err := srv.chatSvc.Runtime().BeginTurn(id); err != nil {
		t.Fatal(err)
	}
	defer srv.chatSvc.Runtime().EndTurn(id)

	w := postJSONAuth(t, h, "/chats/"+id+"/messages", token, map[string]string{"text": "hola"})
	if w.Code != http.StatusConflict {
		t.Errorf("POST while turn in flight = %d, want 409", w.Code)
	}

	gw := getJSON(t, h, "/chats/"+id, token)
	var c map[string]any
	json.NewDecoder(gw.Body).Decode(&c)
	if c["status"] != "running" {
		t.Errorf("chat status = %v, want running", c["status"])
	}
}

func TestIntegration_Settings_ListEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := getJSON(t, h, "/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d, want 200", w.Code)
	}
	var m map[string]any
	json.NewDecoder(w.Body).Decode(&m)
	if m == nil {
		t.Error("expected object, got nil")
	}
}

func TestIntegration_Settings_PutAndGet(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	pw := putJSON(t, h, "/settings/sandbox.max", 4, "")
	if pw.Code != http.StatusNoContent {
		t.Fatalf("PUT /settings/sandbox.max = %d: %s", pw.Code, pw.Body.String())
	}

	gw := getJSON(t, h, "/settings/sandbox.max", "")
	if gw.Code != http.StatusOK {
		t.Fatalf("GET /settings/sandbox.max = %d", gw.Code)
	}
	var n int
	json.NewDecoder(gw.Body).Decode(&n)
	if n != 4 {
		t.Errorf("value = %d, want 4", n)
	}
}

func TestIntegration_Settings_PatchBatchIsAllOrNothing(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	bad := patchJSONAuth(t, h, "/settings", "", map[string]any{
		"sandbox.max": 5, "sandbox.idle_evict_seconds": nil, "sandbox.acquire_block_ms": 9000,
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("PATCH /settings with a null = %d, want 400: %s", bad.Code, bad.Body.String())
	}
	var n int
	json.NewDecoder(getJSON(t, h, "/settings/sandbox.max", "").Body).Decode(&n)
	if n == 5 {
		t.Error("sandbox.max was stored although another value in the batch was refused")
	}

	ok := patchJSONAuth(t, h, "/settings", "", map[string]any{
		"sandbox.max": 5, "sandbox.idle_evict_seconds": 600, "sandbox.acquire_block_ms": 9000,
		"sandbox.priorities": map[string]int{"in_progress": 10},
	})
	if ok.Code != http.StatusNoContent {
		t.Fatalf("PATCH /settings = %d: %s", ok.Code, ok.Body.String())
	}
	json.NewDecoder(getJSON(t, h, "/settings/sandbox.idle_evict_seconds", "").Body).Decode(&n)
	if n != 600 {
		t.Errorf("sandbox.idle_evict_seconds = %d, want 600", n)
	}

	if w := patchJSONAuth(t, h, "/settings", "", map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH /settings {} = %d, want 400", w.Code)
	}
}

func TestIntegration_Settings_GetNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/settings/nonexistent-key", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /settings/nonexistent = %d, want 404", w.Code)
	}
}

func TestIntegration_Audit_ListEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := getJSON(t, h, "/audit", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /audit = %d, want 200", w.Code)
	}
	var list []any
	json.NewDecoder(w.Body).Decode(&list)
	if list == nil {
		t.Error("expected array, got nil")
	}
}

func TestIntegration_Webhooks_CRUD(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	// Check the raw body: a nil slice marshals to null and still decodes to an empty []any, but the
	// dashboard breaks on null.
	lw := getJSON(t, h, "/webhooks", "")
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /webhooks = %d, want 200", lw.Code)
	}
	raw := strings.TrimSpace(lw.Body.String())
	if raw != "[]" {
		t.Errorf("an empty list must serialize as [], got %q", raw)
	}
	var list []any
	json.NewDecoder(strings.NewReader(raw)).Decode(&list)
	if len(list) != 0 {
		t.Errorf("want 0 webhooks, got %d", len(list))
	}

	cw := postJSON(t, h, "/webhooks", map[string]any{
		"url":    "https://example.com/hook",
		"events": []string{"task.created", "task.stage_changed"},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatalf("POST /webhooks: no id in response: %+v", created)
	}
	if created["secret"] == nil {
		t.Error("POST /webhooks: missing secret in creation response")
	}

	lw2 := getJSON(t, h, "/webhooks", "")
	var list2 []any
	json.NewDecoder(lw2.Body).Decode(&list2)
	if len(list2) != 1 {
		t.Errorf("want 1 webhook, got %d", len(list2))
	}

	dw := deleteRequest(t, h, "/webhooks/"+id, "")
	if dw.Code != http.StatusNoContent {
		t.Errorf("DELETE /webhooks/%s = %d, want 204", id, dw.Code)
	}

	lw3 := getJSON(t, h, "/webhooks", "")
	var list3 []any
	json.NewDecoder(lw3.Body).Decode(&list3)
	if len(list3) != 0 {
		t.Errorf("want 0 webhooks after delete, got %d", len(list3))
	}
}

func TestIntegration_Webhooks_RotateSecret(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	cw := postJSON(t, h, "/webhooks", map[string]any{"url": "https://example.com/hook"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	rw := postJSON(t, h, "/webhooks/"+id+"/rotate-secret", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("POST /webhooks/{id}/rotate-secret = %d: %s", rw.Code, rw.Body.String())
	}
	var rotated map[string]any
	json.NewDecoder(rw.Body).Decode(&rotated)
	if s, _ := rotated["secret"].(string); s == "" || s == created["secret"] {
		t.Errorf("rotation must return a new secret, got %v (was %v)", rotated["secret"], created["secret"])
	}
	if rotated["legacy_signing"] != false {
		t.Errorf("legacy_signing = %v after rotation, want false", rotated["legacy_signing"])
	}

	if lw := getJSON(t, h, "/webhooks", ""); strings.Contains(lw.Body.String(), "whsec_") {
		t.Errorf("GET /webhooks leaks a secret: %s", lw.Body.String())
	}

	if nw := postJSON(t, h, "/webhooks/does-not-exist/rotate-secret", nil); nw.Code != http.StatusNotFound {
		t.Errorf("rotating an unknown endpoint = %d, want 404", nw.Code)
	}
}

func TestIntegration_Webhooks_EventCatalog(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	lw := getJSON(t, h, "/webhooks/events", "")
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /webhooks/events = %d: %s", lw.Code, lw.Body.String())
	}
	var events []string
	json.NewDecoder(lw.Body).Decode(&events)
	if !slices.Contains(events, "task.stage_changed") || slices.Contains(events, "task.updated") {
		t.Errorf("GET /webhooks/events = %v", events)
	}

	w := postJSON(t, h, "/webhooks", map[string]any{
		"url":    "https://example.com/hook",
		"events": []string{"task.created", "task.updated"},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "task.updated") {
		t.Errorf("unknown event: %d %s, want 400 naming task.updated", w.Code, w.Body.String())
	}

	w = postJSON(t, h, "/webhooks", map[string]any{
		"url":    "https://example.com/hook",
		"events": []string{"task.*"},
	})
	if w.Code != http.StatusCreated {
		t.Errorf("prefix pattern task.*: %d %s, want 201", w.Code, w.Body.String())
	}
}

func TestIntegration_Webhooks_CreateMissingURL(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := postJSON(t, h, "/webhooks", map[string]any{"events": []string{"*"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing url should yield 400, got %d", w.Code)
	}
}

func TestIntegration_BlockedMoveNamesTheFinalStageInTheCallersLanguage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "deps-final-es@test.com", "D", "SecurePassword1")
	blocker := createTaskViaAPI(t, h, token, "blocker")
	blockedID := createTaskViaAPI(t, h, token, "blocked")
	aw := postJSONAuth(t, h, "/tasks/"+blockedID+"/dependencies", token, map[string]any{
		"depends_on_task_id": blocker, "kind": "hard",
	})
	if aw.Code != http.StatusCreated {
		t.Fatalf("add dependency = %d: %s", aw.Code, aw.Body.String())
	}
	var tk map[string]any
	json.NewDecoder(getJSON(t, h, "/tasks/"+blockedID, token).Body).Decode(&tk)
	var stages []map[string]any
	json.NewDecoder(getJSON(t, h, "/boards/"+tk["board_id"].(string)+"/stages", token).Body).Decode(&stages)
	next := ""
	for _, st := range stages {
		if st["initial"] != true && st["terminal"] != true && next == "" {
			next = st["id"].(string)
		}
	}
	r := httptest.NewRequest(http.MethodPatch, "/tasks/"+blockedID, strings.NewReader(`{"stage":"`+next+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept-Language", "es")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("move a blocked task = %d want 409: %s", w.Code, w.Body.String())
	}
	want := i18n.T("es", "stage.labels.done")
	if !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), i18n.T("en", "stage.labels.done")+".") {
		t.Errorf("the refusal does not name the final stage as %q: %s", want, w.Body.String())
	}
}

func putJSON(t *testing.T, h http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func deleteRequest(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIntegration_TaskUpdateAndDelete(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskud@test.com", "UD", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "original", "body": "desc", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	pw := patchJSONAuth(t, h, "/tasks/"+id, token, map[string]any{"title": "updated"})
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH /tasks/%s = %d: %s", id, pw.Code, pw.Body.String())
	}
	var updated map[string]any
	json.NewDecoder(pw.Body).Decode(&updated)
	if updated["title"] != "updated" {
		t.Errorf("title = %q, want 'updated'", updated["title"])
	}

	dw := deleteRequest(t, h, "/tasks/"+id, token)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /tasks/%s = %d: %s", id, dw.Code, dw.Body.String())
	}

	gw := getJSON(t, h, "/tasks/"+id, token)
	if gw.Code != http.StatusNotFound {
		t.Errorf("GET deleted task should 404, got %d", gw.Code)
	}
}

func TestIntegration_TaskEvents(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "events@test.com", "E", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "evented", "body": "", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	lw := getJSON(t, h, "/tasks/"+id+"/events", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/events = %d", id, lw.Code)
	}
	var events []any
	json.NewDecoder(lw.Body).Decode(&events)
	if len(events) == 0 {
		t.Error("expected at least one event (task_created)")
	}
}

func TestIntegration_ImportFlagsYAML(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagimp@test.com", "FI", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "import flags", "body": "", "project_ids": []string{},
	})
	var created map[string]any
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID := created["id"].(string)

	yamlBody := `
flags:
  - kind: infra_impact
    label: "Mapeo de observabilidad propuesto para prod"
    payload:
      environment: prod
      signals_mapped: 4
  - kind: custom
    label: "Sin tracing: no hay latencia por endpoint"
`
	req := httptest.NewRequest(http.MethodPost,
		"/tasks/"+taskID+"/flags/import?agent=devops", strings.NewReader(yamlBody))
	req.Header.Set("Content-Type", "application/x-yaml")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode import: %v", err)
	}
	if out["flags_created"] != float64(2) {
		t.Errorf("flags_created = %v, want 2", out["flags_created"])
	}

	lw := getJSON(t, h, "/tasks/"+taskID+"/flags", token)
	var flags []map[string]any
	if err := json.NewDecoder(lw.Body).Decode(&flags); err != nil {
		t.Fatalf("decode flags: %v", err)
	}
	if len(flags) != 2 {
		t.Fatalf("listed %d flags, want 2", len(flags))
	}
	byLabel := map[string]map[string]any{}
	for _, f := range flags {
		byLabel[f["label"].(string)] = f
	}
	infra, ok := byLabel["Mapeo de observabilidad propuesto para prod"]
	if !ok {
		t.Fatalf("infra_impact flag missing, got %v", flags)
	}
	if infra["kind"] != "infra_impact" {
		t.Errorf("kind = %v, want infra_impact", infra["kind"])
	}
	if infra["created_by"] != "flagimp@test.com" {
		t.Errorf("created_by = %v, want flagimp@test.com (the credential's author)", infra["created_by"])
	}
	if infra["payload"] == nil {
		t.Error("payload dropped on import")
	}
	if infra["status"] != "pending" {
		t.Errorf("status = %v, want pending", infra["status"])
	}
}

func TestIntegration_ImportFlagsYAMLRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagbad@test.com", "FB", "SecurePassword1")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "bad flags", "body": "", "project_ids": []string{},
	})
	var created map[string]any
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID := created["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/flags/import",
		strings.NewReader("flags: [this is not: valid: yaml"))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed yaml = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_FlagUpdateAndDelete(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagud@test.com", "FUD", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "flagtest", "body": "", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	taskID := created["id"].(string)

	fw := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"kind": "arch_decision", "label": "pick auth lib",
	})
	var flag map[string]any
	json.NewDecoder(fw.Body).Decode(&flag)
	flagID := flag["id"].(string)

	upw := patchJSONAuth(t, h, "/tasks/"+taskID+"/flags/"+flagID, token, map[string]any{
		"status": "approved",
	})
	if upw.Code != http.StatusOK {
		t.Fatalf("PATCH flag = %d: %s", upw.Code, upw.Body.String())
	}

	dfw := deleteRequest(t, h, "/tasks/"+taskID+"/flags/"+flagID, token)
	if dfw.Code != http.StatusOK {
		t.Fatalf("DELETE flag = %d: %s", dfw.Code, dfw.Body.String())
	}

	lw := getJSON(t, h, "/tasks/"+taskID+"/flags", token)
	var flags []any
	json.NewDecoder(lw.Body).Decode(&flags)
	if len(flags) != 0 {
		t.Errorf("flag not deleted, list = %d", len(flags))
	}
}

func TestIntegration_TaskReports(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "reports@test.com", "R", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "reported", "body": "", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	lw := getJSON(t, h, "/tasks/"+id+"/reports", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/reports = %d", id, lw.Code)
	}
	var reports []any
	json.NewDecoder(lw.Body).Decode(&reports)
	if reports == nil {
		t.Error("expected array, got nil")
	}
}

func TestIntegration_ListStages(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := getJSON(t, h, "/stages", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /stages = %d", w.Code)
	}
	var stages []any
	json.NewDecoder(w.Body).Decode(&stages)
	if len(stages) == 0 {
		t.Error("expected at least one stage")
	}
}

func TestIntegration_UsageStats(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := getJSON(t, h, "/usage", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_UsageSessions(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := getJSON(t, h, "/usage/sessions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/sessions = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_TaskDependencies(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "deps@test.com", "D", "SecurePassword1")

	cw1 := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "blocker", "body": "", "project_ids": []string{},
	})
	var t1 map[string]any
	json.NewDecoder(cw1.Body).Decode(&t1)
	id1 := t1["id"].(string)

	cw2 := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "blocked", "body": "", "project_ids": []string{},
	})
	var t2 map[string]any
	json.NewDecoder(cw2.Body).Decode(&t2)
	id2 := t2["id"].(string)

	lw := getJSON(t, h, "/tasks/"+id2+"/dependencies", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/dependencies = %d", id2, lw.Code)
	}

	aw := postJSONAuth(t, h, "/tasks/"+id2+"/dependencies", token, map[string]any{
		"depends_on_task_id": id1, "kind": "hard",
	})
	if aw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks/%s/dependencies = %d: %s", id2, aw.Code, aw.Body.String())
	}

	lw2 := getJSON(t, h, "/tasks/"+id2+"/dependencies", token)
	var depResp map[string]any
	json.NewDecoder(lw2.Body).Decode(&depResp)
	blockedBy, _ := depResp["blocked_by"].([]any)
	if len(blockedBy) != 1 {
		t.Errorf("expected 1 blocker in blocked_by, got %d (response: %+v)", len(blockedBy), depResp)
	}
}

func TestIntegration_BlockedMoveNamesTheFinalStage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "deps-final@test.com", "D", "SecurePassword1")
	blocker := createTaskViaAPI(t, h, token, "blocker")
	blockedID := createTaskViaAPI(t, h, token, "blocked")
	aw := postJSONAuth(t, h, "/tasks/"+blockedID+"/dependencies", token, map[string]any{
		"depends_on_task_id": blocker, "kind": "hard",
	})
	if aw.Code != http.StatusCreated {
		t.Fatalf("add dependency = %d: %s", aw.Code, aw.Body.String())
	}

	var tk map[string]any
	json.NewDecoder(getJSON(t, h, "/tasks/"+blockedID, token).Body).Decode(&tk)
	var stages []map[string]any
	json.NewDecoder(getJSON(t, h, "/boards/"+tk["board_id"].(string)+"/stages", token).Body).Decode(&stages)
	var next, final string
	for _, st := range stages {
		if st["initial"] == true {
			continue
		}
		if next == "" {
			next = st["id"].(string)
		}
		if st["terminal"] == true && final == "" {
			final = st["label"].(string)
		}
	}
	if next == "" || final == "" {
		t.Fatalf("board has no stage after the first or no final one: %v", stages)
	}

	w := patchJSONAuth(t, h, "/tasks/"+blockedID, token, map[string]any{"stage": next})
	if w.Code != http.StatusConflict {
		t.Fatalf("move a blocked task = %d want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), final) {
		t.Errorf("the refusal does not name the final stage %q: %s", final, w.Body.String())
	}
}

func patchJSONAuth(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIntegration_SecurityHeaders(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/health/live", "")
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options: DENY")
	}
}

func TestIntegration_Dashboard_Root(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct == "" {
		t.Error("GET / missing Content-Type")
	}
}

func TestIntegration_Dashboard_UnknownPath_Falls_Back_To_Index(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/some-nonexistent-spa-route", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /unknown-spa-route = %d, want 200 (SPA fallback)", w.Code)
	}
}

func TestIntegration_Dashboard_StaticAsset(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/_astro/", "")
	if w.Code == http.StatusNotFound {
		t.Error("GET /_astro/ should not 404")
	}
}

func TestIntegration_APIKeys_CRUD(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "apikey@test.com", "K", "SecurePassword1")

	lw := getJSON(t, h, "/auth/api-keys", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /auth/api-keys = %d, want 200", lw.Code)
	}
	var keys []any
	json.NewDecoder(lw.Body).Decode(&keys)
	if len(keys) != 0 {
		t.Errorf("want 0 keys initially, got %d", len(keys))
	}

	cw := postJSONAuth(t, h, "/auth/api-keys", token, map[string]any{"name": "CI key", "permissions": []string{"full"}, "role": "owner"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /auth/api-keys = %d, want 201 (body: %s)", cw.Code, cw.Body)
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	rawKey, _ := created["key"].(string)
	keyID, _ := created["id"].(string)
	prefix, _ := created["prefix"].(string)
	if rawKey == "" {
		t.Fatal("expected raw key in response")
	}
	if !strings.HasPrefix(rawKey, "oct_") {
		t.Errorf("key should start with oct_, got %q", rawKey)
	}
	if prefix == "" || keyID == "" {
		t.Errorf("expected non-empty prefix and id, got prefix=%q id=%q", prefix, keyID)
	}

	lw2 := getJSON(t, h, "/auth/api-keys", token)
	var keys2 []any
	json.NewDecoder(lw2.Body).Decode(&keys2)
	if len(keys2) != 1 {
		t.Errorf("want 1 key, got %d", len(keys2))
	}

	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r.Header.Set("X-API-Key", rawKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("request with oct_ key = %d, want 200", w.Code)
	}

	dw := deleteRequest(t, h, "/auth/api-keys/"+keyID, token)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /auth/api-keys/%s = %d, want 200", keyID, dw.Code)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	r2.Header.Set("X-API-Key", rawKey)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("revoked key on /tasks = %d, want 401", w2.Code)
	}
}

func TestIntegration_APIKeys_CreateNoAuth(t *testing.T) {
	_, h := newIntegrationServer(t)
	registerUser(t, h, "ak2@test.com", "X", "SecurePassword1")
	// In dev mode the suite sends the dev session, which may manage keys; disable it so the request
	// is anonymous.
	t.Setenv("OCTOPUS_DEV_MODE", "0")
	w := postJSON(t, h, "/auth/api-keys", map[string]any{"name": "bad"})
	if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated create should be 401 or 403, got %d", w.Code)
	}
}

func TestIntegration_AgentSettings_Get(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsget@test.com", "AG", "SecurePassword1")
	setupAgent(t, srv, "probe-settings")

	w := getJSON(t, h, "/types/probe-settings/settings", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /types/probe-settings/settings = %d, want 200: %s", w.Code, w.Body.String())
	}
	var settings map[string]any
	json.NewDecoder(w.Body).Decode(&settings)
	if settings["name"] == nil {
		t.Error("expected name field in settings response")
	}
}

func TestIntegration_AgentSettings_GetNotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsnf@test.com", "AN", "SecurePassword1")

	w := getJSON(t, h, "/types/nonexistent/settings", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /types/nonexistent/settings = %d, want 404", w.Code)
	}
}

func setupAgent(t *testing.T, srv *Server, name string) string {
	t.Helper()
	agentDir := filepath.Join(srv.agentsDir, "agents", name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	promptFile := filepath.Join(agentDir, "main.md")
	if err := os.WriteFile(promptFile, []byte("# Agent Prompt\n"), 0644); err != nil {
		t.Fatalf("write main.md: %v", err)
	}
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "` + name + `"
      thulu.valid_stages: "refinement,done"
`
	if err := os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
	return name
}

func putRequest(t *testing.T, h http.Handler, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "text/plain")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIntegration_AgentFiles_ListAndRead(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afiles@test.com", "AF", "SecurePassword1")
	setupAgent(t, srv, "probe")

	lw := getJSON(t, h, "/types/probe/files", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /types/probe/files = %d, want 200", lw.Code)
	}
	var files []map[string]any
	json.NewDecoder(lw.Body).Decode(&files)
	if len(files) == 0 {
		t.Error("expected at least one file in listing")
	}

	rw := getJSON(t, h, "/types/probe/files/main.md", token)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET /types/probe/files/main.md = %d, want 200", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "Agent Prompt") {
		t.Errorf("unexpected file content: %q", rw.Body.String())
	}
}

func TestIntegration_AgentFiles_WriteAndDelete(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afileswd@test.com", "AW", "SecurePassword1")
	setupAgent(t, srv, "probe2")

	pw := putRequest(t, h, "/types/probe2/files/main.md", token, []byte("# Updated\n"))
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT /types/probe2/files/main.md = %d, want 200", pw.Code)
	}

	rw := getJSON(t, h, "/types/probe2/files/main.md", token)
	if !strings.Contains(rw.Body.String(), "Updated") {
		t.Errorf("content not updated: %q", rw.Body.String())
	}

	dw := deleteRequest(t, h, "/types/probe2/files/main.md", token)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /types/probe2/files/main.md = %d, want 200", dw.Code)
	}

	rw2 := getJSON(t, h, "/types/probe2/files/main.md", token)
	if rw2.Code != http.StatusNotFound {
		t.Errorf("after delete, GET = %d, want 404", rw2.Code)
	}
}

func TestIntegration_AgentFiles_NotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afnf@test.com", "NF", "SecurePassword1")

	w := getJSON(t, h, "/types/nonexistent/files", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET unknown agent = %d, want 404", w.Code)
	}
}

func TestIntegration_AgentFiles_PathTraversal(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "aftrav@test.com", "PT", "SecurePassword1")
	setupAgent(t, srv, "probe3")

	w := getJSON(t, h, "/types/probe3/files/docker-compose.yml", token)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-md file = %d, want 403", w.Code)
	}
}

func TestIntegration_AgentFiles_CreateAndDeleteAgent(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agcreate@test.com", "AC", "SecurePassword1")
	setupAgent(t, srv, "probe")

	cw := postJSONAuth(t, h, "/types", token, map[string]any{
		"name":     "myagent",
		"template": "probe",
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /types = %d, want 201: %s", cw.Code, cw.Body.String())
	}

	rw := getJSON(t, h, "/types/myagent/files/main.md", token)
	if rw.Code != http.StatusOK {
		t.Errorf("GET cloned file = %d, want 200", rw.Code)
	}

	dw := deleteRequest(t, h, "/types/myagent", token)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /types/myagent = %d, want 200", dw.Code)
	}

	w2 := getJSON(t, h, "/types/myagent/files", token)
	if w2.Code != http.StatusNotFound {
		t.Errorf("after delete, GET files = %d, want 404", w2.Code)
	}
	if _, err := os.Stat(filepath.Join(srv.agentsDir, "agents", "myagent")); !os.IsNotExist(err) {
		t.Error("expected agent dir to be deleted from disk")
	}
}

func TestIntegration_CreateAgent_BlankFromScratch(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agscratch@test.com", "AS", "SecurePassword1")
	if err := os.MkdirAll(filepath.Join(srv.agentsDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}

	blank := postJSONAuth(t, h, "/types", token, map[string]any{"name": "from-zero"})
	if blank.Code != http.StatusCreated {
		t.Fatalf("blank create with no agents = %d, want 201: %s", blank.Code, blank.Body.String())
	}

	composeBytes, rerr := os.ReadFile(filepath.Join(srv.agentsDir, "agents", "from-zero", "docker-compose.yml"))
	if rerr != nil {
		t.Fatalf("read scaffolded compose: %v", rerr)
	}
	compose := string(composeBytes)
	for _, want := range []string{`thulu.type: from-zero`, `thulu.agent: "true"`, `from-zero:`} {
		if !strings.Contains(compose, want) {
			t.Errorf("scaffolded compose missing %q:\n%s", want, compose)
		}
	}
	for _, f := range []string{"setup.sh", filepath.Join("engines", "claude-code.sh")} {
		if _, err := os.Stat(filepath.Join(srv.agentsDir, "agents", "from-zero", f)); err != nil {
			t.Errorf("scaffold missing %s: %v", f, err)
		}
	}
	lst := getJSON(t, h, "/types", token)
	if !strings.Contains(lst.Body.String(), `"from-zero"`) {
		t.Errorf("scaffolded agent not discoverable: %s", lst.Body.String())
	}

	setupAgent(t, srv, "probe")
	cl := postJSONAuth(t, h, "/types", token, map[string]any{"name": "from-probe", "template": "probe"})
	if cl.Code != http.StatusCreated {
		t.Fatalf("clone from probe = %d, want 201: %s", cl.Code, cl.Body.String())
	}
	clonedBytes, rerr := os.ReadFile(filepath.Join(srv.agentsDir, "agents", "from-probe", "docker-compose.yml"))
	if rerr != nil {
		t.Fatalf("read cloned compose: %v", rerr)
	}
	if !strings.Contains(string(clonedBytes), `thulu.type: "from-probe"`) {
		t.Errorf("from-probe compose not rewritten from probe:\n%s", clonedBytes)
	}
}

func TestIntegration_AgentFiles_CreateAgent_DuplicateName(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agdup@test.com", "AD", "SecurePassword1")
	setupAgent(t, srv, "probe")
	setupAgent(t, srv, "existing")

	cw := postJSONAuth(t, h, "/types", token, map[string]any{
		"name":     "existing",
		"template": "probe",
	})
	if cw.Code != http.StatusConflict {
		t.Errorf("duplicate agent create = %d, want 409", cw.Code)
	}
}

func TestIntegration_AgentFiles_CreateAgent_InvalidName(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "aginv@test.com", "AI", "SecurePassword1")

	cw := postJSONAuth(t, h, "/types", token, map[string]any{
		"name": "bad name",
	})
	if cw.Code != http.StatusBadRequest {
		t.Errorf("invalid name = %d, want 400", cw.Code)
	}
}

func TestIntegration_ListTypes_Empty(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "types@test.com", "TL", "SecurePassword1")

	os.MkdirAll(filepath.Join(srv.agentsDir, "agents"), 0755)

	w := getJSON(t, h, "/types", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /types = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "null" && body != "[]" && !strings.HasPrefix(body, "[") {
		t.Errorf("expected array or null response, got %q", body)
	}
}

func TestIntegration_ProjectStageFiles_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "psfnf@test.com", "SF", "SecurePassword1")

	w := getJSON(t, h, "/projects/nonexistent-id/stage-files", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET stage files bad project = %d, want 404", w.Code)
	}
}

func TestIntegration_ProjectStageFiles_ReadInvalidKind(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "psfik@test.com", "IK", "SecurePassword1")

	w := getJSON(t, h, "/projects/nonexistent/stage-files/invalid-kind/refinement", token)
	if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
		t.Errorf("GET stage file bad kind = %d, want 404 or 400", w.Code)
	}
}

func TestIntegration_AgentPrompts_List(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "aprlist@test.com", "PR", "SecurePassword1")
	setupAgent(t, srv, "probe")

	promptsDir := filepath.Join(srv.agentsDir, "agents", "probe", "prompts")
	os.MkdirAll(promptsDir, 0755)
	os.WriteFile(filepath.Join(promptsDir, "main.md"), []byte("# Main\n"), 0644)

	w := getJSON(t, h, "/types/probe/prompts", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /types/probe/prompts = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["sets"] == nil {
		t.Error("expected sets field in response")
	}
}

func TestIntegration_AgentPrompts_NotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "aprnf@test.com", "PN", "SecurePassword1")

	w := getJSON(t, h, "/types/nonexistent/prompts", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /types/nonexistent/prompts = %d, want 404", w.Code)
	}
}

func TestIntegration_Agents_ListEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "aglist@test.com", "AL", "SecurePassword1")

	w := getJSON(t, h, "/agents", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents = %d, want 200", w.Code)
	}
	var agents []any
	json.NewDecoder(w.Body).Decode(&agents)
	if len(agents) != 0 {
		t.Errorf("expected empty agents list, got %d", len(agents))
	}
}

func TestIntegration_Agents_GetNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "agget@test.com", "AG", "SecurePassword1")

	w := getJSON(t, h, "/agents/nonexistent-id", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /agents/nonexistent = %d, want 404", w.Code)
	}
}

func TestIntegration_Agents_StopNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "agstop@test.com", "AS", "SecurePassword1")

	r := httptest.NewRequest(http.MethodPost, "/agents/missing-id/stop", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /agents/missing/stop = %d, want 404", w.Code)
	}
}

func TestIntegration_Agents_LogsNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "aglogs@test.com", "LG", "SecurePassword1")

	w := getJSON(t, h, "/agents/missing-id/logs", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /agents/missing/logs = %d, want 404", w.Code)
	}
}

func TestIntegration_Chat_Reset(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chclr@test.com", "CC", "SecurePassword1")
	setupChatAgent(t, srv, "chatty-reset")
	id := createChat(t, h, token, "chatty-reset")

	w := postJSONAuth(t, h, "/chats/"+id+"/reset", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /chats/{id}/reset = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["reset"] != true {
		t.Error("expected reset=true in response")
	}

	srv.chatSvc.Runtime().BeginTurn(id)
	defer srv.chatSvc.Runtime().EndTurn(id)
	if w := postJSONAuth(t, h, "/chats/"+id+"/reset", token, nil); w.Code != http.StatusConflict {
		t.Errorf("reset during turn = %d, want 409", w.Code)
	}
}

func TestIntegration_Project_CreateAndGet(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projcg@test.com", "PC", "SecurePassword1")
	repo := testgit.BareRepo(t)

	b, _ := json.Marshal(map[string]any{"name": "My Project", "repo_url": repo})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	h.ServeHTTP(wc, req)
	if wc.Code != http.StatusCreated {
		t.Fatalf("POST /projects = %d, want 201: %s", wc.Code, wc.Body.String())
	}
	var proj map[string]any
	json.NewDecoder(wc.Body).Decode(&proj)
	id, _ := proj["id"].(string)
	if id == "" {
		t.Fatal("expected project id")
	}

	gw := getJSON(t, h, "/projects/"+id, token)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET /projects/%s = %d, want 200", id, gw.Code)
	}
	var got map[string]any
	json.NewDecoder(gw.Body).Decode(&got)
	if got["name"] != "My Project" {
		t.Errorf("name = %v", got["name"])
	}
}

func TestIntegration_Project_CreateMissingFields(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projmf@test.com", "PM", "SecurePassword1")

	b, _ := json.Marshal(map[string]any{"name": "NoRepo"})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create project missing repo_url = %d, want 400", w.Code)
	}
}

func TestIntegration_Project_GetNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projgnf@test.com", "PG", "SecurePassword1")

	w := getJSON(t, h, "/projects/ghost", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /projects/ghost = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_Project_UpdateAndDelete(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projud@test.com", "PU", "SecurePassword1")
	repo := testgit.BareRepo(t)

	b, _ := json.Marshal(map[string]any{"name": "Proj", "repo_url": repo})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	h.ServeHTTP(wc, req)
	var proj map[string]any
	json.NewDecoder(wc.Body).Decode(&proj)
	id := proj["id"].(string)

	desc := "Updated description"
	pw := patchJSONAuth(t, h, "/projects/"+id, token, map[string]any{"description": desc})
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH /projects/%s = %d: %s", id, pw.Code, pw.Body.String())
	}

	dw := deleteRequest(t, h, "/projects/"+id, token)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE /projects/%s = %d: %s", id, dw.Code, dw.Body.String())
	}

	gw := getJSON(t, h, "/projects/"+id, token)
	if gw.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", gw.Code)
	}
}

func TestIntegration_Task_CreateMissingTitle(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskmiss@test.com", "TM", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"body": "no title"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("create task no title = %d, want 400", w.Code)
	}
}

func TestIntegration_Task_GetNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskgnf@test.com", "TG", "SecurePassword1")

	w := getJSON(t, h, "/tasks/does-not-exist", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/does-not-exist = %d, want 404", w.Code)
	}
}

func TestIntegration_Task_UpdateNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskupnf@test.com", "UN", "SecurePassword1")

	wp := patchJSONAuth(t, h, "/tasks/ghost", token, map[string]any{"title": "x"})
	if wp.Code != http.StatusNotFound {
		t.Errorf("PATCH /tasks/ghost = %d, want 404", wp.Code)
	}
}

func TestIntegration_Task_DeleteNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskdnf@test.com", "DN", "SecurePassword1")

	dw := deleteRequest(t, h, "/tasks/ghost", token)
	if dw.Code != http.StatusOK && dw.Code != http.StatusNotFound && dw.Code != http.StatusInternalServerError {
		t.Errorf("DELETE /tasks/ghost = %d, unexpected status", dw.Code)
	}
}

func TestIntegration_Task_AddComment(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskcom@test.com", "CM", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "Commented")

	cw := postJSONAuth(t, h, "/tasks/"+id+"/comments", token, map[string]any{"body": "looks good"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST comment = %d: %s", cw.Code, cw.Body.String())
	}
	var comment map[string]any
	json.NewDecoder(cw.Body).Decode(&comment)
	if comment["body"] != "looks good" {
		t.Errorf("comment body = %v", comment["body"])
	}
}

func TestIntegration_Task_AddCommentMissingBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskcmb@test.com", "CB", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "Ct")

	cw := postJSONAuth(t, h, "/tasks/"+id+"/comments", token, map[string]any{"body": ""})
	if cw.Code != http.StatusBadRequest {
		t.Errorf("empty comment = %d, want 400", cw.Code)
	}
}

func TestIntegration_Task_Brief_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskbrnf@test.com", "BN", "SecurePassword1")

	w := getJSON(t, h, "/tasks/ghost/brief", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/ghost/brief = %d, want 404", w.Code)
	}
}

func TestIntegration_Settings_PutValidation(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "settmax@test.com", "SM", "SecurePassword1")

	cases := []struct {
		name, key, raw string
		want           int
	}{
		{"sandbox.max valid", "sandbox.max", `4`, http.StatusNoContent},
		{"sandbox.max zero", "sandbox.max", `0`, http.StatusBadRequest},
		{"sandbox.max wrong type", "sandbox.max", `"not-a-number"`, http.StatusBadRequest},
		{"priorities valid", "sandbox.priorities", `{"refinement":10,"in_progress":5}`, http.StatusNoContent},
		{"priorities wrong type", "sandbox.priorities", `"not-a-map"`, http.StatusBadRequest},
		{"idle_evict valid", "sandbox.idle_evict_seconds", `300`, http.StatusNoContent},
		{"idle_evict negative", "sandbox.idle_evict_seconds", `-1`, http.StatusBadRequest},
		{"acquire_block valid", "sandbox.acquire_block_ms", `5000`, http.StatusNoContent},
		{"acquire_block wrong type", "sandbox.acquire_block_ms", `"not-a-number"`, http.StatusBadRequest},
		{"invalid JSON body", "sandbox_max_concurrent", `{bad json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/settings/"+c.key, strings.NewReader(c.raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Errorf("PUT /settings/%s body=%s = %d, want %d: %s", c.key, c.raw, w.Code, c.want, w.Body.String())
			}
		})
	}
}

func TestIntegration_Audit_List(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "audit@test.com", "AU", "SecurePassword1")

	w := getJSON(t, h, "/audit", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /audit = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_Audit_VerifyIsOperatorOnly(t *testing.T) {
	_, h := newIntegrationServer(t)
	tenant := registerUser(t, h, "auditv-tenant@test.com", "AT", "SecurePassword1")
	operator := registerCatalogOperator(t, h, "auditv-op@test.com", "AO", "SecurePassword1")

	if w := getJSON(t, h, "/audit/verify", tenant); w.Code != http.StatusForbidden {
		t.Fatalf("tenant admin GET /audit/verify = %d, want 403: %s", w.Code, w.Body.String())
	}

	w := getJSON(t, h, "/audit/verify", operator)
	if w.Code != http.StatusOK {
		t.Fatalf("operator GET /audit/verify = %d: %s", w.Code, w.Body.String())
	}
	var rep struct {
		OK      bool `json:"ok"`
		Checked int  `json:"checked"`
	}
	if err := json.NewDecoder(w.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Errorf("fresh audit log reported broken: %+v", rep)
	}
}

func TestIntegration_Audit_QueryParams(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "auditqp@test.com", "AQ", "SecurePassword1")

	w := getJSON(t, h, "/audit?actor_id=system&resource_id=task-1&kind=task.created&limit=5&offset=0&since=2020-01-01T00:00:00Z&until=2030-01-01T00:00:00Z", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /audit with params = %d: %s", w.Code, w.Body.String())
	}
	var entries []any
	json.NewDecoder(w.Body).Decode(&entries)
	if entries == nil {
		t.Error("expected non-nil (empty) entries slice")
	}
}

func TestIntegration_Audit_InvalidTimeParams(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "auditit@test.com", "AT", "SecurePassword1")

	w := getJSON(t, h, "/audit?since=notadate&until=alsonotadate", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /audit with invalid times = %d, want 200", w.Code)
	}
}

func TestIntegration_AgentSettings_Put(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsput@test.com", "AP", "SecurePassword1")
	setupAgent(t, srv, "probe-put")

	settings := AgentSettings{
		Name:                 "probe-put",
		Description:          "Test agent",
		ValidStages:          []string{"refinement", "done"},
		Model:                "claude-sonnet-4-6",
		TurnsByStage:         map[string]string{"refinement": "5"},
		SkillsByStage:        map[string][]string{},
		MCPByStage:           map[string][]string{},
		AuditorStages:        []string{},
		AuditorTurnsByStage:  map[string]string{},
		AuditorModelByStage:  map[string]string{},
		AuditorSkillsByStage: map[string][]string{},
	}
	w := putJSON(t, h, "/types/probe-put/settings", settings, token)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /types/probe-put/settings = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp AgentSettings
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Name != "probe-put" {
		t.Errorf("name = %q, want probe-put", resp.Name)
	}
}

func TestIntegration_AgentSettings_Put_UpdatesCompose(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agspuc@test.com", "APC", "SecurePassword1")
	setupAgent(t, srv, "probe-compose")

	settings := AgentSettings{
		Name:                 "probe-compose",
		Description:          "Updated description",
		ValidStages:          []string{"refinement", "done"},
		Model:                "claude-haiku-4-5-20251001",
		TurnsByStage:         map[string]string{"refinement": "8"},
		SkillsByStage:        map[string][]string{"refinement": {"visual-verification"}},
		MCPByStage:           map[string][]string{},
		AuditorStages:        []string{"done"},
		AuditorTurnsByStage:  map[string]string{},
		AuditorModelByStage:  map[string]string{},
		AuditorSkillsByStage: map[string][]string{},
	}
	w := putJSON(t, h, "/types/probe-compose/settings", settings, token)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /types/probe-compose/settings = %d, want 200: %s", w.Code, w.Body.String())
	}

	agentDir := filepath.Join(srv.agentsDir, "agents", "probe-compose")
	data, err := os.ReadFile(filepath.Join(agentDir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composed := string(data)
	if !strings.Contains(composed, "claude-haiku-4-5-20251001") {
		t.Errorf("compose file missing model label: %s", composed)
	}
	if !strings.Contains(composed, "Updated description") {
		t.Errorf("compose file missing description: %s", composed)
	}
}

func TestIntegration_AgentSettings_Put_NotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agspnf@test.com", "ANF", "SecurePassword1")

	settings := AgentSettings{Name: "ghost", ValidStages: []string{"refinement"}}
	w := putJSON(t, h, "/types/ghost/settings", settings, token)
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT /types/ghost/settings = %d, want 404", w.Code)
	}
}

func TestIntegration_AgentSettings_Put_InvalidBody(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agspib@test.com", "AIB", "SecurePassword1")
	setupAgent(t, srv, "probe-badput")

	r := httptest.NewRequest(http.MethodPut, "/types/probe-badput/settings", strings.NewReader("not json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT with invalid body = %d, want 400", w.Code)
	}
}

func TestIntegration_AgentSettings_Put_WithAuditor(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agspaud@test.com", "AUD", "SecurePassword1")
	setupAgent(t, srv, "probe-auditor")

	settings := AgentSettings{
		Name:                 "probe-auditor",
		ValidStages:          []string{"refinement", "done"},
		AuditorStages:        []string{"done"},
		AuditorModelByStage:  map[string]string{"done": "claude-haiku-4-5-20251001"},
		AuditorSkillsByStage: map[string][]string{"done": {"audit-skill"}},
		AuditorTurnsByStage:  map[string]string{"done": "3"},
		TurnsByStage:         map[string]string{},
		SkillsByStage:        map[string][]string{},
		MCPByStage:           map[string][]string{},
	}
	w := putJSON(t, h, "/types/probe-auditor/settings", settings, token)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /types/probe-auditor/settings = %d, want 200: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(srv.agentsDir, "agents", "probe-auditor", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composed := string(data)
	if !strings.Contains(composed, "thulu.auditor.stages") {
		t.Errorf("compose missing auditor.stages: %s", composed)
	}
}

func TestIntegration_AgentSettings_AuditorEngine(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agspeng@test.com", "ENG", "SecurePassword1")
	setupAgent(t, srv, "probe-engine")

	put := func(engines map[string]string) *httptest.ResponseRecorder {
		return putJSON(t, h, "/types/probe-engine/settings", AgentSettings{
			Name:                 "probe-engine",
			ValidStages:          []string{"refinement", "done"},
			AuditorStages:        []string{"done"},
			AuditorEngineByStage: engines,
			TurnsByStage:         map[string]string{},
			SkillsByStage:        map[string][]string{},
			MCPByStage:           map[string][]string{},
		}, token)
	}

	if w := put(map[string]string{"done": "no-such-engine"}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown engine = %d, want 400: %s", w.Code, w.Body.String())
	}

	w := put(map[string]string{"done": " codex ", "refinement": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	var saved AgentSettings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if got := saved.AuditorEngineByStage; got["done"] != "codex" || len(got) != 1 {
		t.Errorf("saved engines = %v, want only done=codex", got)
	}

	data, err := os.ReadFile(filepath.Join(srv.agentsDir, "agents", "probe-engine", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "thulu.auditor.engine.done") {
		t.Errorf("compose has no auditor engine label: %s", data)
	}

	if w := put(map[string]string{}); w.Code != http.StatusOK {
		t.Fatalf("clearing = %d: %s", w.Code, w.Body.String())
	}
	data, err = os.ReadFile(filepath.Join(srv.agentsDir, "agents", "probe-engine", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "thulu.auditor.engine.") {
		t.Errorf("the old engine label stayed after clearing: %s", data)
	}
}

func TestParseScopesHeader(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"repo, read:org", []string{"repo", "read:org"}},
		{"repo,read:org,workflow", []string{"repo", "read:org", "workflow"}},
		{" repo ", []string{"repo"}},
	}
	for _, c := range cases {
		got := parseScopesHeader(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseScopesHeader(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("parseScopesHeader(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestMergeAssignments(t *testing.T) {
	base := map[task.Stage]string{"refinement": "probe", "in_progress": "sonnet"}
	overrides := map[task.Stage]string{"in_progress": "opus"}

	got := mergeAssignments(base, overrides)
	if got["refinement"] != "probe" {
		t.Errorf("refinement = %q, want probe", got["refinement"])
	}
	if got["in_progress"] != "opus" {
		t.Errorf("in_progress = %q, want opus (override)", got["in_progress"])
	}

	got2 := mergeAssignments(base, nil)
	if len(got2) != len(base) {
		t.Errorf("empty override changed length: %v", got2)
	}
}

func TestIntegration_Task_Clone(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskclone@test.com", "CL", "SecurePassword1")

	b, _ := json.Marshal(map[string]any{"title": "Source Task", "body": "body"})
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	h.ServeHTTP(wc, req)
	var created map[string]any
	json.NewDecoder(wc.Body).Decode(&created)
	id := created["id"].(string)

	cr := httptest.NewRequest(http.MethodPost, "/tasks/"+id+"/clone", nil)
	cr.Header.Set("Authorization", "Bearer "+token)
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, cr)
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks/%s/clone = %d: %s", id, cw.Code, cw.Body.String())
	}
	var clone map[string]any
	json.NewDecoder(cw.Body).Decode(&clone)
	if clone["title"] != "Copy of Source Task" {
		t.Errorf("clone title = %q, want 'Copy of Source Task'", clone["title"])
	}
	if clone["id"] == id {
		t.Error("clone should have a different ID")
	}

	er := httptest.NewRequest(http.MethodPost, "/tasks/"+id+"/clone", nil)
	er.Header.Set("Authorization", "Bearer "+token)
	er.Header.Set("Accept-Language", "es")
	ew := httptest.NewRecorder()
	h.ServeHTTP(ew, er)
	var esClone map[string]any
	json.NewDecoder(ew.Body).Decode(&esClone)
	if ew.Code != http.StatusCreated || esClone["title"] != "Copia de Source Task" {
		t.Errorf("es clone = %d %q, want 'Copia de Source Task'", ew.Code, esClone["title"])
	}
}

func TestIntegration_Task_Clone_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskclnf@test.com", "CN", "SecurePassword1")

	cr := httptest.NewRequest(http.MethodPost, "/tasks/ghost/clone", nil)
	cr.Header.Set("Authorization", "Bearer "+token)
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, cr)
	if cw.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/ghost/clone = %d, want 404", cw.Code)
	}
}

func TestIntegration_AgentFiles_DeleteAgent_NotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "delagentf@test.com", "DF", "SecurePassword1")

	dr := httptest.NewRequest(http.MethodDelete, "/types/ghost-agent", nil)
	dr.Header.Set("Authorization", "Bearer "+token)
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, dr)
	if dw.Code != http.StatusNotFound {
		t.Errorf("DELETE /types/ghost-agent = %d, want 404", dw.Code)
	}
}

func TestValidKind(t *testing.T) {
	if !validKind("prompts") {
		t.Error("prompts should be valid")
	}
	if !validKind("hooks") {
		t.Error("hooks should be valid")
	}
	if validKind("scripts") {
		t.Error("scripts should be invalid")
	}
	if validKind("") {
		t.Error("empty string should be invalid")
	}
	if validKind("../etc") {
		t.Error("path traversal should be invalid")
	}
}

func TestStageFilePath(t *testing.T) {
	base := "/data/sidecar"
	cases := []struct {
		kind, stage string
		wantSuffix  string
		wantEmpty   bool
	}{
		{"prompts", "backlog", "/data/sidecar/prompts/backlog.md", false},
		{"hooks", "testing", "/data/sidecar/hooks/testing.sh", false},
		{"prompts", "draft", "/data/sidecar/prompts/draft.md", false},
		{"invalid", "backlog", "", true},
		{"prompts", "BACKLOG", "", true},
		{"prompts", "../../etc", "", true},
		{"prompts", "with-hyphen", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		got := stageFilePath(base, c.kind, c.stage)
		if c.wantEmpty {
			if got != "" {
				t.Errorf("stageFilePath(%q,%q,%q) = %q, want empty", base, c.kind, c.stage, got)
			}
		} else {
			if got != c.wantSuffix {
				t.Errorf("stageFilePath(%q,%q,%q) = %q, want %q", base, c.kind, c.stage, got, c.wantSuffix)
			}
		}
	}
}

func TestBuildStageSet(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "prompts"), 0755)
	os.WriteFile(filepath.Join(dir, "prompts", "backlog.md"), []byte("# Backlog\n"), 0644)

	srv := &Server{boards: stagepkg.StaticBoards()}
	set := srv.buildStageSet(srv.boards.Catalog(), dir, "prompts", "Stage prompts", "desc")
	if set.Kind != "prompts" {
		t.Errorf("kind = %q, want prompts", set.Kind)
	}
	if len(set.Stages) == 0 {
		t.Error("expected at least one stage entry")
	}
	var backlogEntry *ProjectStageEntry
	for i := range set.Stages {
		if set.Stages[i].Stage == "backlog" {
			backlogEntry = &set.Stages[i]
		}
	}
	if backlogEntry == nil {
		t.Fatal("backlog stage not found in set")
	}
	if !backlogEntry.Exists {
		t.Error("backlog.md was written; Exists should be true")
	}
	if backlogEntry.Path != "prompts/backlog.md" {
		t.Errorf("path = %q, want prompts/backlog.md", backlogEntry.Path)
	}
}

func createProject(t *testing.T, h http.Handler, token, name string) string {
	t.Helper()
	repo := testgit.BareRepo(t)
	b, _ := json.Marshal(map[string]any{"name": name, "repo_url": repo})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("createProject: POST /projects = %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("createProject: no id in response")
	}
	return id
}

func TestIntegration_ProjectStageFiles_ListAndWriteAndDelete(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "psf@test.com", "SF", "SecurePassword1")
	id := createProject(t, h, token, "Stage Files Project")

	wList := getJSON(t, h, "/projects/"+id+"/stage-files", token)
	if wList.Code != http.StatusOK {
		t.Fatalf("GET /projects/%s/stage-files = %d: %s", id, wList.Code, wList.Body.String())
	}
	var listResp map[string]any
	json.NewDecoder(wList.Body).Decode(&listResp)
	if listResp["sets"] == nil {
		t.Error("expected sets field")
	}

	body := []byte("# Backlog instructions\n")
	wPut := httptest.NewRequest(http.MethodPut, "/projects/"+id+"/stage-files/prompts/backlog", bytes.NewReader(body))
	wPut.Header.Set("Authorization", "Bearer "+token)
	wPutRec := httptest.NewRecorder()
	h.ServeHTTP(wPutRec, wPut)
	if wPutRec.Code != http.StatusOK {
		t.Fatalf("PUT prompt = %d: %s", wPutRec.Code, wPutRec.Body.String())
	}

	wRead := getJSON(t, h, "/projects/"+id+"/stage-files/prompts/backlog", token)
	if wRead.Code != http.StatusOK {
		t.Fatalf("GET prompt = %d: %s", wRead.Code, wRead.Body.String())
	}
	if wRead.Body.String() != string(body) {
		t.Errorf("body = %q, want %q", wRead.Body.String(), string(body))
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/projects/"+id+"/stage-files/prompts/backlog", nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE prompt = %d: %s", delRec.Code, delRec.Body.String())
	}

	delReq2 := httptest.NewRequest(http.MethodDelete, "/projects/"+id+"/stage-files/prompts/backlog", nil)
	delReq2.Header.Set("Authorization", "Bearer "+token)
	delRec2 := httptest.NewRecorder()
	h.ServeHTTP(delRec2, delReq2)
	if delRec2.Code != http.StatusNotFound {
		t.Errorf("DELETE already gone = %d, want 404", delRec2.Code)
	}
}

func TestIntegration_ProjectStageFiles_ReadNonexistent(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "psfrnx@test.com", "RN", "SecurePassword1")
	id := createProject(t, h, token, "Empty Prompts")

	w := getJSON(t, h, "/projects/"+id+"/stage-files/prompts/backlog", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET nonexistent prompt = %d, want 200 empty", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("expected empty body for nonexistent file, got %q", w.Body.String())
	}
}

func TestIntegration_ProjectStageFiles_InvalidKind(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "psfik2@test.com", "IK2", "SecurePassword1")
	id := createProject(t, h, token, "Bad Kind")

	w := getJSON(t, h, "/projects/"+id+"/stage-files/scripts/backlog", token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET invalid kind = %d, want 400", w.Code)
	}
}

func TestIntegration_ProjectStageFiles_HookFile(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "psfhook@test.com", "HK", "SecurePassword1")
	id := createProject(t, h, token, "Hook Project")

	hookBody := []byte("#!/bin/bash\necho 'running tests'\n")
	wPut := httptest.NewRequest(http.MethodPut, "/projects/"+id+"/stage-files/hooks/testing", bytes.NewReader(hookBody))
	wPut.Header.Set("Authorization", "Bearer "+token)
	wPutRec := httptest.NewRecorder()
	h.ServeHTTP(wPutRec, wPut)
	if wPutRec.Code != http.StatusOK {
		t.Fatalf("PUT hook = %d: %s", wPutRec.Code, wPutRec.Body.String())
	}

	wRead := getJSON(t, h, "/projects/"+id+"/stage-files/hooks/testing", token)
	if wRead.Code != http.StatusOK {
		t.Fatalf("GET hook = %d", wRead.Code)
	}
	if wRead.Body.String() != string(hookBody) {
		t.Errorf("hook body mismatch: got %q", wRead.Body.String())
	}
}

func TestIntegration_Task_UpdateInvalidBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tupd@test.com", "TU", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "update target"})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+id, bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH /tasks with invalid JSON = %d, want 400", w.Code)
	}
}

func TestIntegration_Task_UpdateInvalidStage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tupd3@test.com", "TU3", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "stage test"})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+id, bytes.NewReader([]byte(`{"stage":"not-a-stage"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH /tasks with invalid stage = %d, want 400", w.Code)
	}
}

func TestIntegration_TaskDeps_InvalidBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tdepinv@test.com", "TDI", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "dep target"})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+id+"/dependencies", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST dep invalid body = %d, want 400", w.Code)
	}
}

func TestIntegration_TaskDeps_MissingDependsOn(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tdepempty@test.com", "TDE", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "dep target 2"})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+id+"/dependencies", bytes.NewReader([]byte(`{"depends_on_task_id":""}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST dep missing depends_on = %d, want 400", w.Code)
	}
}

func TestIntegration_TaskDeps_InvalidKind(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tdepkind@test.com", "TDK", "SecurePassword1")

	cw1 := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "blocker"})
	var t1 map[string]any
	json.NewDecoder(cw1.Body).Decode(&t1)
	id1 := t1["id"].(string)

	cw2 := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "blocked"})
	var t2 map[string]any
	json.NewDecoder(cw2.Body).Decode(&t2)
	id2 := t2["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+id2+"/dependencies",
		bytes.NewReader([]byte(`{"depends_on_task_id":"`+id1+`","kind":"invalid-kind"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST dep invalid kind = %d, want 400", w.Code)
	}
}

func TestIntegration_TaskDeps_HardDepAfterBacklog(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tdepstage@test.com", "TDS", "SecurePassword1")

	cw1 := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "blocker task"})
	var t1 map[string]any
	json.NewDecoder(cw1.Body).Decode(&t1)
	id1 := t1["id"].(string)

	cw2 := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "in-progress task"})
	var t2 map[string]any
	json.NewDecoder(cw2.Body).Decode(&t2)
	id2 := t2["id"].(string)

	patchReq := httptest.NewRequest(http.MethodPatch, "/tasks/"+id2,
		bytes.NewReader([]byte(`{"stage":"refinement"}`)))
	patchReq.Header.Set("Authorization", "Bearer "+token)
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	h.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("setup: stage update failed (%d): %s", patchRec.Code, patchRec.Body.String())
	}

	depReq := httptest.NewRequest(http.MethodPost, "/tasks/"+id2+"/dependencies",
		bytes.NewReader([]byte(`{"depends_on_task_id":"`+id1+`","kind":"hard"}`)))
	depReq.Header.Set("Authorization", "Bearer "+token)
	depReq.Header.Set("Content-Type", "application/json")
	depRec := httptest.NewRecorder()
	h.ServeHTTP(depRec, depReq)
	if depRec.Code != http.StatusConflict {
		t.Errorf("hard dep after backlog = %d, want 409", depRec.Code)
	}
}

func TestIntegration_Webhooks_TestEndpoint(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "whtest@test.com", "WHT", "SecurePassword1")

	wb, _ := json.Marshal(map[string]any{"url": "https://unreachable.invalid/hook", "events": []string{"*"}})
	wr := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(wb))
	wr.Header.Set("Authorization", "Bearer "+token)
	wr.Header.Set("Content-Type", "application/json")
	wRec := httptest.NewRecorder()
	h.ServeHTTP(wRec, wr)
	var ep map[string]any
	json.NewDecoder(wRec.Body).Decode(&ep)
	epID, _ := ep["id"].(string)

	tr := httptest.NewRequest(http.MethodPost, "/webhooks/"+epID+"/test", nil)
	tr.Header.Set("Authorization", "Bearer "+token)
	tRec := httptest.NewRecorder()
	h.ServeHTTP(tRec, tr)
	if tRec.Code != http.StatusOK {
		t.Errorf("POST /webhooks/%s/test = %d, want 200", epID, tRec.Code)
	}
	var result map[string]any
	json.NewDecoder(tRec.Body).Decode(&result)
	if result["endpoint_id"] != epID {
		t.Errorf("response endpoint_id = %v, want %q", result["endpoint_id"], epID)
	}
}

func TestIntegration_Project_UpdateInvalidBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projupdinv@test.com", "PUI", "SecurePassword1")
	id := createProject(t, h, token, "Update Invalid")

	req := httptest.NewRequest(http.MethodPatch, "/projects/"+id, bytes.NewReader([]byte("{bad")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH /projects invalid body = %d, want 400", w.Code)
	}
}

func TestIntegration_Task_BriefWithProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "briefproj@test.com", "BRP", "SecurePassword1")
	projID := createProject(t, h, token, "Brief Project")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "task with project",
		"body":        "needs repo",
		"project_ids": []string{projID},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	bw := getJSON(t, h, "/tasks/"+id+"/brief", token)
	if bw.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/brief = %d: %s", id, bw.Code, bw.Body.String())
	}
	body := bw.Body.String()
	if !strings.Contains(body, "Brief Project") {
		t.Errorf("brief should mention project name; body = %q", body)
	}
}

func TestIntegration_Task_UpdateFlagNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagupd@test.com", "FLU", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "flag task"})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+id+"/flags/no-such-flag",
		bytes.NewReader([]byte(`{"status":"approved"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("PATCH /tasks/.../flags/no-such = %d, want 404", w.Code)
	}
}

func TestIntegration_Settings_ListAfterPut(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "settlist@test.com", "SL", "SecurePassword1")

	sv, _ := json.Marshal(7)
	pr := httptest.NewRequest(http.MethodPut, "/settings/sandbox.max", bytes.NewReader(sv))
	pr.Header.Set("Content-Type", "application/json")
	pr.Header.Set("Authorization", "Bearer "+token)
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, pr)
	if pw.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", pw.Code, pw.Body.String())
	}

	lw := getJSON(t, h, "/settings", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", lw.Code)
	}
	var out map[string]json.RawMessage
	json.NewDecoder(lw.Body).Decode(&out)
	if out["sandbox.max"] == nil {
		t.Error("sandbox.max should appear in list after PUT")
	}
}

func TestIntegration_Audit_WithFilters(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "auditflt@test.com", "AFL", "SecurePassword1")

	w := getJSON(t, h, "/audit?limit=1&offset=0&actor_id=auditflt@test.com", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /audit with filters = %d, want 200", w.Code)
	}
	var entries []map[string]any
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) > 1 {
		t.Errorf("limit=1 returned %d entries", len(entries))
	}
}

func TestIntegration_Webhook_CreateDefaultEvents(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "whkevt@test.com", "WE", "SecurePassword1")

	cb, _ := json.Marshal(map[string]any{"url": "https://example.com/default-hook"})
	cr := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(cb))
	cr.Header.Set("Content-Type", "application/json")
	cr.Header.Set("Authorization", "Bearer "+token)
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, cr)
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d: %s", cw.Code, cw.Body.String())
	}
	var ep map[string]any
	json.NewDecoder(cw.Body).Decode(&ep)
	events, _ := ep["events"].([]any)
	if len(events) != 1 || events[0] != "*" {
		t.Errorf("default events = %v, want [*]", events)
	}
}

func TestIntegration_Task_EventsAndFlagsFlow(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "evtflg@test.com", "EF", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "events flow task"})
	var task map[string]any
	json.NewDecoder(cw.Body).Decode(&task)
	id := task["id"].(string)

	ew := getJSON(t, h, "/tasks/"+id+"/events", token)
	if ew.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/events = %d", id, ew.Code)
	}
	var events []map[string]any
	json.NewDecoder(ew.Body).Decode(&events)

	fw := postJSONAuth(t, h, "/tasks/"+id+"/flags", token, map[string]any{
		"label":      "needs clarification",
		"kind":       "human_input",
		"created_by": "bot",
	})
	if fw.Code != http.StatusCreated {
		t.Fatalf("POST flags = %d: %s", fw.Code, fw.Body.String())
	}

	lfw := getJSON(t, h, "/tasks/"+id+"/flags", token)
	if lfw.Code != http.StatusOK {
		t.Fatalf("GET flags = %d", lfw.Code)
	}
	var flags []map[string]any
	json.NewDecoder(lfw.Body).Decode(&flags)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if flags[0]["kind"] != "human_input" {
		t.Errorf("flag kind = %v, want human_input", flags[0]["kind"])
	}

	rw := getJSON(t, h, "/tasks/"+id+"/reports", token)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/reports = %d", id, rw.Code)
	}
}

func TestIntegration_Task_CreateFlag_TaskNotFound2(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flgnf2@test.com", "FT", "SecurePassword1")

	fw := postJSONAuth(t, h, "/tasks/no-such-task/flags", token, map[string]any{"label": "test"})
	if fw.Code != http.StatusNotFound {
		t.Errorf("POST flag on nonexistent task = %d, want 404", fw.Code)
	}
}

func TestIntegration_Task_UpdateFlag_InvalidBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flgib@test.com", "FB", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "flag invalid"})
	var task map[string]any
	json.NewDecoder(cw.Body).Decode(&task)
	id := task["id"].(string)

	fw := postJSONAuth(t, h, "/tasks/"+id+"/flags", token, map[string]any{"label": "test flag"})
	var flag map[string]any
	json.NewDecoder(fw.Body).Decode(&flag)
	flagID := flag["id"].(string)

	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+id+"/flags/"+flagID,
		bytes.NewReader([]byte("{bad json")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PATCH flag with bad JSON = %d, want 400", w.Code)
	}
}

func TestIntegration_ListAgents_WithFakeAgent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	srv.registry.Upsert(&agent.Agent{
		ID:        "fake-container-id-abc",
		Name:      "sonnet-test",
		Type:      "sonnet",
		Status:    agent.StatusRunning,
		StartedAt: time.Now().Add(-30 * time.Second),
		OutputDir: "/tmp/fake-output",
	})

	w := getJSON(t, h, "/agents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents = %d, want 200", w.Code)
	}
	var agents []map[string]any
	json.NewDecoder(w.Body).Decode(&agents)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0]["id"] != "fake-container-id-abc" {
		t.Errorf("agent id = %v, want fake-container-id-abc", agents[0]["id"])
	}
}

func TestIntegration_GetAgent_HappyPath(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	srv.registry.Upsert(&agent.Agent{
		ID:        "get-agent-test-id",
		Name:      "haiku-agent",
		Type:      "haiku",
		Status:    agent.StatusStopped,
		StartedAt: time.Now().Add(-5 * time.Minute),
		OutputDir: "/tmp/fake-output",
	})

	w := getJSON(t, h, "/agents/get-agent-test-id", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents/get-agent-test-id = %d, want 200", w.Code)
	}
	var agent map[string]any
	json.NewDecoder(w.Body).Decode(&agent)
	if agent["name"] != "haiku-agent" {
		t.Errorf("agent name = %v, want haiku-agent", agent["name"])
	}
}

func TestIntegration_RemoveAgent_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/agents/nonexistent-agent-id", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE /agents/nonexistent = %d, want 404", w.Code)
	}
}

func setupSkillsDir(t *testing.T, srv *Server) {
	t.Helper()
	skillsDir := filepath.Join(srv.agentsDir, "skills")
	os.MkdirAll(skillsDir, 0755)

	skill1 := filepath.Join(skillsDir, "code-review")
	os.MkdirAll(skill1, 0755)
	os.WriteFile(filepath.Join(skill1, "prompt.md"), []byte("# Code Review\n"), 0644)
	os.WriteFile(filepath.Join(skill1, "skill.yaml"), []byte("name: code-review\ndescription: Reviews code\ntype: knowledge\n"), 0644)

	skill2 := filepath.Join(skillsDir, "visual-analysis")
	os.MkdirAll(skill2, 0755)
	os.WriteFile(filepath.Join(skill2, "prompt.md"), []byte("# Visual Analysis\n"), 0644)
	os.MkdirAll(filepath.Join(skill2, "scripts"), 0755)

	skill3 := filepath.Join(skillsDir, "basic-skill")
	os.MkdirAll(skill3, 0755)
	os.WriteFile(filepath.Join(skill3, "prompt.md"), []byte("# Basic\n"), 0644)

	base := filepath.Join(skillsDir, "_base")
	os.MkdirAll(base, 0755)
	os.WriteFile(filepath.Join(base, "prompt.md"), []byte("# Base\n"), 0644)

	hidden := filepath.Join(skillsDir, ".hidden")
	os.MkdirAll(hidden, 0755)

	noprompt := filepath.Join(skillsDir, "incomplete")
	os.MkdirAll(noprompt, 0755)

	os.WriteFile(filepath.Join(skillsDir, "README.md"), []byte("readme"), 0644)
}

func TestIntegration_ListSkills_HappyPath(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	setupSkillsDir(t, srv)

	token := registerUser(t, h, "skills-happy@test.com", "Skills", "SecurePassword1")
	w := getJSON(t, h, "/skills", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /skills = %d: %s", w.Code, w.Body.String())
	}
	var skills []map[string]any
	json.NewDecoder(w.Body).Decode(&skills)
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills (excluding _base, .hidden, incomplete), got %d: %v", len(skills), skills)
	}
	if skills[0]["name"] != "basic-skill" {
		t.Errorf("skills[0] name = %v, want basic-skill", skills[0]["name"])
	}
	if skills[1]["type"] != "knowledge" {
		t.Errorf("code-review type = %v, want knowledge", skills[1]["type"])
	}
	if skills[2]["has_scripts"] != true {
		t.Errorf("visual-analysis has_scripts = %v, want true", skills[2]["has_scripts"])
	}
}

func setupToolsDir(t *testing.T, srv *Server) {
	t.Helper()
	toolsDir := filepath.Join(srv.agentsDir, "tools")
	os.MkdirAll(toolsDir, 0755)

	tool1 := filepath.Join(toolsDir, "thulu-browser")
	os.MkdirAll(tool1, 0755)
	os.WriteFile(filepath.Join(tool1, "tool.yaml"), []byte(
		"name: thulu-browser\ndescription: Browser automation\ntype: mcp\ntools:\n  - name: screenshot\n    description: Take screenshot\n",
	), 0644)

	tool2 := filepath.Join(toolsDir, "no-yaml-tool")
	os.MkdirAll(tool2, 0755)

	dotDir := filepath.Join(toolsDir, ".ignored")
	os.MkdirAll(dotDir, 0755)

	os.WriteFile(filepath.Join(toolsDir, "docker-compose.yml"), []byte("version: '3'"), 0644)
}

func TestIntegration_ListTools_HappyPath(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	setupToolsDir(t, srv)

	w := getJSON(t, h, "/tools", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tools = %d: %s", w.Code, w.Body.String())
	}
	var tools []map[string]any
	json.NewDecoder(w.Body).Decode(&tools)
	tools = notBuiltin(tools)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (only valid yaml), got %d: %v", len(tools), tools)
	}
	if tools[0]["name"] != "thulu-browser" {
		t.Errorf("tool name = %v, want thulu-browser", tools[0]["name"])
	}
	if tools[0]["description"] != "Browser automation" || tools[0]["source"] != "disk" {
		t.Errorf("tool = %v, want the agents dir's thulu-browser", tools[0])
	}
}

func notBuiltin(tools []map[string]any) []map[string]any {
	var out []map[string]any
	for _, tl := range tools {
		if tl["source"] != "builtin" {
			out = append(out, tl)
		}
	}
	return out
}

func TestIntegration_ListTools_IncludesRegisteredMCP(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	mcpReg, err := mcp.NewRegistry(srv.taskStore.DB())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	srv.SetMCPRegistry(mcpReg)
	if _, err := mcpReg.Register(context.Background(), mcp.Registration{
		Name: "ticket-reader", URL: "http://ticket-reader.internal:3000/sse",
		Description: "Read-only tickets", CreatedBy: "admin@octopus.local",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	w := getJSON(t, h, "/tools", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tools = %d: %s", w.Code, w.Body.String())
	}
	var tools []map[string]any
	json.NewDecoder(w.Body).Decode(&tools)
	var found map[string]any
	for _, tl := range tools {
		if tl["name"] == "ticket-reader" {
			found = tl
			break
		}
	}
	if found == nil {
		t.Fatalf("registered MCP not in /tools: %v", tools)
	}
	if found["source"] != "registered" {
		t.Errorf("source = %v, want registered", found["source"])
	}
	if found["description"] != "Read-only tickets" {
		t.Errorf("description = %v", found["description"])
	}
}

func TestIntegration_ListTools_MissingDir(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/tools", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tools (no dir) = %d, want 200", w.Code)
	}
	var tools []map[string]any
	json.NewDecoder(w.Body).Decode(&tools)
	if tools == nil {
		t.Error("expected empty list, got nil")
	}
}

func TestIntegration_ListSkills_MissingDir(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "skills-missing@test.com", "Skills", "SecurePassword1")
	w := getJSON(t, h, "/skills", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /skills (no dir) = %d, want 200: %s", w.Code, w.Body.String())
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Fatalf("GET /skills (no dir) body = %q, want \"[]\"", body)
	}
}

func TestIntegration_ListTypes_HappyPath(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	agentsDir := filepath.Join(srv.agentsDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	w := getJSON(t, h, "/types", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /types = %d: %s", w.Code, w.Body.String())
	}
}

func setupAgentWithAuditor(t *testing.T, srv *Server, name string) string {
	t.Helper()
	agentDir := filepath.Join(srv.agentsDir, "agents", name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	os.WriteFile(filepath.Join(agentDir, "main.md"), []byte("# Agent\n"), 0644)
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "` + name + `"
      thulu.valid_stages: "refinement,done"
      thulu.auditor.stages: "refinement"
      thulu.auditor.turns.refinement: "3"
      thulu.auditor.model.refinement: "haiku"
      thulu.auditor.skills.refinement: "code-review"
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)
	return name
}

func TestIntegration_AgentSettings_Get_WithAuditor(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsaudit@test.com", "AAu", "SecurePassword1")
	setupAgentWithAuditor(t, srv, "probe-auditor")

	w := getJSON(t, h, "/types/probe-auditor/settings", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /types/probe-auditor/settings = %d: %s", w.Code, w.Body.String())
	}
	var settings map[string]any
	json.NewDecoder(w.Body).Decode(&settings)
	stages, ok := settings["auditor_stages"].([]any)
	if !ok || len(stages) == 0 {
		t.Errorf("expected auditor_stages to be populated, got %v", settings["auditor_stages"])
	}
}

func TestIntegration_AgentSettings_Put_WithFallbackService(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsfall@test.com", "AF2", "SecurePassword1")

	agentDir := filepath.Join(srv.agentsDir, "agents", "fallback-agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "main.md"), []byte("# Agent\n"), 0644)
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.type: "fallback-agent"
      thulu.valid_stages: "refinement"
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)

	settings := AgentSettings{
		Name:        "fallback-agent",
		ValidStages: []string{"refinement"},
	}
	b, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPut, "/types/fallback-agent/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("PUT /types/fallback-agent/settings = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_AgentSettings_Put_WithAuditorLabels(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsaud2@test.com", "AA2", "SecurePassword1")
	setupAgent(t, srv, "probe-put-auditor")

	settings := AgentSettings{
		Name:          "probe-put-auditor",
		ValidStages:   []string{"refinement", "done"},
		AuditorStages: []string{"refinement"},
		AuditorTurnsByStage: map[string]string{
			"refinement": "3",
			"done":       "",
		},
		AuditorModelByStage: map[string]string{
			"refinement": "haiku",
		},
		AuditorSkillsByStage: map[string][]string{
			"refinement": {"code-review"},
		},
		SkillsByStage: map[string][]string{
			"refinement": {"visual-analysis"},
			"done":       {},
		},
		MCPByStage: map[string][]string{
			"refinement": {"thulu-github"},
			"done":       {},
		},
	}
	b, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPut, "/types/probe-put-auditor/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("PUT /types/probe-put-auditor/settings = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_AgentSettings_Put_NoServicesInCompose(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsnos@test.com", "NS", "SecurePassword1")

	agentDir := filepath.Join(srv.agentsDir, "agents", "noservices-agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "main.md"), []byte("# Agent\n"), 0644)
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(`version: "3"`+"\n"), 0644)

	settings := AgentSettings{Name: "noservices-agent", ValidStages: []string{"refinement"}}
	b, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPut, "/types/noservices-agent/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PUT noservices compose = %d, want 500", w.Code)
	}
}

func TestIntegration_Task_CreateWithInvalidProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskbadproj@test.com", "TBP", "SecurePassword1")

	b, _ := json.Marshal(map[string]any{
		"title":       "Task with bad project",
		"project_ids": []string{"nonexistent-proj-id"},
	})
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /tasks with bad project = %d, want 400", w.Code)
	}
}

func TestIntegration_Task_CreateWithBackcompatProjectID(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskcompat@test.com", "TCOMPAT", "SecurePassword1")

	b, _ := json.Marshal(map[string]any{
		"title":      "Compat Task",
		"project_id": "nonexistent-single-proj",
	})
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /tasks with singular project_id bad = %d, want 400", w.Code)
	}
}

func TestIntegration_Task_DeleteWithLinkedProject(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskdellp@test.com", "TDLP", "SecurePassword1")
	projID := createProject(t, h, token, "Task Delete Project")

	b, _ := json.Marshal(map[string]any{
		"title":       "Task to delete",
		"project_ids": []string{projID},
	})
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	cr := httptest.NewRecorder()
	h.ServeHTTP(cr, req)
	var created map[string]any
	json.NewDecoder(cr.Body).Decode(&created)
	taskID, _ := created["id"].(string)
	if taskID == "" {
		t.Fatal("expected task ID")
	}
	_ = srv

	delReq := httptest.NewRequest(http.MethodDelete, "/tasks/"+taskID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, delReq)
	if dw.Code != http.StatusOK {
		t.Errorf("DELETE /tasks/%s = %d, want 200: %s", taskID, dw.Code, dw.Body.String())
	}
}

func TestIntegration_AgentFiles_WriteNotEditable(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "afwne@test.com", "WN", "SecurePassword1")
	setupAgent(t, srv, "probe-wne")

	w := putRequest(t, h, "/types/probe-wne/files/setup.sh", token, []byte("#!/bin/bash\n"))
	if w.Code != http.StatusForbidden {
		t.Errorf("PUT non-.md = %d, want 403", w.Code)
	}
}

func TestIntegration_AgentFiles_WritePathEscape(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "afwpe@test.com", "WE", "SecurePassword1")
	setupAgent(t, srv, "probe-wpe")

	req := httptest.NewRequest(http.MethodPut, "/types/probe-wpe/files/..%2F..%2Fetc%2Fpasswd.md", bytes.NewReader([]byte("x")))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("path escape PUT should not succeed, got 200")
	}
}

func TestIntegration_AgentFiles_WriteMkdirAllError(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afwmkdir@test.com", "WMK", "SecurePassword1")
	setupAgent(t, srv, "probe-wmkdir")

	agDir := filepath.Join(srv.agentsDir, "agents", "probe-wmkdir")
	os.WriteFile(filepath.Join(agDir, "subdir.md"), []byte("# existing file\n"), 0644)

	w := putRequest(t, h, "/types/probe-wmkdir/files/subdir.md/nested.md", token, []byte("# content\n"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PUT when MkdirAll fails = %d, want 500", w.Code)
	}
}

func TestIntegration_AgentFiles_ReadAgentNotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afranf@test.com", "RAF", "SecurePassword1")

	w := getJSON(t, h, "/types/nonexistent-agent/files/main.md", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /types/nonexistent/files/main.md = %d, want 404", w.Code)
	}
}

func TestIntegration_AgentFiles_CreateAgent_InvalidJSON(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afcainvjson@test.com", "CAJ", "SecurePassword1")

	req := httptest.NewRequest(http.MethodPost, "/types", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /types invalid JSON = %d, want 400", w.Code)
	}
}

func TestIntegration_AgentFiles_WriteAgentNotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afwnf@test.com", "WNF", "SecurePassword1")

	w := putRequest(t, h, "/types/nonexistent-agent/files/main.md", token, []byte("# content\n"))
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT nonexistent agent = %d, want 404", w.Code)
	}
}

func TestIntegration_AgentFiles_DeleteAgentNotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afdanf@test.com", "DAF", "SecurePassword1")

	w := deleteRequest(t, h, "/types/nonexistent-agent/files/main.md", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE nonexistent agent = %d, want 404", w.Code)
	}
}

func TestIntegration_AgentFiles_DeleteNotEditable(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "afdne@test.com", "DN", "SecurePassword1")
	setupAgent(t, srv, "probe-dne")

	w := deleteRequest(t, h, "/types/probe-dne/files/docker-compose.yml", token)
	if w.Code != http.StatusForbidden {
		t.Errorf("DELETE non-.md = %d, want 403", w.Code)
	}
}

func TestIntegration_AgentFiles_DeleteFileNotFound(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afdfnf@test.com", "FF", "SecurePassword1")
	setupAgent(t, srv, "probe-dfnf")

	w := deleteRequest(t, h, "/types/probe-dfnf/files/nonexistent-stage.md", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE nonexistent .md = %d, want 404", w.Code)
	}
}

func TestIntegration_ListProjects_WithProjects(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "prjlistp@test.com", "LP", "SecurePassword1")

	id := createProject(t, h, token, "My Project")
	if id == "" {
		t.Fatal("createProject returned empty id")
	}

	w := getJSON(t, h, "/projects", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /projects = %d, want 200", w.Code)
	}
	var projects []map[string]any
	json.NewDecoder(w.Body).Decode(&projects)
	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}
}

func TestIntegration_DeleteProject_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "delprj404@test.com", "DP", "SecurePassword1")

	w := deleteRequest(t, h, "/projects/nonexistent-id", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE /projects/nonexistent-id = %d, want 404", w.Code)
	}
}

func TestIntegration_DeleteProject_Valid(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "delprjv@test.com", "DV", "SecurePassword1")

	id := createProject(t, h, token, "Delete Me Project")

	w := deleteRequest(t, h, "/projects/"+id, token)
	if w.Code != http.StatusOK {
		t.Errorf("DELETE /projects/%s = %d, want 200: %s", id, w.Code, w.Body.String())
	}

	w2 := getJSON(t, h, "/projects/"+id, token)
	if w2.Code != http.StatusNotFound {
		t.Errorf("GET deleted project = %d, want 404", w2.Code)
	}
}

func TestIntegration_ListWorktrees_Empty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wktlist@test.com", "WL", "SecurePassword1")

	id := createProject(t, h, token, "Worktree Project")

	w := getJSON(t, h, "/projects/"+id+"/worktrees", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /projects/%s/worktrees = %d: %s", id, w.Code, w.Body.String())
	}
	var wts []any
	json.NewDecoder(w.Body).Decode(&wts)
	if len(wts) != 0 {
		t.Errorf("expected 0 worktrees, got %d", len(wts))
	}
}

func TestIntegration_Webhook_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wbhknf@test.com", "WD", "SecurePassword1")

	w := deleteRequest(t, h, "/webhooks/nonexistent-webhook-id", token)
	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE nonexistent webhook = %d, want 204 (idempotent)", w.Code)
	}
}

func TestIntegration_Task_ListWithStageFilter(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tklstflt@test.com", "TF", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "filtered task", "body": "desc", "project_ids": []string{},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d", cw.Code)
	}

	w := getJSON(t, h, "/tasks?stage=backlog", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks?stage=backlog = %d: %s", w.Code, w.Body.String())
	}
	var tasks []any
	json.NewDecoder(w.Body).Decode(&tasks)
	if len(tasks) == 0 {
		t.Error("expected at least 1 task in backlog stage")
	}
}

func TestIntegration_Task_ListWithNoneFilter(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tklstnone@test.com", "TN", "SecurePassword1")

	w := getJSON(t, h, "/tasks?stage=done", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks?stage=done = %d: %s", w.Code, w.Body.String())
	}
	var tasks []any
	json.NewDecoder(w.Body).Decode(&tasks)
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks in done stage, got %d", len(tasks))
	}
}

func TestIntegration_Task_UpdateDescription(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkupddesc@test.com", "TD", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "desc test", "body": "initial body", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	newBody := "updated body content"
	pw := patchJSONAuth(t, h, "/tasks/"+id, token, map[string]any{"body": newBody})
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH /tasks/%s body = %d: %s", id, pw.Code, pw.Body.String())
	}
	var updated map[string]any
	json.NewDecoder(pw.Body).Decode(&updated)
	if updated["body"] != newBody {
		t.Errorf("body = %q, want %q", updated["body"], newBody)
	}
}

func TestIntegration_Task_AddComment_Reply(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkcmtreply@test.com", "CR", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "reply test", "body": "desc", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	root := postJSONAuth(t, h, "/tasks/"+id+"/comments", token, map[string]any{
		"body": "root comment",
	})
	if root.Code != http.StatusCreated {
		t.Fatalf("POST /tasks/%s/comments = %d: %s", id, root.Code, root.Body.String())
	}
	var rootComment map[string]any
	json.NewDecoder(root.Body).Decode(&rootComment)
	rootID, _ := rootComment["id"].(string)

	reply := postJSONAuth(t, h, "/tasks/"+id+"/comments", token, map[string]any{
		"body":      "reply comment",
		"parent_id": rootID,
	})
	if reply.Code != http.StatusCreated {
		t.Errorf("POST reply comment = %d: %s", reply.Code, reply.Body.String())
	}
}

func TestIntegration_TaskDeps_RemoveHappyPath(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "depremove@test.com", "DR", "SecurePassword1")

	a := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "dep A", "body": "", "project_ids": []string{},
	})
	b := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "dep B", "body": "", "project_ids": []string{},
	})
	var aM, bM map[string]any
	json.NewDecoder(a.Body).Decode(&aM)
	json.NewDecoder(b.Body).Decode(&bM)
	aID, _ := aM["id"].(string)
	bID, _ := bM["id"].(string)

	addW := postJSONAuth(t, h, "/tasks/"+bID+"/dependencies", token, map[string]any{
		"depends_on_task_id": aID,
		"kind":               "soft",
	})
	if addW.Code != http.StatusCreated {
		t.Fatalf("add dep = %d: %s", addW.Code, addW.Body.String())
	}

	delW := deleteRequest(t, h, "/tasks/"+bID+"/dependencies/"+aID, token)
	if delW.Code != http.StatusOK {
		t.Errorf("remove dep = %d: %s", delW.Code, delW.Body.String())
	}

	listW := getJSON(t, h, "/tasks/"+bID+"/dependencies", token)
	if listW.Code != http.StatusOK {
		t.Fatalf("list deps = %d", listW.Code)
	}
	var deps []any
	json.NewDecoder(listW.Body).Decode(&deps)
	if len(deps) != 0 {
		t.Errorf("expected 0 deps after remove, got %d", len(deps))
	}
}

func TestIntegration_TaskDeps_RemoveNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "depremovnf@test.com", "RN", "SecurePassword1")

	w := deleteRequest(t, h, "/tasks/nonexistent/dependencies/other", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("remove dep from nonexistent task = %d, want 404", w.Code)
	}
}

func TestIntegration_Task_ListEvents_Empty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkeventsnf@test.com", "EN", "SecurePassword1")

	w := getJSON(t, h, "/tasks/nonexistent-task/events", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/nonexistent/events = %d, want 404", w.Code)
	}
}

func TestIntegration_Task_UsageEndpoint(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkusage@test.com", "TU", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "usage task", "body": "desc", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	w := getJSON(t, h, "/tasks/"+id+"/usage", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/usage = %d: %s", id, w.Code, w.Body.String())
	}
}

func TestIntegration_Task_ListFlags_Empty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkflagsmt@test.com", "FL", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "flags task", "body": "desc", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	w := getJSON(t, h, "/tasks/"+id+"/flags", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/flags = %d: %s", id, w.Code, w.Body.String())
	}
	var flags []any
	json.NewDecoder(w.Body).Decode(&flags)
	if len(flags) != 0 {
		t.Errorf("expected 0 flags, got %d", len(flags))
	}
}

func TestIntegration_Task_AddComment_EmptyBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkcmtempty@test.com", "CE", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "empty comment test", "body": "desc", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	w := postJSONAuth(t, h, "/tasks/"+id+"/comments", token, map[string]any{
		"body": "   ",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST empty comment = %d, want 400", w.Code)
	}
}

func TestIntegration_Task_CreateFlag_EmptyLabel(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tkflagelbl@test.com", "FE", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "flag label test", "body": "desc", "project_ids": []string{},
	})
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	w := postJSONAuth(t, h, "/tasks/"+id+"/flags", token, map[string]any{
		"kind": "custom",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST flag without label = %d, want 400", w.Code)
	}
}

func TestIntegration_CreateProject_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "prjinvjson@test.com", "IJ", "SecurePassword1")

	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader([]byte("{invalid json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /projects invalid JSON = %d, want 400", w.Code)
	}
}

func TestIntegration_ListWorktrees_MissingClone(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wktgone@test.com", "WG", "SecurePassword1")
	id := createProject(t, h, token, "Gone Clone Project")

	var proj struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(getJSON(t, h, "/projects/"+id, token).Body).Decode(&proj); err != nil || proj.Path == "" {
		t.Fatalf("project path: %v %q", err, proj.Path)
	}
	if err := os.RemoveAll(proj.Path); err != nil {
		t.Fatal(err)
	}
	w := getJSON(t, h, "/projects/"+id+"/worktrees", token)
	if w.Code != http.StatusPreconditionFailed {
		t.Errorf("GET worktrees of a missing clone = %d, want 412: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_ListWorktrees_NonexistentProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wktnonexist@test.com", "WN", "SecurePassword1")

	w := getJSON(t, h, "/projects/nonexistent-proj-id/worktrees", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /projects/nonexistent/worktrees = %d, want 404", w.Code)
	}
}

func createTaskViaAPI(t *testing.T, h http.Handler, token, title string) string {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": title, "body": ""})
	if w.Code != http.StatusCreated {
		t.Fatalf("createTaskViaAPI: expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("createTaskViaAPI: no id in response")
	}
	return id
}

func TestIntegration_ListSettings_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "lsdb@test.com", "LS", "SecurePassword1")
	srv.settingsStore.DB().Close()
	w := getJSON(t, h, "/settings", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /settings DB error = %d, want 500", w.Code)
	}
}

func TestIntegration_GetSetting_DBError(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "gsdb@test.com", "GS", "SecurePassword1")
	srv.settingsStore.DB().Close()
	w := getJSON(t, h, "/settings/sandbox.max", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /settings/sandbox.max DB error = %d, want 500", w.Code)
	}
}

func TestIntegration_PutSetting_DBError(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "psdb@test.com", "PS", "SecurePassword1")
	srv.settingsStore.DB().Close()
	b, _ := json.Marshal(5)
	req := httptest.NewRequest(http.MethodPut, "/settings/sandbox.max", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PUT /settings/sandbox.max DB error = %d, want 500", w.Code)
	}
}

func TestIntegration_ListWebhooks_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "lwdb@test.com", "LW", "SecurePassword1")
	srv.webhookStore.DB().Close()
	w := getJSON(t, h, "/webhooks", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /webhooks DB error = %d, want 500", w.Code)
	}
}

func TestIntegration_CreateWebhook_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "cwdb@test.com", "CW", "SecurePassword1")
	srv.webhookStore.DB().Close()
	b, _ := json.Marshal(map[string]any{"url": "https://example.com/hook", "events": []string{"*"}})
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /webhooks DB error = %d, want 500", w.Code)
	}
}

func TestIntegration_DeleteWebhook_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "dwdb@test.com", "DW", "SecurePassword1")
	srv.webhookStore.DB().Close()
	req := httptest.NewRequest(http.MethodDelete, "/webhooks/some-id", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("DELETE /webhooks/some-id DB error = %d, want 500", w.Code)
	}
}

func TestIntegration_ListReports_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "lrdb@test.com", "LR", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "report-task")
	srv.taskStore.DB().Close()
	w := getJSON(t, h, "/tasks/"+taskID+"/reports", token)
	// The workspace-scoped fetch runs first and the store maps any read failure (closed DB
	// included) to not-found, so 404.
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/{id}/reports DB error = %d, want 404", w.Code)
	}
}

func TestIntegration_ListFlags_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "lfdb@test.com", "LF", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "flag-task")
	srv.taskStore.DB().Close()
	w := getJSON(t, h, "/tasks/"+taskID+"/flags", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/{id}/flags DB error = %d, want 404", w.Code)
	}
}

func TestIntegration_ListEvents_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "ledb@test.com", "LE", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "event-task")
	srv.taskStore.DB().Close()
	w := getJSON(t, h, "/tasks/"+taskID+"/events", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/{id}/events DB error = %d, want 404", w.Code)
	}
}

func TestIntegration_ListDeps_TaskNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ldnf@test.com", "LD", "SecurePassword1")
	w := getJSON(t, h, "/tasks/nonexistent-task/dependencies", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/nonexistent/dependencies = %d, want 404", w.Code)
	}
}

func TestIntegration_Task_UpdateStage_BlockedByDeps(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "depblock@test.com", "DB", "SecurePassword1")

	aID := createTaskViaAPI(t, h, token, "Blocker task A")
	bID := createTaskViaAPI(t, h, token, "Dependant task B")

	dw := postJSONAuth(t, h, "/tasks/"+bID+"/dependencies", token, map[string]any{
		"depends_on_task_id": aID,
		"kind":               "hard",
	})
	if dw.Code != http.StatusCreated {
		t.Fatalf("add dep: %d %s", dw.Code, dw.Body.String())
	}

	b, _ := json.Marshal(map[string]string{"stage": "refinement"})
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+bID, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("PATCH /tasks/%s with blocked dep = %d, want 409", bID, w.Code)
	}
}

func TestIntegration_Task_UpdateStage_BlockedByFlags(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagblock@test.com", "FB", "SecurePassword1")

	taskID := createTaskViaAPI(t, h, token, "review task")
	b, _ := json.Marshal(map[string]string{"stage": "review"})
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+taskID, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", w.Code, w.Body.String())
	}

	fw := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"label": "needs decision",
		"kind":  "custom",
	})
	if fw.Code != http.StatusCreated {
		t.Fatalf("add flag: %d %s", fw.Code, fw.Body.String())
	}

	b2, _ := json.Marshal(map[string]string{"stage": "handoff"})
	req2 := httptest.NewRequest(http.MethodPatch, "/tasks/"+taskID, bytes.NewReader(b2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Errorf("PATCH /tasks/%s out of review with flag = %d, want 409", taskID, w2.Code)
	}
}

func TestIntegration_Task_Brief_FullTask(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "brief@test.com", "BT", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "Full Brief Task",
		"body":  "This is the task description.",
		"tags":  []string{"alpha", "beta"},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	taskID := created["id"].(string)

	cmw := postJSONAuth(t, h, "/tasks/"+taskID+"/comments", token, map[string]any{
		"body":   "A discussion comment",
		"source": "agent",
		"author": "probe",
	})
	if cmw.Code != http.StatusCreated {
		t.Fatalf("add comment: %d %s", cmw.Code, cmw.Body.String())
	}

	flw := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"label": "needs architecture decision",
		"kind":  "arch_decision",
	})
	if flw.Code != http.StatusCreated {
		t.Fatalf("add flag: %d %s", flw.Code, flw.Body.String())
	}

	bw := getJSON(t, h, "/tasks/"+taskID+"/brief", token)
	if bw.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/brief = %d %s", taskID, bw.Code, bw.Body.String())
	}
	body := bw.Body.String()
	if !strings.Contains(body, "Full Brief Task") {
		t.Error("brief missing task title")
	}
	if !strings.Contains(body, "This is the task description.") {
		t.Error("brief missing task body/description")
	}
	if !strings.Contains(body, "alpha") {
		t.Error("brief missing tags")
	}
	if !strings.Contains(body, "A discussion comment") {
		t.Error("brief missing comment")
	}
	if !strings.Contains(body, "needs architecture decision") {
		t.Error("brief missing flag")
	}
}

func TestIntegration_Task_Create_WithStageAssignments(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "stageasgn@test.com", "SA", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "task with assignments",
		"body":  "",
		"stage_assignments": map[string]string{
			"refinement":  "sonnet",
			"in_progress": "probe",
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task with stage_assignments: %d %s", w.Code, w.Body.String())
	}
}

func TestIntegration_Task_Create_WithInitialStage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "initstage@test.com", "IS", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "task with initial stage",
		"body":  "",
		"stage": "refinement",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task with initial stage: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if stage, _ := resp["stage"].(string); stage != "refinement" {
		t.Errorf("created task stage = %q, want refinement", stage)
	}
}

func TestIntegration_SessionLogs_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sesslog@test.com", "SL", "SecurePassword1")
	w := getJSON(t, h, "/usage/sessions/nonexistent-session-id/logs", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /usage/sessions/nonexistent/logs: got %d, want 404", w.Code)
	}
}

func TestIntegration_SessionFiles_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sessfiles@test.com", "SF", "SecurePassword1")
	w := getJSON(t, h, "/usage/sessions/nonexistent-session-id/files", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /usage/sessions/nonexistent/files: got %d, want 404", w.Code)
	}
}

func TestIntegration_SessionServeFile_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sessserve@test.com", "SS", "SecurePassword1")
	w := getJSON(t, h, "/usage/sessions/nonexistent-session-id/files/report.txt", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /usage/sessions/nonexistent/files/report.txt: got %d, want 404", w.Code)
	}
}

func TestIntegration_SessionServeFile_PathTraversal(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "sesspathtr@test.com", "SP")

	// The session must belong to the caller's workspace, or the handler answers 404 before reaching
	// the path check.
	srv.usageStore.Add(t.Context(), usage.Session{
		ID:          "traversal-test-sess",
		WorkspaceID: wsID,
		OutputDir:   t.TempDir(),
	})

	// "a..b" contains ".." but is not a "../" segment, so ServeMux does not clean it and the
	// handler must reject it.
	w := getJSON(t, h, "/usage/sessions/traversal-test-sess/files/a..b", token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("path traversal: got %d, want 400", w.Code)
	}
}

func TestIntegration_SessionLogsStream_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sessstream@test.com", "SS2", "SecurePassword1")
	w := getJSON(t, h, "/usage/sessions/nonexistent-stream-id/logs/stream", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /usage/sessions/nonexistent/logs/stream: got %d, want 404", w.Code)
	}
}

func TestIntegration_Usage_WithRateLimit(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := registerUserInWorkspace(t, h, "usagerate@test.com", "UR", "SecurePassword1")

	srv.usageStore.SetRateLimit(wid, usage.RateLimit{
		Utilization: 0.5,
		ResetsAt:    9999999999,
	})

	w := getJSON(t, h, "/usage", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /usage with rate limit: got %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["rate_limit"] == nil {
		t.Error("GET /usage: expected rate_limit key when set")
	}
}

func TestIntegration_SessionFiles_EmptyOutputDir(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "sessfilesnodir@test.com", "SN")

	srv.usageStore.Add(t.Context(), usage.Session{ID: "no-output-sess", WorkspaceID: wsID})

	w := getJSON(t, h, "/usage/sessions/no-output-sess/files", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("session with empty OutputDir: got %d, want 404", w.Code)
	}
}

func TestIntegration_SessionFiles_WithOutputDir(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "sessfileswithdir@test.com", "SW")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "out.txt"), []byte("content"), 0644)
	srv.usageStore.Add(t.Context(), usage.Session{ID: "with-output-sess", WorkspaceID: wsID, OutputDir: dir})

	w := getJSON(t, h, "/usage/sessions/with-output-sess/files", token)
	if w.Code != http.StatusOK {
		t.Errorf("session with OutputDir: got %d, want 200", w.Code)
	}
}

func TestIntegration_SessionLogs_WithLogFile(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "sesslogfile@test.com", "LF")

	logFile := filepath.Join(t.TempDir(), "test.log")
	os.WriteFile(logFile, []byte("log line 1\nlog line 2\n"), 0644)
	srv.usageStore.Add(t.Context(), usage.Session{ID: "log-file-sess", WorkspaceID: wsID, LogsFile: logFile})

	w := getJSON(t, h, "/usage/sessions/log-file-sess/logs", token)
	if w.Code != http.StatusOK {
		t.Errorf("session with log file: got %d, want 200", w.Code)
	}
}

func TestIntegration_SessionLogs_LogFileUnreadable(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "sesslogunread@test.com", "LU")

	srv.usageStore.Add(t.Context(), usage.Session{ID: "unread-log-sess", WorkspaceID: wsID, LogsFile: "/nonexistent/path/nope.log"})

	w := getJSON(t, h, "/usage/sessions/unread-log-sess/logs", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("unreadable log file: got %d, want 500", w.Code)
	}
}

func TestIntegration_DeleteFlag_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "delflagdberr@test.com", "DF", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "delete flag db error task")

	fw := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"kind":  "custom",
		"label": "Test Flag",
	})
	if fw.Code != http.StatusCreated {
		t.Fatalf("POST flag = %d: %s", fw.Code, fw.Body.String())
	}
	var flag map[string]any
	json.NewDecoder(fw.Body).Decode(&flag)
	flagID, _ := flag["id"].(string)

	srv.taskStore.DB().Close()

	dw := deleteRequest(t, h, "/tasks/"+taskID+"/flags/"+flagID, token)
	if dw.Code != http.StatusNotFound {
		t.Errorf("DELETE flag with DB closed: got %d, want 404", dw.Code)
	}
}

func TestIntegration_SessionLogs_FallbackFindByContainerID(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "sessfallback@test.com", "FB")

	containerID := "abcdef1234567890"
	logsDir := filepath.Join(srv.usageStore.DataDir(), "logs")
	os.MkdirAll(logsDir, 0755)
	logPath := filepath.Join(logsDir, "task_"+containerID+".log")
	os.WriteFile(logPath, []byte("container log\n"), 0644)

	w := getJSON(t, h, "/usage/sessions/"+containerID+"/logs", "")
	if w.Code != http.StatusOK {
		t.Errorf("fallback log by container ID: got %d, want 200", w.Code)
	}

	if w := getJSON(t, h, "/usage/sessions/"+containerID+"/logs", token); w.Code != http.StatusNotFound {
		t.Errorf("scoped caller on an unowned log file: got %d, want 404", w.Code)
	}
}

func TestIntegration_TaskUsage_WithRunEvent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskusagerun@test.com", "TU", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "task usage with run event")

	payload := map[string]any{"container_id": "cont-abc123", "stage": "refinement"}
	srv.taskStore.RecordEvent(t.Context(), taskID, task.EventAgentRunStarted, "inst-1", "probe", "started", payload)
	now := time.Now().UTC()
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: "cont-abc123", Agent: "probe", TaskID: taskID,
		StartedAt: now.Add(-time.Minute), FinishedAt: now, InputTokens: 100, CostUSD: 0.34,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	w := getJSON(t, h, "/tasks/"+taskID+"/usage", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s/usage: got %d, want 200", taskID, w.Code)
	}
	var body struct {
		Sessions int     `json:"sessions"`
		CostUSD  float64 `json:"cost_usd"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Sessions != 1 || body.CostUSD != 0.34 {
		t.Errorf("usage = %+v, want 1 session costing 0.34", body)
	}
}

func TestIntegration_ListTools_UnderscorePrefix(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	toolsDir := filepath.Join(srv.agentsDir, "tools")
	os.MkdirAll(toolsDir, 0755)

	underscoreDir := filepath.Join(toolsDir, "_internal-tool")
	os.MkdirAll(underscoreDir, 0755)
	os.WriteFile(filepath.Join(underscoreDir, "tool.yaml"), []byte("name: internal\ndescription: x\n"), 0644)

	w := getJSON(t, h, "/tools", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tools = %d", w.Code)
	}
	var tools []map[string]any
	json.NewDecoder(w.Body).Decode(&tools)
	if tools = notBuiltin(tools); len(tools) != 0 {
		t.Errorf("underscore tool should be skipped, got %d tools", len(tools))
	}
}

func TestIntegration_ListTools_InvalidYAML(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	toolsDir := filepath.Join(srv.agentsDir, "tools")
	os.MkdirAll(toolsDir, 0755)

	toolDir := filepath.Join(toolsDir, "bad-yaml-tool")
	os.MkdirAll(toolDir, 0755)
	os.WriteFile(filepath.Join(toolDir, "tool.yaml"), []byte(":\tinvalid: yaml: content: [unclosed"), 0644)

	w := getJSON(t, h, "/tools", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tools = %d", w.Code)
	}
	var tools []map[string]any
	json.NewDecoder(w.Body).Decode(&tools)
	if tools = notBuiltin(tools); len(tools) != 0 {
		t.Errorf("invalid YAML tool should be skipped, got %d tools", len(tools))
	}
}

func TestIntegration_PutAgentSettings_FirstServiceFallback(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "picksvc@test.com", "PS", "SecurePassword1")
	setupAgent(t, srv, "probe-picksvc")

	compose := `
services:
  myservice:
    image: octopus-docker
    environment:
      - AGENT_NAME=probe-picksvc
`
	os.WriteFile(filepath.Join(srv.agentsDir, "agents", "probe-picksvc", "docker-compose.yml"), []byte(compose), 0644)

	w := putJSON(t, h, "/types/probe-picksvc/settings", map[string]any{
		"turns": 10,
	}, token)
	if w.Code == http.StatusInternalServerError {
		t.Errorf("PUT settings with non-agent-keyed service: got 500, want non-500")
	}
}

func TestIntegration_ListTasks_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "listtaskserr@test.com", "LTE", "SecurePassword1")
	srv.taskStore.DB().Close()
	w := getJSON(t, h, "/tasks", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /tasks with closed DB = %d, want 500", w.Code)
	}
}

func TestIntegration_ListFiles_AgentNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "listfilesnf@test.com", "LFN", "SecurePassword1")
	w := getJSON(t, h, "/agents/nonexistent-agent-id/files", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /agents/nonexistent/files = %d, want 404", w.Code)
	}
}

func TestIntegration_ServeFile_AgentNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "servfilesnf@test.com", "SFN", "SecurePassword1")
	w := getJSON(t, h, "/agents/nonexistent-agent-id/files/foo.txt", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /agents/nonexistent/files/foo.txt = %d, want 404", w.Code)
	}
}

func TestIntegration_ServeFile_PathTraversal(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := registerUserInWorkspace(t, h, "servfiletraversal@test.com", "SFT", "SecurePassword1")
	// Stamped with the caller's workspace: an unstamped run answers 404 to scoped callers before
	// the path check.
	srv.registry.Upsert(&agent.Agent{ID: "fake-agent-traverse", Name: "traverse-agent", WorkspaceID: wid})
	// ".." embedded in a filename segment is not cleaned by ServeMux, so the handler must reject
	// it.
	w := getJSON(t, h, "/agents/fake-agent-traverse/files/a..b", token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET /agents/{id}/files/a..b = %d, want 400", w.Code)
	}
}

func TestIntegration_ListFiles_WithOutputDir(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := registerUserInWorkspace(t, h, "listfileswod@test.com", "LFW", "SecurePassword1")
	outDir := t.TempDir()
	os.WriteFile(filepath.Join(outDir, "out.txt"), []byte("output"), 0644)
	srv.registry.Upsert(&agent.Agent{
		ID:          "agent-with-output",
		Name:        "output-agent",
		WorkspaceID: wid,
		OutputDir:   outDir,
	})
	w := getJSON(t, h, "/agents/agent-with-output/files", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /agents/{id}/files with OutputDir = %d, want 200: %s", w.Code, w.Body.String())
	}
	var files []fileEntry
	if err := json.NewDecoder(w.Body).Decode(&files); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(files) == 0 {
		t.Error("expected at least one file in output dir")
	}
}

func TestIntegration_ServeFile_Success(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := registerUserInWorkspace(t, h, "servfileok@test.com", "SFO", "SecurePassword1")
	outDir := t.TempDir()
	os.WriteFile(filepath.Join(outDir, "report.txt"), []byte("report content"), 0644)
	srv.registry.Upsert(&agent.Agent{
		ID:          "agent-serve-ok",
		Name:        "serve-agent",
		WorkspaceID: wid,
		OutputDir:   outDir,
	})

	w := getJSON(t, h, "/agents/agent-serve-ok/files/report.txt", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /agents/{id}/files/report.txt = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "report content") {
		t.Errorf("expected file content in response, got: %q", w.Body.String())
	}
}

func TestIntegration_Chats_NilService(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatnilsvc@test.com", "CN", "SecurePassword1")
	srv.chatSvc = nil

	if w := getJSON(t, h, "/chats", token); w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /chats with nil service = %d, want 503", w.Code)
	}
	if w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "x"}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /chats with nil service = %d, want 503", w.Code)
	}

	srv.chatBus = nil
	if w := getJSON(t, h, "/chats/events", token); w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /chats/events with nil bus = %d, want 503", w.Code)
	}
}

func TestIntegration_AgentFiles_NestedFeedbackForbidden(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "nested-feedback@test.com", "NF2", "SecurePassword1")
	setupAgent(t, srv, "probe-nested-fb")

	fbDir := filepath.Join(srv.agentsDir, "agents", "probe-nested-fb", "prompts", "feedback")
	os.MkdirAll(fbDir, 0755)
	os.WriteFile(filepath.Join(fbDir, "custom.md"), []byte("# feedback note\n"), 0644)

	w := getJSON(t, h, "/types/probe-nested-fb/files/prompts/feedback/custom.md", token)
	if w.Code != http.StatusForbidden {
		t.Errorf("nested feedback .md = %d, want 403", w.Code)
	}
}

func TestIntegration_AgentFiles_ListSkipsHiddenAndNodeModules(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "list-skip@test.com", "LS2", "SecurePassword1")
	setupAgent(t, srv, "probe-skip-dirs")

	agDir := filepath.Join(srv.agentsDir, "agents", "probe-skip-dirs")
	os.MkdirAll(filepath.Join(agDir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(agDir, ".hidden", "secret.md"), []byte("secret"), 0644)
	os.MkdirAll(filepath.Join(agDir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(agDir, "node_modules", "dep.md"), []byte("dep"), 0644)

	lw := getJSON(t, h, "/types/probe-skip-dirs/files", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET /types/probe-skip-dirs/files = %d, want 200", lw.Code)
	}
	body := lw.Body.String()
	if strings.Contains(body, ".hidden") {
		t.Error("listing should not include .hidden directory contents")
	}
	if strings.Contains(body, "node_modules") {
		t.Error("listing should not include node_modules directory contents")
	}
}

func TestIntegration_AgentFiles_CreateAgent_TemplateNotFound(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "ag-tmplt-nf@test.com", "TN", "SecurePassword1")

	cw := postJSONAuth(t, h, "/types", token, map[string]any{
		"name":     "new-agent",
		"template": "nonexistent-template",
	})
	if cw.Code != http.StatusBadRequest {
		t.Errorf("create agent with missing template = %d, want 400", cw.Code)
	}
}

func TestIntegration_Webhook_DeleteDBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "wh-del-dberr@test.com", "WDD", "SecurePassword1")

	cw := postJSONAuth(t, h, "/webhooks", token, map[string]any{"url": "https://example.com", "events": []string{"*"}})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d", cw.Code)
	}
	var ep map[string]any
	json.NewDecoder(cw.Body).Decode(&ep)
	epID, _ := ep["id"].(string)

	srv.webhookStore.DB().Close()
	dw := deleteRequest(t, h, "/webhooks/"+epID, token)
	if dw.Code != http.StatusInternalServerError {
		t.Errorf("DELETE /webhooks/%s with closed DB = %d, want 500", epID, dw.Code)
	}
}

func TestIntegration_Webhook_TestEndpointNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wh-test-nf@test.com", "WTN", "SecurePassword1")

	r := httptest.NewRequest(http.MethodPost, "/webhooks/nonexistent-endpoint-id/test", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /webhooks/nonexistent/test = %d, want 500", w.Code)
	}
}

func TestIntegration_UpdateTask_RepoRequiredForAgent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "repo-req@test.com", "RR", "SecurePassword1")

	setupAgent(t, srv, "probe-repo-req")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "needs repo task",
		"stage_assignments": map[string]string{
			"refinement": "probe-repo-req",
		},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", cw.Code, cw.Body.String())
	}
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	stage := "refinement"
	patch, _ := json.Marshal(map[string]any{"stage": stage})
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+taskID, bytes.NewReader(patch))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, req)
	if wr.Code != http.StatusConflict {
		t.Errorf("PATCH stage to refinement without repo = %d, want 409: %s", wr.Code, wr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(wr.Body).Decode(&resp)
	if resp["requires_repo"] != true {
		t.Errorf("expected requires_repo=true in response, got %v", resp)
	}
}

func TestIntegration_TaskUsage_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "task-usage-dberr@test.com", "TUD", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "usage db error task")

	srv.taskStore.DB().Close()
	w := getJSON(t, h, "/tasks/"+taskID+"/usage", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/%s/usage with closed DB = %d, want 404", taskID, w.Code)
	}
}

func TestIntegration_InternalProjectToken_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/internal/projects/no-such-proj/github-token", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/projects/.../github-token not found = %d, want 404", w.Code)
	}
}

func TestIntegration_InternalProjectToken_NilStore(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	w := getJSON(t, h, "/internal/projects/any-id/github-token", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/projects/.../github-token with nothing granted = %d, want 404", w.Code)
	}
	srv.gh.tokens = nil
	w = getJSON(t, h, "/internal/projects/any-id/github-token", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /internal/projects/.../github-token with no credential source = %d, want 503", w.Code)
	}
}

func TestIntegration_InternalTaskCommit_TaskNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/nonexistent-task/commit", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /internal/tasks/nonexistent/commit = %d, want 404", w.Code)
	}
}

func TestIntegration_InternalTaskCommit_TaskNoProjects(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "inttaskcommit@test.com", "ITC", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "commit task no projects")

	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/"+taskID+"/commit",
		bytes.NewReader([]byte(`{"message":"test commit"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST /internal/tasks/%s/commit (no projects) = %d, want 200", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskBranchStatus_TaskNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/internal/tasks/no-such-task/branch-status", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/tasks/nonexistent/branch-status = %d, want 404", w.Code)
	}
}

func TestIntegration_InternalTaskBranchStatus_TaskNoProjects(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "intbranchstatus@test.com", "IBS", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "branch status task no projects")

	w := getJSON(t, h, "/internal/tasks/"+taskID+"/branch-status", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /internal/tasks/%s/branch-status (no projects) = %d, want 200", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskGithubToken_TaskNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/internal/tasks/no-such-task/github-token", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/tasks/nonexistent/github-token = %d, want 404", w.Code)
	}
}

func TestIntegration_InternalTaskGithubToken_NoProjects(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "inttaskghtoken@test.com", "ITG", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "gh token task no projects")

	w := getJSON(t, h, "/internal/tasks/"+taskID+"/github-token", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/tasks/%s/github-token (no projects) = %d, want 404", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskGithubToken_NilStore(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	w := getJSON(t, h, "/internal/tasks/any-id/github-token", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/tasks/any-id/github-token for an unknown task = %d, want 404", w.Code)
	}
	srv.gh.tokens = nil
	w = getJSON(t, h, "/internal/tasks/any-id/github-token", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /internal/tasks/any-id/github-token with no credential source = %d, want 503", w.Code)
	}
}

func TestIntegration_InternalTaskBranchStatus_WithProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "intbranchstatusproj@test.com", "IBSP", "SecurePassword1")
	projID := createProject(t, h, token, "branch-status-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "branch status task with project",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	w := getJSON(t, h, "/internal/tasks/"+taskID+"/branch-status", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /internal/tasks/%s/branch-status = %d, want 200", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskCommit_WithProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "inttaskcomproj@test.com", "ITCP", "SecurePassword1")
	projID := createProject(t, h, token, "commit-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "commit task with project",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/"+taskID+"/commit",
		bytes.NewReader([]byte(`{"message":"test commit"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST /internal/tasks/%s/commit (with project, no worktree) = %d, want 200", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskBranchPush_WithProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "intbranchpushproj@test.com", "IBPP", "SecurePassword1")
	projID := createProject(t, h, token, "push-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "branch push task with project",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/"+taskID+"/branch-push", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST /internal/tasks/%s/branch-push (with project, no worktree) = %d, want 200", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskGithubToken_ProjectNoToken(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "inttaskghtkproj@test.com", "ITGP", "SecurePassword1")
	projID := createProject(t, h, token, "gh-token-proj-no-tok")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "github token task with project",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	w := getJSON(t, h, "/internal/tasks/"+taskID+"/github-token", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /internal/tasks/%s/github-token (project, no token) = %d, want 404", taskID, w.Code)
	}
}

func TestIntegration_InternalProjectToken_Success(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "intprojgh-ok@test.com", "IPT", "SecurePassword1")
	projID := createProject(t, h, token, "intproj-gh-ok")

	seedProjectCredential(t, srv, projID, "ghp_test_token")

	w := getJSON(t, h, "/internal/projects/"+projID+"/github-token", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /internal/projects/%s/github-token = %d, want 200: %s", projID, w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] != "ghp_test_token" {
		t.Errorf("token = %v, want ghp_test_token", resp["token"])
	}
}

func TestIntegration_InternalTaskGithubToken_WithToken(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "inttaskghok@test.com", "ITGT", "SecurePassword1")
	projID := createProject(t, h, token, "inttask-gh-tok-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "gh token task with token",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	seedProjectCredential(t, srv, projID, "ghp_task_token")

	w := getJSON(t, h, "/internal/tasks/"+taskID+"/github-token", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /internal/tasks/%s/github-token = %d, want 200: %s", taskID, w.Code, w.Body.String())
	}
}

func TestIntegration_InternalTaskBranchPush_TaskNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/no-such-task/branch-push", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /internal/tasks/nonexistent/branch-push = %d, want 404", w.Code)
	}
}

func TestIntegration_InternalTaskBranchPush_TaskNoProjects(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "intbranchpush@test.com", "IBP", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "branch push no projects")

	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/"+taskID+"/branch-push", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST /internal/tasks/%s/branch-push (no projects) = %d, want 200", taskID, w.Code)
	}
}

func TestIntegration_InternalTaskBranchPush_CleanWorktree(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "intbranchpushwt@test.com", "IBPW", "SecurePassword1")
	projID := createProject(t, h, token, "push-clean-wt-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "branch push clean worktree",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	if _, _, err := srv.projectStore.CreateTaskWorktree(t.Context(), projID, taskID); err != nil {
		t.Fatalf("setup: CreateTaskWorktree failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/"+taskID+"/branch-push", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST /internal/tasks/%s/branch-push (clean worktree) = %d, want 200", taskID, w.Code)
	}
	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)
	results, _ := result["results"].([]any)
	if len(results) == 0 {
		t.Error("expected at least one result entry")
	}
}

func TestIntegration_InternalTaskCommit_CleanWorktree(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "inttaskcommitwt@test.com", "ITCW", "SecurePassword1")
	projID := createProject(t, h, token, "commit-clean-wt-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":       "commit clean worktree",
		"project_ids": []string{projID},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	if _, _, err := srv.projectStore.CreateTaskWorktree(t.Context(), projID, taskID); err != nil {
		t.Fatalf("setup: CreateTaskWorktree failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/tasks/"+taskID+"/commit",
		bytes.NewReader([]byte(`{"message":"test commit"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("POST /internal/tasks/%s/commit (clean worktree) = %d, want 200", taskID, w.Code)
	}
	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)
	results, _ := result["results"].([]any)
	if len(results) == 0 {
		t.Error("expected at least one result entry")
	}
	first, _ := results[0].(map[string]any)
	if first["skipped"] == nil {
		t.Errorf("expected skipped field for clean worktree, got: %v", first)
	}
}

func TestIntegration_UpdateTask_AgentWorksWithoutRepo(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "works-without-repo@test.com", "WR", "SecurePassword1")

	agDir := filepath.Join(srv.agentsDir, "agents", "probe-no-repo")
	os.MkdirAll(agDir, 0755)
	os.WriteFile(filepath.Join(agDir, "main.md"), []byte("# Prompt\n"), 0644)
	// thulu.stages excludes refinement so the dispatcher returns before launching; with nil Docker
	// a launch would panic.
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "probe-no-repo"
      thulu.stages: "done"
      thulu.works_without_repo: "true"
`
	os.WriteFile(filepath.Join(agDir, "docker-compose.yml"), []byte(compose), 0644)

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "task works without repo",
		"stage_assignments": map[string]string{
			"refinement": "probe-no-repo",
		},
	})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	stage := "refinement"
	patch, _ := json.Marshal(map[string]any{"stage": stage})
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+taskID, bytes.NewReader(patch))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, req)
	if wr.Code != http.StatusOK {
		t.Errorf("PATCH stage to refinement with works_without_repo agent = %d, want 200: %s",
			wr.Code, wr.Body.String())
	}
}

func TestIntegration_UploadAttachment_TaskNotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "upatt-nf@test.com", "UAN", "SecurePassword1")

	var buf bytes.Buffer
	w2 := multipart.NewWriter(&buf)
	fw, _ := w2.CreateFormFile("file", "test.txt")
	fmt.Fprint(fw, "test content")
	w2.Close()

	req := httptest.NewRequest(http.MethodPost, "/tasks/nonexistent-task-id/attachments", &buf)
	req.Header.Set("Content-Type", w2.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, req)
	if wr.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/nonexistent/attachments = %d, want 404", wr.Code)
	}
}

func TestIntegration_UploadAttachment_MissingFile(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "upatt-mf@test.com", "UAM", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "upload-task")

	var buf bytes.Buffer
	w2 := multipart.NewWriter(&buf)
	w2.WriteField("other", "value")
	w2.Close()

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/attachments", &buf)
	req.Header.Set("Content-Type", w2.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, req)
	if wr.Code != http.StatusBadRequest {
		t.Errorf("POST /tasks/%s/attachments (no file) = %d, want 400", taskID, wr.Code)
	}
}

func TestIntegration_UploadAttachment_Success(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "upatt-ok@test.com", "UAO", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "upload-task-ok")

	var buf bytes.Buffer
	w2 := multipart.NewWriter(&buf)
	fw, _ := w2.CreateFormFile("file", "report.png")
	fmt.Fprint(fw, "fake png data")
	w2.Close()

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/attachments", &buf)
	req.Header.Set("Content-Type", w2.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, req)
	if wr.Code != http.StatusOK {
		t.Errorf("POST /tasks/%s/attachments = %d, want 200: %s", taskID, wr.Code, wr.Body.String())
	}
	var att map[string]any
	json.NewDecoder(wr.Body).Decode(&att)
	if att["id"] == nil {
		t.Error("expected attachment id in response")
	}
}

func TestIntegration_ServeAttachment_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "servatt-nf@test.com", "SAN", "SecurePassword1")
	w := getJSON(t, h, "/task-attachments/nonexistent-attachment-id", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /task-attachments/nonexistent = %d, want 404", w.Code)
	}
}

func TestIntegration_ServeAttachment_Success(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "servatt-ok@test.com", "SAO", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "serve-att-task")

	attDir := t.TempDir()
	attPath := filepath.Join(attDir, "img.png")
	os.WriteFile(attPath, []byte("PNG content"), 0644)

	att, err := srv.taskStore.AddAttachment(t.Context(), task.NewAttachment{TaskID: taskID, Filename: "img.png", MimeType: "image/png", Path: attPath, Size: 11})
	if err != nil {
		t.Fatalf("AddAttachment: %v", err)
	}

	w := getJSON(t, h, "/task-attachments/"+att.ID, token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /task-attachments/%s = %d, want 200: %s", att.ID, w.Code, w.Body.String())
	}
}

func TestIntegration_DispatchMentions_NoMatches(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	c := &task.Comment{ID: "c1", Body: "no mention here"}
	srv.dispatchMentions(context.Background(), "some-task-id", c)
}

func TestIntegration_DispatchMentions_WithAgent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "mention@test.com", "MC", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "mention task")

	// thulu.stages excludes mention so HandleMention posts a system comment instead of launching a
	// container (nil Docker).
	agDir := filepath.Join(srv.agentsDir, "agents", "probe-mention-test")
	os.MkdirAll(agDir, 0755)
	os.WriteFile(filepath.Join(agDir, "main.md"), []byte("# Prompt\n"), 0644)
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "probe-mention-test"
      thulu.stages: "refinement"
`
	os.WriteFile(filepath.Join(agDir, "docker-compose.yml"), []byte(compose), 0644)

	c := &task.Comment{ID: "c2", Body: "@probe-mention-test please review"}
	srv.dispatchMentions(context.Background(), taskID, c)
}

func TestIntegration_DispatchMentions_DuplicateMention(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "mention-dup@test.com", "MCD", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "dup mention task")

	agDir := filepath.Join(srv.agentsDir, "agents", "probe-dup-mention")
	os.MkdirAll(agDir, 0755)
	os.WriteFile(filepath.Join(agDir, "main.md"), []byte("# Prompt\n"), 0644)
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "probe-dup-mention"
      thulu.stages: "refinement"
`
	os.WriteFile(filepath.Join(agDir, "docker-compose.yml"), []byte(compose), 0644)

	c := &task.Comment{ID: "c3", Body: "@probe-dup-mention @probe-dup-mention twice"}
	srv.dispatchMentions(context.Background(), taskID, c)
}

func TestIntegration_DispatchMentions_UnknownAgent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "mention-unknown@test.com", "MCU", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "unknown mention task")

	c := &task.Comment{ID: "c4", Body: "@nonexistent-agent-xyz check this"}
	srv.dispatchMentions(context.Background(), taskID, c)
}

func TestIntegration_DeleteTask_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "deltetaskdberr@test.com", "DTD", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "task to delete"})
	var tk map[string]any
	json.NewDecoder(w.Body).Decode(&tk)
	id, _ := tk["id"].(string)

	srv.taskStore.DB().Close()
	r := httptest.NewRequest(http.MethodDelete, "/tasks/"+id, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, r)
	if wr.Code != http.StatusNotFound {
		t.Errorf("DELETE /tasks/%s with closed DB = %d, want 404", id, wr.Code)
	}
}

func TestIntegration_ForkTask_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "forknotfound@test.com", "FNF", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks/nonexistent-task-id/fork", token, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/nonexistent/fork = %d, want 404", w.Code)
	}
}

func TestIntegration_ForkTask_Success(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "forksuccess@test.com", "FSC", "SecurePassword1")

	srcW := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "Original Task",
		"body":  "some body",
		"tags":  []string{"tag1"},
	})
	var src map[string]any
	json.NewDecoder(srcW.Body).Decode(&src)
	srcID, _ := src["id"].(string)
	if srcID == "" {
		t.Fatal("failed to create source task")
	}

	w := postJSONAuth(t, h, "/tasks/"+srcID+"/fork", token, nil)
	if w.Code != http.StatusCreated {
		t.Errorf("POST /tasks/%s/fork = %d, want 201; body=%s", srcID, w.Code, w.Body.String())
	}
	var fork map[string]any
	json.NewDecoder(w.Body).Decode(&fork)
	forkTitle, _ := fork["title"].(string)
	if !strings.Contains(forkTitle, "Fork of") {
		t.Errorf("fork title = %q, want 'Fork of ...'", forkTitle)
	}
	forkID, _ := fork["id"].(string)
	if forkID == "" || forkID == srcID {
		t.Errorf("expected new task ID, got %q", forkID)
	}
}

func TestIntegration_ForkTask_ForkOfFork(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "forkoffork@test.com", "FOF", "SecurePassword1")

	srcW := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "Fork of Something"})
	var src map[string]any
	json.NewDecoder(srcW.Body).Decode(&src)
	srcID, _ := src["id"].(string)

	w := postJSONAuth(t, h, "/tasks/"+srcID+"/fork", token, nil)
	if w.Code != http.StatusCreated {
		t.Errorf("fork of fork: %d, want 201", w.Code)
	}
	var fork map[string]any
	json.NewDecoder(w.Body).Decode(&fork)
	forkTitle, _ := fork["title"].(string)
	if strings.HasPrefix(forkTitle, "Fork of Fork of") {
		t.Errorf("double-prefixed title: %q", forkTitle)
	}
}

func TestIntegration_EnsurePR_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ensureprnotfound@test.com", "ENF", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks/nonexistent-task/ensure-pr", token, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/nonexistent/ensure-pr = %d, want 404", w.Code)
	}
}

func TestIntegration_EnsurePR_NoProjects(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ensurprnoproj@test.com", "ENP", "SecurePassword1")

	tw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "no-project task"})
	var tk map[string]any
	json.NewDecoder(tw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	w := postJSONAuth(t, h, "/tasks/"+taskID+"/ensure-pr", token, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /tasks/%s/ensure-pr (no projects) = %d, want 400", taskID, w.Code)
	}
}

func TestIntegration_RelaunchTask_NotFound(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "relaunchnotfound@test.com", "RNF", "SecurePassword1")

	w := postJSONAuth(t, h, "/tasks/nonexistent-task/relaunch", token, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/nonexistent/relaunch = %d, want 404", w.Code)
	}
}

func TestIntegration_ReadAgentFile_FileNotFound(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "readagentfile@test.com", "RAF", "SecurePassword1")
	setupAgent(t, srv, "probe-read-missing")

	w := getJSON(t, h, "/types/probe-read-missing/files/prompts/does-not-exist.md", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /types/probe-read-missing/files/prompts/does-not-exist.md = %d, want 404", w.Code)
	}
}

func TestIntegration_ListProjects_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "listprojdberr@test.com", "LPD", "SecurePassword1")

	srv.projectStore.DB().Close()
	w := getJSON(t, h, "/projects", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /projects with closed DB = %d, want 500", w.Code)
	}
}

func TestIntegration_AgentDir_BackslashInName(t *testing.T) {
	_, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agentdirbs@test.com", "ADB", "SecurePassword1")

	w := getJSON(t, h, `/types/bad\name/files`, token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /types/bad\\name/files = %d, want 404", w.Code)
	}
}

func TestIntegration_RecordEvent_DBError(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	srv.taskStore.DB().Close()
	srv.recordEvent(t.Context(), "nonexistent-task", task.EventTaskCreated, "", "test", "should-not-panic", nil)
}

func TestIntegration_Chats_ListDBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatlistdberr@test.com", "CLD", "SecurePassword1")

	srv.chatSvc.Store().DB().Close()
	w := getJSON(t, h, "/chats", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /chats with closed DB = %d, want 500", w.Code)
	}
}

func TestIntegration_AddComment_AgentSource(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "cmtagent@test.com", "CAS", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "agent source task")

	w := postJSONAuth(t, h, "/tasks/"+taskID+"/comments", token, map[string]any{
		"body":   "automated update",
		"source": "agent",
		"author": "probe",
	})
	if w.Code != http.StatusCreated {
		t.Errorf("POST comment with source=agent = %d, want 201", w.Code)
	}
}

func TestIntegration_AddComment_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "cmtdberr@test.com", "CDE", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "db error task")

	srv.taskStore.DB().Close()
	w := postJSONAuth(t, h, "/tasks/"+taskID+"/comments", token, map[string]any{
		"body": "will fail",
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("POST comment with closed DB = %d, want 404", w.Code)
	}
}

func TestIntegration_DispatchMentions_TaskNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	setupAgent(t, srv, "probe-dm-tasknotfound")
	c := &task.Comment{ID: "cx1", Body: "@probe-dm-tasknotfound check this out"}
	srv.dispatchMentions(context.Background(), "nonexistent-task-id-xyz", c)
}

func TestIntegration_DispatchMentions_DiscoverError(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	c := &task.Comment{ID: "cx2", Body: "@anything check this"}
	srv.dispatchMentions(context.Background(), "some-task-id", c)
}

func TestIntegration_CloneTask_CopyOfCopy(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "copycopy@test.com", "CC2", "SecurePassword1")

	srcW := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "Copy of Original"})
	var src map[string]any
	json.NewDecoder(srcW.Body).Decode(&src)
	srcID, _ := src["id"].(string)

	r := httptest.NewRequest(http.MethodPost, "/tasks/"+srcID+"/clone", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Errorf("clone = %d, want 201", w.Code)
	}
	var clone map[string]any
	json.NewDecoder(w.Body).Decode(&clone)
	cloneTitle, _ := clone["title"].(string)
	if strings.HasPrefix(cloneTitle, "Copy of Copy of") {
		t.Errorf("double-prefixed title: %q", cloneTitle)
	}
}

func TestIntegration_CreateFlag_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagdberr@test.com", "FDE", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "flag db error task")

	srv.taskStore.DB().Close()
	w := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"label": "something wrong",
	})
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/%s/flags with closed DB = %d, want 500 or 404", taskID, w.Code)
	}
}

func TestIntegration_AgentSettings_Put_InvalidYAML(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsinvalidyaml@test.com", "AIY", "SecurePassword1")

	agentDir := filepath.Join(srv.agentsDir, "agents", "invalid-yaml-agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "main.md"), []byte("# Agent\n"), 0644)
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(":\tbad: yaml: {\n"), 0644)

	settings := AgentSettings{Name: "invalid-yaml-agent", ValidStages: []string{"refinement"}}
	b, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPut, "/types/invalid-yaml-agent/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PUT with invalid YAML = %d, want 500", w.Code)
	}
}

func TestIntegration_AgentSettings_Put_NonAgentServiceName(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsnonagent@test.com", "ANA", "SecurePassword1")

	agentDir := filepath.Join(srv.agentsDir, "agents", "nonagent-svc")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "main.md"), []byte("# Agent\n"), 0644)
	compose := `services:
  myworker:
    image: octopus-docker
    labels:
      thulu.type: "nonagent-svc"
      thulu.stages: "refinement"
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)

	settings := AgentSettings{Name: "nonagent-svc", ValidStages: []string{"refinement"}}
	b, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPut, "/types/nonagent-svc/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("PUT /types/nonagent-svc/settings = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestIntegration_TestWebhook_NilDispatcher(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "whtestnil@test.com", "WTN", "SecurePassword1")

	wRec := postJSONAuth(t, h, "/webhooks", token, map[string]any{
		"url":    "https://unreachable.invalid/hook",
		"events": []string{"*"},
	})
	if wRec.Code != http.StatusCreated {
		t.Fatalf("create webhook = %d: %s", wRec.Code, wRec.Body.String())
	}
	var ep map[string]any
	json.NewDecoder(wRec.Body).Decode(&ep)
	epID, _ := ep["id"].(string)

	srv.webhookDisp = nil
	w := postJSONAuth(t, h, "/webhooks/"+epID+"/test", token, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /webhooks/%s/test with nil disp = %d, want 503", epID, w.Code)
	}
}

func TestIntegration_AgentSettings_Get_NotInCatalog(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsnotcatalog@test.com", "ANC", "SecurePassword1")

	agentDir := filepath.Join(srv.agentsDir, "agents", "uncatalogued-agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "main.md"), []byte("# Agent\n"), 0644)

	w := getJSON(t, h, "/types/uncatalogued-agent/settings", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /types/uncatalogued-agent/settings = %d, want 404", w.Code)
	}
}

func TestResolveAgentFile_EmptyPath(t *testing.T) {
	root := t.TempDir()
	_, err := resolveAgentFile(root, "")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestResolveAgentFile_LeadingSlashStripped(t *testing.T) {
	root := t.TempDir()
	abs, err := resolveAgentFile(root, "/main.md")
	if err != nil {
		t.Fatalf("resolveAgentFile with leading slash: %v", err)
	}
	if abs == "" {
		t.Error("expected non-empty abs path")
	}
}

func TestResolveAgentFile_PathEscapesRoot(t *testing.T) {
	root := t.TempDir()
	_, err := resolveAgentFile(root, "../../etc/passwd")
	if err == nil {
		t.Error("expected error when path escapes agent dir")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' in error message, got: %v", err)
	}
}

func TestPickAgentService_EmptyServices(t *testing.T) {
	result := pickAgentService(map[string]interface{}{})
	if result != "" {
		t.Errorf("expected empty string for empty services, got %q", result)
	}
}

func TestPickAgentService_NonMapService(t *testing.T) {
	services := map[string]interface{}{
		"myworker": "not-a-map",
	}
	result := pickAgentService(services)
	if result != "myworker" {
		t.Errorf("expected last-resort 'myworker', got %q", result)
	}
}

func TestPickAgentService_NoLabels(t *testing.T) {
	services := map[string]interface{}{
		"mysvc": map[string]interface{}{"image": "alpine"},
	}
	result := pickAgentService(services)
	if result != "mysvc" {
		t.Errorf("expected last-resort 'mysvc', got %q", result)
	}
}

func TestTurnsByStageToLabel_EmptyMap(t *testing.T) {
	if got := turnsByStageToLabel(map[string]string{}); got != "" {
		t.Errorf("empty map = %q, want empty string", got)
	}
}

func TestTurnsByStageToLabel_EmptyKeyFiltered(t *testing.T) {
	got := turnsByStageToLabel(map[string]string{
		"":            "8",
		"   ":         "5",
		"in_progress": "3",
	})
	if got != "in_progress:3" {
		t.Errorf("got %q, want \"in_progress:3\"", got)
	}
}

func TestTurnsByStageToLabel_EmptyValueFiltered(t *testing.T) {
	got := turnsByStageToLabel(map[string]string{
		"refinement":  "",
		"in_progress": "  ",
		"review":      "5",
	})
	if got != "review:5" {
		t.Errorf("got %q, want \"review:5\"", got)
	}
}

func TestIntegration_AgentFiles_DeletePathEscape(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "afdpe@test.com", "DPE", "SecurePassword1")
	setupAgent(t, srv, "probe-dpe")

	req := httptest.NewRequest(http.MethodDelete, "/types/probe-dpe/files/..%2F..%2Fetc%2Fpasswd.md", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("path escape DELETE should not succeed (200)")
	}
}

func TestIntegration_AgentFiles_CreateAgent_SkipsRunsDir(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agcreatruns@test.com", "ACR", "SecurePassword1")
	setupAgent(t, srv, "probe-withruns")

	agDir := filepath.Join(srv.agentsDir, "agents", "probe-withruns")
	os.MkdirAll(filepath.Join(agDir, "runs", "run-001"), 0755)
	os.WriteFile(filepath.Join(agDir, "runs", "run-001", "output.txt"), []byte("run output"), 0644)
	os.MkdirAll(filepath.Join(agDir, ".git"), 0755)
	os.WriteFile(filepath.Join(agDir, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0644)

	cw := postJSONAuth(t, h, "/types", token, map[string]any{
		"name":     "probe-withruns-copy",
		"template": "probe-withruns",
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /types = %d, want 201: %s", cw.Code, cw.Body.String())
	}

	dstDir := filepath.Join(srv.agentsDir, "agents", "probe-withruns-copy")
	if _, err := os.Stat(filepath.Join(dstDir, "runs")); !os.IsNotExist(err) {
		t.Error("expected runs/ to be excluded from agent copy")
	}
	if _, err := os.Stat(filepath.Join(dstDir, ".git")); !os.IsNotExist(err) {
		t.Error("expected .git/ to be excluded from agent copy")
	}
}

func TestIntegration_CloneTask_DBGetError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "clonetaskdberr@test.com", "CTD", "SecurePassword1")

	srv.taskStore.DB().Close()
	req := httptest.NewRequest(http.MethodPost, "/tasks/some-id/clone", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST /tasks/clone with closed DB (Get fails) = %d, want 404", w.Code)
	}
}

func TestIntegration_CreateTask_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "createtaskdberr@test.com", "CTD2", "SecurePassword1")

	srv.taskStore.DB().Close()
	b, _ := json.Marshal(map[string]any{"title": "db error task"})
	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /tasks with closed DB = %d, want 500", w.Code)
	}
}

func TestIntegration_Audit_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "auditdberr@test.com", "ADB", "SecurePassword1")

	srv.auditStore.DB().Close()
	w := getJSON(t, h, "/audit", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET /audit with closed DB = %d, want 500", w.Code)
	}
}

func TestIntegration_Audit_WithTimeParams(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "audittimeparams@test.com", "ATP", "SecurePassword1")

	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	until := time.Now().UTC().Format(time.RFC3339)
	w := getJSON(t, h, "/audit?since="+since+"&until="+until+"&limit=10&offset=0", token)
	if w.Code != http.StatusOK {
		t.Errorf("GET /audit with time params = %d, want 200", w.Code)
	}
}

func TestIntegration_AgentFiles_WriteFileError(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "afwfe@test.com", "AFW", "SecurePassword1")
	setupAgent(t, srv, "probe-writefile-error")

	agDir := filepath.Join(srv.agentsDir, "agents", "probe-writefile-error")
	os.MkdirAll(filepath.Join(agDir, "output.md"), 0755)

	w := putRequest(t, h, "/types/probe-writefile-error/files/output.md", token, []byte("# content\n"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PUT when WriteFile target is dir = %d, want 500", w.Code)
	}
}

func TestIntegration_Audit_NilStore(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "auditnilstore@test.com", "ANS", "SecurePassword1")

	srv.auditStore = nil
	w := getJSON(t, h, "/audit", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /audit with nil auditStore = %d, want 404", w.Code)
	}
}

func TestIntegration_WriteProjectStageFile_InvalidProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wpsf-notfound@test.com", "WNF", "SecurePassword1")

	w := putRequest(t, h, "/projects/nonexistent-id/stage-files/prompts/backlog", token, []byte("content"))
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT invalid project = %d, want 404", w.Code)
	}
}

func TestIntegration_WriteProjectStageFile_InvalidKind(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "wpsf-badkind@test.com", "WBK", "SecurePassword1")
	id := createProject(t, h, token, "Write Bad Kind")

	w := putRequest(t, h, "/projects/"+id+"/stage-files/scripts/backlog", token, []byte("content"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT invalid kind = %d, want 400", w.Code)
	}
}

func TestIntegration_DeleteProjectStageFile_InvalidProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "dpsf-notfound@test.com", "DNF", "SecurePassword1")

	w := deleteRequest(t, h, "/projects/nonexistent-id/stage-files/prompts/backlog", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE invalid project = %d, want 404", w.Code)
	}
}

func TestIntegration_DeleteProjectStageFile_InvalidKind(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "dpsf-badkind@test.com", "DBK", "SecurePassword1")
	id := createProject(t, h, token, "Delete Bad Kind")

	w := deleteRequest(t, h, "/projects/"+id+"/stage-files/scripts/backlog", token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("DELETE invalid kind = %d, want 400", w.Code)
	}
}

func TestIntegration_TaskDeps_ListDBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "tdeplist-dberr@test.com", "TLD", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "dep list db err"})
	var task map[string]any
	json.NewDecoder(cw.Body).Decode(&task)
	id := task["id"].(string)

	srv.taskStore.DB().Close()

	w := getJSON(t, h, "/tasks/"+id+"/dependencies", token)
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusNotFound {
		t.Errorf("GET deps DB closed = %d, want 500 or 404", w.Code)
	}
}

func TestIntegration_CreateProject_InvalidRepoURL(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "createproj-badurl@test.com", "CPU", "SecurePassword1")

	w := postJSONAuth(t, h, "/projects", token, map[string]any{
		"name":     "Bad Repo",
		"repo_url": "file:///nonexistent/path/that/does/not/exist.git",
	})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /projects bad repo = %d, want 500: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_UpdateProject_DBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "updateproj-dberr@test.com", "UPD", "SecurePassword1")
	id := createProject(t, h, token, "Update DB Error")

	srv.projectStore.DB().Close()

	req := httptest.NewRequest(http.MethodPatch, "/projects/"+id, bytes.NewReader([]byte(`{"description":"new"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("PATCH /projects DB closed = %d, want 404", w.Code)
	}
}

func TestIntegration_TaskDeps_RemoveDBError(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "tdepremove-dberr@test.com", "TRD", "SecurePassword1")

	cw1 := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "blocker-rm-err"})
	var t1 map[string]any
	json.NewDecoder(cw1.Body).Decode(&t1)
	id1 := t1["id"].(string)

	cw2 := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "blocked-rm-err"})
	var t2 map[string]any
	json.NewDecoder(cw2.Body).Decode(&t2)
	id2 := t2["id"].(string)

	srv.taskStore.DB().Close()

	w := deleteRequest(t, h, "/tasks/"+id2+"/dependencies/"+id1, token)
	if w.Code != http.StatusInternalServerError && w.Code != http.StatusNotFound {
		t.Errorf("DELETE dep DB closed = %d, want 500 or 404", w.Code)
	}
}

func TestIntegration_ListSandboxes_Empty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sandboxes", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /sandboxes = %d", w.Code)
	}
	var out []any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array, got %d items", len(out))
	}
}

func TestIntegration_TaskSandbox_NoneIsNoContent(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	cw := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "no sandbox"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	if w := getJSON(t, h, "/tasks/"+created["id"].(string)+"/sandbox", ""); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Errorf("GET sandbox of a task without one = %d %q, want 204 and no body", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/tasks/0000000000000000/sandbox", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET sandbox of a missing task = %d, want 404", w.Code)
	}
}

func TestIntegration_ListSandboxes_IncludeAll(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sandboxes?include=all", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /sandboxes?include=all = %d", w.Code)
	}
}

func TestIntegration_ListSandboxQueue_Empty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sandboxes/queue", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /sandboxes/queue = %d", w.Code)
	}
	var out []any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array, got %d items", len(out))
	}
}

func TestIntegration_DeleteTaskSandbox_NoManager(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/tasks/sometaskid/sandbox", nil)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE /tasks/x/sandbox no manager = %d, want 503", w.Code)
	}
}

func TestIntegration_GetTaskSandbox_NoneIsNoContent(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sbget@test.com", "SB", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "no sandbox here")

	w := getJSON(t, h, "/tasks/"+id+"/sandbox", token)
	if w.Code != http.StatusNoContent {
		t.Fatalf("GET /tasks/{id}/sandbox without one = %d, want 204: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Errorf("204 carried a body: %q", w.Body.String())
	}

	w = getJSON(t, h, "/tasks/nosuchtask/sandbox", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/nosuchtask/sandbox = %d, want 404", w.Code)
	}
}

func TestSetSSEHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	setSSEHeaders(w)
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

func TestWriteSSELine_Success(t *testing.T) {
	var buf bytes.Buffer
	ok := writeSSELine(&buf, "hello world")
	if !ok {
		t.Fatal("writeSSELine returned false for valid writer")
	}
	if got := buf.String(); got != "data: hello world\n\n" {
		t.Errorf("unexpected SSE output: %q", got)
	}
}

func TestWriteSSELine_MultiLine(t *testing.T) {
	var buf bytes.Buffer
	ok := writeSSELine(&buf, "line1\nline2")
	if !ok {
		t.Fatal("writeSSELine returned false")
	}
	got := buf.String()
	if !strings.Contains(got, "data: line1\n") || !strings.Contains(got, "data: line2\n") {
		t.Errorf("unexpected SSE output: %q", got)
	}
}

func TestWriteSSELine_BrokenWriter(t *testing.T) {
	ok := writeSSELine(brokenWriter{}, "anything")
	if ok {
		t.Error("writeSSELine should return false for broken writer")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("broken") }

func TestFindLogFile_ShortID(t *testing.T) {
	if got := findLogFileByContainerID("/tmp", "abc"); got != "" {
		t.Errorf("expected empty for short ID, got %q", got)
	}
}

func TestFindLogFile_MissingDir(t *testing.T) {
	if got := findLogFileByContainerID("/nonexistent/dir", "abcdef1234567890"); got != "" {
		t.Errorf("expected empty for missing dir, got %q", got)
	}
}

func TestFindLogFile_MatchFound(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatal(err)
	}
	containerID := "abcdef1234567890"
	logName := "session_" + containerID + ".log"
	logPath := filepath.Join(logsDir, logName)
	if err := os.WriteFile(logPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	got := findLogFileByContainerID(dir, containerID)
	if got != logPath {
		t.Errorf("got %q, want %q", got, logPath)
	}
}

func TestFindLogFile_NoMatch(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if got := findLogFileByContainerID(dir, "ffffffff11111111"); got != "" {
		t.Errorf("expected empty for no match, got %q", got)
	}
}

// K8s run ids share the agent prefix in their first 8 chars; a run must never resolve to another
// run's log file.
func TestFindLogFile_K8sRunIDNoPrefixCollision(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatal(err)
	}
	oldRun := "refiner-task-bbbb000000000002-refinement-1783858139130"
	newRun := "refiner-task-bbbb000000000003-refinement-1783892175632"
	oldPath := filepath.Join(logsDir, "refiner_2026-07-12_12-08-59_"+oldRun+".log")
	if err := os.WriteFile(oldPath, []byte("old run\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := findLogFileByContainerID(dir, newRun); got != "" {
		t.Errorf("in-progress run should not resolve to a file, got %q", got)
	}
	if got := findLogFileByContainerID(dir, oldRun); got != oldPath {
		t.Errorf("old run: got %q, want %q", got, oldPath)
	}
}

func TestResolveTaskWorkspace_NilStore(t *testing.T) {
	s := &Server{}
	owner, ws, _, projects, err := s.resolveTaskWorkspace(t.Context(), "any-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "any-id" || ws != "" || len(projects) != 0 {
		t.Errorf("expected owner=any-id empty result, got owner=%q ws=%q projects=%v", owner, ws, projects)
	}
}

func TestResolveTaskWorkspace_TaskNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	owner, _, _, _, err := srv.resolveTaskWorkspace(t.Context(), "nonexistent-task-id")
	if err == nil {
		t.Fatalf("missing task resolved to owner %q, want an error", owner)
	}
}

func TestToSandboxView_NilHostPorts(t *testing.T) {
	sb := &sandbox.Sandbox{
		TaskID:      "task-123",
		ContainerID: "ctr-abc",
		Image:       "octopus-runtime",
		Status:      sandbox.StatusReady,
		HostPorts:   nil,
	}
	v := toSandboxView(sb)
	if v.TaskID != "task-123" {
		t.Errorf("TaskID = %q", v.TaskID)
	}
	if v.HostPorts == nil {
		t.Error("HostPorts should be non-nil (empty map)")
	}
}

// Introspection is stamped, not fetched: the tokens are fake and tests must not depend on
// api.github.com.
func seedProjectCredential(t *testing.T, srv *Server, projectID, token string) string {
	t.Helper()
	ctx := context.Background()
	proj, err := srv.projectStore.Get(ctx, projectID)
	if err != nil {
		t.Fatalf("get project %s: %v", projectID, err)
	}
	c, err := srv.credStore.Create(ctx, proj.WorkspaceID, credentials.Input{
		Name:     "github for " + projectID,
		Provider: "github",
		Kind:     "github_pat",
		Payload:  map[string]string{"token": token},
	}, "tester")
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if err := srv.credStore.SetGrants(ctx, proj.WorkspaceID, c.ID, []credentials.Grant{
		{SubjectType: credentials.SubjectProject, SubjectID: projectID},
	}); err != nil {
		t.Fatalf("grant credential: %v", err)
	}
	if err := srv.credStore.SetIntrospection(ctx, proj.WorkspaceID, c.ID, credentials.Introspection{
		State: credentials.StateValid, Identity: "octocat",
	}); err != nil {
		t.Fatalf("stamp introspection: %v", err)
	}
	return c.ID
}
