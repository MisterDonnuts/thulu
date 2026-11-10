package task

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func snapRef(task, sid string) SessionRef {
	return SessionRef{RootTaskID: "root1", TaskID: task, Agent: "developer", SessionID: sid}
}

func readSnapshot(t *testing.T, root string, ref SessionRef) string {
	t.Helper()
	f, err := OpenSessionSnapshot(root, ref)
	if err != nil {
		t.Fatalf("open %+v: %v", ref, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestSessionsRootBeside(t *testing.T) {
	if got := SessionsRootBeside("/data/runs"); got != "/data/sessions" {
		t.Errorf("SessionsRootBeside(/data/runs) = %q", got)
	}
	for _, bad := range []string{"", "runs", "./data/runs"} {
		if got := SessionsRootBeside(bad); got != "" {
			t.Errorf("SessionsRootBeside(%q) = %q, want empty for a non-absolute root", bad, got)
		}
	}
}

func TestSaveSessionSnapshotReplacesThePairsOlderOne(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	if _, err := SaveSessionSnapshot(root, snapRef("root1", "sid-1"), strings.NewReader("first"), 1024); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	n, err := SaveSessionSnapshot(root, snapRef("root1", "sid-2"), strings.NewReader("second"), 1024)
	if err != nil || n != int64(len("second")) {
		t.Fatalf("save 2 = %d, %v", n, err)
	}
	if got := readSnapshot(t, root, snapRef("root1", "sid-2")); got != "second" {
		t.Errorf("snapshot = %q", got)
	}
	if _, err := OpenSessionSnapshot(root, snapRef("root1", "sid-1")); apperr.KindOf(err) != apperr.KindNotFound {
		t.Errorf("the replaced snapshot answers %v, want NotFound", err)
	}

	file := filepath.Join(root, "root1", "root1", "developer", "sid-2.tar.gz")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("snapshot mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(file))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("agent dir mode = %o, want 700", perm)
	}

	if _, err := SaveSessionSnapshot(root, snapRef("sub1", "sid-3"), strings.NewReader("sub"), 1024); err != nil {
		t.Fatalf("save sub: %v", err)
	}
	if got := readSnapshot(t, root, snapRef("root1", "sid-2")); got != "second" {
		t.Errorf("the subtask's upload touched the parent's snapshot: %q", got)
	}
}

func TestSaveSessionSnapshotRefusesPastTheCap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	if _, err := SaveSessionSnapshot(root, snapRef("root1", "sid-1"), strings.NewReader("ok"), 4); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := SaveSessionSnapshot(root, snapRef("root1", "sid-2"), strings.NewReader("too big"), 4)
	if apperr.KindOf(err) != apperr.KindTooLarge {
		t.Fatalf("err = %v, want TooLarge", err)
	}
	if got := readSnapshot(t, root, snapRef("root1", "sid-1")); got != "ok" {
		t.Errorf("previous snapshot = %q after a refused upload", got)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "root1", "root1", "developer"))
	if len(entries) != 1 {
		t.Errorf("agent dir holds %d entries, want only the old snapshot (no partial upload left)", len(entries))
	}
	if _, err := SaveSessionSnapshot(root, snapRef("root1", "sid-3"), strings.NewReader("four"), 4); err != nil {
		t.Errorf("a body of exactly max bytes: %v", err)
	}
	if _, err := SaveSessionSnapshot(root, snapRef("root1", "sid-4"), strings.NewReader(""), 4); apperr.KindOf(err) != apperr.KindInvalid {
		t.Errorf("empty body: %v, want Invalid", err)
	}
}

func TestSessionRefRefusesPathTricks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	bad := []SessionRef{
		{RootTaskID: "..", TaskID: "t", Agent: "a", SessionID: "s"},
		{RootTaskID: "r", TaskID: "../x", Agent: "a", SessionID: "s"},
		{RootTaskID: "r", TaskID: "t", Agent: "a/b", SessionID: "s"},
		{RootTaskID: "r", TaskID: "t", Agent: "a", SessionID: "../../etc/passwd"},
		{RootTaskID: "r", TaskID: "t", Agent: "a", SessionID: ".hidden"},
		{RootTaskID: "r", TaskID: "t", Agent: "a", SessionID: "a..b"},
		{RootTaskID: "r", TaskID: "t", Agent: "", SessionID: "s"},
		{RootTaskID: "r", TaskID: "t", Agent: "a", SessionID: strings.Repeat("x", 129)},
	}
	for _, ref := range bad {
		if _, err := SaveSessionSnapshot(root, ref, strings.NewReader("x"), 10); apperr.KindOf(err) != apperr.KindInvalid {
			t.Errorf("save %+v: %v, want Invalid", ref, err)
		}
		if _, err := OpenSessionSnapshot(root, ref); apperr.KindOf(err) != apperr.KindNotFound {
			t.Errorf("open %+v: %v, want NotFound", ref, err)
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused ref created the store: %v", err)
	}
}

func TestRemoveTaskSessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	for _, ref := range []SessionRef{snapRef("root1", "s1"), snapRef("sub1", "s2")} {
		if _, err := SaveSessionSnapshot(root, ref, strings.NewReader("x"), 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveTaskSessions(root, "root1", "sub1"); err != nil {
		t.Fatalf("remove sub: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "root1", "sub1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("subtask sessions still there: %v", err)
	}
	readSnapshot(t, root, snapRef("root1", "s1"))
	if err := RemoveTaskSessions(root, "root1", "root1"); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "root1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("group sessions still there: %v", err)
	}
	if err := RemoveTaskSessions(root, "root1", "root1"); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
	if err := RemoveTaskSessions(root, "..", ".."); err != nil {
		t.Errorf("a bad id is ignored, got %v", err)
	}
}

func TestPruneSessionsByAge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	oldRef := SessionRef{RootTaskID: "g1", TaskID: "g1", Agent: "developer", SessionID: "old"}
	newRef := SessionRef{RootTaskID: "g2", TaskID: "g2", Agent: "developer", SessionID: "new"}
	for _, ref := range []SessionRef{oldRef, newRef} {
		if _, err := SaveSessionSnapshot(root, ref, strings.NewReader("x"), 10); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, oldRef.file()), past, past); err != nil {
		t.Fatal(err)
	}

	n, err := PruneSessions(root, time.Now().Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneSessions = %d, %v; want 1 removed", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "g1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("emptied group dir left behind: %v", err)
	}
	readSnapshot(t, root, newRef)

	if n, err := PruneSessions(filepath.Join(t.TempDir(), "missing"), time.Now()); n != 0 || err != nil {
		t.Errorf("missing root = %d, %v", n, err)
	}
}
