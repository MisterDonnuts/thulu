package task

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestFlowFlagsNullVersusEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := &Task{WorkspaceID: testWS, Title: "flags"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := store.Get(ctx, tk.ID)
	if got.FlowFlags != nil {
		t.Fatalf("fresh task flow_flags = %#v, want nil", got.FlowFlags)
	}
	if b, _ := json.Marshal(got); !jsonHas(b, "flow_flags", nil) {
		t.Errorf("nil flow_flags must marshal as null: %s", b)
	}

	empty := []string{}
	if _, err := store.Update(ctx, tk.ID, Patch{FlowFlags: &empty}); err != nil {
		t.Fatalf("Update empty: %v", err)
	}
	got, _ = store.Get(ctx, tk.ID)
	if got.FlowFlags == nil || len(got.FlowFlags) != 0 {
		t.Fatalf("after setting [] flow_flags = %#v, want empty non-nil", got.FlowFlags)
	}

	flags := []string{"ui", "api"}
	if _, err := store.Update(ctx, tk.ID, Patch{FlowFlags: &flags}); err != nil {
		t.Fatalf("Update flags: %v", err)
	}
	got, _ = store.Get(ctx, tk.ID)
	if !reflect.DeepEqual(got.FlowFlags, flags) {
		t.Errorf("flow_flags = %v, want %v", got.FlowFlags, flags)
	}
}

func TestMergeSHAsRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := &Task{WorkspaceID: testWS, Title: "merge", MergeSHAs: map[string]string{"p1": "abc"}}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := store.Get(ctx, tk.ID)
	if got.MergeSHAs["p1"] != "abc" {
		t.Fatalf("merge_shas after Create = %v", got.MergeSHAs)
	}
	if _, err := store.Update(ctx, tk.ID, Patch{MergeSHAs: map[string]string{"p1": "abc", "p2": "def"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = store.Get(ctx, tk.ID)
	if len(got.MergeSHAs) != 2 || got.MergeSHAs["p2"] != "def" {
		t.Errorf("merge_shas after Update = %v", got.MergeSHAs)
	}
}

func TestLockedFilesUpsertAndCascade(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := &Task{WorkspaceID: testWS, Title: "locked"}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	f := LockedFile{TaskID: tk.ID, ProjectID: "p1", Path: "tests/a_test.go", BlobSHA: "111", Stage: "test_design", LockedAt: at}
	if err := store.PutLockedFile(ctx, f); err != nil {
		t.Fatalf("PutLockedFile: %v", err)
	}
	f.BlobSHA, f.LockedRef = "222", "refs/heads/x"
	if err := store.PutLockedFile(ctx, f); err != nil {
		t.Fatalf("PutLockedFile again: %v", err)
	}
	got, err := store.ListLockedFiles(ctx, tk.ID)
	if err != nil {
		t.Fatalf("ListLockedFiles: %v", err)
	}
	if len(got) != 1 || got[0].BlobSHA != "222" || got[0].LockedRef != "refs/heads/x" || !got[0].LockedAt.Equal(at) {
		t.Fatalf("locked files = %+v", got)
	}
	if err := store.PutLockedFile(ctx, LockedFile{TaskID: tk.ID}); err == nil {
		t.Error("PutLockedFile without project and path accepted")
	}
	if err := store.Delete(ctx, tk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ = store.ListLockedFiles(ctx, tk.ID); len(got) != 0 {
		t.Errorf("locked files survived the task: %+v", got)
	}
}

func jsonHas(b []byte, key string, want any) bool {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	v, ok := m[key]
	return ok && reflect.DeepEqual(v, want)
}
