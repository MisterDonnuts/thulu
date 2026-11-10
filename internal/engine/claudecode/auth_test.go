package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeEnvFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestReadAuthFromEnvFile_Both(t *testing.T) {
	path := writeEnvFile(t, `
# leading comment
CLAUDE_CODE_OAUTH_TOKEN=sk-oauth-token

ANTHROPIC_API_KEY="sk-ant-key"
UNRELATED=ignored
`)
	got := readAuthFromEnvFile(path)
	sort.Strings(got)
	want := []string{"ANTHROPIC_API_KEY=sk-ant-key", "CLAUDE_CODE_OAUTH_TOKEN=sk-oauth-token"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadAuthFromEnvFile_StripsQuotes(t *testing.T) {
	path := writeEnvFile(t, `CLAUDE_CODE_OAUTH_TOKEN="with-double"
ANTHROPIC_API_KEY='with-single'
`)
	got := readAuthFromEnvFile(path)
	sort.Strings(got)
	want := []string{"ANTHROPIC_API_KEY=with-single", "CLAUDE_CODE_OAUTH_TOKEN=with-double"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReadAuthFromEnvFile_Missing_ReturnsNil(t *testing.T) {
	if got := readAuthFromEnvFile("/nonexistent/.env"); got != nil {
		t.Errorf("missing file: got %v, want nil", got)
	}
}

func TestReadAuthFromEnvFile_NoMatchingKeys_ReturnsNil(t *testing.T) {
	path := writeEnvFile(t, "FOO=bar\nBAZ=qux\n")
	if got := readAuthFromEnvFile(path); got != nil {
		t.Errorf("no matching keys: got %v, want nil", got)
	}
}

func TestAuthEnv_RespectsConfig(t *testing.T) {
	if got := New(Config{}).AuthEnv(context.Background()); got != nil {
		t.Errorf("zero-config AuthEnv = %v, want nil", got)
	}
	path := writeEnvFile(t, "CLAUDE_CODE_OAUTH_TOKEN=v1\n")
	eng := New(Config{EnvFile: path})
	if got := eng.AuthEnv(context.Background()); len(got) != 1 || got[0] != "CLAUDE_CODE_OAUTH_TOKEN=v1" {
		t.Errorf("first call: got %v, want [CLAUDE_CODE_OAUTH_TOKEN=v1]", got)
	}
	if err := os.WriteFile(path, []byte("CLAUDE_CODE_OAUTH_TOKEN=v2\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got := eng.AuthEnv(context.Background()); len(got) != 1 || got[0] != "CLAUDE_CODE_OAUTH_TOKEN=v2" {
		t.Errorf("after refresh: got %v, want [CLAUDE_CODE_OAUTH_TOKEN=v2]", got)
	}
}
