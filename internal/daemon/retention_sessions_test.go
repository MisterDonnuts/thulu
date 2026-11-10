package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestRetentionPrunesSessionSnapshots(t *testing.T) {
	data := t.TempDir()
	r := newRetention(&stores{}, data, "")
	if want := filepath.Join(data, "sessions"); r.sessionsDir != want {
		t.Fatalf("sessionsDir = %q, want %q", r.sessionsDir, want)
	}
	oldRef := task.SessionRef{RootTaskID: "g1", TaskID: "g1", Agent: "developer", SessionID: "old"}
	newRef := task.SessionRef{RootTaskID: "g2", TaskID: "g2", Agent: "developer", SessionID: "new"}
	for _, ref := range []task.SessionRef{oldRef, newRef} {
		if _, err := task.SaveSessionSnapshot(r.sessionsDir, ref, strings.NewReader("x"), 10); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(r.sessionsDir, "g1", "g1", "developer", "old.tar.gz"), past, past); err != nil {
		t.Fatal(err)
	}

	r.pruneRunFiles(t.Context(), time.Now().Add(-30*24*time.Hour))

	if _, err := os.Stat(filepath.Join(r.sessionsDir, "g1")); !os.IsNotExist(err) {
		t.Errorf("old snapshot's group survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.sessionsDir, "g2", "g2", "developer", "new.tar.gz")); err != nil {
		t.Errorf("recent snapshot went: %v", err)
	}
}
