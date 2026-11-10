package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

type bundleFixture struct {
	srv   *Server
	h     http.Handler
	token string
}

func newBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "bundle@test.com", "BN", "SecurePassword1")

	writeAgent := func(name, stage, skills, mcp, identity string) {
		t.Helper()
		dir := filepath.Join(srv.agentsDir, "agents", name)
		if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "prompts", "main.md"),
			[]byte(identity), 0o644); err != nil {
			t.Fatalf("write prompt: %v", err)
		}
		compose := "services:\n  agent:\n    labels:\n" +
			"      thulu.agent: \"true\"\n" +
			"      thulu.type: \"" + name + "\"\n" +
			"      thulu.stages: \"" + stage + "\"\n" +
			"      thulu.skills.default." + stage + ": \"" + skills + "\"\n" +
			"      thulu.mcp.default." + stage + ": \"" + mcp + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
			t.Fatalf("write compose: %v", err)
		}
	}
	writeAgent("developer", "in_progress", "worktree-protocol", "thulu-github@write", "# Developer identity\n")
	writeAgent("acme-manager", "in_progress", "acme-secrets", "ticket-reader", "# Acme manager identity\n")

	writeSkill := func(name, body string) {
		t.Helper()
		dir := filepath.Join(srv.agentsDir, "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir skill %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	writeSkill("_base", "universal rules\n")
	writeSkill("worktree-protocol", "how to use a worktree\n")
	writeSkill("acme-secrets", "CLIENT ACME INTERNALS\n")

	toolDir := filepath.Join(srv.agentsDir, "tools", "thulu-github")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("mkdir tool: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "skill.md"), []byte("github manual\n"), 0o644); err != nil {
		t.Fatalf("write tool skill: %v", err)
	}

	srv.agentCatalog.Invalidate()
	return &bundleFixture{srv: srv, h: h, token: token}
}

func (f *bundleFixture) task(t *testing.T, agent string) (string, string) {
	t.Helper()
	id, workspaceID := f.taskRow(t, agent)
	return id, f.runKey(t, auth.RunScope{TaskID: id, Agent: agent, Stage: "in_progress"}, workspaceID)
}

func (f *bundleFixture) taskRow(t *testing.T, agent string) (id, workspaceID string) {
	t.Helper()
	w := postJSONAuth(t, f.h, "/tasks", f.token, map[string]any{
		"title":             "run for " + agent,
		"stage_assignments": map[string]string{"in_progress": agent},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return created.ID, created.WorkspaceID
}

func (f *bundleFixture) runKey(t *testing.T, scope auth.RunScope, workspaceID string) string {
	t.Helper()
	raw, _, err := f.srv.authStore.CreateRunScopedAPIKey(t.Context(), scope, workspaceID, time.Hour)
	if err != nil {
		t.Fatalf("mint run key: %v", err)
	}
	return raw
}

func (f *bundleFixture) fetchBundle(t *testing.T, key, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/run/bundle"+query, nil)
	if key != "" {
		r.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func entries(t *testing.T, body []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip: %v (body %q)", err, string(body[:min(len(body), 200)]))
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		data, rerr := io.ReadAll(tr)
		if rerr != nil {
			t.Fatalf("read %s: %v", hdr.Name, rerr)
		}
		out[hdr.Name] = string(data)
	}
	return out
}

func TestRunBundle_RequiresTheRunsOwnKey(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)

	if w := f.fetchBundle(t, "", ""); w.Code == http.StatusOK {
		t.Errorf("unauthenticated GET /run/bundle = 200, want a refusal")
	}
	r := httptest.NewRequest(http.MethodGet, "/run/bundle", nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("user-token GET /run/bundle = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
}

func TestRunBundle_ServesOnlyTheRunsOwnAgentAndGrants(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	_, devKey := f.task(t, "developer")

	w := f.fetchBundle(t, devKey, "?stage=in_progress")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /run/bundle = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Octopus-Agent"); got != "developer" {
		t.Errorf("X-Octopus-Agent = %q, want developer", got)
	}
	got := entries(t, w.Body.Bytes())

	for _, want := range []string{
		"prompts/main.md",
		"skills/_base/prompt.md",
		"skills/worktree-protocol/prompt.md",
		"tools/thulu-github/skill.md",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("bundle is missing %s; has %v", want, keysOf(got))
		}
	}
	if got["prompts/main.md"] != "# Developer identity\n" {
		t.Errorf("prompt = %q, want the developer's", got["prompts/main.md"])
	}

	for name, body := range got {
		if bytes.Contains([]byte(body), []byte("CLIENT ACME INTERNALS")) {
			t.Errorf("acme-secrets leaked into the developer bundle at %s", name)
		}
	}
	if _, ok := got["skills/acme-secrets/prompt.md"]; ok {
		t.Error("bundle carries a skill this stage was never granted")
	}
	if _, ok := got["prompts/../acme-manager/prompts/main.md"]; ok {
		t.Error("bundle escaped the agent's own prompt directory")
	}
	if _, ok := got["tools/ticket-reader/skill.md"]; ok {
		t.Error("bundle carries the manual of an unwired MCP")
	}
}

func TestRunBundle_AuditorGetsTheAuditedAgentsFiles(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	taskID, workspaceID := f.taskRow(t, "developer")
	dir := filepath.Join(f.srv.agentsDir, "agents", "developer", "auditor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "in_progress.md"), []byte("audit the developer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.srv.agentCatalog.Invalidate()

	for name, scope := range map[string]auth.RunScope{
		"on the agent's engine": {TaskID: taskID, Agent: auth.AuditorAgent, Stage: "in_progress", LLMAgent: "developer"},
		"on another engine": {
			TaskID: taskID, Agent: auth.AuditorAgent, Stage: "in_progress", LLMAgent: "developer", LLMEngine: "codex",
		},
	} {
		w := f.fetchBundle(t, f.runKey(t, scope, workspaceID), "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: GET /run/bundle = %d: %s", name, w.Code, w.Body.String())
		}
		got := entries(t, w.Body.Bytes())
		if got["auditor/in_progress.md"] != "audit the developer\n" {
			t.Errorf("%s: auditor prompt missing; has %v", name, keysOf(got))
		}
		if _, ok := got["skills/_base/prompt.md"]; !ok {
			t.Errorf("%s: _base missing; has %v", name, keysOf(got))
		}
		if _, ok := got["skills/worktree-protocol/prompt.md"]; ok {
			t.Errorf("%s: bundle carries the agent's skill", name)
		}
		if _, ok := got["tools/thulu-github/skill.md"]; ok {
			t.Errorf("%s: bundle carries an MCP manual; the auditor has no MCP", name)
		}
	}
}

func TestRunBundle_StageParamCannotReachAnotherAgent(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	_, devKey := f.task(t, "developer")

	w := f.fetchBundle(t, devKey, "?stage=in_progress")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Octopus-Agent"); got != "developer" {
		t.Fatalf("X-Octopus-Agent = %q — a query param changed the agent", got)
	}

	_, acmeKey := f.task(t, "acme-manager")
	iw := f.fetchBundle(t, acmeKey, "?stage=in_progress")
	if iw.Code != http.StatusOK {
		t.Fatalf("acme GET = %d: %s", iw.Code, iw.Body.String())
	}
	acme := entries(t, iw.Body.Bytes())
	if acme["prompts/main.md"] != "# Acme manager identity\n" {
		t.Errorf("acme prompt = %q", acme["prompts/main.md"])
	}
	if _, ok := acme["skills/acme-secrets/prompt.md"]; !ok {
		t.Error("the agent that WAS granted acme-secrets did not receive it")
	}
	if _, ok := acme["skills/worktree-protocol/prompt.md"]; ok {
		t.Error("acme-manager received a skill granted to the developer")
	}
}

func TestRunBundle_IsReproducible(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	_, key := f.task(t, "developer")

	first := f.fetchBundle(t, key, "?stage=in_progress").Body.Bytes()
	second := f.fetchBundle(t, key, "?stage=in_progress").Body.Bytes()
	if !bytes.Equal(first, second) {
		t.Errorf("two fetches differ: %d vs %d bytes", len(first), len(second))
	}
}

func TestRunBundle_MissingSkillDoesNotEmptyTheBundle(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	if err := os.RemoveAll(filepath.Join(f.srv.agentsDir, "skills", "worktree-protocol")); err != nil {
		t.Fatalf("rm skill: %v", err)
	}
	_, key := f.task(t, "developer")

	w := f.fetchBundle(t, key, "?stage=in_progress")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
	got := entries(t, w.Body.Bytes())
	if _, ok := got["prompts/main.md"]; !ok {
		t.Error("a missing skill cost the agent its prompt")
	}
	if _, ok := got["skills/_base/prompt.md"]; !ok {
		t.Error("a missing skill cost the agent the base rules")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRunBundle_IdentityComesFromTheKey(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	id, workspaceID := f.taskRow(t, "developer")

	t.Run("mention serves the mentioned agent", func(t *testing.T) {
		key := f.runKey(t, auth.RunScope{TaskID: id, Agent: "acme-manager", Stage: "mention"}, workspaceID)
		w := f.fetchBundle(t, key, "?stage=mention")
		if w.Code != http.StatusOK {
			t.Fatalf("mention bundle = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("X-Octopus-Agent"); got != "acme-manager" {
			t.Errorf("X-Octopus-Agent = %q, want the mentioned agent acme-manager", got)
		}
		if got := entries(t, w.Body.Bytes())["prompts/main.md"]; got != "# Acme manager identity\n" {
			t.Errorf("prompt = %q, want the mentioned agent's own", got)
		}
	})

	t.Run("the key's stage beats the query string", func(t *testing.T) {
		key := f.runKey(t, auth.RunScope{TaskID: id, Agent: "developer", Stage: "in_progress"}, workspaceID)
		w := f.fetchBundle(t, key, "?stage=mention")
		if w.Code != http.StatusOK {
			t.Fatalf("bundle = %d: %s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("X-Octopus-Stage"); got != "in_progress" {
			t.Errorf("X-Octopus-Stage = %q, want in_progress from the key", got)
		}
		if _, ok := entries(t, w.Body.Bytes())["skills/worktree-protocol/prompt.md"]; !ok {
			t.Error("want the stage the key names, with its grants")
		}
	})

	t.Run("a chat needs no task", func(t *testing.T) {
		key := f.runKey(t, auth.RunScope{ChatID: "chat-abc", Agent: "developer", Stage: "chat"}, "")
		w := f.fetchBundle(t, key, "?stage=chat")
		if w.Code != http.StatusOK {
			t.Fatalf("chat bundle = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := entries(t, w.Body.Bytes())["prompts/main.md"]; got != "# Developer identity\n" {
			t.Errorf("prompt = %q, want the chat agent's", got)
		}
	})

	t.Run("a key without an agent still resolves from the task", func(t *testing.T) {
		key := f.runKey(t, auth.RunScope{TaskID: id}, workspaceID)
		w := f.fetchBundle(t, key, "?stage=in_progress")
		if w.Code != http.StatusOK {
			t.Fatalf("legacy bundle = %d, want 200: %s", w.Code, w.Body.String())
		}
		if got := w.Header().Get("X-Octopus-Agent"); got != "developer" {
			t.Errorf("X-Octopus-Agent = %q, want developer from the task", got)
		}
	})
}
