package agentscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestClaudeAdapter_PersonalLogin(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	cfg := filepath.Join(filepath.Dir(r.home), "login")
	writeFile(t, filepath.Join(cfg, ".credentials.json"), `{"stored":"by claude auth login"}`)
	writeFile(t, filepath.Join(cfg, ".claude.json"), `{}`)
	writeFile(t, filepath.Join(cfg, "projects", "-workspace", "old.jsonl"), "earlier task")
	writeFile(t, filepath.Join(cfg, "skills", "old", "SKILL.md"), "earlier skill")
	writeFile(t, filepath.Join(cfg, ".hidden-leftover"), "x")
	env := []string{
		"OCTOPUS_LLM_SOURCE=personal_login", "CLAUDE_CONFIG_DIR=" + cfg,
		"CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=",
	}

	r.run(t, "claude-code.sh", "engine_auth_check", "", env...)
	entries, err := os.ReadDir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	if strings.Join(left, ",") != ".claude.json,.credentials.json" {
		t.Errorf("config dir after the check = %v, want only the login", left)
	}

	if got := strings.TrimSpace(r.run(t, "claude-code.sh", "engine_skills_dir", "", env...)); got != filepath.Join(cfg, "skills") {
		t.Errorf("skills dir = %q", got)
	}
	if got := strings.TrimSpace(r.run(t, "claude-code.sh", "claude_projects_dir", "", env...)); !strings.HasPrefix(got, filepath.Join(cfg, "projects")+"/") {
		t.Errorf("projects dir = %q", got)
	}
}

func TestClaudeAdapter_PersonalLoginMissing(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	cfg := filepath.Join(filepath.Dir(r.home), "login")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bashOrSkip(t), "-c", "source "+filepath.Join(snippetsDir(t), "i18n.sh")+
		"; source "+filepath.Join(snippetsDir(t), "engines", "claude-code.sh")+"; engine_auth_check")
	cmd.Env = append(append([]string{}, r.env...),
		"OCTOPUS_LLM_SOURCE=personal_login", "CLAUDE_CONFIG_DIR="+cfg, "ANTHROPIC_API_KEY=sk-ant-x")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "personal login") {
		t.Errorf("check passed or said %q", out)
	}
}
