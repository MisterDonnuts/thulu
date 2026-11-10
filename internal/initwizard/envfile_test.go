package initwizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvFileRevivesCommentedKeyInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# Optional — SSO (OIDC)\n# OCTOPUS_OIDC_ISSUER=https://accounts.google.com\nOCTOPUS_DOMAIN=old.example\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("OCTOPUS_OIDC_ISSUER", "https://id.example.com")
	f.Set("OCTOPUS_DOMAIN", "new.example")

	got := f.Render()
	want := "# Optional — SSO (OIDC)\nOCTOPUS_OIDC_ISSUER=https://id.example.com\nOCTOPUS_DOMAIN=new.example\n"
	if got != want {
		t.Errorf("render:\n%q\nwant:\n%q", got, want)
	}
	if strings.Count(got, "OCTOPUS_OIDC_ISSUER") != 1 {
		t.Errorf("issuer written twice:\n%s", got)
	}
}

func TestEnvFileSavesPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	f := &EnvFile{}
	f.Set("OCTOPUS_JWT_SECRET", "deadbeef")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600: the file holds the JWT secret", perm)
	}
}

func TestEnvFileSaveTightensAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("OCTOPUS_DOMAIN=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	f := &EnvFile{}
	f.Set("OCTOPUS_JWT_SECRET", "deadbeef")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600: an existing 0644 file kept its mode", perm)
	}
	if body, _ := os.ReadFile(path); string(body) != "OCTOPUS_JWT_SECRET=deadbeef\n" {
		t.Errorf("body = %q", body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left %d files in the directory, want only .env", len(entries))
	}
}

func TestEnvFileUnsetComments(t *testing.T) {
	f := &EnvFile{lines: []string{"OCTOPUS_DEV_MODE=1"}}
	f.Unset("OCTOPUS_DEV_MODE")
	if got := f.Render(); got != "# OCTOPUS_DEV_MODE=1\n" {
		t.Errorf("render = %q", got)
	}
	if v := f.Get("OCTOPUS_DEV_MODE"); v != "" {
		t.Errorf("Get after Unset = %q, want empty", v)
	}
}

func TestQuoteEnvOnlyWhenNeeded(t *testing.T) {
	cases := map[string]string{
		"plain":            "plain",
		"with space":       `"with space"`,
		"has#hash":         `"has#hash"`,
		"postgres://a:b@c": "postgres://a:b@c",
	}
	for in, want := range cases {
		if got := quoteEnv(in); got != want {
			t.Errorf("quoteEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiffReportsChangedSecretWithoutRevealingIt(t *testing.T) {
	before := "OCTOPUS_JWT_SECRET=\nOCTOPUS_DOMAIN=old.example\n"
	after := "OCTOPUS_JWT_SECRET=abcdef0123456789\nOCTOPUS_DOMAIN=new.example\nOCTOPUS_DEV_MODE=1\n"

	got := Diff(before, after)
	if !strings.Contains(got, "OCTOPUS_JWT_SECRET") {
		t.Errorf("diff omits the secret's line entirely:\n%s", got)
	}
	if strings.Contains(got, "abcdef0123456789") {
		t.Errorf("diff printed the secret in clear:\n%s", got)
	}
	if !strings.Contains(got, "+ OCTOPUS_DEV_MODE=1") {
		t.Errorf("diff omits the added key:\n%s", got)
	}
	if !strings.Contains(got, "old.example → new.example") {
		t.Errorf("diff omits the changed domain:\n%s", got)
	}
}

func TestParseAssignmentDropsInlineComment(t *testing.T) {
	key, value, ok := parseAssignment("OCTOPUS_JWT_SECRET=   # generate with: openssl rand -hex 32")
	if !ok || key != "OCTOPUS_JWT_SECRET" {
		t.Fatalf("parse = (%q, %q, %v)", key, value, ok)
	}
	if value != "" {
		t.Errorf("value = %q, want empty", value)
	}

	_, dsn, _ := parseAssignment("OCTOPUS_DATABASE_URL=postgres://u:pa#ss@host:5432/db")
	if dsn != "postgres://u:pa#ss@host:5432/db" {
		t.Errorf("dsn = %q — a hash inside the value was treated as a comment", dsn)
	}
}

func TestHostDatabaseURL(t *testing.T) {
	cases := map[string]string{
		"OCTOPUS_DATABASE_URL=postgres://octopus:pw@pg:5432/octopus?sslmode=disable\n": "postgres://octopus:pw@localhost:5433/octopus?sslmode=disable",
		"OCTOPUS_DATABASE_URL=postgres://u:p@db.acme.internal:5432/octopus\n":          "postgres://u:p@db.acme.internal:5432/octopus",
		"OCTOPUS_PG_PASSWORD=pw\n":                            "postgres://octopus:pw@localhost:5433/octopus?sslmode=disable",
		"OCTOPUS_PG_PASSWORD=pw\nOCTOPUS_PG_HOST_PORT=5440\n": "postgres://octopus:pw@localhost:5440/octopus?sslmode=disable",
		"OCTOPUS_DATABASE_URL=postgres://octopus:pw@pg:5432/octopus?sslmode=disable\nOCTOPUS_PG_HOST_PORT=5440\n": "postgres://octopus:pw@localhost:5440/octopus?sslmode=disable",
		"OCTOPUS_DOMAIN=localhost\n": "",
	}
	for body, want := range cases {
		f := &EnvFile{lines: strings.Split(strings.TrimSuffix(body, "\n"), "\n")}
		if got := HostDatabaseURL(f); got != want {
			t.Errorf("%q → %q, want %q", body, got, want)
		}
	}
	if HostDatabaseURL(nil) != "" {
		t.Error("nil file named a database")
	}
}

func TestAdoptEnvKeepsWhatTheShellSets(t *testing.T) {
	f := &EnvFile{lines: []string{"OCTOPUS_TEST_ADOPT_A=from-file", "OCTOPUS_TEST_ADOPT_B=from-file"}}
	t.Setenv("OCTOPUS_TEST_ADOPT_A", "from-shell")
	t.Setenv("OCTOPUS_TEST_ADOPT_B", "")
	_ = os.Unsetenv("OCTOPUS_TEST_ADOPT_B")

	if err := AdoptEnv(f, "OCTOPUS_TEST_ADOPT_A", "OCTOPUS_TEST_ADOPT_B"); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("OCTOPUS_TEST_ADOPT_A"); got != "from-shell" {
		t.Errorf("A = %q, the shell's value was replaced", got)
	}
	if got := os.Getenv("OCTOPUS_TEST_ADOPT_B"); got != "from-file" {
		t.Errorf("B = %q, want the install's value", got)
	}
}
