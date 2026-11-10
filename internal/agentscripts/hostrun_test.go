package agentscripts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func bundleTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRunBundle_UnpacksUnderOctopusRoot(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	tgz := bundleTarGz(t, map[string]string{
		"prompts/main.md":          "identity",
		"skills/zebra/prompt.md":   "zebra",
		"auditor/main.md":          "auditor",
		"tools/thulu-x/skill.md":   "manual",
		"prompts/development.md":   "stage",
		"skills/_base/skill.yaml":  "name: _base",
		"skills/zebra/skill.yaml":  "name: zebra",
		"auditor/quality-check.md": "q",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "run-key" || r.URL.Path != "/run/bundle" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(tgz)
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := snippetsDir(t)
	wrapper := "set -euo pipefail; source " + filepath.Join(dir, "i18n.sh") +
		"; source " + filepath.Join(dir, "run-bundle.sh")
	cmd := exec.Command(bashOrSkip(t), "-c", wrapper)
	cmd.Env = append(cmd.Environ(), "OCTOPUS_LOCALE=en", "OCTOPUS_API="+srv.URL,
		"OCTOPUS_API_KEY=run-key", "STAGE=development", "OCTOPUS_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-bundle.sh: %v\n%s", err, out)
	}
	for path, want := range map[string]string{
		"instructions/prompts/main.md":        "identity",
		"instructions/prompts/development.md": "stage",
		"skills/zebra/prompt.md":              "zebra",
		"auditor/main.md":                     "auditor",
		"tools/thulu-x/skill.md":              "manual",
	} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
}

func hostRun(t *testing.T) (*adapterRun, string) {
	t.Helper()
	r := newAdapterRun(t, "claude", fakeClaudeEnv)
	return r, r.wd
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func flagValue(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 {
		return "", false
	}
	if i+1 >= len(args) {
		return "", true
	}
	return args[i+1], true
}

func TestClaudeAdapter_HostRun(t *testing.T) {
	r, root := hostRun(t)
	writeFile(t, filepath.Join(root, ".octopus-plugin", "skills", "zebra", "SKILL.md"), "---\nname: zebra\n---\n")
	local := filepath.Join(t.TempDir(), "local-mcp.json")
	writeFile(t, local, `{"mcpServers":{}}`)

	r.run(t, "claude-code.sh", "engine_auth_check && engine_run", "hello",
		"OCTOPUS_HOST_RUN=1", "OCTOPUS_ROOT="+root, "OCTOPUS_LLM_SOURCE=runner_login",
		"CLAUDE_CODE_OAUTH_TOKEN=daemon-token", "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=",
		"OCTOPUS_LOCAL_MCP_CONFIG="+local, "MCP_CONFIG_ARGS=--mcp-config /x/.mcp.json",
	)

	env, err := os.ReadFile(filepath.Join(r.log, "env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ANTHROPIC_API_KEY unset", "ANTHROPIC_AUTH_TOKEN unset", "CLAUDE_CODE_OAUTH_TOKEN unset"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("CLI env lacks %q:\n%s", want, env)
		}
	}
	args := readLines(t, filepath.Join(r.log, "args"))
	if v, ok := flagValue(args, "--plugin-dir"); !ok || v != filepath.Join(root, ".octopus-plugin") {
		t.Errorf("--plugin-dir = %q (%v), want the run's plugin:\n%s", v, ok, strings.Join(args, "\n"))
	}
	if v, ok := flagValue(args, "--setting-sources"); !ok || v != "" {
		t.Errorf("--setting-sources = %q (%v), want an empty list", v, ok)
	}
	var configs []string
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			configs = append(configs, args[i+1])
		}
	}
	if !slices.Equal(configs, []string{"/x/.mcp.json", local}) {
		t.Errorf("--mcp-config = %q, want Thulu's then the runner's", configs)
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Errorf("claude args lack --strict-mcp-config")
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".octopus-plugin", ".claude-plugin", "plugin.json"))
	if err != nil || !strings.Contains(string(manifest), `"name":"octopus-run"`) {
		t.Errorf("plugin manifest = %q, %v", manifest, err)
	}
	if out := r.run(t, "claude-code.sh", "engine_skills_dir", "", "OCTOPUS_HOST_RUN=1", "OCTOPUS_ROOT="+root); strings.TrimSpace(out) != filepath.Join(root, ".octopus-plugin", "skills") {
		t.Errorf("engine_skills_dir = %q", out)
	}
}

func TestClaudeAdapter_HostRunOptions(t *testing.T) {
	r, root := hostRun(t)
	local := filepath.Join(t.TempDir(), "local-mcp.json")
	writeFile(t, local, `{"mcpServers":{}}`)
	r.run(t, "claude-code.sh", "engine_auth_check && engine_run", "hello",
		"OCTOPUS_HOST_RUN=1", "OCTOPUS_ROOT="+root, "OCTOPUS_LLM_SOURCE=runner_login",
		"OCTOPUS_USER_SETTINGS=1", "AUDIT_ONLY=1", "OCTOPUS_LOCAL_MCP_CONFIG="+local,
	)
	args := readLines(t, filepath.Join(r.log, "args"))
	for _, flag := range []string{"--setting-sources", "--plugin-dir", "--mcp-config"} {
		if slices.Contains(args, flag) {
			t.Errorf("claude got %s:\n%s", flag, strings.Join(args, "\n"))
		}
	}
}

func TestClaudeAdapter_ContainerRunUnchanged(t *testing.T) {
	r, root := hostRun(t)
	writeFile(t, filepath.Join(root, ".octopus-plugin", "skills", "zebra", "SKILL.md"), "x")
	r.run(t, "claude-code.sh", "engine_auth_check && engine_run", "hello",
		"OCTOPUS_ROOT="+root, "ANTHROPIC_API_KEY=sk-ant-api-x", "OCTOPUS_LOCAL_MCP_CONFIG="+filepath.Join(root, "x.json"))
	args := readLines(t, filepath.Join(r.log, "args"))
	for _, flag := range []string{"--setting-sources", "--plugin-dir"} {
		if slices.Contains(args, flag) {
			t.Errorf("container run got %s", flag)
		}
	}
	if out := r.run(t, "claude-code.sh", "engine_skills_dir", ""); strings.TrimSpace(out) != filepath.Join(r.home, ".claude", "skills") {
		t.Errorf("engine_skills_dir = %q", out)
	}
	cmd := exec.Command(bashOrSkip(t), "-c", "source "+filepath.Join(snippetsDir(t), "i18n.sh")+
		"; source "+filepath.Join(snippetsDir(t), "engines", "claude-code.sh")+"; engine_auth_check")
	cmd.Env = append(append([]string{}, r.env...), "OCTOPUS_LLM_SOURCE=", "ANTHROPIC_API_KEY=", "CLAUDE_CODE_OAUTH_TOKEN=")
	if err := cmd.Run(); err == nil {
		t.Error("engine_auth_check passed with no credential and no runner login")
	}
}

func claudeProjectDir(t *testing.T, home, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".claude", "projects", regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(real, "-"))
}

func TestClaudeAdapter_HostSessionSaveStaysInRunDir(t *testing.T) {
	r, root := hostRun(t)
	const mine, theirs = "11111111-0000-0000-0000-000000000001", "22222222-0000-0000-0000-000000000002"
	own := filepath.Join(claudeProjectDir(t, r.home, root), mine+".jsonl")
	other := filepath.Join(r.home, ".claude", "projects", "-Users-person-elsewhere", theirs+".jsonl")
	writeFile(t, own, "{}\n")
	writeFile(t, other, "{}\n")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(other, later, later); err != nil {
		t.Fatal(err)
	}

	save := func(env ...string) string {
		out := filepath.Join(t.TempDir(), "snap")
		r.run(t, "claude-code.sh", "engine_session_save "+out, "", env...)
		b, _ := os.ReadFile(filepath.Join(out, "id"))
		return strings.TrimSpace(string(b))
	}
	if got := save("OCTOPUS_HOST_RUN=1", "OCTOPUS_ROOT="+root); got != mine {
		t.Errorf("host run saved session %q, want its own %q", got, mine)
	}
	if got := save(); got != theirs {
		t.Errorf("container run saved session %q, want the newest %q", got, theirs)
	}
}

func TestClaudeAdapter_HostRunKeepsTokenFileInRunTmp(t *testing.T) {
	r, root := hostRun(t)
	tmp := t.TempDir()
	r.run(t, "claude-code.sh", "engine_auth_check && engine_run", "hello",
		"OCTOPUS_HOST_RUN=1", "OCTOPUS_ROOT="+root, "OCTOPUS_TMP="+tmp,
		"OCTOPUS_ANTHROPIC_IDENTITY_JWT=hdr.claims.sig", "ANTHROPIC_FEDERATION_RULE_ID=fdrl_1",
		"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=",
	)
	env, err := os.ReadFile(filepath.Join(r.log, "env"))
	if err != nil {
		t.Fatal(err)
	}
	want := "ANTHROPIC_IDENTITY_TOKEN_FILE=" + filepath.Join(tmp, ".config", "octopus", "anthropic-identity.jwt")
	if !strings.Contains(string(env), want+"\n") {
		t.Errorf("CLI env lacks %q:\n%s", want, env)
	}
	if _, err := os.Stat(filepath.Join(r.home, ".config", "octopus")); err == nil {
		t.Error("host run wrote credentials under the person's ~/.config")
	}
}
