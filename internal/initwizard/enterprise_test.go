package initwizard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func askStep(t *testing.T, cfg *Config, existing *EnvFile, answers string, step func(*Interviewer) error) string {
	t.Helper()
	var out bytes.Buffer
	iv := &Interviewer{
		P:        NewTestPrompter(strings.NewReader(answers), &out),
		Cfg:      cfg,
		Ctx:      context.Background(),
		Existing: existing,
	}
	if err := step(iv); err != nil {
		t.Fatalf("step: %v\n%s", err, out.String())
	}
	return out.String()
}

func TestTrustedProxiesPerStack(t *testing.T) {
	ask := func(iv *Interviewer) error { return iv.askTrustedProxies() }
	legacy := &EnvFile{lines: []string{"OCTOPUS_TRUSTED_PROXIES=private"}}

	cfg := &Config{Mode: ModeDocker, ComposeFile: prodComposeFile}
	out := askStep(t, cfg, legacy, "", ask)
	if cfg.TrustedProxies != "" {
		t.Errorf("self-hosted stack kept %q", cfg.TrustedProxies)
	}
	if !strings.Contains(out, "agent containers") {
		t.Errorf("dropping \"private\" was not explained:\n%s", out)
	}

	cfg = &Config{Mode: ModeDocker, ComposeFile: prodComposeFile}
	askStep(t, cfg, &EnvFile{lines: []string{"OCTOPUS_TRUSTED_PROXIES=10.9.0.0/24"}}, "", ask)
	if cfg.TrustedProxies != "10.9.0.0/24" {
		t.Errorf("self-hosted rerun lost the operator's value: %q", cfg.TrustedProxies)
	}

	cfg = &Config{Mode: ModeDocker, ComposeFile: devComposeFile}
	askStep(t, cfg, legacy, "", ask)
	if cfg.TrustedProxies != "" {
		t.Errorf("dev stack trusts %q", cfg.TrustedProxies)
	}

	cfg = &Config{Mode: ModeKubernetes}
	askStep(t, cfg, nil, "\n", ask)
	if cfg.TrustedProxies != "" {
		t.Errorf("kubernetes default = %q, want none", cfg.TrustedProxies)
	}
	cfg = &Config{Mode: ModeKubernetes}
	out = askStep(t, cfg, nil, "ingress-nginx\n10.42.0.0/24, 10.42.1.7\n", ask)
	if cfg.TrustedProxies != "10.42.0.0/24, 10.42.1.7" {
		t.Errorf("kubernetes = %q", cfg.TrustedProxies)
	}
	if !strings.Contains(out, "not an IP address or a CIDR") {
		t.Errorf("a name was accepted as a proxy address:\n%s", out)
	}
}

func TestDevStackAlwaysAllowsVite(t *testing.T) {
	cfg := &Config{Mode: ModeDocker, ComposeFile: devComposeFile, Identity: IdentityNone}
	existing := &EnvFile{lines: []string{"OCTOPUS_ALLOWED_ORIGINS=http://localhost:8585"}}
	askStep(t, cfg, existing, "\n\n", func(iv *Interviewer) error { return iv.askNetwork() })
	if !strings.Contains(cfg.PublicOrigin, "http://localhost:4321") {
		t.Errorf("origins = %q: the dev dashboard runs on Vite at 4321", cfg.PublicOrigin)
	}
}

func TestGitHubServer(t *testing.T) {
	ask := func(iv *Interviewer) error { return iv.askGitHubServer() }

	cfg := &Config{}
	askStep(t, cfg, nil, "\n", ask)
	if cfg.GitHubURL != "" || cfg.GitHubAPIURL != "" {
		t.Errorf("Enter configured a server: %q %q", cfg.GitHubURL, cfg.GitHubAPIURL)
	}

	cfg = &Config{}
	out := askStep(t, cfg, nil, "ghe.acme.example\nhttps://ghe.acme.example/\n", ask)
	if cfg.GitHubURL != "https://ghe.acme.example" || cfg.GitHubAPIURL != "https://ghe.acme.example/api/v3" {
		t.Errorf("GHES = %q / %q", cfg.GitHubURL, cfg.GitHubAPIURL)
	}
	if !strings.Contains(out, "not an http(s) URL") {
		t.Errorf("a bare host was accepted:\n%s", out)
	}

	cfg = &Config{}
	askStep(t, cfg, nil, "https://github.com\n", ask)
	if cfg.GitHubURL != "" {
		t.Errorf("github.com recorded as GHES: %q", cfg.GitHubURL)
	}

	existing := &EnvFile{lines: []string{
		"OCTOPUS_GITHUB_URL=https://ghe.acme.example",
		"OCTOPUS_GITHUB_API_URL=https://api.ghe.acme.example",
	}}
	cfg = &Config{}
	askStep(t, cfg, existing, "\n", ask)
	if cfg.GitHubAPIURL != "https://api.ghe.acme.example" {
		t.Errorf("rerun replaced the recorded API URL: %q", cfg.GitHubAPIURL)
	}
}

func TestEnterpriseSettingsReachEnvAndHelm(t *testing.T) {
	cfg := &Config{
		Mode: ModeKubernetes, Identity: IdentityOIDC, JWTSecret: "j", SecretboxKey: "k",
		DatabaseURL: "postgres://x", Domain: "thulu.acme.example",
		OIDC:      OIDCConfig{Issuer: "https://id", ClientID: "c", ClientSecret: "s", AllowedDomains: "acme.com"},
		GitHubURL: "https://ghe.acme.example", GitHubAPIURL: "https://ghe.acme.example/api/v3",
	}
	f := &EnvFile{}
	cfg.ApplyToEnv(f)
	for key, want := range map[string]string{
		"OCTOPUS_SECRETBOX_KEY":       "k",
		"OCTOPUS_SSO_ALLOWED_DOMAINS": "acme.com",
		"OCTOPUS_GITHUB_URL":          "https://ghe.acme.example",
		"OCTOPUS_GITHUB_API_URL":      "https://ghe.acme.example/api/v3",
	} {
		if got := f.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	(&Config{JWTSecret: "j"}).ApplyToEnv(f)
	if f.Get("OCTOPUS_SECRETBOX_KEY") != "k" {
		t.Error("the credential key was removed from deploy/.env")
	}

	values := cfg.HelmValues()
	secrets, _ := values["secrets"].(map[string]any)
	if secrets["secretboxKey"] != "k" {
		t.Errorf("secrets = %v, want secretboxKey", secrets)
	}
	config, _ := values["config"].(map[string]any)
	sso, _ := config["sso"].(map[string]any)
	if sso["allowedDomains"] != "acme.com" {
		t.Errorf("config.sso = %v", config["sso"])
	}
	github, _ := values["github"].(map[string]any)
	if github["url"] != "https://ghe.acme.example" || github["apiUrl"] != "https://ghe.acme.example/api/v3" {
		t.Errorf("github = %v", values["github"])
	}
}

func TestHelmValuesWithoutRuntimeClassSayItOnPurpose(t *testing.T) {
	values := (&Config{Mode: ModeKubernetes, JWTSecret: "j", DatabaseURL: "postgres://x"}).HelmValues()
	extra, _ := values["extraEnv"].(map[string]any)
	if extra["OCTOPUS_KUBE_ALLOW_NO_RUNTIMECLASS"] != "1" {
		t.Errorf("extraEnv = %v, want the explicit opt-out", extra)
	}
	runtime, _ := values["agentRuntime"].(map[string]any)
	policy, _ := runtime["admissionPolicy"].(map[string]any)
	if list, ok := policy["allowedRuntimeClasses"].([]any); !ok || len(list) != 0 {
		t.Errorf("admissionPolicy = %v, want an empty RuntimeClass list", runtime["admissionPolicy"])
	}

	values = (&Config{Mode: ModeKubernetes, RuntimeClass: "kata-clh", JWTSecret: "j", DatabaseURL: "postgres://x"}).HelmValues()
	extra, _ = values["extraEnv"].(map[string]any)
	if _, set := extra["OCTOPUS_KUBE_ALLOW_NO_RUNTIMECLASS"]; set {
		t.Error("a Kata install was told running without a RuntimeClass is fine")
	}
}

func TestLoadHelmSecretsReadsThePreviousRun(t *testing.T) {
	root := t.TempDir()
	deploy := filepath.Join(root, "deploy")
	if err := os.MkdirAll(deploy, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Mode: ModeKubernetes, Release: "prod", JWTSecret: "jwt-1", SecretboxKey: "box-1", DatabaseURL: "postgres://x"}
	if _, err := WriteHelmValues(root, cfg); err != nil {
		t.Fatal(err)
	}
	jwt, box := LoadHelmSecrets(root, "prod")
	if jwt != "jwt-1" || box != "box-1" {
		t.Errorf("LoadHelmSecrets = %q, %q", jwt, box)
	}
	if jwt, box := LoadHelmSecrets(root, "missing"); jwt != "" || box != "" {
		t.Errorf("a missing file produced secrets: %q %q", jwt, box)
	}

	if err := os.WriteFile(filepath.Join(root, "values-escape.yaml"), []byte("secrets:\n  jwtSecret: outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if jwt, _ := LoadHelmSecrets(root, "../values-escape"); jwt != "" {
		t.Errorf("read a values file outside deploy/: %q", jwt)
	}
}

func TestKubernetesRerunReadsTheValuesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Mode: ModeKubernetes, Identity: IdentityOIDC, Release: "prod",
		JWTSecret: "jwt-1", SecretboxKey: "box-1", LicenseKey: "lic-1",
		DatabaseURL: "postgres://u:p@db/octopus", WorkspaceID: "ws-1",
		Domain: "thulu.acme.com", PublicOrigin: "https://thulu.acme.com", TrustedProxies: "10.0.0.0/8",
		OIDC: OIDCConfig{Issuer: "https://id.acme.com", ClientID: "cid", ClientSecret: "csecret",
			DisplayName: "Acme", GroupsClaim: "roles", GroupRoleMap: "eng=member", AllowedDomains: "acme.com"},
		GitHubURL: "https://ghe.acme.com", GitHubAPIURL: "https://ghe.acme.com/api/v3",
	}
	if _, err := WriteHelmValues(root, cfg); err != nil {
		t.Fatal(err)
	}
	f := loadHelmEnv(root, "prod")
	if f == nil {
		t.Fatal("no values read back")
	}
	for key, want := range map[string]string{
		"OCTOPUS_DATABASE_URL":        cfg.DatabaseURL,
		"OCTOPUS_OPERATOR_WORKSPACE":  "ws-1",
		"OCTOPUS_LICENSE_KEY":         "lic-1",
		"OCTOPUS_JWT_SECRET":          "jwt-1",
		"OCTOPUS_SECRETBOX_KEY":       "box-1",
		"OCTOPUS_DOMAIN":              cfg.Domain,
		"OCTOPUS_ALLOWED_ORIGINS":     cfg.PublicOrigin,
		"OCTOPUS_TRUSTED_PROXIES":     cfg.TrustedProxies,
		"OCTOPUS_OIDC_ISSUER":         cfg.OIDC.Issuer,
		"OCTOPUS_OIDC_CLIENT_ID":      "cid",
		"OCTOPUS_OIDC_CLIENT_SECRET":  "csecret",
		"OCTOPUS_OIDC_NAME":           "Acme",
		"OCTOPUS_OIDC_GROUPS_CLAIM":   "roles",
		"OCTOPUS_OIDC_GROUP_ROLE_MAP": "eng=member",
		"OCTOPUS_SSO_ALLOWED_DOMAINS": "acme.com",
		"OCTOPUS_GITHUB_URL":          cfg.GitHubURL,
		"OCTOPUS_GITHUB_API_URL":      cfg.GitHubAPIURL,
	} {
		if got := f.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	p := NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
	p.NonInteractive = true
	iv := &Interviewer{P: p, Cfg: &Config{Mode: ModeKubernetes}, Ctx: context.Background()}
	iv.PreviousCluster.Namespace, iv.PreviousCluster.Release = "acme", "prod"
	iv.PreviousCluster.AgentImage = "registry.acme.com/agent:1"
	if err := iv.askKubernetes(); err != nil {
		t.Fatal(err)
	}
	if c := iv.Cfg; c.Namespace != "acme" || c.Release != "prod" || c.RuntimeClass != "" || c.AgentImage != "registry.acme.com/agent:1" {
		t.Errorf("rerun answered namespace=%q release=%q runtime class=%q image=%q", c.Namespace, c.Release, c.RuntimeClass, c.AgentImage)
	}
}

func TestEnsureSecretKeepsTheCredentialKey(t *testing.T) {
	root := t.TempDir()
	env := &EnvFile{lines: []string{"OCTOPUS_JWT_SECRET=jwt-old", "OCTOPUS_SECRETBOX_KEY=box-old"}}
	w := &run{opts: Options{Root: root}, cfg: &Config{Mode: ModeDocker}, env: env}
	if err := w.ensureSecret(); err != nil {
		t.Fatal(err)
	}
	if w.cfg.JWTSecret != "jwt-old" || w.cfg.SecretboxKey != "box-old" {
		t.Errorf("rerun replaced secrets: %q %q", w.cfg.JWTSecret, w.cfg.SecretboxKey)
	}

	w = &run{opts: Options{Root: root}, cfg: &Config{Mode: ModeDocker},
		env: &EnvFile{lines: []string{"OCTOPUS_JWT_SECRET=jwt-old"}}}
	if err := w.ensureSecret(); err != nil {
		t.Fatal(err)
	}
	if w.cfg.JWTSecret != "jwt-old" || len(w.cfg.SecretboxKey) != 64 {
		t.Errorf("upgrade = %q / %q", w.cfg.JWTSecret, w.cfg.SecretboxKey)
	}
	if len(w.cfg.DevToken) != 64 {
		t.Errorf("no dev token generated: %q", w.cfg.DevToken)
	}

	w = &run{opts: Options{Root: root}, cfg: &Config{Mode: ModeDocker},
		env: &EnvFile{lines: []string{"OCTOPUS_JWT_SECRET=jwt-old", "OCTOPUS_DEV_TOKEN=dev-old"}}}
	if err := w.ensureSecret(); err != nil {
		t.Fatal(err)
	}
	if w.cfg.DevToken != "dev-old" {
		t.Errorf("rerun replaced the dev token: %q", w.cfg.DevToken)
	}
}
