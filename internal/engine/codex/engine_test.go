package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestNameAndRegistration(t *testing.T) {
	got, err := engine.Get(Name)
	if err != nil {
		t.Fatalf("engine.Get(%q): %v", Name, err)
	}
	if got.Name() != Name {
		t.Fatalf("Name() = %q, want %q", got.Name(), Name)
	}
}

func TestCapabilities(t *testing.T) {
	caps := New(Config{}).Capabilities()
	if caps.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", caps.Provider)
	}
	if !caps.StreamJSON {
		t.Error("StreamJSON should be true")
	}
	if !caps.MCP {
		t.Error("MCP expected true — codex.sh renders mcp_servers via mcp-remote")
	}
	if caps.EffortFlag {
		t.Error("EffortFlag expected false until the adapter renders the effort knob")
	}
	if len(caps.KeyKinds) != 1 || caps.KeyKinds[0].Prefix != "sk-" {
		t.Fatalf("want a single sk- KeyKind, got %+v", caps.KeyKinds)
	}
	if got := caps.KeyKinds[0].EnvVars; len(got) != 1 || got[0] != "OPENAI_API_KEY" {
		t.Errorf("KeyKind EnvVars = %v, want [OPENAI_API_KEY]", got)
	}
}

func TestAuthEnv(t *testing.T) {
	if got := New(Config{}).AuthEnv(context.Background()); got != nil {
		t.Errorf("AuthEnv with no env file = %v, want nil", got)
	}

	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	const body = "# comment\n" +
		"OPENAI_API_KEY='sk-proj-abc'\n" +
		"UNRELATED=nope\n"
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := New(Config{EnvFile: envFile}).AuthEnv(context.Background())
	if len(got) != 1 || got[0] != "OPENAI_API_KEY=sk-proj-abc" {
		t.Fatalf("AuthEnv = %v, want [OPENAI_API_KEY=sk-proj-abc]", got)
	}
}

func TestAuthEnvCodexAlias(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("CODEX_API_KEY=sk-xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := New(Config{EnvFile: envFile}).AuthEnv(context.Background())
	if len(got) != 1 || got[0] != "OPENAI_API_KEY=sk-xyz" {
		t.Fatalf("AuthEnv = %v, want [OPENAI_API_KEY=sk-xyz]", got)
	}
}
