package initwizard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func runInterview(t *testing.T, iv *Interviewer, answers string) (*Config, string) {
	t.Helper()
	var out bytes.Buffer
	cfg := &Config{}
	iv.P = NewTestPrompter(strings.NewReader(answers), &out)
	iv.Cfg = cfg
	iv.Ctx = context.Background()
	iv.Detect = func() {}
	if err := iv.Run(); err != nil {
		t.Fatalf("interview: %v\n%s", err, out.String())
	}
	return cfg, out.String()
}

func transcript(t *testing.T, answers string, existing *EnvFile) (*Config, string) {
	t.Helper()
	return runInterview(t, &Interviewer{Existing: existing}, answers)
}

func TestInterviewDockerNoLogin(t *testing.T) {
	cfg, out := transcript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n", nil)

	if cfg.Mode != ModeDocker || cfg.ComposeFile != standaloneComposeFile {
		t.Errorf("mode=%q compose=%q", cfg.Mode, cfg.ComposeFile)
	}
	if cfg.Identity != IdentityNone {
		t.Fatalf("identity = %q, want none", cfg.Identity)
	}
	if cfg.Domain != "localhost" {
		t.Errorf("domain = %q, want localhost — the local stack must not default to the example domain", cfg.Domain)
	}
	if cfg.PublicOrigin != "http://localhost:8585" {
		t.Errorf("origins = %q: the dashboard and the API answer on 8585", cfg.PublicOrigin)
	}
	if cfg.DatabaseURL == cfg.BootstrapDSN {
		t.Errorf("daemon and wizard share a DSN (%q) — inside compose they cannot", cfg.DatabaseURL)
	}
	if strings.Contains(cfg.DatabaseURL, "octopus:octopus@") {
		t.Errorf("DSN %q uses the old shared password", cfg.DatabaseURL)
	}
	if cfg.PGPassword == "" || !strings.Contains(cfg.BootstrapDSN, cfg.PGPassword) {
		t.Errorf("bootstrap DSN %q does not carry the generated password", cfg.BootstrapDSN)
	}
	if got := strings.Join(cfg.MCPServers, ","); got != "octopus-api,thulu-github,thulu-browser" {
		t.Errorf("MCP servers = %q, want every native server except thulu-cloud and thulu-figma", got)
	}
	if strings.Contains(out, "agent definitions") {
		t.Errorf("the setup still offers a bundled agent pack:\n%s", out)
	}
	names := strings.Join(deferredNames(cfg.Deferred), ",")
	for _, want := range []string{"SSO", "EE license", "Git credential", "LLM key"} {
		if !strings.Contains(names, want) {
			t.Errorf("deferred = %q, missing %q", names, want)
		}
	}
	if !strings.Contains(out, "127.0.0.1") {
		t.Error("the no-login path never says the install is loopback-only")
	}
}

func TestInterviewOIDCRejectsHalfCredentials(t *testing.T) {
	_, out := transcriptErr(t, "2\n2\n\n1\nhttp://127.0.0.1:1/idp\ny\nclient-id\n\n")
	if !strings.Contains(out, "client ID and a client secret") {
		t.Errorf("expected the half-configured OIDC error, got:\n%s", out)
	}
}

func transcriptErr(t *testing.T, answers string) (*Config, string) {
	t.Helper()
	var out bytes.Buffer
	cfg := &Config{}
	iv := &Interviewer{
		P:      NewTestPrompter(strings.NewReader(answers), &out),
		Cfg:    cfg,
		Ctx:    context.Background(),
		Detect: func() {},
	}
	err := iv.Run()
	if err == nil {
		t.Fatalf("interview succeeded, expected a refusal:\n%s", out.String())
	}
	return cfg, err.Error()
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Default":         "default",
		"Acme  Platform!": "acme-platform",
		"   ":             "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInterviewHidesNoLoginOnTheSelfHostedStack(t *testing.T) {
	var out bytes.Buffer
	cfg := &Config{}
	iv := &Interviewer{
		P:      NewTestPrompter(strings.NewReader("2\n1\n\n1\nhttps://id.invalid\ny\nid\nsecret\n\n\ns\n"), &out),
		Cfg:    cfg,
		Ctx:    context.Background(),
		Detect: func() {},
	}
	_ = iv.Run()

	if cfg.ComposeFile != caddyComposeFile {
		t.Fatalf("compose = %q, want the self-hosted stack", cfg.ComposeFile)
	}
	if strings.Contains(out.String(), i18n.Text("initwizard.options.identity_none.label")) {
		t.Error("the self-hosted stack offered a no-login install")
	}
	if !strings.Contains(out.String(), i18n.Text("initwizard.cli.no_login_unavailable")) {
		t.Error("the menu never says why no-login is missing")
	}
}
