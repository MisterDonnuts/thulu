package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type tokenOptFixture struct {
	srv      *Server
	h        http.Handler
	token    string
	ws       string
	profile  string
	agents   *agentstore.Store
	provider *httptest.Server
	status   int
	answer   string
	got      map[string]any
}

func newTokenOptFixture(t *testing.T) *tokenOptFixture {
	t.Helper()
	srv, h := newIntegrationServer(t)
	f := &tokenOptFixture{srv: srv, h: h, status: http.StatusOK}
	f.token, f.ws = registerUserInWorkspace(t, h, "tokopt@test.com", "TO", "")
	srv.SetTokenOptimizer()

	agents, err := agentstore.New(srv.usageStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAgentStore(agents)
	f.agents = agents

	f.provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.got = nil
		_ = json.Unmarshal(raw, &f.got)
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.answer)
	}))
	t.Cleanup(f.provider.Close)
	srv.SetLLMUpstreams(map[string]string{"anthropic": f.provider.URL})

	p, err := srv.secretsStore.CreateLLMProfile(t.Context(), f.ws, secrets.LLMProfileInput{
		Name: "haiku", Engine: "claude-code", RawKey: "sk-ant-api03-tokopt-test", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.profile = p.ID
	return f
}

func (f *tokenOptFixture) agent(t *testing.T, name string, policy types.TokenOptimizer) {
	t.Helper()
	at := types.AgentType{Name: name, Type: name, TokenOptimizer: policy}
	if _, err := f.agents.PutAgent(t.Context(), f.srv.catalogTenant(t.Context(), f.ws), agentstore.Agent{AgentType: at}, "test"); err != nil {
		t.Fatal(err)
	}
	f.srv.invalidateCatalog(f.srv.catalogTenant(t.Context(), f.ws))
}

var haikuWorker = types.TokenOptimizer{Level: "suggest", Engine: "claude-code", Model: "claude-haiku-4-5"}

func (f *tokenOptFixture) delegate(t *testing.T, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/internal/token-optimizer/delegate", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *tokenOptFixture) runKey(t *testing.T, scope auth.RunScope) string {
	t.Helper()
	raw, _, err := f.srv.authStore.CreateRunScopedAPIKey(t.Context(), scope, f.ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Code
}

var tokenOptRead = map[string]any{
	"kind": "read", "question": "What does F return?",
	"files": []map[string]string{{"path": "a.go", "content": "package a\nfunc F() error { return nil }"}},
}

func (f *tokenOptFixture) workers(t *testing.T) map[string]map[string]any {
	t.Helper()
	w := getJSON(t, f.h, "/token-optimizer/workers", f.token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET workers = %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "sk-ant-") {
		t.Fatalf("a credential leaked: %s", w.Body)
	}
	var body struct {
		Workers []map[string]any `json:"workers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, wk := range body.Workers {
		for _, k := range []string{"engine", "available", "via", "provider", "profile_name", "reason"} {
			if _, ok := wk[k]; !ok {
				t.Errorf("worker is missing %q: %v", k, wk)
			}
		}
		name, _ := wk["engine"].(string)
		out[name] = wk
	}
	return out
}

func TestTokenOptimizerWorkersAPI(t *testing.T) {
	f := newTokenOptFixture(t)

	got := f.workers(t)
	cc := got["claude-code"]
	if cc["available"] != true || cc["via"] != "api" || cc["provider"] != "anthropic" || cc["profile_name"] != "haiku" || cc["reason"] != "" {
		t.Fatalf("claude-code with an API key = %v", cc)
	}
	for name, wk := range got {
		if name != "claude-code" && (wk["available"] != false || wk["reason"] != "no_default_profile" || wk["via"] != "") {
			t.Fatalf("%s without a default profile = %v", name, wk)
		}
	}

	f.oauthProfile(t, secrets.LLMProfileInput{IsDefault: true})
	if cc := f.workers(t)["claude-code"]; cc["available"] != true || cc["via"] != "cli" || cc["provider"] != "anthropic" || cc["profile_name"] != "sub" {
		t.Fatalf("claude-code with a subscription token = %v", cc)
	}

	if _, ok := got["codex"]; ok {
		f.oauthProfile(t, secrets.LLMProfileInput{Name: "codex-sub", Engine: "codex", IsDefault: true})
		if cx := f.workers(t)["codex"]; cx["available"] != false || cx["reason"] != "unsupported_credential" || cx["profile_name"] != "codex-sub" {
			t.Fatalf("codex with a Claude token = %v", cx)
		}
	}

	if w := getJSON(t, f.h, "/token-optimizer/settings", f.token); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "default_level") {
		t.Fatalf("the settings endpoint still answers: %s", w.Body)
	}
}

func TestTokenOptimizerResolveAPI(t *testing.T) {
	f := newTokenOptFixture(t)
	f.agent(t, "developer", types.TokenOptimizer{Level: "enforce", Engine: "claude-code", Model: "claude-haiku-4-5"})
	f.agent(t, "plain", types.TokenOptimizer{})
	f.agent(t, "orphan", types.TokenOptimizer{Level: "suggest", Engine: "opencode", Model: "m"})

	w := getJSON(t, f.h, "/token-optimizer/resolve?agent=developer", f.token)
	for _, want := range []string{`"level":"enforce"`, `"reason":""`, `"read_engine":"claude-code"`, `"read_via":"api"`, `"read_model":"claude-haiku-4-5"`, `"threshold_bytes":24576`} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("resolve developer = %d %s, want %s", w.Code, w.Body, want)
		}
	}
	if w := getJSON(t, f.h, "/token-optimizer/resolve?agent=plain", f.token); !strings.Contains(w.Body.String(), `"level":"off"`) {
		t.Fatalf("resolve plain = %s", w.Body)
	}
	if w := getJSON(t, f.h, "/token-optimizer/resolve?agent=orphan", f.token); !strings.Contains(w.Body.String(), `"reason":"no_worker"`) {
		t.Fatalf("resolve an engine without a default profile = %s", w.Body)
	}
	if w := getJSON(t, f.h, "/token-optimizer/resolve?agent=nobody", f.token); w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}
}

func TestTokenOptimizerRunEnvToken(t *testing.T) {
	f := newTokenOptFixture(t)
	at := &types.AgentType{Name: "developer", TokenOptimizer: haikuWorker}
	env := func() map[string]string {
		info, oauth, err := f.srv.tokenOptimizerWorker(t.Context(), f.ws, "claude-code")
		if err != nil {
			t.Fatal(err)
		}
		return tokenopt.Env(tokenopt.Resolve(at, "claude-code", info), oauth)
	}
	api := env()
	if api[tokenopt.EnvReadVia] != "api" || api[tokenopt.EnvLevel] != "suggest" {
		t.Fatalf("api env = %v", api)
	}
	for k, v := range api {
		if k == tokenopt.EnvWorkerOAuth || strings.Contains(v, "sk-ant") {
			t.Fatalf("an api worker's run got a credential: %s=%s", k, v)
		}
	}
	f.oauthProfile(t, secrets.LLMProfileInput{IsDefault: true})
	cli := env()
	if cli[tokenopt.EnvReadVia] != "cli" || cli[tokenopt.EnvReadModel] != "claude-haiku-4-5" || cli[tokenopt.EnvWorkerOAuth] != "sk-ant-oat01-subscription" {
		t.Fatalf("cli env = %v", cli)
	}
}

func TestTokenOptimizerDelegate(t *testing.T) {
	f := newTokenOptFixture(t)
	runKey := f.runKey(t, auth.RunScope{ChatID: "chat-1", Agent: "developer", Stage: "chat"})

	opKey, _, err := f.srv.authStore.CreateAPIKey(t.Context(), "ops", "test", auth.KeyGrant{
		WorkspaceID: f.ws, Role: auth.RoleOwner, Permissions: []auth.Permission{auth.PermAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.delegate(t, opKey, tokenOptRead); w.Code != http.StatusForbidden || errorCode(t, w) != "api.token_optimizer.errors.run_keys_only" {
		t.Fatalf("operator key = %d %s", w.Code, w.Body)
	}

	f.agent(t, "developer", types.TokenOptimizer{})
	if w := f.delegate(t, runKey, tokenOptRead); w.Code != http.StatusForbidden || errorCode(t, w) != "tokenopt.errors.disabled" {
		t.Fatalf("off = %d %s", w.Code, w.Body)
	}
	f.agent(t, "developer", types.TokenOptimizer{Level: "suggest", Engine: "opencode", Model: "m"})
	if w := f.delegate(t, runKey, tokenOptRead); w.Code != http.StatusFailedDependency {
		t.Fatalf("no default profile = %d %s", w.Code, w.Body)
	}

	f.agent(t, "developer", haikuWorker)
	if w := f.delegate(t, runKey, map[string]any{"kind": "read", "question": "q"}); w.Code != http.StatusBadRequest {
		t.Fatalf("read without files = %d", w.Code)
	}
	write := map[string]any{"kind": "write", "spec": "s", "reference": map[string]string{"path": "a.go", "content": "package a"}}
	if w := f.delegate(t, runKey, write); w.Code != http.StatusBadRequest || errorCode(t, w) != "tokenopt.errors.write_removed" {
		t.Fatalf("write = %d %s", w.Code, w.Body)
	}

	f.answer = `{"model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"- F (func, a.go:2) returns error"}],"usage":{"input_tokens":1000,"output_tokens":50}}`
	w := f.delegate(t, runKey, tokenOptRead)
	if w.Code != http.StatusOK {
		t.Fatalf("delegate = %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "text", "model", "provider", "input_tokens", "output_tokens", "cost_usd", "duration_ms"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("response is missing %q: %s", k, w.Body)
		}
	}
	id, _ := resp["id"].(string)
	if !strings.HasPrefix(id, "dlg_") || len(id) != len("dlg_")+16 || resp["provider"] != "anthropic" ||
		resp["text"] != "- F (func, a.go:2) returns error" || resp["input_tokens"] != float64(1000) {
		t.Fatalf("response = %s", w.Body)
	}
	if f.got["model"] != "claude-haiku-4-5" {
		t.Fatalf("the provider was asked for %v", f.got["model"])
	}

	f.status, f.answer = http.StatusInternalServerError, `{"error":{"message":"overloaded"}}`
	failed := f.delegate(t, runKey, tokenOptRead)
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("provider failure = %d: %s", failed.Code, failed.Body)
	}

	rows := f.delegationRows(t)
	ok, okFound := rows[id]
	if !okFound || ok.Status != usage.DelegationOK || ok.ChatID != "chat-1" || ok.Agent != "developer" || ok.Stage != "chat" ||
		ok.WorkspaceID != f.ws || ok.Kind != "read" || ok.Files != 1 || ok.ProfileID != f.profile ||
		ok.InputTokens != 1000 || ok.OutputTokens != 50 || ok.RequestBytes == 0 || ok.ResponseBytes == 0 {
		t.Fatalf("ok row = %+v (found %v)", ok, okFound)
	}
	var sawFailed bool
	for _, r := range rows {
		if r.Status == usage.DelegationFailed && r.ErrorCode == "tokenopt.errors.upstream_status" {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatalf("no failed row among %+v", rows)
	}
}

func (f *tokenOptFixture) delegationRows(t *testing.T) map[string]usage.Delegation {
	t.Helper()
	rows, err := f.srv.usageStore.DB().QueryContext(t.Context(), `SELECT id FROM delegations WHERE workspace_id = $1`, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out, err := f.srv.usageStore.DelegationsByID(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *tokenOptFixture) post(t *testing.T, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *tokenOptFixture) oauthProfile(t *testing.T, in secrets.LLMProfileInput) string {
	t.Helper()
	if in.Name == "" {
		in.Name = "sub"
	}
	if in.Engine == "" {
		in.Engine = "claude-code"
	}
	in.RawKey = "sk-ant-oat01-subscription"
	p, err := f.srv.secretsStore.CreateLLMProfile(t.Context(), f.ws, in)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestTokenOptimizerOAuthProfileIsCLIWorker(t *testing.T) {
	f := newTokenOptFixture(t)
	f.agent(t, "developer", haikuWorker)
	runKey := f.runKey(t, auth.RunScope{ChatID: "chat-2", Agent: "developer", Stage: "chat"})
	for name, in := range map[string]secrets.LLMProfileInput{
		"oauth":                   {Name: "sub", IsDefault: true},
		"oauth behind a base_url": {Name: "sub-proxy", BaseURL: "https://llm.example.com", IsDefault: true},
	} {
		f.oauthProfile(t, in)
		w := getJSON(t, f.h, "/token-optimizer/resolve?agent=developer", f.token)
		if !strings.Contains(w.Body.String(), `"level":"suggest"`) || !strings.Contains(w.Body.String(), `"read_via":"cli"`) {
			t.Fatalf("%s: resolve = %s", name, w.Body)
		}
		if w := f.delegate(t, runKey, tokenOptRead); w.Code != http.StatusFailedDependency || errorCode(t, w) != "tokenopt.errors.worker_in_pod" {
			t.Fatalf("%s: delegate to a cli worker = %d %s", name, w.Code, w.Body)
		}
	}
	if f.got != nil {
		t.Fatalf("the provider was called with an OAuth profile: %v", f.got)
	}
}

func TestTokenOptimizerPrepareFinish(t *testing.T) {
	f := newTokenOptFixture(t)
	scope := auth.RunScope{ChatID: "chat-3", Agent: "developer", Stage: "chat"}
	runKey := f.runKey(t, scope)
	prepare := func(key, kind string) *httptest.ResponseRecorder {
		return f.post(t, "/internal/token-optimizer/prepare", key, map[string]string{"kind": kind})
	}
	finish := func(key string, body map[string]any) *httptest.ResponseRecorder {
		return f.post(t, "/internal/token-optimizer/finish", key, body)
	}

	f.agent(t, "developer", types.TokenOptimizer{})
	if w := prepare(runKey, "read"); w.Code != http.StatusForbidden || errorCode(t, w) != "tokenopt.errors.disabled" {
		t.Fatalf("prepare while off = %d %s", w.Code, w.Body)
	}
	f.agent(t, "developer", haikuWorker)
	if w := prepare(runKey, "read"); w.Code != http.StatusFailedDependency || errorCode(t, w) != "tokenopt.errors.worker_not_in_pod" {
		t.Fatalf("prepare for an api worker = %d %s", w.Code, w.Body)
	}

	oauth := f.oauthProfile(t, secrets.LLMProfileInput{IsDefault: true})
	if w := prepare(runKey, "write"); w.Code != http.StatusBadRequest || errorCode(t, w) != "tokenopt.errors.write_removed" {
		t.Fatalf("prepare a write = %d %s", w.Code, w.Body)
	}
	if w := prepare(runKey, "delete"); w.Code != http.StatusBadRequest {
		t.Fatalf("prepare with a bad kind = %d", w.Code)
	}

	w := prepare(runKey, "read")
	if w.Code != http.StatusOK {
		t.Fatalf("prepare = %d %s", w.Code, w.Body)
	}
	var prep map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &prep); err != nil {
		t.Fatal(err)
	}
	id, _ := prep["id"].(string)
	if !strings.HasPrefix(id, "dlg_") || len(id) != len("dlg_")+16 || prep["model"] != "claude-haiku-4-5" ||
		prep["system_prompt"] != tokenopt.Instructions() || prep["max_output_tokens"] != float64(tokenopt.MaxOutputTokens) {
		t.Fatalf("prepare = %s", w.Body)
	}
	started := f.delegationRows(t)[id]
	if started.Status != usage.DelegationStarted || started.Provider != "claude-code" || started.ProfileID != oauth ||
		started.ChatID != "chat-3" || started.Agent != "developer" || started.Stage != "chat" || started.Kind != "read" {
		t.Fatalf("started row = %+v", started)
	}

	done := map[string]any{"id": id, "status": "ok", "error_code": "", "files": 3, "request_bytes": 19000,
		"response_bytes": 900, "input_tokens": 5200, "output_tokens": 300, "cost_usd": 0.0026, "duration_ms": 11000}

	for name, body := range map[string]map[string]any{
		"bad status":         {"id": id, "status": "started"},
		"foreign error code": {"id": id, "status": "failed", "error_code": "api.server.errors.internal"},
		"negative tokens":    {"id": id, "status": "ok", "input_tokens": -1},
	} {
		if w := finish(runKey, body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: finish = %d %s", name, w.Code, w.Body)
		}
	}
	for name, other := range map[string]auth.RunScope{
		"another chat":  {ChatID: "chat-4", Agent: "developer", Stage: "chat"},
		"another agent": {ChatID: "chat-3", Agent: "reviewer", Stage: "chat"},
	} {
		if w := finish(f.runKey(t, other), done); w.Code != http.StatusNotFound {
			t.Fatalf("%s: finish = %d %s", name, w.Code, w.Body)
		}
	}
	if w := finish(runKey, map[string]any{"id": "dlg_0000000000000000", "status": "ok"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: finish = %d", w.Code)
	}

	w = finish(runKey, done)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"`+id+`"`) {
		t.Fatalf("finish = %d %s", w.Code, w.Body)
	}
	row := f.delegationRows(t)[id]
	if row.Status != usage.DelegationOK || row.Files != 3 || row.RequestBytes != 19000 || row.ResponseBytes != 900 ||
		row.InputTokens != 5200 || row.OutputTokens != 300 || row.CostUSD != 0.0026 || row.CostEstimated || row.DurationMs != 11000 {
		t.Fatalf("finished row = %+v", row)
	}
	if w := finish(runKey, done); w.Code != http.StatusNotFound {
		t.Fatalf("second finish = %d %s", w.Code, w.Body)
	}

	w = prepare(runKey, "read")
	_ = json.Unmarshal(w.Body.Bytes(), &prep)
	failedID, _ := prep["id"].(string)
	if w := finish(runKey, map[string]any{"id": failedID, "status": "failed", "error_code": "tokenopt.errors.cli_failed",
		"input_tokens": 1000, "duration_ms": 40}); w.Code != http.StatusOK {
		t.Fatalf("failed finish = %d %s", w.Code, w.Body)
	}
	failedRow := f.delegationRows(t)[failedID]
	if failedRow.Status != usage.DelegationFailed || failedRow.ErrorCode != "tokenopt.errors.cli_failed" {
		t.Fatalf("failed row = %+v", failedRow)
	}
	if price, ok := f.srv.usageStore.PricingForModel(t.Context(), "claude-haiku-4-5"); ok {
		if want := price.Cost(1000, 0, 0, 0); failedRow.CostUSD != want || failedRow.CostEstimated != (want > 0) {
			t.Fatalf("estimated cost = %v (estimated %v), want %v", failedRow.CostUSD, failedRow.CostEstimated, want)
		}
	}

	opKey, _, err := f.srv.authStore.CreateAPIKey(t.Context(), "ops", "test", auth.KeyGrant{
		WorkspaceID: f.ws, Role: auth.RoleOwner, Permissions: []auth.Permission{auth.PermAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := prepare(opKey, "read"); w.Code != http.StatusForbidden || errorCode(t, w) != "api.token_optimizer.errors.run_keys_only" {
		t.Fatalf("prepare with an operator key = %d %s", w.Code, w.Body)
	}
	if w := finish(opKey, done); w.Code != http.StatusForbidden {
		t.Fatalf("finish with an operator key = %d", w.Code)
	}
}
