package agentscripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const fakeOpenCode = `#!/bin/sh
n=$(cat "$FAKE_LOG/calls" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG/calls"
printf '%s\n' "$@" >"$FAKE_LOG/args.$n"
env >"$FAKE_LOG/env.$n"
cat >"$FAKE_LOG/stdin.$n"
case "$1" in
run)
  case " $* " in
  *" --session "*)
    if [ "${FAKE_RESUME:-}" = fail ]; then
      echo "Error: Session not found" >&2
      exit 1
    fi ;;
  esac
  echo '{"type":"step_start","timestamp":1,"sessionID":"ses_T","part":{}}'
  echo '{"type":"step_finish","timestamp":2,"sessionID":"ses_T","part":{"reason":"stop"}}'
  ;;
session) printf '[\n  {\n    "id": "ses_T",\n    "title": "x"\n  }\n]\n' ;;
export) echo "Exporting session: $2" >&2; printf '{"info":{"id":"%s"},"messages":[]}\n' "$2" ;;
import)
  mkdir -p "$HOME/.local/share/opencode" && : >"$HOME/.local/share/opencode/opencode.db"
  echo "Imported session: ses_T"
  ;;
esac
`

func needNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
}

func newOpenCodeRun(t *testing.T) *adapterRun {
	t.Helper()
	needNode(t)
	r := newAdapterRun(t, "opencode", fakeOpenCode)
	r.env = append(r.env,
		"XDG_CONFIG_HOME=", "XDG_DATA_HOME=",
		"LLM_API_KEY=sk-secret-run-token", "OCTOPUS_API_KEY=oct_run_key",
		"OCTOPUS_LLM_PROVIDER=", "OCTOPUS_LLM_BASE_URL=https://api.deepseek.com/",
		`OCTOPUS_MCP_SERVERS=[{"name":"octopus-api","url":"http://octopus:8585/mcp-proxy/octopus-api","transport":"http","run_key":true},`+
			`{"name":"docs","url":"https://docs.example/mcp","transport":"sse","run_key":false}]`,
		"OPENCODE_CONFIG=/somewhere/else.json", "OPENCODE_CONFIG_DIR=/somewhere",
	)
	return r
}

func (r *adapterRun) envOf(t *testing.T, n int) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.log, "env."+strconv.Itoa(n)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestOpenCodeAdapter_FreshRunConfig(t *testing.T) {
	r := newOpenCodeRun(t)
	global := filepath.Join(r.home, ".config", "opencode")
	writeFile(t, filepath.Join(global, "opencode.json"), `{"mcp":{"planted":{"type":"remote","url":"https://evil.example"}}}`)
	writeFile(t, filepath.Join(r.home, ".opencode", "agent", "x.md"), "planted agent")

	out := r.run(t, "opencode.sh", "engine_run", "do the task")

	if got := firstLine(out); got != `{"type":"octopus.session","mode":"fresh","model":"deepseek-chat"}` {
		t.Errorf("first line = %q", got)
	}
	args, stdin := r.call(t, 1)
	for _, want := range [][]string{{"run"}, {"--format", "json"}, {"--auto"}, {"-m", "openai-compatible/deepseek-chat"}} {
		if !hasArg(args, want...) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if stdin != "do the task" {
		t.Errorf("stdin = %q", stdin)
	}

	env := r.envOf(t, 1)
	for k, want := range map[string]string{
		"OPENCODE_AUTH_CONTENT":           "{}",
		"OPENCODE_DISABLE_PROJECT_CONFIG": "1",
		"OPENCODE_PURE":                   "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":    "1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	for _, k := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s reached the CLI: another config layer", k)
		}
	}
	if _, err := os.Stat(filepath.Join(global, "opencode.json")); err == nil {
		t.Error("the planted global config survived")
	}
	if _, err := os.Stat(filepath.Join(r.home, ".opencode")); err == nil {
		t.Error("~/.opencode survived")
	}
	if fi, err := os.Stat(global); err != nil || fi.Mode().Perm()&0o200 != 0 {
		t.Errorf("the global config dir must be left read-only: %v %v", fi, err)
	}

	raw := env["OPENCODE_CONFIG_CONTENT"]
	if strings.Contains(raw, "sk-secret-run-token") || strings.Contains(raw, "oct_run_key") {
		t.Fatalf("a secret is in the config text: %s", raw)
	}
	var cfg struct {
		EnabledProviders []string `json:"enabled_providers"`
		Model            string   `json:"model"`
		Provider         map[string]struct {
			NPM     string                    `json:"npm"`
			Options map[string]string         `json:"options"`
			Models  map[string]map[string]any `json:"models"`
		} `json:"provider"`
		MCP map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			OAuth   bool              `json:"oauth"`
			Headers map[string]string `json:"headers"`
		} `json:"mcp"`
		Skills struct {
			Paths []string `json:"paths"`
		} `json:"skills"`
		Share string `json:"share"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config is not JSON: %v\n%s", err, raw)
	}
	p := cfg.Provider["openai-compatible"]
	if p.NPM != "@ai-sdk/openai-compatible" || p.Options["baseURL"] != "https://api.deepseek.com" ||
		p.Options["apiKey"] != "{env:LLM_API_KEY}" || p.Models["deepseek-chat"] == nil {
		t.Errorf("provider block = %+v", p)
	}
	if strings.Join(cfg.EnabledProviders, ",") != "openai-compatible" || cfg.Model != "openai-compatible/deepseek-chat" ||
		cfg.Share != "disabled" {
		t.Errorf("config = %+v", cfg)
	}
	api, docs := cfg.MCP["octopus-api"], cfg.MCP["docs"]
	if api.Type != "remote" || api.URL != "http://octopus:8585/mcp-proxy/octopus-api" || api.OAuth ||
		api.Headers["X-API-Key"] != "{env:OCTOPUS_API_KEY}" {
		t.Errorf("octopus-api = %+v", api)
	}
	if docs.Type != "remote" || docs.OAuth || len(docs.Headers) != 0 {
		t.Errorf("a server without run_key must get no key: %+v", docs)
	}
	if _, ok := cfg.MCP["planted"]; ok {
		t.Error("the planted server reached the config")
	}
	if len(cfg.Skills.Paths) != 1 || cfg.Skills.Paths[0] != filepath.Join(r.home, ".octopus", "opencode-skills") {
		t.Errorf("skills = %+v", cfg.Skills)
	}
}

func TestOpenCodeAdapter_AnthropicProvider(t *testing.T) {
	r := newOpenCodeRun(t)
	r.run(t, "opencode.sh", "engine_run", "hola",
		"OCTOPUS_LLM_PROVIDER=anthropic", "OCTOPUS_LLM_BASE_URL=https://api.deepseek.com/anthropic", "MODEL=deepseek-chat")
	args, _ := r.call(t, 1)
	if !hasArg(args, "-m", "anthropic/deepseek-chat") {
		t.Errorf("args = %q", args)
	}
	cfg := r.envOf(t, 1)["OPENCODE_CONFIG_CONTENT"]
	if !strings.Contains(cfg, `"baseURL":"https://api.deepseek.com/anthropic/v1"`) || !strings.Contains(cfg, `"@ai-sdk/anthropic"`) {
		t.Errorf("config = %s", cfg)
	}

	noModel := newOpenCodeRun(t)
	dir := snippetsDir(t)
	cmd := exec.Command(bashOrSkip(t), "-c", "set -eo pipefail; source "+filepath.Join(dir, "i18n.sh")+
		"; source "+filepath.Join(dir, "engines", "opencode.sh")+"; engine_run")
	cmd.Dir = noModel.wd
	cmd.Env = append(append([]string{}, noModel.env...), "OCTOPUS_LLM_PROVIDER=openai")
	cmd.Stdin = strings.NewReader("hola")
	outb, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(outb), "no model for a openai profile") {
		t.Errorf("an OpenAI profile with no model must stop the run: %v\n%s", err, outb)
	}
	if noModel.calls(t) != 0 {
		t.Error("the CLI ran with no model")
	}
}

func TestOpenCodeAdapter_SessionRoundTrip(t *testing.T) {
	r := newOpenCodeRun(t)
	writeFile(t, filepath.Join(r.home, ".local", "share", "opencode", "opencode.db"), "")
	writeFile(t, filepath.Join(r.home, ".local", "share", "opencode", "auth.json"), "secret")

	dir := filepath.Join(t.TempDir(), "out")
	r.run(t, "opencode.sh", "engine_session_save "+dir, "")
	if id, _ := os.ReadFile(filepath.Join(dir, "id")); strings.TrimSpace(string(id)) != "ses_T" {
		t.Errorf("id = %q", id)
	}
	if got := listFiles(t, filepath.Join(dir, "state")); strings.Join(got, ",") != "session.json" {
		t.Errorf("state = %q, want only the export", got)
	}

	next := newOpenCodeRun(t)
	out := next.run(t, "opencode.sh", "engine_run", "continue", "SESSION_ID=ses_T", "SESSION_STATE_DIR="+dir)
	if got := firstLine(out); got != `{"type":"octopus.session","mode":"native","model":"deepseek-chat"}` {
		t.Errorf("first line = %q", got)
	}
	if args, _ := next.call(t, 1); !hasArg(args, "import", filepath.Join(dir, "state", "session.json")) {
		t.Errorf("first call = %q, want the import", args)
	}
	if args, stdin := next.call(t, 2); !hasArg(args, "--session", "ses_T") || stdin != "continue" {
		t.Errorf("resume args = %q stdin = %q", args, stdin)
	}

	empty := newOpenCodeRun(t)
	dir2 := filepath.Join(t.TempDir(), "out")
	empty.run(t, "opencode.sh", "mkdir -p "+dir2+"; engine_session_save "+dir2, "")
	if _, err := os.Stat(filepath.Join(dir2, "id")); err == nil {
		t.Error("no session, but an id was written")
	}
}

func TestOpenCodeAdapter_FailedResumeStartsOver(t *testing.T) {
	r := newOpenCodeRun(t)
	writeFile(t, filepath.Join(r.home, ".local", "share", "opencode", "opencode.db"), "")
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "conversation.md")
	writeFile(t, transcript, "**assistant:** listo")

	out := r.run(t, "opencode.sh", "engine_run", "dale", "SESSION_ID=ses_gone",
		"CONVERSATION_FILE="+transcript, "FAKE_RESUME=fail")
	if got := sessionLines(out); len(got) != 1 || !strings.Contains(got[0], `"rehydrated"`) {
		t.Errorf("session lines = %q", got)
	}
	if n := r.calls(t); n != 2 {
		t.Fatalf("opencode ran %d times, want the resume and the new session", n)
	}
	args, stdin := r.call(t, 2)
	if hasArg(args, "--session") || !strings.Contains(stdin, "listo") || !strings.Contains(stdin, "dale") {
		t.Errorf("fallback args = %q stdin = %q", args, stdin)
	}

	fresh := newOpenCodeRun(t)
	fresh.run(t, "opencode.sh", "engine_run", "dale", "SESSION_ID=ses_gone", "CONVERSATION_FILE="+transcript)
	if n := fresh.calls(t); n != 1 {
		t.Errorf("opencode ran %d times, want only the new session", n)
	}
}
