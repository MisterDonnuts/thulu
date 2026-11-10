package project

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func zipEntries(t *testing.T, rc io.ReadCloser) map[string]string {
	t.Helper()
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		_ = r.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestArchiveBranchNamesItsCommit(t *testing.T) {
	store := newTestStore(t)
	proj := newUploadProject(t, store, "Marked")
	head := gitOut(t, store.hostedRepoDir(proj.ID), "rev-parse", "main")

	rc, err := store.ArchiveBranch(t.Context(), proj.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	files := zipEntries(t, rc)
	if got := strings.TrimSpace(files[baseMarker]); got != head {
		t.Errorf("%s = %q, want %s", baseMarker, got, head)
	}
}

func moveMain(t *testing.T, store *Store, proj *Project) {
	t.Helper()
	_, err := store.ReuploadProject(t.Context(), proj.ID, writeTarGz(t, []upFile{
		{name: "README.md", body: "# base\n"},
		{name: "a.txt", body: "a\n"},
		{name: "b.txt", body: "b\n"},
		{name: "landed.txt", body: "from someone else\n"},
	}), DefaultUploadLimits(), ReuploadOpts{})
	if err != nil {
		t.Fatalf("move main: %v", err)
	}
}

func TestReuploadRefusesWhenMainMoved(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	proj := newUploadProject(t, store, "Moved")
	bare := store.hostedRepoDir(proj.ID)
	base := gitOut(t, bare, "rev-parse", "main")
	moveMain(t, store, proj)
	moved := gitOut(t, bare, "rev-parse", "main")

	folder := []upFile{
		{name: baseMarker, body: base + "\n"},
		{name: "README.md", body: "# base\n"},
		{name: "a.txt", body: "a, edited\n"},
		{name: "b.txt", body: "b\n"},
	}
	_, err := store.ReuploadProject(ctx, proj.ID, writeTarGz(t, folder), DefaultUploadLimits(), ReuploadOpts{})
	var mm *MainMovedError
	if !errors.As(err, &mm) || mm.Ahead != 1 || mm.Branch != "main" {
		t.Fatalf("reupload over a moved main = %v, want MainMovedError ahead 1", err)
	}
	if got := gitOut(t, bare, "rev-parse", "main"); got != moved {
		t.Errorf("a refused reupload moved main: %s -> %s", moved, got)
	}

	report, err := store.ReuploadProject(ctx, proj.ID, writeTarGz(t, folder), DefaultUploadLimits(), ReuploadOpts{Mode: ReuploadMerge})
	if err != nil || !report.Changed {
		t.Fatalf("merge reupload = %+v, %v", report, err)
	}
	files := strings.Split(gitOut(t, bare, "ls-tree", "--name-only", "main"), "\n")
	if !slices.Contains(files, "landed.txt") || slices.Contains(files, baseMarker) {
		t.Errorf("merged tree = %v, want landed.txt kept and no marker", files)
	}
	if got := gitOut(t, bare, "show", "main:a.txt"); got != "a, edited" {
		t.Errorf("a.txt = %q, want the folder's edit", got)
	}
	parents := strings.Fields(gitOut(t, bare, "rev-list", "--parents", "-n1", "main"))
	if len(parents) != 3 {
		t.Fatalf("merge commit parents = %q, want two", parents)
	}
	if report.Commit != parents[0] || report.Version != parents[2] {
		t.Errorf("report commit %q version %q, want merge %q and folder %q", report.Commit, report.Version, parents[0], parents[2])
	}
}

func TestReuploadReplaceDropsWhatLanded(t *testing.T) {
	store := newTestStore(t)
	proj := newUploadProject(t, store, "Replaced")
	bare := store.hostedRepoDir(proj.ID)
	base := gitOut(t, bare, "rev-parse", "main")
	moveMain(t, store, proj)

	report, err := store.ReuploadProject(t.Context(), proj.ID, writeTarGz(t, []upFile{
		{name: baseMarker, body: base + "\n"},
		{name: "README.md", body: "# mine\n"},
	}), DefaultUploadLimits(), ReuploadOpts{Mode: ReuploadReplace})
	if err != nil || !report.Changed || report.Files != 1 || report.Version != "" {
		t.Fatalf("replace reupload = %+v, %v", report, err)
	}
	if files := gitOut(t, bare, "ls-tree", "--name-only", "main"); files != "README.md" {
		t.Errorf("tree = %q, want only README.md", files)
	}
}

func TestReuploadMergeConflictWritesNothing(t *testing.T) {
	store := newTestStore(t)
	proj := newUploadProject(t, store, "Clash")
	bare := store.hostedRepoDir(proj.ID)
	base := gitOut(t, bare, "rev-parse", "main")
	if _, err := store.ReuploadProject(t.Context(), proj.ID, writeTarGz(t, []upFile{
		{name: "README.md", body: "# theirs\n"}, {name: "a.txt", body: "a\n"}, {name: "b.txt", body: "b\n"},
	}), DefaultUploadLimits(), ReuploadOpts{}); err != nil {
		t.Fatal(err)
	}
	moved := gitOut(t, bare, "rev-parse", "main")

	_, err := store.ReuploadProject(t.Context(), proj.ID, writeTarGz(t, []upFile{
		{name: baseMarker, body: base + "\n"},
		{name: "README.md", body: "# mine\n"}, {name: "a.txt", body: "a\n"}, {name: "b.txt", body: "b\n"},
	}), DefaultUploadLimits(), ReuploadOpts{Mode: ReuploadMerge})
	var clash *ReuploadConflictError
	if !errors.As(err, &clash) || !slices.Equal(clash.Files, []string{"README.md"}) {
		t.Fatalf("clashing merge = %v, want README.md in conflict", err)
	}
	if got := gitOut(t, bare, "rev-parse", "main"); got != moved {
		t.Errorf("a clashing merge moved main: %s -> %s", moved, got)
	}
}

func TestReuploadWithCurrentBaseOrNone(t *testing.T) {
	store := newTestStore(t)
	proj := newUploadProject(t, store, "Current")
	bare := store.hostedRepoDir(proj.ID)
	head := gitOut(t, bare, "rev-parse", "main")

	for _, marker := range []string{head, strings.Repeat("0", 40), "not a sha"} {
		report, err := store.ReuploadProject(t.Context(), proj.ID, writeTarGz(t, []upFile{
			{name: baseMarker, body: marker + "\n"},
			{name: "README.md", body: "# base\n"}, {name: "a.txt", body: "a\n"}, {name: "b.txt", body: "b\n"},
		}), DefaultUploadLimits(), ReuploadOpts{})
		if err != nil || report.Changed || report.Files != 3 {
			t.Errorf("marker %q: report = %+v, %v; want unchanged and the marker uncounted", marker, report, err)
		}
	}
}

func TestReuploadFromSideBranchRefusesWhenMainMoved(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	proj := newUploadProject(t, store, "Side")
	bare := store.hostedRepoDir(proj.ID)
	wt := taskEdit(t, store, proj, "side", map[string]string{"a.txt": "a from the task\n"})
	if _, err := wt.Publish(""); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rc, err := store.ArchiveBranch(ctx, proj.ID, wt.Branch)
	if err != nil {
		t.Fatal(err)
	}
	zipped := zipEntries(t, rc)
	moveMain(t, store, proj)
	moved := gitOut(t, bare, "rev-parse", "main")

	var folder []upFile
	for name, body := range zipped {
		folder = append(folder, upFile{name: name, body: body})
	}
	_, err = store.ReuploadProject(ctx, proj.ID, writeTarGz(t, folder), DefaultUploadLimits(), ReuploadOpts{})
	var mm *MainMovedError
	if !errors.As(err, &mm) || mm.Ahead != 1 {
		t.Fatalf("side-branch folder over a moved main = %v, want MainMovedError ahead 1", err)
	}
	if got := gitOut(t, bare, "rev-parse", "main"); got != moved {
		t.Errorf("a refused reupload moved main: %s -> %s", moved, got)
	}

	if _, err := store.ReuploadProject(ctx, proj.ID, writeTarGz(t, folder), DefaultUploadLimits(), ReuploadOpts{Mode: ReuploadMerge}); err != nil {
		t.Fatalf("merge reupload: %v", err)
	}
	files := strings.Split(gitOut(t, bare, "ls-tree", "--name-only", "main"), "\n")
	if !slices.Contains(files, "landed.txt") {
		t.Errorf("merged tree = %v, want landed.txt kept", files)
	}
	if got := gitOut(t, bare, "show", "main:a.txt"); got != "a from the task" {
		t.Errorf("a.txt = %q, want the task's edit", got)
	}
}
