package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func (e mergeEnv) uploaded(t *testing.T, name string) *project.Project {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "folder.tar.gz")
	if err := os.WriteFile(archive, uploadTarGz(t, map[string]string{"README.md": "# " + name + "\n"}), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _, err := e.srv.projectStore.CreateFromUpload(t.Context(), name, archive,
		project.CreateOpts{WorkspaceID: mergeWS}, project.DefaultUploadLimits())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A task on a GitHub repository and an uploaded project; the upload has a worktree and no PR.
func (e mergeEnv) taskWithUpload(t *testing.T, gh, up *project.Project, url string) (*task.Task, string) {
	t.Helper()
	tk := e.task(t, mtBoard, mtReview, map[string]string{gh.ID: url})
	ids := []string{gh.ID, up.ID}
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{ProjectIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	dir, _, err := e.srv.projectStore.CreateTaskWorktree(t.Context(), up.ID, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return e.get(t, tk.ID), dir
}

func commitIn(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "change"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// An uploaded project the task never changed has nothing to merge: the GitHub PR is the last merge
// the task waits for, and it moves it.
func TestPRMerge_UnchangedUploadDoesNotHoldTheTask(t *testing.T) {
	e := newMergeEnv(t)
	gh, up := e.project(t, "api"), e.uploaded(t, "folder")
	const url = "https://github.com/acme/api/pull/7"
	tk, _ := e.taskWithUpload(t, gh, up, url)

	e.deliver(t, mergedPayload(100, url, sha40('e'), gh.DefaultBranch))
	if got := e.get(t, tk.ID); got.Stage != mtRelease {
		t.Fatalf("after the PR merged: stage %s, want %s (the upload has nothing to merge)", got.Stage, mtRelease)
	}
	if n := e.notes(t, tk.ID, "api.merges.comments.waiting"); n != 0 {
		t.Errorf("waiting comments = %d, want 0", n)
	}
}

// A task whose upload never got a worktree (no run needed one) has nothing there to merge.
func TestPRMerge_UploadWithoutWorktreeDoesNotHoldTheTask(t *testing.T) {
	e := newMergeEnv(t)
	gh, up := e.project(t, "api"), e.uploaded(t, "folder")
	const url = "https://github.com/acme/api/pull/11"
	tk := e.task(t, mtBoard, mtReview, map[string]string{gh.ID: url})
	ids := []string{gh.ID, up.ID}
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{ProjectIDs: &ids}); err != nil {
		t.Fatal(err)
	}

	e.deliver(t, mergedPayload(100, url, sha40('b'), gh.DefaultBranch))
	if got := e.get(t, tk.ID); got.Stage != mtRelease {
		t.Fatalf("after the PR merged: stage %s, want %s (the upload has no worktree)", got.Stage, mtRelease)
	}
}

// Committed or not, a change in the upload's worktree still has to merge through Thulu.
func TestPRMerge_ChangedUploadStillHoldsTheTask(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, dir string){
		"committed": func(t *testing.T, dir string) { commitIn(t, dir, "new.txt") },
		"uncommitted": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("edited\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newMergeEnv(t)
			gh, up := e.project(t, "api"), e.uploaded(t, "folder")
			const url = "https://github.com/acme/api/pull/8"
			tk, dir := e.taskWithUpload(t, gh, up, url)
			change(t, dir)

			e.deliver(t, mergedPayload(100, url, sha40('f'), gh.DefaultBranch))
			if got := e.get(t, tk.ID); got.Stage != mtReview {
				t.Fatalf("after the PR merged: stage %s, want %s until the upload merges", got.Stage, mtReview)
			}
			if n := e.notes(t, tk.ID, "api.merges.comments.waiting"); n != 1 {
				t.Errorf("waiting comments = %d, want 1", n)
			}
		})
	}
}

// pushResult leaves a result the agent pushed and the daemon has not integrated yet: a commit on the
// task's result branch in the project's repository, ahead of the default branch.
func pushResult(t *testing.T, up *project.Project, tk *task.Task) {
	t.Helper()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{
			"-c", "user.name=test", "-c", "user.email=test@example.com",
		}, args...)...)
		cmd.Dir = up.Path
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	head := git("rev-parse", "refs/heads/"+up.DefaultBranch)
	tree := git("rev-parse", head+"^{tree}")
	tip := git("commit-tree", tree, "-p", head, "-m", "agent result")
	git("update-ref", "refs/heads/"+project.ResultBranchName(tk.SandboxRootID()), tip)
}

// A result the agent pushed counts as a change before the daemon integrates it: the merge from
// Thulu integrates it first, so the task must wait for that merge.
func TestPRMerge_PendingAgentResultHoldsTheTask(t *testing.T) {
	for name, withWorktree := range map[string]bool{"worktree": true, "no worktree": false} {
		t.Run(name, func(t *testing.T) {
			e := newMergeEnv(t)
			gh, up := e.project(t, "api"), e.uploaded(t, "folder")
			const url = "https://github.com/acme/api/pull/10"
			tk := e.task(t, mtBoard, mtReview, map[string]string{gh.ID: url})
			ids := []string{gh.ID, up.ID}
			if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{ProjectIDs: &ids}); err != nil {
				t.Fatal(err)
			}
			if withWorktree {
				if _, _, err := e.srv.projectStore.CreateTaskWorktree(t.Context(), up.ID, tk.ID); err != nil {
					t.Fatal(err)
				}
			}
			pushResult(t, up, e.get(t, tk.ID))

			e.deliver(t, mergedPayload(100, url, sha40('a'), gh.DefaultBranch))
			if got := e.get(t, tk.ID); got.Stage != mtReview {
				t.Fatalf("after the PR merged: stage %s, want %s until the pushed result merges", got.Stage, mtReview)
			}
		})
	}
}
