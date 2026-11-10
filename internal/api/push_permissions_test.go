package api

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type scriptedGit struct {
	remotes, log string
	calls        int
}

func (g *scriptedGit) Run(string, ...string) error { g.calls++; return nil }

func (g *scriptedGit) Output(_ string, args ...string) (string, error) {
	g.calls++
	switch args[0] {
	case "for-each-ref":
		return g.remotes, nil
	case "log":
		return g.log, nil
	}
	return "", errors.New("unexpected git " + args[0])
}

func TestPushPermissionsAddWorkflowsOnlyWhenNeededAndAllowed(t *testing.T) {
	wt := func(g *scriptedGit) project.WorktreeGit {
		return project.WorktreeGit{Exec: g, Dir: "/wt", Branch: "task-1", DefaultBranch: "main"}
	}
	withWorkflows := githubapp.PermsPush.With(githubapp.PermWorkflowsWrite)

	t.Setenv(githubapp.EnvAllowWorkflowWrites, "")
	off := &scriptedGit{remotes: "refs/remotes/origin/main\n", log: ".github/workflows/ci.yml\n"}
	if got := pushPermissions(wt(off)); !reflect.DeepEqual(got, githubapp.PermsPush) {
		t.Errorf("opted out: perms = %v, want PermsPush", got)
	}
	if off.calls != 0 {
		t.Error("opted out, yet the branch was inspected")
	}

	t.Setenv(githubapp.EnvAllowWorkflowWrites, "1")
	touching := &scriptedGit{remotes: "refs/remotes/origin/main\n", log: ".github/workflows/ci.yml\n"}
	if got := pushPermissions(wt(touching)); !reflect.DeepEqual(got, withWorkflows) {
		t.Errorf("workflow change: perms = %v, want %v", got, withWorkflows)
	}
	plain := &scriptedGit{remotes: "refs/remotes/origin/main\n"}
	if got := pushPermissions(wt(plain)); !reflect.DeepEqual(got, githubapp.PermsPush) {
		t.Errorf("no workflow change: perms = %v, want PermsPush", got)
	}
	if _, has := githubapp.PermsPush["workflows"]; has {
		t.Fatal("With modified the shared PermsPush set")
	}
}

func TestWorkflowPushHintNamesTheSetting(t *testing.T) {
	refusal := errors.New("git push: ! [remote rejected] task-1 -> task-1 (refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` without `workflows` permission)")

	t.Setenv(githubapp.EnvAllowWorkflowWrites, "")
	if got := workflowPushHint(refusal); !strings.Contains(got.Error(), githubapp.EnvAllowWorkflowWrites) || !errors.Is(got, refusal) {
		t.Errorf("hint = %v", got)
	}
	other := errors.New("git push: network unreachable")
	if got := workflowPushHint(other); got.Error() != other.Error() {
		t.Errorf("unrelated error rewritten: %v", got)
	}
}
