package initwizard

import (
	"strings"
	"testing"
)

func TestApplyToEnvDevModePairsTheContainerFlag(t *testing.T) {
	cfg := &Config{Identity: IdentityNone, JWTSecret: "abc", DatabaseURL: "postgres://x"}
	f := &EnvFile{}
	cfg.ApplyToEnv(f)

	body := f.Render()
	if !strings.Contains(body, "OCTOPUS_DEV_MODE=1") || !strings.Contains(body, "OCTOPUS_DEV_MODE_CONTAINER=1") {
		t.Errorf("dev mode written without the container flag:\n%s", body)
	}
}

func TestApplyToEnvWritesTheDevTokenOnlyForNoLogin(t *testing.T) {
	cfg := &Config{Identity: IdentityNone, JWTSecret: "abc", DatabaseURL: "postgres://x", DevToken: "tok-123"}
	f := &EnvFile{}
	cfg.ApplyToEnv(f)
	if got := f.Get("OCTOPUS_DEV_TOKEN"); got != "tok-123" {
		t.Fatalf("dev token = %q", got)
	}
	if got := InstallDevTokenFromEnvFile(f); got != "tok-123" {
		t.Errorf("read back = %q", got)
	}

	cfg.Identity = IdentityOIDC
	cfg.ApplyToEnv(f)
	if got := f.Get("OCTOPUS_DEV_TOKEN"); got != "" {
		t.Errorf("switching to logins kept the dev token: %q", got)
	}
}

func TestApplyToEnvClearsSSOWhenNotConfigured(t *testing.T) {
	f := &EnvFile{lines: []string{"OCTOPUS_OIDC_ISSUER=https://old.example", "OCTOPUS_OIDC_CLIENT_ID=old"}}
	cfg := &Config{Identity: IdentityNone, JWTSecret: "abc", DatabaseURL: "postgres://x"}
	cfg.ApplyToEnv(f)

	if got := f.Get("OCTOPUS_OIDC_ISSUER"); got != "" {
		t.Errorf("stale issuer survived a switch to no-login: %q", got)
	}
}

func TestHelmValuesDropTheDockerSocket(t *testing.T) {
	cfg := &Config{
		Mode: ModeKubernetes, Identity: IdentityOIDC, Release: "octopus",
		Domain: "thulu.example.com", RuntimeClass: "kata-clh", JWTSecret: "abc",
		DatabaseURL: "postgres://x", OIDC: OIDCConfig{Issuer: "https://id.example", ClientID: "c", ClientSecret: "s"},
	}
	values := cfg.HelmValues()

	socket, ok := values["dockerSocket"].(map[string]any)
	if !ok || socket["enabled"] != false {
		t.Errorf("dockerSocket = %v, want disabled", values["dockerSocket"])
	}
	runtime, _ := values["agentRuntime"].(map[string]any)
	if runtime["mode"] != "kubernetes" || runtime["runtimeClass"] != "kata-clh" {
		t.Errorf("agentRuntime = %v", runtime)
	}
	secrets, _ := values["secrets"].(map[string]any)
	if _, hasGitHub := secrets["github"]; hasGitHub {
		t.Error("GitHub OAuth secrets emitted for an OIDC install")
	}
}

func TestGenerateSecretIsLongEnoughForTheStartupCheck(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 64 {
		t.Errorf("secret length = %d hex chars, want 64", len(secret))
	}
	other, _ := GenerateSecret()
	if secret == other {
		t.Error("two generated secrets are identical")
	}
}
