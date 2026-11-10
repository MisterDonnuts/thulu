package task

import (
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/listpage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func seedBoard(t *testing.T, s *Store, boardID string, n int) []Task {
	t.Helper()
	for i := range n {
		task := &Task{
			Title:       "task " + string(rune('a'+i)),
			Body:        "# description of task " + string(rune('a'+i)),
			WorkspaceID: "ws1",
			BoardID:     boardID,
			Stage:       types.StageBacklog,
		}
		if err := s.Create(t.Context(), task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
		if _, err := s.db.ExecContext(t.Context(), s.rb(
			`UPDATE tasks SET updated_at = ? WHERE id = ?`),
			time.Now().UTC().Add(time.Duration(i)*time.Minute), task.ID); err != nil {
			t.Fatalf("stamp updated_at: %v", err)
		}
	}
	all, err := s.ListByBoard(t.Context(), boardID)
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(all) != n {
		t.Fatalf("seeded %d tasks, listed %d", n, len(all))
	}
	return all
}

func TestCardProjectionDropsBody(t *testing.T) {
	if !strings.Contains(taskCardCols, `'' AS body`) {
		t.Fatalf("taskCardCols still selects the body column:\n%s", taskCardCols)
	}
	if strings.Count(taskCardCols, "body") != strings.Count(taskCols, "body") {
		t.Errorf("card projection lost or gained a column relative to taskCols")
	}
}

func idsOf(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	return ids
}

func TestListOmitsBodyButGetKeepsIt(t *testing.T) {
	s := newTestStore(t)
	seeded := seedBoard(t, s, "b1", 3)

	for _, got := range seeded {
		if got.Body != "" {
			t.Errorf("ListByBoard returned body %q for %s, want empty", got.Body, got.ID)
		}
	}

	full, err := s.Get(t.Context(), seeded[0].ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if full.Body == "" {
		t.Error("Get returned an empty body; the drawer would render nothing")
	}

	byWorkspace, err := s.ListByWorkspace(t.Context(), "ws1")
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	for _, got := range byWorkspace {
		if got.Body == "" {
			t.Fatalf("ListByWorkspace dropped the body of %s — export would blank it", got.ID)
		}
	}
}

func TestUnpaginatedListIsUnchanged(t *testing.T) {
	s := newTestStore(t)
	seeded := seedBoard(t, s, "b1", 5)

	paged, cursor, err := s.ListByBoardPage(t.Context(), "b1", listpage.Request{})
	if err != nil {
		t.Fatalf("ListByBoardPage: %v", err)
	}
	if cursor != "" {
		t.Errorf("unpaginated read handed out cursor %q", cursor)
	}
	if got, want := idsOf(paged), idsOf(seeded); len(got) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(got), len(want))
	} else {
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("order changed at %d: got %s want %s", i, got[i], want[i])
			}
		}
	}
}

func TestPagingTwoPagesMatchesWholeList(t *testing.T) {
	s := newTestStore(t)
	want := idsOf(seedBoard(t, s, "b1", 5))

	var got []string
	page := listpage.Request{Limit: 2}
	for range 10 {
		tasks, next, err := s.ListByBoardPage(t.Context(), "b1", page)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		got = append(got, idsOf(tasks)...)
		if next == "" {
			break
		}
		page.Cursor = next
	}
	if len(got) != len(want) {
		t.Fatalf("paged through %d tasks, want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("page walk differs at %d: got %s want %s", i, got[i], want[i])
		}
	}
}

func TestCursorTieBreaksOnEqualTimestamps(t *testing.T) {
	s := newTestStore(t)
	seedBoard(t, s, "b1", 4)

	same := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := s.db.ExecContext(t.Context(), s.rb(
		`UPDATE tasks SET updated_at = ? WHERE board_id = ?`), same, "b1"); err != nil {
		t.Fatalf("flatten updated_at: %v", err)
	}

	whole, err := s.ListByBoard(t.Context(), "b1")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	want := idsOf(whole)

	var got []string
	seen := map[string]bool{}
	page := listpage.Request{Limit: 1}
	for range 10 {
		tasks, next, err := s.ListByBoardPage(t.Context(), "b1", page)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		for _, id := range idsOf(tasks) {
			if seen[id] {
				t.Fatalf("task %s served twice while every row shares updated_at", id)
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
		t.Fatalf("paged %d of %d rows that share a timestamp: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("tie-broken order differs at %d: got %s want %s", i, got[i], want[i])
		}
	}
}

func TestBadCursorIsRejected(t *testing.T) {
	s := newTestStore(t)
	seedBoard(t, s, "b1", 2)

	if _, _, err := s.ListByBoardPage(t.Context(), "b1", listpage.Request{Limit: 1, Cursor: "not-a-cursor!"}); err == nil {
		t.Error("a malformed cursor was accepted; a paging client would loop on page one")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	want := listpage.Key{At: time.Now().UTC().Truncate(time.Microsecond), ID: "abc|def"}
	got, err := listpage.Decode(listpage.Encode(want))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !got.At.Equal(want.At) || got.ID != want.ID {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	for _, bad := range []string{"", "!!!", "YWJj", "bm90LWEtbnVtYmVyfGlk"} {
		if _, err := listpage.Decode(bad); err == nil {
			t.Errorf("Decode(%q) accepted a token Encode never produced", bad)
		}
	}
}

func TestClampCapsLimit(t *testing.T) {
	got := listpage.Request{Limit: 100_000}.Clamp(listpage.MaxLimit)
	if got.Limit != listpage.MaxLimit {
		t.Errorf("Clamp left limit at %d, want %d", got.Limit, listpage.MaxLimit)
	}
	if got := (listpage.Request{Limit: 10}).Clamp(listpage.MaxLimit); got.Limit != 10 {
		t.Errorf("Clamp lowered a limit under the ceiling to %d", got.Limit)
	}
}
