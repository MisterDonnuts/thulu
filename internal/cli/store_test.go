package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/stage"
)

func installAt(t *testing.T, env string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "deploy", "docker-compose.yml"), "services: {}\n")
	writeFile(t, filepath.Join(dir, "deploy", ".env"), env)
	t.Setenv("OCTOPUS_HOME", t.TempDir())
	t.Chdir(dir)
	return dir
}

func TestRequireDSNReadsTheInstallEnvFile(t *testing.T) {
	t.Setenv("OCTOPUS_DATABASE_URL", "")
	installAt(t, "OCTOPUS_DATABASE_URL=postgres://octopus:s3cret@pg:5432/octopus?sslmode=disable\n")

	dsn, err := requireDSN()
	if err != nil {
		t.Fatalf("requireDSN: %v", err)
	}
	if want := "postgres://octopus:s3cret@localhost:5433/octopus?sslmode=disable"; dsn != want {
		t.Errorf("dsn = %q, want %q", dsn, want)
	}
}

func TestRequireDSNPrefersTheEnvironment(t *testing.T) {
	installAt(t, "OCTOPUS_DATABASE_URL=postgres://octopus:x@pg:5432/octopus\n")
	t.Setenv("OCTOPUS_DATABASE_URL", "postgres://u:p@db.internal:5432/octopus")

	dsn, err := requireDSN()
	if err != nil || dsn != "postgres://u:p@db.internal:5432/octopus" {
		t.Errorf("dsn = %q, %v; the environment has to win over deploy/.env", dsn, err)
	}
}

func TestRequireDSNWithNoInstallSaysWhatToDo(t *testing.T) {
	t.Setenv("OCTOPUS_DATABASE_URL", "")
	t.Setenv("OCTOPUS_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	_, err := requireDSN()
	if err == nil || !strings.Contains(err.Error(), "deploy/.env") {
		t.Errorf("err = %v, want one that names deploy/.env", err)
	}
}

func TestMintAdoptsTheInstallKeyCap(t *testing.T) {
	installAt(t, "OCTOPUS_API_KEY_MAX_TTL=0\n")
	t.Setenv(auth.MaxKeyTTLEnv, "")
	_ = os.Unsetenv(auth.MaxKeyTTLEnv)

	if err := adoptKeyCap(); err != nil {
		t.Fatal(err)
	}
	if got := auth.APIKeyMaxTTL(); got != 0 {
		t.Errorf("cap = %v, want 0 (no cap) from deploy/.env", got)
	}

	t.Setenv(auth.MaxKeyTTLEnv, "30d")
	if err := adoptKeyCap(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(auth.MaxKeyTTLEnv); got != "30d" {
		t.Errorf("cap env = %q, the exported value was overwritten", got)
	}
}

func TestPickWorkspace(t *testing.T) {
	all := []workspaceRow{{id: "bbbb000000000006", slug: "acme"}, {id: "bbbb000000000007", slug: "beta"}}
	one := all[:1]

	cases := []struct {
		name     string
		rows     []workspaceRow
		named    string
		fallback bool
		want     string
		kind     apperr.Kind
	}{
		{"by id", all, "bbbb000000000007", false, "bbbb000000000007", apperr.KindUnspecified},
		{"by slug", all, "acme", false, "bbbb000000000006", apperr.KindUnspecified},
		{"the only one", one, "", false, "bbbb000000000006", apperr.KindUnspecified},
		{"several, none named", all, "", false, "", apperr.KindInvalid},
		{"unknown", all, "gamma", false, "", apperr.KindNotFound},
		{"none, keys", nil, "", false, "", apperr.KindInvalid},
		{"none, data", nil, "", true, stage.FallbackWorkspaceID, apperr.KindUnspecified},
		{"none, fallback named", nil, stage.FallbackWorkspaceID, true, stage.FallbackWorkspaceID, apperr.KindUnspecified},
		{"fallback named on a real install", all, stage.FallbackWorkspaceID, true, stage.FallbackWorkspaceID, apperr.KindUnspecified},
		{"fallback named for a key", one, stage.FallbackWorkspaceID, false, "", apperr.KindNotFound},
	}
	for _, c := range cases {
		got, err := pickWorkspace(c.rows, c.named, c.fallback)
		if c.kind != apperr.KindUnspecified {
			if err == nil || apperr.KindOf(err) != c.kind {
				t.Errorf("%s: err = %v, want kind %d", c.name, err, c.kind)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestDaemonErrorShowsTheMessageOnce(t *testing.T) {
	err := daemonErrorFrom(404, []byte(`{"error":"agent not found: nada","code":"not_found"}`))
	if err == nil {
		t.Fatal("no error")
	}
	text := err.Error()
	if strings.Contains(text, "{") || strings.Contains(text, `"code"`) {
		t.Errorf("the raw JSON leaked into %q", text)
	}
	if !strings.Contains(text, "agent not found: nada") {
		t.Errorf("the daemon's message is missing from %q", text)
	}
	if strings.Count(strings.ToLower(text), "error") > 0 {
		t.Errorf("%q repeats the prefix main already prints", text)
	}

	plain := daemonErrorFrom(502, []byte("<html>bad gateway</html>"))
	if !strings.Contains(plain.Error(), "502") || strings.Contains(plain.Error(), "html") {
		t.Errorf("a proxy page should report the status only: %q", plain.Error())
	}
}
