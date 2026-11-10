package audit_test

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}

func TestAudit_AppendAndQuery(t *testing.T) {
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}

	store.Log(audit.Entry{
		Kind:         audit.KindTaskCreated,
		WorkspaceID:  "ws-1",
		ActorID:      "user-1",
		ResourceID:   "task-1",
		ResourceType: "task",
		Summary:      "Task created: Fix login bug",
	})
	store.Log(audit.Entry{
		Kind:         audit.KindAgentStarted,
		WorkspaceID:  "ws-1",
		ActorID:      "system",
		ResourceID:   "task-1",
		ResourceType: "task",
		Summary:      "Agent started: developer",
	})
	store.Log(audit.Entry{
		Kind:         audit.KindTaskDeleted,
		WorkspaceID:  "ws-2",
		ActorID:      "user-2",
		ResourceID:   "task-99",
		ResourceType: "task",
		Summary:      "Task deleted",
	})

	entries, err := store.List(audit.ListOptions{WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries for ws-1, got %d", len(entries))
	}

	byKind, _ := store.List(audit.ListOptions{Kind: audit.KindTaskCreated})
	if len(byKind) != 1 {
		t.Errorf("expected 1 task.created entry, got %d", len(byKind))
	}

	byResource, _ := store.List(audit.ListOptions{ResourceID: "task-1"})
	if len(byResource) != 2 {
		t.Errorf("expected 2 entries for task-1, got %d", len(byResource))
	}

	byActor, _ := store.List(audit.ListOptions{ActorID: "system"})
	if len(byActor) != 1 {
		t.Errorf("expected 1 system entry, got %d", len(byActor))
	}
}

func TestAudit_Pagination(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	for i := 0; i < 10; i++ {
		store.Log(audit.Entry{
			Kind:    audit.KindTaskCreated,
			Summary: "task created",
		})
	}

	page1, _ := store.List(audit.ListOptions{Limit: 3, Offset: 0})
	page2, _ := store.List(audit.ListOptions{Limit: 3, Offset: 3})
	all, _ := store.List(audit.ListOptions{Limit: 100})

	if len(page1) != 3 {
		t.Errorf("page1: expected 3, got %d", len(page1))
	}
	if len(page2) != 3 {
		t.Errorf("page2: expected 3, got %d", len(page2))
	}
	if len(all) != 10 {
		t.Errorf("all: expected 10, got %d", len(all))
	}

	if page1[0].ID == page2[0].ID {
		t.Error("pages overlap")
	}
}

func TestAudit_TimeFilter(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	store.Log(audit.Entry{Kind: audit.KindTaskCreated, Summary: "entry1"})
	store.Log(audit.Entry{Kind: audit.KindAgentStarted, Summary: "entry2"})

	all, _ := store.List(audit.ListOptions{Since: time.Now().Add(-time.Hour)})
	if len(all) != 2 {
		t.Errorf("expected 2 entries since 1h ago, got %d", len(all))
	}

	none, _ := store.List(audit.ListOptions{Since: time.Now().Add(time.Hour)})
	if len(none) != 0 {
		t.Errorf("expected 0 entries since future, got %d", len(none))
	}

	all2, _ := store.List(audit.ListOptions{Until: time.Now().Add(time.Hour)})
	if len(all2) != 2 {
		t.Errorf("expected 2 entries until future, got %d", len(all2))
	}
}

func TestAudit_LimitCap(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	entries, _ := store.List(audit.ListOptions{Limit: 9999})
	_ = entries
}

func TestAudit_LogRequest(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	r := httptest.NewRequest("POST", "/projects", nil)
	r.Header.Set("User-Agent", "test-agent/1.0")
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	store.LogRequest(r, audit.KindProjectCreated, "proj-1", "project", "created", map[string]string{"name": "test"})

	entries, err := store.List(audit.ListOptions{ResourceID: "proj-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Kind != audit.KindProjectCreated {
		t.Errorf("kind = %q, want %q", e.Kind, audit.KindProjectCreated)
	}
	if e.UserAgent != "test-agent/1.0" {
		t.Errorf("user agent = %q", e.UserAgent)
	}
	if e.ClientIP != "192.0.2.1" {
		t.Errorf("client ip = %q, want RemoteAddr (XFF untrusted by default)", e.ClientIP)
	}
}

func TestAudit_DB_Accessor(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))
	if store.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func TestAudit_LogRequest_NoXFF(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	r := httptest.NewRequest("GET", "/tasks", nil)
	r.RemoteAddr = "192.168.1.100:5000"
	store.LogRequest(r, audit.KindTaskCreated, "task-xyz", "task", "created", nil)

	entries, err := store.List(audit.ListOptions{ResourceID: "task-xyz"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ClientIP != "192.168.1.100" {
		t.Errorf("client_ip = %q, want RemoteAddr host (port stripped)", entries[0].ClientIP)
	}
}

func TestAudit_Log_WithExplicitID(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	store.Log(audit.Entry{
		ID:           "my-explicit-id",
		Kind:         audit.KindTaskCreated,
		WorkspaceID:  "ws-1",
		ResourceID:   "res-1",
		ResourceType: "task",
		Summary:      "explicit id test",
	})

	entries, _ := store.List(audit.ListOptions{ResourceID: "res-1"})
	if len(entries) != 1 || entries[0].ID != "my-explicit-id" {
		t.Errorf("expected explicit ID to be preserved, got %v", entries)
	}
}

func TestAudit_Log_DBError(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))
	store.DB().Close()
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, Summary: "test"})
}

func TestAudit_List_DBError(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))
	store.DB().Close()
	_, err := store.List(audit.ListOptions{})
	if err == nil {
		t.Error("expected error from List after DB close")
	}
}

func TestAudit_List_KindPrefix(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	store.Log(audit.Entry{Kind: audit.KindTaskCreated, Summary: "task created"})
	store.Log(audit.Entry{Kind: audit.KindTaskDeleted, Summary: "task deleted"})
	store.Log(audit.Entry{Kind: audit.KindAgentStarted, Summary: "agent started"})

	entries, err := store.List(audit.ListOptions{Kind: audit.EventKind("task")})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 task.* entries, got %d", len(entries))
	}
}

func TestAudit_List_DefaultLimit(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	entries, err := store.List(audit.ListOptions{Limit: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	_ = entries
}

func TestAudit_Export_UnavailableWithoutEE(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws1", Summary: "a"})

	var buf bytes.Buffer
	if err := store.Export(audit.ListOptions{WorkspaceID: "ws1"}, "csv", &buf); !errors.Is(err, audit.ErrExportUnavailable) {
		t.Fatalf("Export without ee/ = %v, want ErrExportUnavailable", err)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing may be written before the refusal, got %q", buf.String())
	}
}

func TestAudit_PurgeOlderThan(t *testing.T) {
	store, _ := audit.NewStore(tempDB(t))

	old := time.Now().Add(-48 * time.Hour)
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, ResourceID: "old", Summary: "old", CreatedAt: old})
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, ResourceID: "new", Summary: "new"})

	if n, err := store.PurgeOlderThan(time.Time{}); err != nil || n != 0 {
		t.Fatalf("zero cutoff should purge nothing: n=%d err=%v", n, err)
	}

	n, err := store.PurgeOlderThan(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("PurgeOlderThan: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 purged, got %d", n)
	}
	remaining, _ := store.List(audit.ListOptions{})
	var sawNew, sawPurge bool
	for _, e := range remaining {
		switch {
		case e.ResourceID == "old":
			t.Error("the old entry survived the purge")
		case e.ResourceID == "new":
			sawNew = true
		case e.Kind == audit.KindAuditPurged:
			sawPurge = true
			if e.Metadata["rows"] != "1" || e.ActorType != "system" {
				t.Errorf("purge record = %+v, want rows=1 by the system", e)
			}
		}
	}
	if !sawNew || !sawPurge || len(remaining) != 2 {
		t.Errorf("got %d entries (new=%v purge=%v), want the recent one plus the purge record", len(remaining), sawNew, sawPurge)
	}
	rep, err := store.VerifyChain(t.Context())
	if err != nil || !rep.OK {
		t.Errorf("VerifyChain after purge = %+v, %v", rep, err)
	}
}
