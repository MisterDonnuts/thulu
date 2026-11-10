package project

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

type upFile struct {
	name, body, target string
	exec               bool
}

func writeTarGz(t *testing.T, files []upFile) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if f.exec {
			h.Mode = 0o755
		}
		if f.target != "" {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, f.target, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if f.target == "" {
			if _, err := tw.Write([]byte(f.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "upload.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeZip(t *testing.T, files []upFile) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "upload.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tarDir(t *testing.T, dir string) string {
	t.Helper()
	var files []upFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, upFile{name: filepath.ToSlash(rel), body: string(body)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeTarGz(t, files)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (err=%v), want it left out", p, err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateFromUploadWithoutHistory(t *testing.T) {
	store := newTestStore(t)
	src := writeTarGz(t, []upFile{
		{name: "README.md", body: "# hello\n"},
		{name: "bin/run.sh", body: "#!/bin/sh\necho hi\n", exec: true},
		{name: "node_modules/left/index.js", body: "module.exports = 1\n"},
		{name: "web/node_modules/x/y.js", body: "x\n"},
		{name: ".env", body: "SECRET=1\n"},
		{name: ".env.example", body: "SECRET=\n"},
		{name: "keys/deploy.pem", body: "-----BEGIN-----\n"},
		{name: "vendor/lib/.git/config", body: "[core]\n"},
		{name: ".DS_Store", body: "junk"},
		{name: "link", target: "/etc"},
		{name: "link/passwd", body: "not through the link\n"},
	})
	proj, report, err := store.CreateFromUpload(t.Context(), "Uploaded", src, CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("CreateFromUpload: %v", err)
	}
	if proj.Source != SourceUpload || !proj.Hosted() || proj.RepoURL != "" {
		t.Errorf("source=%q hosted=%v repo=%q", proj.Source, proj.Hosted(), proj.RepoURL)
	}
	if proj.DefaultBranch != "main" || report.Branch != "main" || report.Commit == "" || report.History {
		t.Errorf("project/report = %+v / %+v", proj, report)
	}
	for _, f := range []string{"README.md", "bin/run.sh", ".env.example"} {
		if _, err := os.Stat(filepath.Join(proj.Path, f)); err != nil {
			t.Errorf("%s missing from the clone: %v", f, err)
		}
	}
	for _, f := range []string{"node_modules", "web/node_modules", ".env", "keys/deploy.pem", "vendor/lib/.git", ".DS_Store"} {
		mustNotExist(t, filepath.Join(proj.Path, f))
	}
	mustNotExist(t, filepath.Join(proj.Path, "link"))
	if !slices.Contains(report.Excluded, ".env") || !slices.Contains(report.Excluded, "keys/deploy.pem") || slices.Contains(report.Excluded, ".env.example") {
		t.Errorf("Excluded = %v", report.Excluded)
	}
	for _, want := range []string{"node_modules/", "web/node_modules/", "vendor/lib/.git/", "link"} {
		if !slices.Contains(report.Skipped, want) {
			t.Errorf("Skipped = %v, want %s", report.Skipped, want)
		}
	}
	if mode := gitOut(t, proj.Path, "ls-files", "-s", "bin/run.sh"); !strings.HasPrefix(mode, "100755") {
		t.Errorf("bin/run.sh mode = %q, want executable", mode)
	}
	if author := gitOut(t, proj.Path, "log", "-1", "--format=%an <%ae>"); author != "Thulu <noreply@thulu.dev>" {
		t.Errorf("author = %q", author)
	}
	if !hostedClone(proj.Path) {
		t.Error("clone lacks the hosted marker")
	}
	if _, err := os.Stat(store.hostedRepoDir(proj.ID)); err != nil {
		t.Errorf("hosted repository missing: %v", err)
	}
	got, err := store.Get(t.Context(), proj.ID)
	if err != nil || got.Source != SourceUpload {
		t.Errorf("Get: %+v %v", got, err)
	}
}

func TestCreateFromUploadKeepsHistory(t *testing.T) {
	store := newTestStore(t)
	work := filepath.Join(t.TempDir(), "mine")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	gitOut(t, work, "init", "-b", "trunk")
	gitOut(t, work, "config", "user.email", "me@example.com")
	gitOut(t, work, "config", "user.name", "me")
	gitOut(t, work, "config", "core.sshCommand", "touch /tmp/pwned")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, work, "add", ".")
	gitOut(t, work, "commit", "-m", "first")
	first := gitOut(t, work, "rev-parse", "HEAD")
	gitOut(t, work, "update-ref", "refs/remotes/origin/trunk", first)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	proj, report, err := store.CreateFromUpload(t.Context(), "WithHistory", tarDir(t, work), CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("CreateFromUpload: %v", err)
	}
	if !report.History || proj.DefaultBranch != "trunk" {
		t.Fatalf("report=%+v branch=%q", report, proj.DefaultBranch)
	}
	bare := store.hostedRepoDir(proj.ID)
	if n := gitOut(t, bare, "rev-list", "--count", "trunk"); n != "2" {
		t.Errorf("trunk has %s commits, want the uploaded one plus the folder's", n)
	}
	if parent := gitOut(t, bare, "rev-parse", "trunk^"); parent != first {
		t.Errorf("parent = %s, want the uploaded commit %s", parent, first)
	}
	if got := readFile(t, filepath.Join(proj.Path, "a.txt")); got != "two\n" {
		t.Errorf("a.txt = %q", got)
	}
	if refs := gitOut(t, bare, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "refs/remotes/") {
		t.Errorf("remote-tracking refs survived: %s", refs)
	}
	if cfg := readFile(t, filepath.Join(bare, "config")); strings.Contains(cfg, "sshCommand") {
		t.Errorf("the uploaded config reached the repository: %s", cfg)
	}
}

func TestCreateFromUploadZipDropsTopFolder(t *testing.T) {
	store := newTestStore(t)
	src := writeZip(t, []upFile{
		{name: "proj/"},
		{name: "proj/README.md", body: "zip\n"},
		{name: "proj/src/main.go", body: "package main\n"},
		{name: "__MACOSX/proj/._README.md", body: "x"},
	})
	proj, _, err := store.CreateFromUpload(t.Context(), "Zipped", src, CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("CreateFromUpload: %v", err)
	}
	if got := readFile(t, filepath.Join(proj.Path, "README.md")); got != "zip\n" {
		t.Errorf("README.md = %q", got)
	}
	mustNotExist(t, filepath.Join(proj.Path, "proj"))
	mustNotExist(t, filepath.Join(proj.Path, "__MACOSX"))
}

func TestUploadLimitsAndRefusals(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	cases := []struct {
		name  string
		files []upFile
		lim   UploadLimits
	}{
		{"empty", []upFile{{name: ".env", body: "A=1"}}, DefaultUploadLimits()},
		{"too many", []upFile{{name: "a", body: "1"}, {name: "b", body: "2"}}, UploadLimits{MaxFiles: 1}},
		{"too big", []upFile{{name: "a", body: strings.Repeat("x", 100)}}, UploadLimits{MaxBytes: 10}},
		{"twice", []upFile{{name: "a", body: "1"}, {name: "./a", body: "2"}}, DefaultUploadLimits()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := store.CreateFromUpload(ctx, "Refused "+c.name, writeTarGz(t, c.files), CreateOpts{WorkspaceID: testWS}, c.lim); err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
	bad := filepath.Join(t.TempDir(), "x.rar")
	if err := os.WriteFile(bad, []byte("Rar!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateFromUpload(ctx, "NotArchive", bad, CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits()); err == nil {
		t.Fatal("want a refusal for a non-archive")
	}
	entries, _ := os.ReadDir(store.baseDir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left %s in the projects directory", e.Name())
		}
	}
	hosted, _ := os.ReadDir(store.hostedRoot())
	for _, e := range hosted {
		if e.Name() != incomingDirName {
			t.Errorf("left hosted repository %s", e.Name())
		}
	}
}

func TestUploadEntriesStayInside(t *testing.T) {
	store := newTestStore(t)
	src := writeTarGz(t, []upFile{
		{name: "../../escaped.txt", body: "no\n"},
		{name: "/abs.txt", body: "no\n"},
		{name: "ok.txt", body: "yes\n"},
	})
	proj, _, err := store.CreateFromUpload(t.Context(), "Inside", src, CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("CreateFromUpload: %v", err)
	}
	mustNotExist(t, filepath.Join(filepath.Dir(store.baseDir), "escaped.txt"))
	for _, f := range []string{"escaped.txt", "abs.txt", "ok.txt"} {
		if _, err := os.Stat(filepath.Join(proj.Path, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func newUploadProject(t *testing.T, store *Store, name string) *Project {
	t.Helper()
	src := writeTarGz(t, []upFile{
		{name: "README.md", body: "# base\n"},
		{name: "a.txt", body: "a\n"},
		{name: "b.txt", body: "b\n"},
	})
	proj, _, err := store.CreateFromUpload(t.Context(), name, src, CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("CreateFromUpload: %v", err)
	}
	return proj
}

func taskEdit(t *testing.T, store *Store, proj *Project, taskID string, files map[string]string) WorktreeGit {
	t.Helper()
	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, taskID)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(wtPath, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wt, err := store.HostWorktreeGit(t.Context(), proj.ID, taskID)
	if err != nil {
		t.Fatalf("HostWorktreeGit: %v", err)
	}
	if _, err := wt.Commit("edit " + taskID); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return wt
}

func TestMergeIntoFastForwardMergeAndConflict(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	proj := newUploadProject(t, store, "Merging")
	bare := store.hostedRepoDir(proj.ID)

	wt1 := taskEdit(t, store, proj, "t1", map[string]string{"a.txt": "a from t1\n"})
	wt2 := taskEdit(t, store, proj, "t2", map[string]string{"b.txt": "b from t2\n"})
	wt3 := taskEdit(t, store, proj, "t3", map[string]string{"a.txt": "a from t3\n"})
	if _, err := wt1.Push(""); err != nil {
		t.Fatalf("Push t1: %v", err)
	}
	res, err := store.MergeInto(ctx, proj.ID, wt1.Branch, "main")
	if err != nil || res.Status != MergeFastForward {
		t.Fatalf("MergeInto t1 = %+v, %v", res, err)
	}
	if got := gitOut(t, bare, "rev-parse", "main"); got != res.Commit {
		t.Errorf("main = %s, want %s", got, res.Commit)
	}
	if got := gitOut(t, proj.Path, "rev-parse", "origin/main"); got != res.Commit {
		t.Errorf("the clone's origin/main = %s, want %s", got, res.Commit)
	}
	if res, err := store.MergeInto(ctx, proj.ID, wt1.Branch, "main"); err != nil || res.Status != MergeUpToDate {
		t.Errorf("second MergeInto = %+v, %v", res, err)
	}

	if _, err := wt2.Publish(""); err != nil {
		t.Fatalf("Publish t2: %v", err)
	}
	res, err = store.MergeInto(ctx, proj.ID, wt2.Branch, "main")
	if err != nil || res.Status != MergeMerged {
		t.Fatalf("MergeInto t2 = %+v, %v", res, err)
	}
	if parents := strings.Fields(gitOut(t, bare, "rev-list", "--parents", "-n1", "main")); len(parents) != 3 {
		t.Errorf("merge commit parents = %v, want two", parents[1:])
	}
	if got := gitOut(t, bare, "show", "main:b.txt"); got != "b from t2" {
		t.Errorf("b.txt on main = %q", got)
	}

	if _, err := wt3.Publish(""); err != nil {
		t.Fatalf("Publish t3: %v", err)
	}
	before := gitOut(t, bare, "rev-parse", "main")
	res, err = store.MergeInto(ctx, proj.ID, wt3.Branch, "main")
	if err != nil || res.Status != MergeConflict || !slices.Contains(res.Conflicts, "a.txt") {
		t.Fatalf("MergeInto t3 = %+v, %v", res, err)
	}
	if after := gitOut(t, bare, "rev-parse", "main"); after != before {
		t.Errorf("main moved on a conflict: %s -> %s", before, after)
	}

	if _, err := store.MergeInto(ctx, proj.ID, "task-nope", "main"); err == nil {
		t.Error("want an error for a branch that was never pushed")
	}
	if _, err := store.MergeInto(ctx, proj.ID, wt1.Branch, "nope"); err == nil {
		t.Error("want an error for a missing target")
	}
	if _, err := store.MergeInto(ctx, proj.ID, wt1.Branch, "--upload-pack=x"); err == nil {
		t.Error("want an error for an option-shaped target")
	}
}

func TestReuploadAddsCommit(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	proj := newUploadProject(t, store, "Reupload")
	bare := store.hostedRepoDir(proj.ID)
	base := gitOut(t, bare, "rev-parse", "main")

	same := writeTarGz(t, []upFile{
		{name: "README.md", body: "# base\n"},
		{name: "a.txt", body: "a\n"},
		{name: "b.txt", body: "b\n"},
		{name: ".env", body: "LEFT=out\n"},
	})
	report, err := store.ReuploadProject(ctx, proj.ID, same, DefaultUploadLimits(), ReuploadOpts{})
	if err != nil || report.Changed || report.Commit != base {
		t.Fatalf("unchanged reupload = %+v, %v", report, err)
	}

	next := writeTarGz(t, []upFile{
		{name: "README.md", body: "# v2\n"},
		{name: "a.txt", body: "a\n"},
		{name: "c.txt", body: "c\n"},
		{name: ".git/HEAD", body: "ref: refs/heads/other\n"},
	})
	report, err = store.ReuploadProject(ctx, proj.ID, next, DefaultUploadLimits(), ReuploadOpts{})
	if err != nil || !report.Changed || !report.HistoryIgnored {
		t.Fatalf("reupload = %+v, %v", report, err)
	}
	if parent := gitOut(t, bare, "rev-parse", "main^"); parent != base {
		t.Errorf("parent = %s, want %s", parent, base)
	}
	files := gitOut(t, bare, "ls-tree", "--name-only", "main")
	if files != "README.md\na.txt\nc.txt" {
		t.Errorf("tree = %q", files)
	}
	if got := gitOut(t, proj.Path, "rev-parse", "origin/main"); got != report.Commit {
		t.Errorf("clone's origin/main = %s, want %s", got, report.Commit)
	}
	if _, err := store.ReuploadProject(ctx, proj.ID, writeTarGz(t, []upFile{{name: ".env", body: "x"}}), DefaultUploadLimits(), ReuploadOpts{}); err == nil {
		t.Error("want a refusal for a version with no files")
	}
}

func TestReuploadKeepsWhatItLeavesOut(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	work := filepath.Join(t.TempDir(), "w")
	if err := os.MkdirAll(filepath.Join(work, "node_modules", "dep"), 0o750); err != nil {
		t.Fatal(err)
	}
	gitOut(t, work, "init", "-b", "main")
	gitOut(t, work, "config", "user.email", "me@example.com")
	gitOut(t, work, "config", "user.name", "me")
	for name, body := range map[string]string{"app.js": "1\n", "config/.env.production": "tracked by mistake\n", "node_modules/dep/i.js": "dep\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(work, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitOut(t, work, "add", "-f", ".")
	gitOut(t, work, "commit", "-m", "with mistakes")
	proj, _, err := store.CreateFromUpload(ctx, "Keeps", tarDir(t, work), CreateOpts{WorkspaceID: testWS}, DefaultUploadLimits())
	if err != nil {
		t.Fatalf("CreateFromUpload: %v", err)
	}
	bare := store.hostedRepoDir(proj.ID)
	if n := gitOut(t, bare, "rev-list", "--count", "main"); n != "1" {
		t.Errorf("main has %s commits; the upload matched its history, so none should be added", n)
	}
	report, err := store.ReuploadProject(ctx, proj.ID, writeTarGz(t, []upFile{{name: "app.js", body: "2\n"}, {name: "config/.env.production", body: "changed\n"}}), DefaultUploadLimits(), ReuploadOpts{})
	if err != nil || !report.Changed {
		t.Fatalf("reupload = %+v, %v", report, err)
	}
	if got := gitOut(t, bare, "show", "main:config/.env.production"); got != "tracked by mistake" {
		t.Errorf(".env.production = %q, want the tracked copy untouched", got)
	}
	if got := gitOut(t, bare, "show", "main:node_modules/dep/i.js"); got != "dep" {
		t.Errorf("node_modules/dep/i.js = %q, want it untouched", got)
	}
}

func TestBranchesArchiveAndQuota(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	proj := newUploadProject(t, store, "Branchy")
	wt := taskEdit(t, store, proj, "bt", map[string]string{"a.txt": "edited\n"})
	if _, err := wt.Push(""); err != nil {
		t.Fatalf("Push: %v", err)
	}
	gitOut(t, store.hostedRepoDir(proj.ID), "branch", "release", "main")
	branches, err := store.Branches(ctx, proj.ID)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if !slices.Equal(branches, []string{"main", "release"}) {
		t.Errorf("Branches = %v, want main and release without the task branch", branches)
	}
	if tasks, err := store.TaskBranches(ctx, proj.ID); err != nil || !slices.Equal(tasks, []string{"bt"}) {
		t.Errorf("TaskBranches = %v, %v; want the pushed task", tasks, err)
	}

	rc, err := store.ArchiveBranch(ctx, proj.ID, "release")
	if err != nil {
		t.Fatalf("ArchiveBranch: %v", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if !slices.Contains(names, "README.md") {
		t.Errorf("zip entries = %v", names)
	}
	if _, err := store.ArchiveBranch(ctx, proj.ID, "nope"); err == nil {
		t.Error("want an error for a missing branch")
	}

	if _, err := store.Update(ctx, proj.ID, Patch{DefaultBranch: ptr("nope")}); err == nil {
		t.Error("Update to a missing default branch: want an error")
	}
	if got, err := store.Update(ctx, proj.ID, Patch{DefaultBranch: ptr("release")}); err != nil || got.DefaultBranch != "release" {
		t.Errorf("Update default branch = %+v, %v", got, err)
	}
	if out := gitOut(t, proj.Path, "rev-parse", "--verify", "origin/release"); out == "" {
		t.Error("origin/release not fetched into the clone")
	}

	n, err := store.HostedBytes(ctx, testWS)
	if err != nil || n <= 0 {
		t.Errorf("HostedBytes = %d, %v", n, err)
	}
	if n, _ := store.HostedBytes(ctx, "someone-else"); n != 0 {
		t.Errorf("another workspace's HostedBytes = %d", n)
	}
}

func TestExportRestoreAndDeleteHostedCode(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	proj := newUploadProject(t, store, "Moving")
	bundle, err := store.ExportCode(ctx, proj)
	if err != nil || len(bundle) == 0 {
		t.Fatalf("ExportCode: %d bytes, %v", len(bundle), err)
	}

	other := &Store{db: store.db, baseDir: filepath.Join(t.TempDir(), "projects")}
	moved := *proj
	moved.Path = filepath.Join(other.baseDir, filepath.Base(proj.Path))
	if err := other.RestoreCode(ctx, &moved, bundle); err != nil {
		t.Fatalf("RestoreCode: %v", err)
	}
	if got := readFile(t, filepath.Join(moved.Path, "README.md")); got != "# base\n" {
		t.Errorf("README.md = %q", got)
	}
	if !hostedClone(moved.Path) {
		t.Error("restored clone lacks the hosted marker")
	}
	if remotes := gitOut(t, other.hostedRepoDir(proj.ID), "remote"); remotes != "" {
		t.Errorf("restored repository keeps remotes %q", remotes)
	}
	if err := other.RestoreCode(ctx, &moved, bundle); err != nil {
		t.Errorf("second RestoreCode: %v", err)
	}
	outside := moved
	outside.Path = filepath.Join(t.TempDir(), "elsewhere", "x")
	if err := other.RestoreCode(ctx, &outside, bundle); err == nil {
		t.Error("want a refusal for a path outside the projects directory")
	}

	if err := store.Delete(ctx, proj.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	mustNotExist(t, store.hostedRepoDir(proj.ID))
}

func TestGitProjectIsNotHosted(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	repo := testgit.BareRepo(t)
	proj, err := store.Create(ctx, "Cloned", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if proj.Source != SourceGit || proj.Hosted() || hostedClone(proj.Path) {
		t.Errorf("source=%q hosted=%v", proj.Source, proj.Hosted())
	}
	if b, err := store.ExportCode(ctx, proj); err != nil || b != nil {
		t.Errorf("ExportCode = %d bytes, %v", len(b), err)
	}
	if _, err := store.MergeInto(ctx, proj.ID, "task-x", "main"); err == nil {
		t.Error("MergeInto on a cloned project: want an error")
	}
	if _, err := store.ReuploadProject(ctx, proj.ID, writeTarGz(t, []upFile{{name: "a", body: "1"}}), DefaultUploadLimits(), ReuploadOpts{}); err == nil {
		t.Error("ReuploadProject on a cloned project: want an error")
	}
	branches, err := store.Branches(ctx, proj.ID)
	if err != nil || !slices.Equal(branches, []string{"main"}) {
		t.Errorf("Branches = %v, %v", branches, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestHostedRepoDirNeverLeavesTheRoot(t *testing.T) {
	s := &Store{baseDir: "/data/projects"}
	for _, id := range []string{"../../etc", "a/b", "", "UPPER", strings.Repeat("a", 65)} {
		if got := s.hostedRepoDir(id); filepath.Dir(got) != s.hostedRoot() || filepath.Base(got) != "invalid.git" {
			t.Errorf("hostedRepoDir(%q) = %s", id, got)
		}
	}
	if got := s.hostedRepoDir("abc123"); got != "/data/projects/.hosted/abc123.git" {
		t.Errorf("hostedRepoDir = %s", got)
	}
}

func TestIsSecretPath(t *testing.T) {
	for p, want := range map[string]bool{
		".env":                  true,
		"app/.env.local":        true,
		".env.production":       true,
		".env.example":          false,
		"config/.env.sample":    false,
		"certs/server.key":      true,
		"id_rsa":                true,
		"store.p12":             true,
		"home/.aws/credentials": true,
		"docs/credentials":      false,
		"src/env.ts":            false,
		"README.md":             false,
	} {
		if got := IsSecretPath(p); got != want {
			t.Errorf("IsSecretPath(%q) = %v, want %v", p, got, want)
		}
	}
}
