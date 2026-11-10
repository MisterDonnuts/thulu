package task

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func mkTask(t *testing.T, s *Store, title string) string {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: title}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create %q: %v", title, err)
	}
	return tk.ID
}

func setStage(t *testing.T, s *Store, id string, stage Stage) {
	t.Helper()
	if _, err := s.Update(t.Context(), id, Patch{Stage: &stage}); err != nil {
		t.Fatalf("Update stage on %s: %v", id, err)
	}
}

func TestAddDependency_BasicHappyPath(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")

	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	deps, err := s.GetDependencies(t.Context(), b)
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].TaskID != a || deps[0].Kind != DepHard {
		t.Fatalf("unexpected deps: %+v", deps)
	}

	dependents, err := s.GetDependents(t.Context(), a)
	if err != nil {
		t.Fatalf("GetDependents: %v", err)
	}
	if len(dependents) != 1 || dependents[0].TaskID != b {
		t.Fatalf("unexpected dependents: %+v", dependents)
	}
}

func TestAddDependency_RejectsSelfDep(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	if err := s.AddDependency(t.Context(), a, a, DepHard); err == nil {
		t.Fatal("expected error on self-dep")
	}
}

func TestAddDependency_RejectsMissingTasks(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	if err := s.AddDependency(t.Context(), a, "does-not-exist", DepHard); err == nil {
		t.Fatal("expected error on missing dep target")
	}
	if err := s.AddDependency(t.Context(), "does-not-exist", a, DepHard); err == nil {
		t.Fatal("expected error on missing source task")
	}
}

func TestAddDependency_RejectsDirectCycle(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := s.AddDependency(t.Context(), a, b, DepHard); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestAddDependency_RejectsTransitiveCycle(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	c := mkTask(t, s, "C")

	if err := s.AddDependency(t.Context(), a, b, DepHard); err != nil {
		t.Fatalf("a→b: %v", err)
	}
	if err := s.AddDependency(t.Context(), b, c, DepHard); err != nil {
		t.Fatalf("b→c: %v", err)
	}
	if err := s.AddDependency(t.Context(), c, a, DepHard); err == nil {
		t.Fatal("expected transitive cycle error")
	}
}

func TestAddDependency_RejectsDuplicate(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.AddDependency(t.Context(), b, a, DepHard); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestIsBlocked_HardBlockerNotDone(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	blocked, blockers, err := s.IsBlocked(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || len(blockers) != 1 || blockers[0].TaskID != a {
		t.Fatalf("expected B blocked by A, got blocked=%v blockers=%+v", blocked, blockers)
	}
}

func TestIsBlocked_DoneClearsBlock(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	setStage(t, s, a, types.StageDone)

	blocked, blockers, err := s.IsBlocked(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if blocked || len(blockers) != 0 {
		t.Fatalf("expected B unblocked after A=Done, got blocked=%v blockers=%+v", blocked, blockers)
	}
}

func TestIsBlocked_SoftDoesNotGate(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepSoft); err != nil {
		t.Fatal(err)
	}
	blocked, _, err := s.IsBlocked(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("soft dep must not block")
	}
}

func TestJustUnblockedDependents(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	c := mkTask(t, s, "C")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(t.Context(), c, a, DepHard); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(t.Context(), c, b, DepHard); err != nil {
		t.Fatal(err)
	}
	if freed, err := s.JustUnblockedDependents(t.Context(), a); err != nil {
		t.Fatal(err)
	} else if len(freed) != 0 {
		t.Fatalf("nothing should be freed while A is pending, got %+v", freed)
	}
	if freed, err := s.JustUnblockedDependents(t.Context(), c); err != nil {
		t.Fatal(err)
	} else if len(freed) != 0 {
		t.Fatalf("C has no dependents, got %+v", freed)
	}
	setStage(t, s, a, types.StageDone)
	freed, err := s.JustUnblockedDependents(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 1 || freed[0] != b {
		t.Fatalf("expected only B freed, got %+v", freed)
	}
	setStage(t, s, b, types.StageDone)
	freed, err = s.JustUnblockedDependents(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 1 || freed[0] != c {
		t.Fatalf("expected C freed, got %+v", freed)
	}
}

func TestBlockedSummary(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	c := mkTask(t, s, "C")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(t.Context(), c, a, DepSoft); err != nil {
		t.Fatal(err)
	}
	summary, err := s.BlockedSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if summary[b] != 1 {
		t.Errorf("expected B count 1, got %d", summary[b])
	}
	if _, ok := summary[c]; ok {
		t.Errorf("soft dep should not contribute to summary, got %d", summary[c])
	}
}

func TestRemoveDependency(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDependency(t.Context(), b, a); err != nil {
		t.Fatalf("remove: %v", err)
	}
	deps, _ := s.GetDependencies(t.Context(), b)
	if len(deps) != 0 {
		t.Fatalf("expected no deps, got %+v", deps)
	}
	if err := s.RemoveDependency(t.Context(), b, a); err != nil {
		t.Fatalf("second remove must be no-op: %v", err)
	}
}

func TestDeleteTaskCleansDeps(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), a); err != nil {
		t.Fatalf("delete A: %v", err)
	}
	deps, _ := s.GetDependencies(t.Context(), b)
	if len(deps) != 0 {
		t.Fatalf("deps should be cleaned when target task is deleted, got %+v", deps)
	}
}

func TestTaskListIncludesBlockedStatus(t *testing.T) {
	s := newTestStore(t)
	a := mkTask(t, s, "A")
	b := mkTask(t, s, "B")
	if err := s.AddDependency(t.Context(), b, a, DepHard); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var sawB bool
	for _, tk := range tasks {
		if tk.ID == b {
			sawB = true
			if !tk.IsBlocked || tk.BlockedByCount != 1 {
				t.Errorf("List should mark B as blocked count=1, got %+v", tk)
			}
		}
	}
	if !sawB {
		t.Fatal("B missing from list")
	}
}
