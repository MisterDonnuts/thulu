package initwizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAnswersKeepsEverySecretOut(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Mode:         ModeDocker,
		Identity:     IdentityOIDC,
		OIDC:         OIDCConfig{Issuer: "https://id.invalid", ClientID: "cid", ClientSecret: "secret-oidc"},
		GitHub:       GitHubOAuth{ClientID: "gid", ClientSecret: "secret-github"},
		JWTSecret:    "secret-jwt",
		SecretboxKey: "secret-box",
		DevToken:     "secret-dev",
		PGPassword:   "secret-pg",
		DatabaseURL:  "postgres://octopus:secret-dsn@pg:5432/octopus",
		BootstrapDSN: "postgres://octopus:secret-dsn@localhost:5433/octopus",
		LicenseKey:   "secret-license",
		GitToken:     "secret-git",
		Credentials:  []EngineCredential{{Engine: engineClaude, Key: "secret-llm"}},
		MintedKey:    "secret-key",
	}
	if err := SaveAnswers(root, cfg); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, AnswersFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-") {
		t.Errorf("the answers file holds a secret:\n%s", body)
	}
	back, err := LoadAnswers(root)
	if err != nil {
		t.Fatal(err)
	}
	if back.OIDC.Issuer != "https://id.invalid" || back.GitHub.ClientID != "gid" {
		t.Errorf("non-secret answers did not round-trip: %+v %+v", back.OIDC, back.GitHub)
	}
}
