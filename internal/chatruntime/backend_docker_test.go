package chatruntime

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestDockerChatMountsWorktreeGitReadOnly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, "projects")
	wt := testgit.LinkedWorktree(t, testgit.BareRepo(t), projects, "webshop", "task-1")
	gitDir := filepath.Join(projects, "webshop", ".git")

	var got types.LaunchParams
	b := &dockerBackend{
		dataDir:    root,
		dataVolume: "octopus-data",
		launch: func(_ context.Context, _ types.AgentType, p types.LaunchParams) (string, error) {
			got = p
			return "c1", nil
		},
	}
	_, err = b.Create(t.Context(), CreateSpec{
		Chat:              &chat.Chat{ID: "chat1"},
		AgentType:         &types.AgentType{Name: "a"},
		Env:               map[string]string{engine.LLMSourceEnv: "profile"},
		Code:              map[string]types.CodeView{"webshop": {Path: wt}},
		WorkspaceReadOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.WorkspacePaths["webshop"] != "projects/.worktrees/webshop--task-1" {
		t.Errorf("worktree subpath = %q", got.WorkspacePaths["webshop"])
	}
	var found bool
	for _, em := range got.ExtraMounts {
		if em.Target == gitDir {
			found = true
			if em.Source != "projects/webshop/.git" || !em.ReadOnly {
				t.Errorf("git mount = %+v", em)
			}
		}
	}
	if !found {
		t.Fatalf("no git mount at %s in %+v", gitDir, got.ExtraMounts)
	}
	if got.Env["GIT_CONFIG_VALUE_0"] != "/workspace/projects/webshop" {
		t.Errorf("safe.directory env = %v", got.Env)
	}
}

type listOnlyDocker struct {
	dockerAPI
	list []dockertypes.Container
}

func (d listOnlyDocker) ContainerList(context.Context, container.ListOptions) ([]dockertypes.Container, error) {
	return d.list, nil
}

func TestDockerListManagedLeavesAnotherInstallsChatsOut(t *testing.T) {
	dockerx.SetInstallID("mine")
	t.Cleanup(func() { dockerx.SetInstallID("") })

	b := &dockerBackend{docker: listOnlyDocker{list: []dockertypes.Container{
		{ID: "aaaaaaaaaaaaaaaa", Labels: map[string]string{dockerx.InstallLabel: "mine"}},
		{ID: "bbbbbbbbbbbbbbbb", Labels: map[string]string{dockerx.InstallLabel: "theirs"}},
		{ID: "cccccccccccccccc", Labels: map[string]string{}},
	}}}
	got, err := b.ListManaged(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"aaaaaaaaaaaa", "cccccccccccc"}; !slices.Equal(got, want) {
		t.Fatalf("ListManaged = %v, want %v", got, want)
	}
}
