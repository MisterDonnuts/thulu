package task

import (
	"errors"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/listpage"
)

func childIDs(t *testing.T, s *Store, parentID string) []string {
	t.Helper()
	kids, err := s.ListChildren(t.Context(), parentID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	ids := make([]string, len(kids))
	for i, k := range kids {
		ids[i] = k.ID
	}
	return ids
}

func TestListChildren_SortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	parent := &Task{WorkspaceID: testWS, Title: "parent"}
	if err := s.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	var made []string
	for _, title := range []string{"a", "b", "c"} {
		c := &Task{WorkspaceID: testWS, Title: title, ParentID: parent.ID}
		if err := s.Create(ctx, c); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		made = append(made, c.ID)
	}

	if got := childIDs(t, s, parent.ID); !slices.Equal(got, made) {
		t.Fatalf("default order = %v, want creation order %v", got, made)
	}

	want := []string{made[2], made[0], made[1]}
	if err := s.SetChildOrder(ctx, parent.ID, want); err != nil {
		t.Fatalf("SetChildOrder: %v", err)
	}
	if got := childIDs(t, s, parent.ID); !slices.Equal(got, want) {
		t.Fatalf("after reorder = %v, want %v", got, want)
	}

	late := &Task{WorkspaceID: testWS, Title: "d", ParentID: parent.ID}
	if err := s.Create(ctx, late); err != nil {
		t.Fatalf("create d: %v", err)
	}
	if got := childIDs(t, s, parent.ID); !slices.Equal(got, append(slices.Clone(want), late.ID)) {
		t.Fatalf("new child after a reorder = %v, want it last", got)
	}
}

func TestSetChildOrder_RefusesForeignID(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	parent := &Task{WorkspaceID: testWS, Title: "parent"}
	other := &Task{WorkspaceID: testWS, Title: "other"}
	for _, tk := range []*Task{parent, other} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	a := &Task{WorkspaceID: testWS, Title: "a", ParentID: parent.ID}
	b := &Task{WorkspaceID: testWS, Title: "b", ParentID: parent.ID}
	for _, tk := range []*Task{a, b} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("create child: %v", err)
		}
	}
	if err := s.SetChildOrder(ctx, parent.ID, []string{b.ID, other.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetChildOrder with a foreign id = %v, want ErrInvalid", err)
	}
	if got := childIDs(t, s, parent.ID); !slices.Equal(got, []string{a.ID, b.ID}) {
		t.Fatalf("order after a refused write = %v, want it unchanged", got)
	}
}

func TestListPage_CarriesSortOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	parent := &Task{WorkspaceID: testWS, Title: "parent"}
	if err := s.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	a := &Task{WorkspaceID: testWS, Title: "a", ParentID: parent.ID}
	b := &Task{WorkspaceID: testWS, Title: "b", ParentID: parent.ID}
	for _, tk := range []*Task{a, b} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("create child: %v", err)
		}
	}
	if err := s.SetChildOrder(ctx, parent.ID, []string{b.ID, a.ID}); err != nil {
		t.Fatalf("SetChildOrder: %v", err)
	}
	rows, _, err := s.ListPage(ctx, CardFilter{Workspace: testWS}, listpage.Request{})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	got := map[string]int{}
	for _, r := range rows {
		got[r.ID] = r.SortOrder
	}
	if got[b.ID] != 1 || got[a.ID] != 2 || got[parent.ID] != 0 {
		t.Fatalf("sort_order on list rows = %v, want b=1 a=2 parent=0", got)
	}
}
