package initwizard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine/opencode"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
)

func TestQuickInstallAsksOnlyEnginesRunnerAndGitHub(t *testing.T) {
	cfg, out := transcript(t, "1\n\nsk-ant-api03-abc\n\n", nil)

	if !cfg.Quick || cfg.Mode != ModeDocker || cfg.ComposeFile != standaloneComposeFile {
		t.Fatalf("quick=%v mode=%q compose=%q, want the published image on Docker", cfg.Quick, cfg.Mode, cfg.ComposeFile)
	}
	if cfg.Identity != IdentityNone {
		t.Errorf("identity = %q, want no login", cfg.Identity)
	}
	for _, key := range []string{"initwizard.sections.database.title", "initwizard.sections.network.title", "initwizard.sections.mcp.title"} {
		if title := i18n.Text(key); strings.Contains(out, title) {
			t.Errorf("the quick install showed %q:\n%s", title, out)
		}
	}
	if !strings.Contains(out, i18n.Text("initwizard.sections.engines.title")) || !strings.Contains(out, i18n.Text("initwizard.prompts.runner")) {
		t.Errorf("the quick install skipped the engines or the runner:\n%s", out)
	}
	if got := strings.Join(cfg.Engines, ","); got != engineClaude {
		t.Errorf("engines = %q, want claude-code", got)
	}
	if len(cfg.Credentials) != 1 || cfg.Credentials[0].Key != "sk-ant-api03-abc" {
		t.Errorf("credentials = %+v, want the Claude key", cfg.Credentials)
	}
	if cfg.Runner.Enabled {
		t.Error("Enter on the runner question installed it")
	}
	if cfg.WorkspaceName == "" || cfg.OperatorEmail == "" {
		t.Errorf("workspace %q / email %q: the defaults never ran", cfg.WorkspaceName, cfg.OperatorEmail)
	}
}

func TestEnginesCheckEachCredentialsShape(t *testing.T) {
	cfg, out := transcript(t, "1\n1\n2\n3\n\nsk-ant-api03-x\nsk-proj-1\nprovider-key\nftp://x\n\n\n\n", nil)

	if got := strings.Join(cfg.Engines, ","); got != engineCodex+","+engineOpenCode {
		t.Fatalf("engines = %q, want codex,opencode", got)
	}
	if !strings.Contains(out, i18n.Text("initwizard.cli.invalid_base_url", "entry", "ftp://x")) {
		t.Errorf("the ftp base URL was not refused:\n%s", out)
	}
	want := []EngineCredential{
		{Engine: engineCodex, Key: "sk-proj-1"},
		{Engine: engineOpenCode, Key: "provider-key", BaseURL: openCodeBaseURL, Model: opencode.DefaultModel},
	}
	if !slices.Equal(cfg.Credentials, want) {
		t.Errorf("credentials = %+v, want %+v", cfg.Credentials, want)
	}
}

func TestEnginesRefuseAnEmptySelection(t *testing.T) {
	cfg, out := transcript(t, "1\n1\n\n\n\n", nil)
	if !strings.Contains(out, i18n.Text("initwizard.cli.engines_none")) {
		t.Errorf("an empty selection went through:\n%s", out)
	}
	if got := strings.Join(cfg.Engines, ","); got != engineClaude {
		t.Errorf("engines = %q, want the default asked again", got)
	}
	if !slices.Contains(deferredNames(cfg.Deferred), i18n.Text("initwizard.deferred.llm_key.step")) {
		t.Error("no credential and nothing deferred")
	}
}

func stubRunnerHost(t *testing.T, tools []hostTool, loggedIn, npm bool) {
	t.Helper()
	oldTools, oldLogin, oldNpm := lookTools, claudeLogin, npmPresent
	t.Cleanup(func() { lookTools, claudeLogin, npmPresent = oldTools, oldLogin, oldNpm })
	lookTools = func(context.Context) []hostTool { return tools }
	claudeLogin = func(context.Context) (bool, string, bool) { return loggedIn, "op@example.com", true }
	npmPresent = func() bool { return npm }
}

func TestRunnerStepPlansClaudeFoldersAndService(t *testing.T) {
	stubRunnerHost(t, []hostTool{{name: "git", present: true}, {name: "node", present: true, version: "v22.1.0"}, {name: "claude"}}, false, true)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg, out := transcript(t, "1\n\n\n2\n\n"+repo+"\n"+t.TempDir()+"\n"+repo+"\n\n\n", nil)

	if !strings.Contains(out, i18n.Text("initwizard.runner.box_title")) {
		t.Errorf("the runner step never says what the runner does:\n%s", out)
	}
	r := cfg.Runner
	if !r.Enabled || !r.InstallClaude {
		t.Fatalf("runner = %+v, want it enabled with claude to install", r)
	}
	if !slices.Equal(r.Folders, []string{repo}) {
		t.Errorf("folders = %q, want only %s", r.Folders, repo)
	}
	if !strings.Contains(out, "is not a git checkout") {
		t.Errorf("a folder without .git went through:\n%s", out)
	}
	if want := runtimeHasService(); r.Service != want {
		t.Errorf("service = %v, want %v", r.Service, want)
	}
	if slices.Contains(deferredNames(cfg.Deferred), i18n.Text("initwizard.deferred.runner_login.step")) {
		t.Error("deferred a login for a claude that is not installed")
	}
}

func TestRunnerStepDefersWhatTheMachineLacks(t *testing.T) {
	stubRunnerHost(t, []hostTool{{name: "git"}, {name: "claude"}}, false, false)
	cfg, out := transcript(t, "1\n\n\n2\n\n\n", nil)

	names := strings.Join(deferredNames(cfg.Deferred), "|")
	for _, want := range []string{i18n.Text("initwizard.deferred.runner_tools.step"), i18n.Text("initwizard.deferred.runner_claude.step")} {
		if !strings.Contains(names, want) {
			t.Errorf("deferred = %q, missing %q", names, want)
		}
	}
	if cfg.Runner.InstallClaude {
		t.Error("planned an npm install with no npm")
	}
	if !strings.Contains(out, "npm install -g @anthropic-ai/claude-code@") {
		t.Errorf("no install command for claude:\n%s", out)
	}
}

func TestRunnerStepIsDockerOnly(t *testing.T) {
	iv := &Interviewer{Cfg: &Config{Mode: ModeKubernetes}}
	if err := iv.askRunner(); err != nil || iv.Cfg.Runner.Enabled {
		t.Fatalf("askRunner on Kubernetes: err=%v runner=%+v", err, iv.Cfg.Runner)
	}
}

func TestDefaultModeFollowsThePreviousRun(t *testing.T) {
	for _, c := range []struct {
		iv   Interviewer
		want int
	}{
		{Interviewer{}, 0},
		{Interviewer{PreviousMode: ModeDocker, PreviousQuick: true}, 0},
		{Interviewer{PreviousMode: ModeDocker}, 1},
		{Interviewer{PreviousMode: ModeKubernetes}, 2},
	} {
		if got := c.iv.defaultMode(); got != c.want {
			t.Errorf("mode=%q quick=%v: default %d, want %d", c.iv.PreviousMode, c.iv.PreviousQuick, got, c.want)
		}
	}
}

func TestReviewNamesTheRunnerPlan(t *testing.T) {
	w := &run{cfg: &Config{
		Mode: ModeDocker, Quick: true, ComposeFile: standaloneComposeFile,
		Engines:     []string{engineClaude},
		Credentials: []EngineCredential{{Engine: engineClaude, Key: "k"}},
		Runner:      RunnerPlan{Enabled: true, Service: true, InstallClaude: true, Folders: []string{"/a", "/b"}},
	}}
	var all strings.Builder
	for _, r := range w.reviewRows() {
		all.WriteString(r[0] + ": " + r[1] + "\n")
	}
	for _, want := range []string{
		i18n.Text("initwizard.review.install_quick"),
		i18n.Text("initwizard.review.runner_folders", "count", 2),
		i18n.Text("initwizard.review.runner_service"),
		i18n.Text("initwizard.review.with_credential"),
	} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("review omits %q:\n%s", want, all.String())
		}
	}
	if strings.Contains(all.String(), "k\n") {
		t.Errorf("the review shows a key:\n%s", all.String())
	}
}

func runtimeHasService() bool { return service.Supported() }

func TestWrapBreaksAtSpacesOnly(t *testing.T) {
	got := wrap("the stack runs deploy/docker-compose.standalone.yml on this machine\nsecond", 30)
	want := "the stack runs\ndeploy/docker-compose.standalone.yml\non this machine\nsecond"
	if got != want {
		t.Errorf("wrap =\n%q\nwant\n%q", got, want)
	}
	if got := wrap("  npm install -g @anthropic-ai/claude-code@2.1.280 now", 30); got != "  npm install -g\n  @anthropic-ai/claude-code@2.1.280\n  now" {
		t.Errorf("indented wrap = %q", got)
	}
}

func TestQuickInstallListsOnlyWhatItAsked(t *testing.T) {
	cfg, out := transcript(t, "1\n\n\n\n\n", nil)
	names := strings.Join(deferredNames(cfg.Deferred), "|")
	for _, never := range []string{i18n.Text("initwizard.deferred.sso.step"), i18n.Text("initwizard.deferred.license.step")} {
		if strings.Contains(names, never) {
			t.Errorf("deferred = %q, lists %q", names, never)
		}
	}
	if !strings.Contains(names, i18n.Text("initwizard.deferred.git_credential.step")) {
		t.Errorf("deferred = %q, want the skipped GitHub token", names)
	}
	if strings.Contains(out, i18n.Text("initwizard.prompts.github_server")) {
		t.Errorf("the quick install asked for a GitHub Enterprise server:\n%s", out)
	}
}

func TestWorkspaceSlugFollowsThePreviousRun(t *testing.T) {
	for _, c := range []struct{ answers, want string }{
		{"\n\n", "acme-eng"},
		{"Labs\n\n", "labs"},
	} {
		var out bytes.Buffer
		iv := &Interviewer{
			P:   NewTestPrompter(strings.NewReader(c.answers), &out),
			Cfg: &Config{Identity: IdentityNone}, Ctx: context.Background(),
		}
		iv.PreviousWorkspace.Name, iv.PreviousWorkspace.Slug = "Acme", "acme-eng"
		if err := iv.askWorkspace(); err != nil {
			t.Fatal(err)
		}
		if iv.Cfg.WorkspaceSlug != c.want {
			t.Errorf("answers %q: slug = %q, want %q", c.answers, iv.Cfg.WorkspaceSlug, c.want)
		}
	}
}

func TestLocalNetworkAsksOnlyTheDomain(t *testing.T) {
	var out bytes.Buffer
	iv := &Interviewer{
		P:        NewTestPrompter(strings.NewReader("\n"), &out),
		Cfg:      &Config{Mode: ModeDocker, ComposeFile: standaloneComposeFile, Identity: IdentityNone},
		Ctx:      context.Background(),
		Existing: &EnvFile{lines: []string{"OCTOPUS_TRUSTED_PROXIES=10.0.0.1"}},
	}
	if err := iv.askNetwork(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"initwizard.prompts.allowed_origins", "initwizard.prompts.reverse_proxies"} {
		if strings.Contains(out.String(), i18n.Text(key)) {
			t.Errorf("asked %q at localhost:\n%s", i18n.Text(key), out.String())
		}
	}
	if iv.Cfg.PublicOrigin != "http://localhost:8585" || iv.Cfg.TrustedProxies != "10.0.0.1" {
		t.Errorf("origins=%q proxies=%q", iv.Cfg.PublicOrigin, iv.Cfg.TrustedProxies)
	}
}

func TestPlainPromptsDrawNothing(t *testing.T) {
	_, out := transcript(t, "1\n\n\ny\n\n\n\n", nil)
	for _, glyph := range []string{"──▶", "╭", "◕"} {
		if strings.Contains(out, glyph) {
			t.Errorf("the plain prompts drew %q:\n%s", glyph, out)
		}
	}
}

func TestScreenNeverShowsAPassword(t *testing.T) {
	dsn := "postgres://octopus:s3cr3t@db:5432/octopus?sslmode=disable"
	if got := shown(dsn); strings.Contains(got, "s3cr3t") || !strings.Contains(got, "db:5432") {
		t.Errorf("shown(%q) = %q", dsn, got)
	}
	if got := promptLabel("DSN", dsn, false); strings.Contains(got, "s3cr3t") {
		t.Errorf("the default leaks the password: %q", got)
	}
	if got := shown("https://github.com"); got != "https://github.com" {
		t.Errorf("a URL without a password changed: %q", got)
	}
	token := "sk-ant-oat01-abcdefghijklmnopqrstuvwxyz"
	if got := maskSecret(token); strings.Contains(got, "sk-ant") || !strings.HasSuffix(got, "wxyz") {
		t.Errorf("maskSecret(%q) = %q", token, got)
	}
	if got := maskSecret("short-token"); got != "••••" {
		t.Errorf("a short key shows characters: %q", got)
	}
}
