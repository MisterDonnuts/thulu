package audit_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/audit"
)

// created_at is truncated to the second, so these entries share one timestamp: an offset breaks
// here, a (created_at, id) cursor does not.
func TestAudit_CursorWalkCoversEveryEntry(t *testing.T) {
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		store.Log(audit.Entry{Kind: audit.KindTaskCreated, Summary: "task created"})
	}

	all, _, err := store.ListPage(audit.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(all) != 10 {
		t.Fatalf("seeded 10 entries, listed %d", len(all))
	}

	var walked []string
	seen := map[string]bool{}
	opts := audit.ListOptions{Limit: 3}
	for range 10 {
		page, next, pageErr := store.ListPage(opts)
		if pageErr != nil {
			t.Fatalf("page: %v", pageErr)
		}
		for _, e := range page {
			if seen[e.ID] {
				t.Fatalf("entry %s served on two pages", e.ID)
			}
			seen[e.ID] = true
			walked = append(walked, e.ID)
		}
		if next == "" {
			break
		}
		opts.Cursor = next
	}
	if len(walked) != len(all) {
		t.Fatalf("walked %d entries, want %d", len(walked), len(all))
	}
	for i := range walked {
		if walked[i] != all[i].ID {
			t.Errorf("walk differs at %d: got %s want %s", i, walked[i], all[i].ID)
		}
	}
}

func TestAudit_ListUnchangedWithoutCursor(t *testing.T) {
	store, err := audit.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		store.Log(audit.Entry{Kind: audit.KindTaskCreated, Summary: "task created"})
	}

	got, err := store.List(audit.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("List with no options = %d entries, want 4", len(got))
	}
}
