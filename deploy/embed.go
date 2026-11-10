// Package deploy carries the deployment tree — compose files, the Caddyfile,
// the Helm chart — as data inside the binary.
//
// It exists so that a Thulu installed from a release has the same files a
// checkout would have given it. `thulu init` reads deploy/.env.example and
// starts a compose file or the Helm chart by path; without the tree, a
// downloaded binary can configure nothing and start nothing, and "install
// without cloning" would mean "clone anyway, quietly".
package deploy

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/deployfiles"
)

// Files is the tree, rooted at the deploy directory itself: "docker-compose.yml",
// "helm/octopus/Chart.yaml".
//
// The patterns are enumerated one by one, and none of them uses the `all:`
// prefix, because this same directory is where a running install keeps its
// secrets: deploy/.env holds the JWT secret, deploy/values-*.yaml the OIDC
// client secret, and both are gitignored precisely because they are per-machine.
// A pattern broad enough to be convenient would bake whatever a developer had
// lying around into every binary they build. embed_test.go asserts the result.
//
// A directory pattern skips names that start with "_" or ".", and Helm keeps
// its named templates in exactly such files. Without the last line below, the
// chart a downloaded binary installs has no octopus.fullname and fails to render.
//
//go:embed .env.example
//go:embed Caddyfile backup.sh Dockerfile Dockerfile.dev
//go:embed docker-compose.yml docker-compose.caddy.yml docker-compose.dev.yml docker-compose.standalone.yml docker-compose.daemon.yml
//go:embed helm k8s-data-plane
//go:embed helm/octopus/.helmignore helm/octopus/templates/_helpers.tpl helm/octopus/templates/_mcp.tpl
var Files embed.FS

// ReleasePublicKey is the public half of the release signing key, an ECDSA
// P-256 key. Release manifests are signed with it; the CLI verifies them against this copy, so verifying a
// release needs no network. SECURITY.md publishes the same key. Rotation adds
// a second key here for one overlapping release.
//
//go:embed octopus-release.pub
var ReleasePublicKey []byte

// Marker is the file whose presence means a directory already holds the
// deployment tree. Everything that looks for a root looks for this one.
const Marker = "docker-compose.yml"

// Materialize writes the embedded tree into <root>/deploy and returns the
// paths it created, relative to root.
//
// Files that already exist are left untouched rather than refreshed. The
// operator's own edits live in this directory — a hand-tuned Caddyfile, a
// compose override — and an update that silently reverted them would be a
// worse failure than a stale file, which `thulu update` reports anyway.
//
// Every file on disk that equals the embedded copy is recorded in
// deploy/.octopus-files.json with its hash. `thulu apply` replaces a
// recorded file whose hash still matches and stops at one that does not: that
// is a hand edit, which belongs in deploy/docker-compose.override.yml.
func Materialize(root string) ([]string, error) {
	written, err := materialize(root)
	if err != nil {
		return written, err
	}
	if err := recordHashes(root); err != nil {
		return written, apperr.Wrap(err, "deploy.errors.materialize", "root", root)
	}
	return written, nil
}

func recordHashes(root string) error {
	rec, err := deployfiles.LoadRecord(root)
	if err != nil {
		return err
	}
	if rec == nil {
		rec = &deployfiles.Record{Files: map[string]string{}}
	}
	tree, err := deployfiles.FromFS(Files)
	if err != nil {
		return err
	}
	dir, err := os.OpenRoot(filepath.Join(root, "deploy"))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	changed := false
	for _, name := range tree.Paths() {
		if rec.Files[name] != "" {
			continue
		}
		disk, readErr := dir.ReadFile(filepath.FromSlash(name))
		if readErr != nil {
			continue
		}
		if h := deployfiles.Hash(disk); h == deployfiles.Hash(tree[name].Body) {
			rec.Files[name] = h
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return deployfiles.SaveRecord(root, rec)
}

func materialize(root string) ([]string, error) {
	var written []string
	err := fs.WalkDir(Files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := filepath.Join("deploy", filepath.FromSlash(name))
		dst := filepath.Join(root, rel)
		if _, statErr := os.Stat(dst); statErr == nil {
			return nil
		}
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
			return apperr.Wrap(mkErr, "deploy.errors.create_dir", "path", filepath.Dir(rel))
		}
		body, readErr := Files.ReadFile(name)
		if readErr != nil {
			return apperr.Wrap(readErr, "deploy.errors.read_embedded", "name", name)
		}
		if writeErr := os.WriteFile(dst, body, modeFor(name)); writeErr != nil {
			return apperr.Wrap(writeErr, "deploy.errors.write_file", "path", rel)
		}
		written = append(written, rel)
		return nil
	})
	if err != nil {
		return written, apperr.Wrap(err, "deploy.errors.materialize", "root", root)
	}
	return written, nil
}

// embed.FS drops permission bits; without this backup.sh lands at 0644 and the updater's snapshot
// silently falls back to a bare pg_dump.
func modeFor(name string) os.FileMode { return deployfiles.ModeFor(name) }
