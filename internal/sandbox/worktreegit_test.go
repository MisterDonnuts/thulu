package sandbox

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/mount"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestSandboxWorktreeGit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, "projects")
	src := testgit.BareRepo(t)
	wt := testgit.LinkedWorktree(t, src, projects, "webshop", "task-1")
	plain := filepath.Join(projects, "plain")
	testgit.Clone(t, src, plain)
	gitDir := filepath.Join(projects, "webshop", ".git")

	safeEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=/workspace/projects/webshop",
	}
	cases := []struct {
		name       string
		opts       CreateOpts
		wantMounts []mount.Mount
		wantEnv    []string
	}{
		{
			name: "copy mode",
			opts: CreateOpts{
				ProjectsPaths: map[string]string{"webshop": wt},
				DataVolume:    "octopus-data",
				DataRoot:      root,
			},
			wantMounts: []mount.Mount{{
				Type: mount.TypeVolume, Source: "octopus-data", Target: gitDir, ReadOnly: true,
				VolumeOptions: &mount.VolumeOptions{Subpath: "projects/webshop/.git"},
			}},
			wantEnv: safeEnv,
		},
		{
			name:       "host mode",
			opts:       CreateOpts{ProjectsPaths: map[string]string{"webshop": wt}},
			wantMounts: []mount.Mount{{Type: mount.TypeBind, Source: gitDir, Target: gitDir, ReadOnly: true}},
			wantEnv:    safeEnv,
		},
		{
			name: "keeps the caller's git config entries",
			opts: CreateOpts{
				ProjectsPaths: map[string]string{"webshop": wt},
				ExtraEnv:      []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.pager", "GIT_CONFIG_VALUE_0=cat"},
			},
			wantMounts: []mount.Mount{{Type: mount.TypeBind, Source: gitDir, Target: gitDir, ReadOnly: true}},
			wantEnv: []string{
				"GIT_CONFIG_COUNT=2",
				"GIT_CONFIG_KEY_1=safe.directory",
				"GIT_CONFIG_VALUE_1=/workspace/projects/webshop",
			},
		},
		{
			name:       "a plain clone is marked safe without a mount",
			opts:       CreateOpts{ProjectsPaths: map[string]string{"plain": plain}},
			wantMounts: []mount.Mount{},
			wantEnv: []string{
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=safe.directory",
				"GIT_CONFIG_VALUE_0=/workspace/projects/plain",
			},
		},
		{
			name:       "no git leaves the env alone",
			opts:       CreateOpts{ProjectsPaths: map[string]string{"empty": root}},
			wantMounts: []mount.Mount{},
			wantEnv:    []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mounts, env := sandboxWorktreeGit(tc.opts)
			if !reflect.DeepEqual(mounts, tc.wantMounts) {
				t.Errorf("mounts = %+v, want %+v", mounts, tc.wantMounts)
			}
			if !reflect.DeepEqual(env, tc.wantEnv) {
				t.Errorf("env = %v, want %v", env, tc.wantEnv)
			}
		})
	}
}
