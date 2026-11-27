package task

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestSetMergeSHA_ConcurrentRepos(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := &Task{Title: "two repos", WorkspaceID: "ws"}
	if err := s.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	// The webhooks of a many-repo task land together; none may drop another's SHA.
	const n = 6
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.SetMergeSHA(ctx, tk.ID, fmt.Sprintf("p%d", i), fmt.Sprintf("%040d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := s.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MergeSHAs) != n {
		t.Fatalf("merge_shas = %v, want %d entries", got.MergeSHAs, n)
	}
}

func TestSetMergeSHA_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := &Task{Title: "t", WorkspaceID: "ws"}
	if err := s.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	if _, changed, err := s.SetMergeSHA(ctx, tk.ID, "p1", sha); err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	if _, changed, err := s.SetMergeSHA(ctx, tk.ID, "p1", sha); err != nil || changed {
		t.Fatalf("same sha again: changed=%v err=%v, want unchanged", changed, err)
	}
	if _, _, err := s.SetMergeSHA(ctx, tk.ID, "", sha); err == nil {
		t.Fatal("an empty project id went through")
	}
	if _, _, err := s.SetMergeSHA(ctx, "nope", "p1", sha); err == nil {
		t.Fatal("a missing task went through")
	}
}

func TestFindByPRURL_Workspace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const url = "https://github.com/acme/app/pull/7"
	mine := &Task{Title: "mine", WorkspaceID: "ws-a"}
	theirs := &Task{Title: "theirs", WorkspaceID: "ws-b"}
	for _, tk := range []*Task{mine, theirs} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update(ctx, tk.ID, Patch{PRURLs: map[string]string{"p1": "https://github.com/acme/other/pull/1", "p2": url}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.FindByPRURL(ctx, "ws-a", url)
	if err != nil || got == nil || got.ID != mine.ID {
		t.Fatalf("FindByPRURL(ws-a) = %v, %v; want %s", got, err, mine.ID)
	}
	if got, err := s.FindByPRURL(ctx, "ws-c", url); err != nil || got != nil {
		t.Fatalf("FindByPRURL(ws-c) = %v, %v; want nil", got, err)
	}
	if got, err := s.FindByPRURL(ctx, "", url); err != nil || got != nil {
		t.Fatalf("FindByPRURL without workspace = %v, %v; want nil", got, err)
	}
}

func TestUpdateIfStage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk := &Task{Title: "t", WorkspaceID: "ws", Stage: types.StageInProgress}
	if err := s.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}
	next := types.StageAIReview
	if _, err := s.UpdateIfStage(ctx, tk.ID, types.StageBacklog, Patch{Stage: &next}); !IsStageChanged(err) {
		t.Fatalf("from the wrong stage: err = %v, want stage changed", err)
	}
	got, _ := s.Get(ctx, tk.ID)
	if got.Stage != types.StageInProgress {
		t.Fatalf("a refused move changed the stage to %s", got.Stage)
	}
	if got, err := s.UpdateIfStage(ctx, tk.ID, types.StageInProgress, Patch{Stage: &next}); err != nil || got.Stage != next {
		t.Fatalf("from the right stage = %v, %v", got, err)
	}
}

func TestListProjectTasksInStage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mk := func(title, ws string, stage types.Stage, projects []string) *Task {
		t.Helper()
		tk := &Task{Title: title, WorkspaceID: ws, Stage: stage, ProjectIDs: projects}
		if err := s.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	a := mk("a", "ws", types.StageRelease, []string{"p1", "p2"})
	mk("other stage", "ws", types.StageDone, []string{"p1"})
	mk("other project", "ws", types.StageRelease, []string{"p3"})
	mk("other workspace", "ws2", types.StageRelease, []string{"p1"})
	archived := mk("archived", "ws", types.StageRelease, []string{"p1"})
	yes := true
	if _, err := s.Update(ctx, archived.ID, Patch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	sub := &Task{Title: "sub", WorkspaceID: "ws", Stage: types.StageRelease, ProjectIDs: []string{"p1"}, ParentID: a.ID}
	if err := s.Create(ctx, sub); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListProjectTasksInStage(ctx, "ws", "p1", types.StageRelease, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != a.ID {
		ids := []string{}
		for _, g := range got {
			ids = append(ids, g.Title)
		}
		t.Fatalf("listed %v, want only a", ids)
	}
}
