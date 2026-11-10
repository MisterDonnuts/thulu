package initwizard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestInterviewWithoutSourceOffersTheCaddyStack(t *testing.T) {
	cfg, out := noSourceTranscript(t,
		"2\n1\n\n1\nhttps://id.invalid\ny\nclient-id\nclient-secret\n\nacme.com\n\ns\n\nthulu.acme.com\n\n\ns\n\n\n\n\n\n\n")

	if cfg.ComposeFile != caddyComposeFile {
		t.Fatalf("compose = %q, want %s", cfg.ComposeFile, caddyComposeFile)
	}
	if cfg.Image != "ghcr.io/misterdonnuts/octopus:1.4.0" {
		t.Errorf("image = %q: this stack pulls the release the binary belongs to", cfg.Image)
	}
	if args := strings.Join(ComposeArgs(cfg, nil), " "); strings.Contains(args, "--build") {
		t.Errorf("a stack with nothing to build is brought up with --build: docker %s", args)
	}
	if strings.Contains(out, "No login (local only)") {
		t.Error("no-login was offered on a stack Caddy publishes on a public domain")
	}
	if cfg.Bind != "" || cfg.TrustedProxies != "" {
		t.Errorf("bind=%q proxies=%q: Caddy is the only proxy and the stack publishes 80/443", cfg.Bind, cfg.TrustedProxies)
	}
	if cfg.PublicOrigin != "https://thulu.acme.com" {
		t.Errorf("origins = %q", cfg.PublicOrigin)
	}
	for _, d := range cfg.Deferred {
		if d.Step == "TLS" {
			t.Errorf("Caddy terminates TLS, yet TLS was left as pending: %+v", d)
		}
	}
}

func TestPendingTLSWithoutSourceNamesTheCaddyImageStack(t *testing.T) {
	iv := &Interviewer{
		P:        NewTestPrompter(strings.NewReader("0.0.0.0\n"), &bytes.Buffer{}),
		Cfg:      &Config{Mode: ModeDocker, ComposeFile: standaloneComposeFile, Identity: IdentityOIDC},
		NoSource: true,
	}
	if err := iv.askBind(); err != nil {
		t.Fatal(err)
	}
	if len(iv.Cfg.Deferred) != 1 || !strings.Contains(iv.Cfg.Deferred[0].Finish, caddyComposeFile) {
		t.Errorf("deferred = %+v, want a finish that names %s", iv.Cfg.Deferred, caddyComposeFile)
	}
}

func TestIdentityDefaultsToNoLoginOnLocalStacks(t *testing.T) {
	cfg, _ := noSourceTranscript(t, "2\n2\n\n\n\n\n\ns\n\n\n\n\n")
	if cfg.Identity != IdentityNone {
		t.Errorf("standalone: Enter picked %q, want no login", cfg.Identity)
	}

	existing := &EnvFile{lines: []string{"OCTOPUS_OIDC_ISSUER=https://id.invalid"}}
	iv := &Interviewer{Existing: existing, Cfg: &Config{Mode: ModeDocker, ComposeFile: devComposeFile}}
	opts := []Option{{Value: string(IdentityOIDC)}, {Value: string(IdentityGitHub)}, {Value: string(IdentityNone)}}
	if got := iv.defaultIdentity(opts); got != 0 {
		t.Errorf("a rerun over an OIDC install defaults to option %d", got)
	}
	iv = &Interviewer{Cfg: &Config{Mode: ModeDocker, ComposeFile: prodComposeFile}}
	if got := iv.defaultIdentity(opts[:2]); got != 0 {
		t.Errorf("self-hosted defaults to option %d, want OIDC", got)
	}
}

func TestInvalidDomain(t *testing.T) {
	for _, ok := range []string{"localhost", "thulu.acme.com", "192.168.1.10"} {
		if _, bad := invalidDomain(ok); bad {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "https://thulu.acme.com", "thulu.acme.com/", "acme.com:8443", "a b"} {
		if _, refused := invalidDomain(bad); !refused {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestInvalidOrigin(t *testing.T) {
	for _, list := range []string{"http://localhost:4321, https://thulu.acme.com,https://x.io/", "*", " * "} {
		if bad, refused := invalidOrigin(list); refused {
			t.Errorf("%q was refused at %q", list, bad)
		}
	}
	for list, want := range map[string]string{
		"https://ok.io,*":                      "*",
		"localhost:8585":                       "localhost:8585",
		"https://ok.io,thulu.acme.com":         "thulu.acme.com",
		"ftp://files.acme.com":                 "ftp://files.acme.com",
		"https://acme.com/app":                 "https://acme.com/app",
		"https://":                             "https://",
		"https://acme.com?x=1":                 "https://acme.com?x=1",
		"http://localhost:8585,https://u@a.io": "https://u@a.io",
	} {
		if got, _ := invalidOrigin(list); got != want {
			t.Errorf("invalidOrigin(%q) = %q, want %q", list, got, want)
		}
	}
}

func TestNormalizeOrigins(t *testing.T) {
	for in, want := range map[string]string{
		"https://thulu.acme.com/":                   "https://thulu.acme.com",
		" http://localhost:4321/ ,, https://x.io//": "http://localhost:4321,https://x.io",
		"http://localhost:8585":                     "http://localhost:8585",
		" * ":                                       "*",
	} {
		if got := normalizeOrigins(in); got != want {
			t.Errorf("normalizeOrigins(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNetworkStepKeepsTheWildcardOrigin(t *testing.T) {
	for _, file := range []string{prodComposeFile, devComposeFile} {
		ni := NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
		ni.NonInteractive = true
		iv := &Interviewer{
			P:   ni,
			Cfg: &Config{Mode: ModeDocker, ComposeFile: file},
			Ctx: context.Background(),
			Existing: &EnvFile{lines: []string{
				"OCTOPUS_DOMAIN=thulu.acme.com",
				"OCTOPUS_ALLOWED_ORIGINS=*",
			}},
		}
		if err := iv.askNetwork(); err != nil {
			t.Fatalf("%s: a rerun refused its own origins: %v", file, err)
		}
		if iv.Cfg.PublicOrigin != "*" {
			t.Errorf("%s: origins = %q, want *", file, iv.Cfg.PublicOrigin)
		}
	}
}

func TestNetworkStepAsksAgainForABadDomainAndOrigin(t *testing.T) {
	var out bytes.Buffer
	iv := &Interviewer{
		P:   NewTestPrompter(strings.NewReader("https://thulu.acme.com\nthulu.acme.com\nthulu.acme.com\nhttps://thulu.acme.com/\n"), &out),
		Cfg: &Config{Mode: ModeDocker, ComposeFile: prodComposeFile, Identity: IdentityOIDC},
		Ctx: context.Background(),
	}
	if err := iv.askNetwork(); err != nil {
		t.Fatalf("network: %v\n%s", err, out.String())
	}
	if iv.Cfg.Domain != "thulu.acme.com" || iv.Cfg.PublicOrigin != "https://thulu.acme.com" {
		t.Errorf("domain=%q origins=%q", iv.Cfg.Domain, iv.Cfg.PublicOrigin)
	}
	if n := strings.Count(out.String(), "! "); n != 2 {
		t.Errorf("%d warnings, want one per bad answer:\n%s", n, out.String())
	}

	ni := NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
	ni.NonInteractive = true
	iv = &Interviewer{
		P:        ni,
		Cfg:      &Config{Mode: ModeDocker, ComposeFile: prodComposeFile},
		Ctx:      context.Background(),
		Existing: &EnvFile{lines: []string{"OCTOPUS_DOMAIN=https://thulu.acme.com"}},
	}
	if err := iv.askNetwork(); err == nil || !strings.Contains(err.Error(), "OCTOPUS_DOMAIN") {
		t.Errorf("a non-interactive run took a bad domain: %v", err)
	}
}

func TestImageStepWarnsAboutAnArchitectureMismatch(t *testing.T) {
	for arch, warn := range map[string]bool{"riscv64": true, "arm64": false, "amd64": false, "": false} {
		var out bytes.Buffer
		iv := &Interviewer{
			P:        NewTestPrompter(strings.NewReader("\n"), &out),
			Cfg:      &Config{},
			HostArch: func() string { return arch },
		}
		if err := iv.askImage(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), "amd64, arm64"); got != warn {
			t.Errorf("arch %q: warned=%v, want %v\n%s", arch, got, warn, out.String())
		}
	}
}

func TestChoosePGHostPort(t *testing.T) {
	const legacyDSN = "postgres://octopus:pw@pg:5432/octopus"
	busy := func(ports ...int) func(int) bool {
		return func(p int) bool {
			for _, b := range ports {
				if b == p {
					return false
				}
			}
			return true
		}
	}
	cases := []struct {
		name string
		env  string
		free func(int) bool
		want string
	}{
		{"new install, 5433 free", "", busy(), ""},
		{"new install, 5433 taken", "", busy(5433), "5434"},
		{"new install, next one taken too", "", busy(5433, 5434), "5435"},
		{"no check available", "", nil, ""},
		{"rerun keeps its chosen port", "OCTOPUS_PG_HOST_PORT=5440", busy(5440), "5440"},
		{"rerun of an older install stays on 5433", "OCTOPUS_PG_PASSWORD=pw", busy(5433), ""},
		{"rerun with the password only in the DSN", "OCTOPUS_DATABASE_URL=" + legacyDSN, busy(5433), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			iv := &Interviewer{
				P:        NewTestPrompter(strings.NewReader(""), &out),
				Cfg:      &Config{},
				Existing: &EnvFile{lines: []string{c.env}},
				PortFree: c.free,
			}
			if got := iv.choosePGHostPort(); got != c.want {
				t.Errorf("port = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPublishedArchsMatchTheReleaseWorkflow(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "octopus-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, a := range publishedImageArchs {
		want = append(want, "linux/"+a)
	}
	line := "IMAGE_PLATFORMS: " + strings.Join(want, ",")
	if !strings.Contains(string(body), line) {
		t.Errorf("octopus-release.yml has no %q; keep publishedImageArchs and the workflow together", line)
	}
}

func TestCallbackBase(t *testing.T) {
	cases := []struct{ origins, want string }{
		{"http://localhost:8585", "http://localhost:8585"},
		{"http://localhost:4321,http://localhost:8585", "http://localhost:8585"},
		{"https://thulu.acme.com", "https://thulu.acme.com"},
		{"https://thulu.acme.com,https://admin.acme.com", "https://thulu.acme.com"},
		{"*", "https://fallback.example"},
		{"", "https://fallback.example"},
	}
	for _, c := range cases {
		if got := callbackBase(c.origins, "https://fallback.example"); got != c.want {
			t.Errorf("callbackBase(%q) = %q, want %q", c.origins, got, c.want)
		}
	}
}

func TestNetworkStepNamesTheCallbackWithThePort(t *testing.T) {
	var out bytes.Buffer
	ni := NewTestPrompter(strings.NewReader(""), &out)
	ni.NonInteractive = true
	iv := &Interviewer{
		P:   ni,
		Cfg: &Config{Mode: ModeDocker, ComposeFile: standaloneComposeFile, Identity: IdentityGitHub},
		Ctx: context.Background(),
	}
	if err := iv.askNetwork(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "http://localhost:8585/github/oauth/callback") {
		t.Errorf("callback without the daemon's port:\n%s", out.String())
	}
}

func TestCaddyStackHasNoDefaultDomain(t *testing.T) {
	ni := NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
	ni.NonInteractive = true
	iv := &Interviewer{
		P:   ni,
		Cfg: &Config{Mode: ModeDocker, ComposeFile: caddyComposeFile},
		Ctx: context.Background(),
	}
	if err := iv.askNetwork(); err == nil || !strings.Contains(err.Error(), "OCTOPUS_DOMAIN") {
		t.Errorf("a Caddy stack took a default domain: %v (domain %q)", err, iv.Cfg.Domain)
	}

	var out bytes.Buffer
	iv = &Interviewer{
		P:   NewTestPrompter(strings.NewReader("\nthulu.acme.com\n\n"), &out),
		Cfg: &Config{Mode: ModeDocker, ComposeFile: caddyComposeFile},
		Ctx: context.Background(),
	}
	if err := iv.askNetwork(); err != nil {
		t.Fatalf("network: %v\n%s", err, out.String())
	}
	if iv.Cfg.Domain != "thulu.acme.com" || !strings.Contains(out.String(), "Caddy") {
		t.Errorf("domain = %q; Enter was not asked again with a reason:\n%s", iv.Cfg.Domain, out.String())
	}
}

func TestClosedInputStopsARefusedAnswer(t *testing.T) {
	iv := &Interviewer{
		P:   NewTestPrompter(strings.NewReader(""), &bytes.Buffer{}),
		Cfg: &Config{Mode: ModeDocker, ComposeFile: prodComposeFile},
		Ctx: context.Background(),
	}
	if err := iv.askNetwork(); err == nil || !strings.Contains(err.Error(), "OCTOPUS_DOMAIN") {
		t.Errorf("err = %v, want the missing domain", err)
	}
}

func TestUnreachableIssuerStopsWhenNobodyCanAnswer(t *testing.T) {
	for _, nonInteractive := range []bool{false, true} {
		p := NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
		p.NonInteractive = nonInteractive
		iv := &Interviewer{
			P:        p,
			Cfg:      &Config{Mode: ModeDocker, ComposeFile: standaloneComposeFile},
			Ctx:      context.Background(),
			Existing: &EnvFile{lines: []string{"OCTOPUS_OIDC_ISSUER=http://127.0.0.1:1/idp"}},
		}
		done := make(chan error, 1)
		go func() { done <- iv.askOIDCIssuer() }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(apperr.Text(err), "127.0.0.1:1/idp") {
				t.Errorf("non-interactive=%v: err = %v, want the issuer that failed", nonInteractive, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("non-interactive=%v: the issuer step is still asking", nonInteractive)
		}
	}
}

func TestComposeStackKeepsABuildStackFromAnEarlierRun(t *testing.T) {
	for _, c := range []struct {
		previous string
		noSource bool
		want     string
	}{
		{"", false, caddyComposeFile},
		{standaloneComposeFile, false, standaloneComposeFile},
		{devComposeFile, false, devComposeFile},
		{prodComposeFile, false, prodComposeFile},
		{devComposeFile, true, caddyComposeFile},
	} {
		var out bytes.Buffer
		iv := &Interviewer{
			P:               NewTestPrompter(strings.NewReader("\n\n"), &out),
			Cfg:             &Config{Mode: ModeDocker},
			PreviousCompose: c.previous,
			NoSource:        c.noSource,
		}
		if err := iv.askComposeStack(); err != nil {
			t.Fatal(err)
		}
		if iv.Cfg.ComposeFile != c.want {
			t.Errorf("previous %q (no source %v): Enter picked %q, want %q", c.previous, c.noSource, iv.Cfg.ComposeFile, c.want)
		}
		if strings.Contains(out.String(), "docker-compose.dev.yml") && c.previous != devComposeFile {
			t.Errorf("previous %q: the menu offered the development stack:\n%s", c.previous, out.String())
		}
	}
}

func TestStackFlagSkipsTheModeAndStackQuestions(t *testing.T) {
	cfg, out := runInterview(t, &Interviewer{Stack: devComposeFile}, "3\n")
	if cfg.Mode != ModeDocker || cfg.ComposeFile != devComposeFile {
		t.Fatalf("mode=%q compose=%q, want Docker on the dev stack", cfg.Mode, cfg.ComposeFile)
	}
	if strings.Contains(out, "Server with a domain and HTTPS") {
		t.Errorf("-stack still showed the stack menu:\n%s", out)
	}
	if cfg.Identity != IdentityNone {
		t.Errorf("identity = %q: the first answer goes to the identity menu", cfg.Identity)
	}

	for _, iv := range []*Interviewer{{Stack: "deploy/nope.yml"}, {Stack: devComposeFile, NoSource: true}} {
		iv.P = NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
		iv.Cfg = &Config{}
		iv.Ctx = context.Background()
		iv.Detect = func() {}
		if err := iv.Run(); err == nil {
			t.Errorf("-stack %q (no source %v) was accepted", iv.Stack, iv.NoSource)
		}
	}
}
