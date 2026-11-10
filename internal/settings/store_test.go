package settings

import (
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := testpg.New(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s.SetStageBoard(func() []types.StageInfo { return testBoard })
	if err := s.SeedSandboxPriorities(t.Context()); err != nil {
		t.Fatalf("SeedSandboxPriorities: %v", err)
	}
	return s
}

func TestSeedsDefaultsOnFirstBoot(t *testing.T) {
	s := newTestStore(t)

	prios, err := s.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities: %v", err)
	}
	if prios["work"] != 10 {
		t.Errorf("expected work=10, got %d", prios["work"])
	}
	if prios["start"] != 0 {
		t.Errorf("expected the initial stage at 0, got %d", prios["start"])
	}

	max, err := s.SandboxMax(t.Context())
	if err != nil {
		t.Fatalf("SandboxMax: %v", err)
	}
	if max != 3 {
		t.Errorf("expected max=3, got %d", max)
	}

	idle, err := s.SandboxIdleEvictSeconds(t.Context())
	if err != nil {
		t.Fatalf("SandboxIdleEvictSeconds: %v", err)
	}
	if idle != 30*60 {
		t.Errorf("expected idle=1800, got %d", idle)
	}

	block, err := s.SandboxAcquireBlockMs(t.Context())
	if err != nil {
		t.Fatalf("SandboxAcquireBlockMs: %v", err)
	}
	if block != 2*60_000 {
		t.Errorf("expected block=120000, got %d", block)
	}
}

func TestUserOverridesArePreservedAcrossRestart(t *testing.T) {
	db := testpg.New(t)
	s1, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	custom := SandboxPriorities{"release": 999, "in_progress": 50}
	if err := s1.SetSandboxPriorities(t.Context(), custom); err != nil {
		t.Fatalf("SetSandboxPriorities: %v", err)
	}
	if err := s1.SetSandboxMax(t.Context(), 7); err != nil {
		t.Fatalf("SetSandboxMax: %v", err)
	}

	s2, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}

	got, err := s2.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities: %v", err)
	}
	if got["release"] != 999 {
		t.Errorf("user override lost: release=%d", got["release"])
	}
	if got["in_progress"] != 50 {
		t.Errorf("user override lost: in_progress=%d", got["in_progress"])
	}

	max, _ := s2.SandboxMax(t.Context())
	if max != 7 {
		t.Errorf("user override lost: max=%d", max)
	}
}

func TestSetSandboxMaxRejectsZero(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetSandboxMax(t.Context(), 0); err == nil {
		t.Error("expected error on max=0")
	}
}

func TestGetRawReturnsNotFoundForUnknownKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetRaw(t.Context(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPutAndGetJSON(t *testing.T) {
	s := newTestStore(t)
	type config struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	in := config{Name: "test", N: 42}
	if err := s.PutJSON(t.Context(), "custom.key", in); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}
	var out config
	if err := s.GetJSON(t.Context(), "custom.key", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip mismatch: in=%+v out=%+v", in, out)
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)
	all, err := s.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range []string{
		SandboxPrioritiesKey,
		SandboxMaxKey,
		SandboxIdleEvictSecondsKey,
		SandboxAcquireBlockMsKey,
	} {
		if _, ok := all[k]; !ok {
			t.Errorf("List missing seeded key %q", k)
		}
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutRaw(t.Context(), "temp", "x"); err != nil {
		t.Fatalf("PutRaw: %v", err)
	}
	if err := s.Delete(t.Context(), "temp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetRaw(t.Context(), "temp"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSandboxMax_DefaultAndSet(t *testing.T) {
	s := newTestStore(t)

	n, err := s.SandboxMax(t.Context())
	if err != nil {
		t.Fatalf("SandboxMax: %v", err)
	}
	if n != 3 {
		t.Errorf("default SandboxMax = %d, want 3", n)
	}

	if err := s.SetSandboxMax(t.Context(), 8); err != nil {
		t.Fatalf("SetSandboxMax: %v", err)
	}
	n, _ = s.SandboxMax(t.Context())
	if n != 8 {
		t.Errorf("SandboxMax after set = %d, want 8", n)
	}

	if err := s.SetSandboxMax(t.Context(), 0); err == nil {
		t.Error("SetSandboxMax(0) should return error")
	}
}

func TestSandboxPriorities_DefaultAndSet(t *testing.T) {
	s := newTestStore(t)

	p, err := s.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities: %v", err)
	}
	if len(p) == 0 {
		t.Error("expected non-empty default priorities")
	}

	newP := SandboxPriorities{"refinement": 10, "in_progress": 5}
	if err := s.SetSandboxPriorities(t.Context(), newP); err != nil {
		t.Fatalf("SetSandboxPriorities: %v", err)
	}
	got, _ := s.SandboxPriorities(t.Context())
	if got["refinement"] != 10 {
		t.Errorf("refinement priority = %d, want 10", got["refinement"])
	}
}

func TestSandboxIdleEvictSeconds_Default(t *testing.T) {
	s := newTestStore(t)
	n, err := s.SandboxIdleEvictSeconds(t.Context())
	if err != nil {
		t.Fatalf("SandboxIdleEvictSeconds: %v", err)
	}
	if n != 30*60 {
		t.Errorf("default idle evict = %d, want %d", n, 30*60)
	}
}

func TestSandboxAcquireBlockMs_Default(t *testing.T) {
	s := newTestStore(t)
	n, err := s.SandboxAcquireBlockMs(t.Context())
	if err != nil {
		t.Fatalf("SandboxAcquireBlockMs: %v", err)
	}
	if n != 2*60_000 {
		t.Errorf("default acquire block ms = %d, want %d", n, 2*60_000)
	}
}

func TestDB_Accessor(t *testing.T) {
	s := newTestStore(t)
	if s.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func TestSandboxPriorities_FallbackWhenDeleted(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(t.Context(), SandboxPrioritiesKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	p, err := s.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities after delete: %v", err)
	}
	if p["work"] != 10 {
		t.Errorf("derived fallback: got %v", p)
	}
}

var testBoard = []types.StageInfo{
	{ID: "start", Label: "Start", Position: 10, Initial: true},
	{ID: "work", Label: "Work", Position: 20},
	{ID: "closed", Label: "Closed", Position: 30, Terminal: true},
}

func TestDerivePriorities(t *testing.T) {
	p := DerivePriorities(testBoard)
	want := SandboxPriorities{"start": 0, "work": 10, "closed": 0}
	for id, w := range want {
		if p[id] != w {
			t.Errorf("%s: got %d, want %d", id, p[id], w)
		}
	}
	if len(p) != len(want) {
		t.Errorf("unexpected entries: %v", p)
	}
}

func TestDerivePrioritiesSkipsVirtualAndSortsByPosition(t *testing.T) {
	board := []types.StageInfo{
		{ID: "later", Position: 30},
		{ID: "mention", Position: 0, Virtual: true},
		{ID: "earlier", Position: 20},
		{ID: "first", Position: 10, Initial: true},
	}
	p := DerivePriorities(board)
	if _, ok := p["mention"]; ok {
		t.Error("virtual stages must not get a queue weight")
	}
	if p["earlier"] >= p["later"] {
		t.Errorf("later stages must outweigh earlier ones: %v", p)
	}
}

func TestSeedSandboxPrioritiesNeverOverwrites(t *testing.T) {
	s := newTestStore(t)
	custom := SandboxPriorities{"work": 999}
	if err := s.SetSandboxPriorities(t.Context(), custom); err != nil {
		t.Fatalf("SetSandboxPriorities: %v", err)
	}
	if err := s.SeedSandboxPriorities(t.Context()); err != nil {
		t.Fatalf("SeedSandboxPriorities: %v", err)
	}
	got, err := s.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities: %v", err)
	}
	if got["work"] != 999 {
		t.Errorf("seed clobbered the user's weights: %v", got)
	}
}

func TestGetJSON_UnmarshalError(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutRaw(t.Context(), "bad.key", "not-json"); err != nil {
		t.Fatalf("PutRaw: %v", err)
	}
	var n int
	if err := s.GetJSON(t.Context(), "bad.key", &n); err == nil {
		t.Error("expected unmarshal error for invalid JSON")
	}
}

func TestPutJSON_MarshalError(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutJSON(t.Context(), "unmarshalable", make(chan int)); err == nil {
		t.Error("expected marshal error for channel value")
	}
}

func TestGetInt_UnmarshalError(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutRaw(t.Context(), SandboxMaxKey, `"not-an-int"`); err != nil {
		t.Fatalf("PutRaw: %v", err)
	}
	if _, err := s.SandboxMax(t.Context()); err == nil {
		t.Error("expected unmarshal error when sandbox.max stores a string")
	}
}

func TestSandboxPriorities_UnmarshalError(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutRaw(t.Context(), SandboxPrioritiesKey, `"not-a-map"`); err != nil {
		t.Fatalf("PutRaw: %v", err)
	}
	if _, err := s.SandboxPriorities(t.Context()); err == nil {
		t.Error("expected unmarshal error for non-map priorities")
	}
}

func TestGetRaw_DBError(t *testing.T) {
	s := newTestStore(t)
	s.DB().Close()
	_, err := s.GetRaw(t.Context(), "any-key")
	if err == nil {
		t.Error("expected error from GetRaw after DB close")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("expected DB error, not ErrNotFound")
	}
}

func TestList_DBError(t *testing.T) {
	s := newTestStore(t)
	s.DB().Close()
	got, err := s.List(t.Context())
	if err == nil {
		t.Error("expected error from List after DB close")
	}
	if got != nil {
		t.Error("expected nil map from List after DB close")
	}
}

func TestGetInt_DBError(t *testing.T) {
	s := newTestStore(t)
	s.DB().Close()
	_, err := s.SandboxMax(t.Context())
	if err == nil {
		t.Error("expected error from SandboxMax after DB close")
	}
}

func TestSeedDefaults_AfterDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(t.Context(), SandboxMaxKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	n, err := s.SandboxMax(t.Context())
	if err != nil {
		t.Errorf("SandboxMax after delete: %v", err)
	}
	if n <= 0 {
		t.Errorf("expected positive fallback value, got %d", n)
	}
}

func TestAddStagePrioritiesFillsOnlyNewStages(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetSandboxPriorities(t.Context(), SandboxPriorities{"start": 0, "work": 999}); err != nil {
		t.Fatalf("SetSandboxPriorities: %v", err)
	}
	board := []types.StageInfo{
		{ID: "start", Position: 10, Initial: true},
		{ID: "work", Position: 20},
		{ID: "review", Position: 30},
		{ID: "done", Position: 40, Terminal: true},
	}
	added, err := s.AddStagePriorities(t.Context(), board)
	if err != nil {
		t.Fatalf("AddStagePriorities: %v", err)
	}
	if len(added) != 2 || added[0] != "done" || added[1] != "review" {
		t.Errorf("added = %v, want [done review]", added)
	}
	got, err := s.SandboxPriorities(t.Context())
	if err != nil {
		t.Fatalf("SandboxPriorities: %v", err)
	}
	if got["work"] != 999 {
		t.Errorf("work = %d, the edited weight must stay", got["work"])
	}
	if got["review"] != 20 {
		t.Errorf("review = %d, want 20 (second working stage)", got["review"])
	}
	if got["done"] != 0 {
		t.Errorf("done = %d, a terminal stage weighs 0", got["done"])
	}

	if added, err = s.AddStagePriorities(t.Context(), board); err != nil || added != nil {
		t.Errorf("second call = %v, %v; want nil, nil", added, err)
	}
}

func TestPutRawManyWritesAll(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutRawMany(t.Context(), map[string]string{SandboxMaxKey: "7", SandboxAcquireBlockMsKey: "5000"}); err != nil {
		t.Fatalf("PutRawMany: %v", err)
	}
	if n, err := s.SandboxMax(t.Context()); err != nil || n != 7 {
		t.Errorf("sandbox.max = %d, %v; want 7", n, err)
	}
	if n, err := s.SandboxAcquireBlockMs(t.Context()); err != nil || n != 5000 {
		t.Errorf("sandbox.acquire_block_ms = %d, %v; want 5000", n, err)
	}
}
