package deploy

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/deployfiles"
)

func TestNoSecretsEmbedded(t *testing.T) {
	banned := []string{".env", "values-", ".octopus-init.json", ".octopus-update.json", "docker-compose.nologin.yml"}
	err := fs.WalkDir(Files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := filepath.Base(name)
		if strings.Contains(base, ".example") {
			return nil
		}
		for _, bad := range banned {
			if strings.HasPrefix(base, bad) {
				t.Errorf("embedded %s — this file belongs to one machine, not to the binary", name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestFirstRunFilesPresent(t *testing.T) {
	for _, name := range []string{
		".env.example",
		"docker-compose.yml",
		"docker-compose.standalone.yml",
		"docker-compose.caddy.yml",
		"docker-compose.dev.yml",
		"Caddyfile",
		"backup.sh",
		"helm/octopus/Chart.yaml",
		"helm/octopus/values.yaml",
		"helm/octopus/templates/mcp.yaml",
		"helm/octopus/templates/_mcp.tpl",
		"helm/octopus/templates/_helpers.tpl",
		"helm/octopus/.helmignore",
	} {
		if _, err := Files.ReadFile(name); err != nil {
			t.Errorf("%s is not embedded: %v", name, err)
		}
	}
}

func TestMaterializeWritesTheTree(t *testing.T) {
	root := t.TempDir()
	written, err := Materialize(root)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("materialize reported nothing written")
	}
	compose := filepath.Join(root, "deploy", "docker-compose.standalone.yml")
	if _, err := os.Stat(compose); err != nil {
		t.Fatalf("standalone stack missing: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "deploy", "backup.sh"))
	if err != nil {
		t.Fatalf("backup.sh missing: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("backup.sh is not executable (%v) — the updater would silently skip the archive backup", info.Mode())
	}
}

func TestEveryChartFileEmbedded(t *testing.T) {
	err := filepath.WalkDir("helm", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, rerr := Files.ReadFile(filepath.ToSlash(name)); rerr != nil {
			t.Errorf("%s is on disk but not embedded", name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestNoAgentPresetsShipped(t *testing.T) {
	err := fs.WalkDir(Files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(name, "agents") {
			t.Errorf("embedded %s — agent definitions are not part of the deployment tree", name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestMaterializeKeepsLocalEdits(t *testing.T) {
	root := t.TempDir()
	if _, err := Materialize(root); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	caddy := filepath.Join(root, "deploy", "Caddyfile")
	if err := os.WriteFile(caddy, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := Materialize(root)
	if err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("second run rewrote %v; every file was already there", written)
	}
	body, err := os.ReadFile(caddy)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "# mine\n" {
		t.Errorf("Caddyfile was overwritten: %q", body)
	}
}

func TestMaterializeRecordsWhatItWrote(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "Caddyfile"), []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(root); err != nil {
		t.Fatal(err)
	}
	rec, err := deployfiles.LoadRecord(root)
	if err != nil || rec == nil {
		t.Fatalf("record = %v, %v", rec, err)
	}
	compose, _ := os.ReadFile(filepath.Join(root, "deploy", "docker-compose.yml"))
	if rec.Files["docker-compose.yml"] != deployfiles.Hash(compose) {
		t.Error("docker-compose.yml not recorded")
	}
	if _, ok := rec.Files["Caddyfile"]; ok {
		t.Error("an edited Caddyfile was recorded as untouched")
	}
	if _, ok := rec.Files["helm/octopus/templates/_helpers.tpl"]; !ok {
		t.Error("chart helpers not recorded")
	}
}
