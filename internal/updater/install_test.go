package updater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

func TestHostDSNRewritesTheComposeServiceName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	env := "OCTOPUS_DATABASE_URL=postgres://octopus:octopus@pg:5432/octopus?sslmode=disable\n"
	if err := os.WriteFile(filepath.Join(root, "deploy", ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, file := range []string{"deploy/docker-compose.dev.yml", "deploy/docker-compose.yml"} {
		shipped, err := os.ReadFile(filepath.Join("..", "..", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, file), shipped, 0o600); err != nil {
			t.Fatal(err)
		}
		in := &Install{Root: root, ComposeFile: file}
		if got := hostDSN(in); got != "postgres://octopus:octopus@localhost:5433/octopus?sslmode=disable" {
			t.Errorf("%s: DSN = %q", file, got)
		}
	}

	private := "services:\n  pg:\n    image: postgres:16-alpine\n"
	if err := os.WriteFile(filepath.Join(root, "deploy", "private.yml"), []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := hostDSN(&Install{Root: root, ComposeFile: "deploy/private.yml"}); got != "" {
		t.Errorf("private stack reported a host DSN it cannot reach: %q", got)
	}
}

func TestHostDSNFollowsTheChosenPort(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	env := "OCTOPUS_DATABASE_URL=postgres://octopus:pw@pg:5432/octopus?sslmode=disable\nOCTOPUS_PG_HOST_PORT=5440\n"
	if err := os.WriteFile(filepath.Join(root, "deploy", ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	const file = "deploy/docker-compose.standalone.yml"
	shipped, err := os.ReadFile(filepath.Join("..", "..", file))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, file), shipped, 0o600); err != nil {
		t.Fatal(err)
	}
	got := hostDSN(&Install{Root: root, ComposeFile: file})
	if want := "postgres://octopus:pw@localhost:5440/octopus?sslmode=disable"; got != want {
		t.Errorf("DSN = %q, want %q", got, want)
	}
}

func TestHostDSNKeepsAnExternalDatabaseAsIs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	dsn := "postgres://u:p@db.example.com:5432/octopus?sslmode=require"
	if err := os.WriteFile(filepath.Join(root, "deploy", ".env"),
		[]byte("OCTOPUS_DATABASE_URL="+dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := &Install{Root: root, ComposeFile: "deploy/docker-compose.yml"}
	if got := hostDSN(in); got != dsn {
		t.Errorf("external DSN was rewritten: %q", got)
	}
}

func TestDevStackDetection(t *testing.T) {
	dev := &Install{ComposeFile: "deploy/docker-compose.dev.yml"}
	if !dev.DevStack() {
		t.Error("dev stack not recognised")
	}
	if (&Install{ComposeFile: "deploy/docker-compose.yml"}).DevStack() {
		t.Error("self-hosted stack reported as the dev stack")
	}
	if dev.String() == "" {
		t.Error("empty description")
	}
}

func TestStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(root)
	if err != nil || got != nil {
		t.Fatalf("LoadState on a fresh install = (%v, %v)", got, err)
	}

	want := &State{
		From:         Ref{Version: "1.3.0", Image: "ghcr.io/o/n:1.3.0"},
		To:           Ref{Version: "1.4.0", Image: "ghcr.io/o/n:1.4.0"},
		BackupPath:   filepath.Join(root, "backups", "octopus_x.sql"),
		BackupMethod: BackupContainerDump,
		ComposeFile:  "deploy/docker-compose.yml",
	}
	if err := SaveState(root, want); err != nil {
		t.Fatal(err)
	}
	got, err = LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.From.Version != "1.3.0" || got.BackupPath != want.BackupPath || got.BackupMethod != BackupContainerDump {
		t.Errorf("round trip lost data: %+v", got)
	}

	info, err := os.Stat(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestDetectFallsBackToTheSelfHostedStack(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	in, err := Detect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if in.Mode != initwizard.ModeDocker || in.ComposeFile != "deploy/docker-compose.yml" {
		t.Errorf("fallback = %s / %s", in.Mode, in.ComposeFile)
	}
	if in.Release != "octopus" || in.Namespace != "octopus" {
		t.Errorf("helm defaults missing: %+v", in)
	}
}
