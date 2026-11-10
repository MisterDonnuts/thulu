package audit_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/audit"
)

func TestAuditChain_VerifiesAndDetectsTampering(t *testing.T) {
	db := tempDB(t)
	store, err := audit.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws1", ResourceID: id, Summary: "created " + id})
	}

	rep, err := store.VerifyChain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Checked != 3 || rep.Head == "" {
		t.Fatalf("fresh chain = %+v, want 3 verified rows and a head", rep)
	}

	if _, err := db.ExecContext(t.Context(),
		`UPDATE audit_log SET summary = 'nothing to see' WHERE resource_id = 'b'`); err != nil {
		t.Fatal(err)
	}
	rep, err = store.VerifyChain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("edited row passed verification")
	}
}

func TestAuditChain_DetectsDeletedMiddleRow(t *testing.T) {
	db := tempDB(t)
	store, _ := audit.NewStore(db)
	for _, id := range []string{"a", "b", "c"} {
		store.Log(audit.Entry{Kind: audit.KindTaskCreated, ResourceID: id, Summary: id})
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM audit_log WHERE resource_id = 'b'`); err != nil {
		t.Fatal(err)
	}
	rep, err := store.VerifyChain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("a row deleted from the middle went unnoticed")
	}
	if rep.BrokenAt == "" || rep.BrokenCreatedAt.IsZero() || rep.Cause == "" || strings.Contains(rep.Cause, rep.BrokenAt) {
		t.Errorf("report = %+v, want the row, its time and a cause without the id", rep)
	}
}

func TestAuditChain_LegacyRowsBeforeChain(t *testing.T) {
	db := tempDB(t)
	store, _ := audit.NewStore(db)
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO audit_log (id, kind, summary, created_at) VALUES ('legacy1', 'task.created', 'old', NOW())`); err != nil {
		t.Fatal(err)
	}
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, ResourceID: "new", Summary: "new"})

	rep, err := store.VerifyChain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Legacy != 1 || rep.Checked != 1 {
		t.Fatalf("report = %+v, want 1 legacy + 1 verified", rep)
	}
}

func TestAuditSink_FileReceivesEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("OCTOPUS_AUDIT_SINK", "file:"+path)
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws1", ResourceID: "t1", Summary: "one"})
	store.Log(audit.Entry{Kind: audit.KindTaskDeleted, WorkspaceID: "ws1", ResourceID: "t1", Summary: "two"})

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, _ := f.Stat()
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("sink file mode = %o, want 600", perm)
	}
	var lines []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("sink line is not JSON: %q", sc.Text())
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("sink got %d lines, want 2", len(lines))
	}
	if lines[0]["type"] != "octopus.audit" || lines[0]["workspace_id"] != "ws1" || lines[1]["row_hash"] == "" {
		t.Errorf("sink line = %v, want the typed entry with its hash", lines[0])
	}
}

func TestAuditSink_UnknownValueFailsStartup(t *testing.T) {
	t.Setenv("OCTOPUS_AUDIT_SINK", "syslog://collector:514")
	if _, err := audit.NewStore(tempDB(t)); err == nil {
		t.Fatal("an unrecognized sink was accepted; the operator would believe logs are shipped")
	}
}
