package project

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func diskReader(dir string) SetupFileReader {
	return func(rel string) ([]byte, bool, error) {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		info, statErr := os.Stat(filepath.Join(dir, rel))
		if statErr == nil && info.IsDir() {
			return nil, false, nil
		}
		return b, err == nil, err
	}
}

func TestSetupKeyText(t *testing.T) {
	m := InitManifest{
		Setup: "npm ci\n",
		Tools: map[string]string{"node": "22", "go": "1.25.1"},
		Cache: InitCache{Key: []string{"package-lock.json", "missing.lock"}},
	}
	files := map[string]string{"package-lock.json": "{}", "mise.toml": "[tools]\n"}
	read := func(rel string) ([]byte, bool, error) {
		c, ok := files[rel]
		return []byte(c), ok, nil
	}
	got, err := SetupKeyText(m, read)
	if err != nil {
		t.Fatal(err)
	}
	want := "octopus-setup-v1\n" +
		"setup:" + sha256Hex([]byte("npm ci\n")) + "\n" +
		"tools:go=1.25.1,node=22\n" +
		"file:package-lock.json:" + sha256Hex([]byte("{}")) + "\n" +
		"file:missing.lock:-\n" +
		"file:mise.toml:" + sha256Hex([]byte("[tools]\n")) + "\n"
	if got != want {
		t.Fatalf("canonical text:\n%s\nwant:\n%s", got, want)
	}

	base, _ := SetupKey(m, read)
	variants := []InitManifest{
		{Setup: "npm install\n", Tools: m.Tools, Cache: m.Cache},
		{Setup: m.Setup, Tools: map[string]string{"node": "20", "go": "1.25.1"}, Cache: m.Cache},
		{Setup: m.Setup, Tools: m.Tools, Cache: InitCache{Key: []string{"package-lock.json"}}},
	}
	for i, v := range variants {
		if k, _ := SetupKey(v, read); k == base {
			t.Errorf("variant %d kept the key", i)
		}
	}
	files["package-lock.json"] = `{"v":2}`
	if k, _ := SetupKey(m, read); k == base {
		t.Error("a lock file change kept the key")
	}
	m.Cache.Key = append(m.Cache.Key, "mise.toml")
	text, _ := SetupKeyText(m, read)
	if strings.Count(text, "file:mise.toml:") != 1 {
		t.Errorf("mise.toml counted twice:\n%s", text)
	}
}

func pushTree(t *testing.T, bare string, files map[string]string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "c")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run(".", "clone", bare, clone)
	run(clone, "config", "user.email", "test@example.com")
	run(clone, "config", "user.name", "test")
	for name, content := range files {
		p := filepath.Join(clone, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, "->"); ok {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(clone, "add", "-A")
	run(clone, "commit", "-m", "tree")
	run(clone, "push", "origin", "main")
	return clone
}

// TestSetupKeyGoMatchesShell (sandbox) holds the checkout key equal to the shell's; this ties the
// git-object key to it.
func TestSetupKeyAtDefaultMatchesCheckout(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "KeyAtDefault", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	checkout := pushTree(t, bare, map[string]string{
		"web/package-lock.json": `{"lockfileVersion":3}`,
		"web/.tool-versions":    "node 22.1.0\n",
		"web/go.sum":            "->../shared/go.sum",
		"shared/go.sum":         "h1:abc\n",
		"web/escape.lock":       "->../../outside",
		"web/dir.lock/x":        "not a file",
	})
	if err := store.FetchDefaultBranch(t.Context(), proj.ID, ""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	m := InitManifest{
		Workdir: "web",
		Setup:   "npm ci",
		Tools:   map[string]string{"python": "3.12"},
		Cache: InitCache{
			Key:   []string{"package-lock.json", "go.sum", "escape.lock", "dir.lock", "absent.lock"},
			Paths: []string{"node_modules"},
		},
	}
	key, commit, err := store.SetupKeyAtDefault(t.Context(), proj.ID, m)
	if err != nil {
		t.Fatalf("SetupKeyAtDefault: %v", err)
	}
	if len(commit) != 40 {
		t.Fatalf("commit = %q", commit)
	}
	want, err := SetupKey(m, diskReader(filepath.Join(checkout, "web")))
	if err != nil {
		t.Fatal(err)
	}
	if key != want {
		gitText, _ := SetupKeyText(m, func(rel string) ([]byte, bool, error) {
			return readTreeFile(proj.Path, commit, "web/"+rel)
		})
		diskText, _ := SetupKeyText(m, diskReader(filepath.Join(checkout, "web")))
		t.Fatalf("git key %s != checkout key %s\ngit:\n%s\ndisk:\n%s", key, want, gitText, diskText)
	}

	dest := filepath.Join(t.TempDir(), "src")
	if err := store.ExtractDefaultBranch(t.Context(), proj.ID, commit, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got, _ := SetupKey(m, diskReader(filepath.Join(dest, "web"))); got != want {
		t.Fatalf("extracted tree key %s != %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("extracted tree has .git: %v", err)
	}

	src, err := store.OpenDefaultBranchArchive(t.Context(), proj.ID, commit)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	raw, err := io.ReadAll(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("git archive: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "web/package-lock.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("archive lacks web/package-lock.json")
	}

	if _, err := store.OpenDefaultBranchArchive(t.Context(), proj.ID, "main; rm -rf /"); apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("a non-hex commit was accepted: %v", err)
	}
	if _, err := store.PrewarmSourceDir("../x"); err == nil {
		t.Fatal("a traversing project id got a source dir")
	}
}

func TestSetupKeySupportFiles(t *testing.T) {
	read := func(string) ([]byte, bool, error) { return nil, false, nil }
	m := InitManifest{Setup: "npm ci\n"}
	before, err := SetupKeyText(m, read)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(before, "files:") {
		t.Fatalf("a manifest without support files has a files line:\n%s", before)
	}
	m.Files = map[string]string{"b.sh": "echo b", "a/seed.mjs": "1"}
	with, _ := SetupKeyText(m, read)
	digest := sha256Hex([]byte("a/seed.mjs\n" + sha256Hex([]byte("1")) + "\nb.sh\n" + sha256Hex([]byte("echo b")) + "\n"))
	if SetupFilesDigest(m.Files) != digest || !strings.Contains(with, "\nfiles:"+digest+"\n") {
		t.Fatalf("files line: %s\nwant digest %s", with, digest)
	}
	k1, _ := SetupKey(m, read)
	m.Files = map[string]string{"b.sh": "echo B", "a/seed.mjs": "1"}
	k2, _ := SetupKey(m, read)
	m.Files = map[string]string{"b2.sh": "echo B", "a/seed.mjs": "1"}
	k3, _ := SetupKey(m, read)
	if k1 == k2 || k2 == k3 {
		t.Fatal("editing or renaming a support file must change the key")
	}
}
