package usage

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/listpage"
)

func sessionIDs(sessions []Session) []string {
	ids := make([]string, len(sessions))
	for i := range sessions {
		ids[i] = sessions[i].ID
	}
	return ids
}

func TestSessionsPageWalkMatchesWholeList(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{"r1", "r2", "r3", "r4", "r5"} {
		store.Add(t.Context(), makeSess(id, "agent", "tester", 10, 10, 0, 0.01, 1))
	}

	whole, cursor, err := store.SessionsPage(t.Context(), Everything(), listpage.Request{})
	if err != nil {
		t.Fatalf("SessionsPage: %v", err)
	}
	if cursor != "" {
		t.Errorf("unpaginated read handed out cursor %q", cursor)
	}
	want := sessionIDs(whole)
	if len(want) != 5 {
		t.Fatalf("seeded 5 sessions, listed %d", len(want))
	}

	var got []string
	seen := map[string]bool{}
	page := listpage.Request{Limit: 2}
	for range 10 {
		sessions, next, pageErr := store.SessionsPage(t.Context(), Everything(), page)
		if pageErr != nil {
			t.Fatalf("page: %v", pageErr)
		}
		for _, id := range sessionIDs(sessions) {
			if seen[id] {
				t.Fatalf("session %s served on two pages", id)
			}
			seen[id] = true
			got = append(got, id)
		}
		if next == "" {
			break
		}
		page.Cursor = next
	}
	if len(got) != len(want) {
		t.Fatalf("walked %d sessions, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("walk differs at %d: got %s want %s", i, got[i], want[i])
		}
	}
}

func TestSessionsPageRejectsBadCursor(t *testing.T) {
	store := newTestStore(t)
	store.Add(t.Context(), makeSess("r1", "agent", "tester", 10, 10, 0, 0.01, 1))

	if _, _, err := store.SessionsPage(t.Context(), Everything(), listpage.Request{Limit: 1, Cursor: "garbage"}); err == nil {
		t.Error("a malformed cursor was accepted")
	}
}
