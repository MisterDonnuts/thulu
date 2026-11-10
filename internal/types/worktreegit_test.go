package types

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/mount"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func dataRootWithWorktrees(t *testing.T) (root string, wts map[string]string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, "projects")
	src := testgit.BareRepo(t)
	gamma := filepath.Join(projects, "gamma")
	testgit.Clone(t, src, gamma)
	return root, map[string]string{
		"alpha":  testgit.LinkedWorktree(t, src, projects, "alpha", "task-1"),
		"alpha2": testgit.LinkedWorktree(t, src, projects, "alpha", "task-2"),
		"beta":   testgit.LinkedWorktree(t, src, projects, "beta", "task-1"),
		"gamma":  gamma,
	}
}

func TestWorktreeGitMounts(t *testing.T) {
	root, wts := dataRootWithWorktrees(t)
	alphaGit := filepath.Join(root, "projects", "alpha", ".git")
	betaGit := filepath.Join(root, "projects", "beta", ".git")
	otherRoot := t.TempDir()

	cases := []struct {
		name       string
		worktrees  map[string]string
		copyMode   bool
		dataRoot   string
		wantMounts []ExtraMount
		wantNames  []string
	}{
		{
			name:      "host mode binds the git dir at its own path",
			worktrees: map[string]string{"alpha": wts["alpha"]},
			wantMounts: []ExtraMount{
				{Source: alphaGit, Target: alphaGit, ReadOnly: true},
			},
			wantNames: []string{"alpha"},
		},
		{
			name:      "copy mode mounts a data-volume subpath",
			worktrees: map[string]string{"alpha": wts["alpha"]},
			copyMode:  true,
			dataRoot:  root,
			wantMounts: []ExtraMount{
				{Source: "projects/alpha/.git", Target: alphaGit, ReadOnly: true},
			},
			wantNames: []string{"alpha"},
		},
		{
			name:      "one mount per clone, every worktree named",
			worktrees: map[string]string{"alpha": wts["alpha"], "alpha2": wts["alpha2"], "beta": wts["beta"]},
			copyMode:  true,
			dataRoot:  root,
			wantMounts: []ExtraMount{
				{Source: "projects/alpha/.git", Target: alphaGit, ReadOnly: true},
				{Source: "projects/beta/.git", Target: betaGit, ReadOnly: true},
			},
			wantNames: []string{"alpha", "alpha2", "beta"},
		},
		{
			name:      "a plain clone is named without a mount",
			worktrees: map[string]string{"gamma": wts["gamma"]},
			wantNames: []string{"gamma"},
		},
		{
			name:      "an empty path or a dir with no git is skipped",
			worktrees: map[string]string{"none": "", "bare": t.TempDir()},
		},
		{
			name:      "copy mode skips a git dir outside the data root",
			worktrees: map[string]string{"alpha": wts["alpha"]},
			copyMode:  true,
			dataRoot:  otherRoot,
		},
		{
			name: "no worktrees",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mounts, names := WorktreeGitMounts("test", tc.worktrees, tc.copyMode, tc.dataRoot)
			if !reflect.DeepEqual(mounts, tc.wantMounts) {
				t.Errorf("mounts = %+v, want %+v", mounts, tc.wantMounts)
			}
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Errorf("names = %v, want %v", names, tc.wantNames)
			}
		})
	}
}

func TestWorktreeGitMountsSkipsATamperedPointer(t *testing.T) {
	root, wts := dataRootWithWorktrees(t)
	beta := filepath.Join(root, "projects", "beta", ".git", "worktrees", filepath.Base(wts["beta"]))
	if err := os.WriteFile(filepath.Join(wts["alpha"], ".git"), []byte("gitdir: "+beta+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts, names := WorktreeGitMounts("test", map[string]string{"alpha": wts["alpha"]}, true, root)
	if len(mounts) != 0 || len(names) != 0 {
		t.Fatalf("tampered pointer mounted %+v (%v)", mounts, names)
	}
}

func TestWorktreeGitEnv(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	cases := []struct {
		name   string
		lookup func(string) (string, bool)
		dirs   []string
		want   map[string]string
	}{
		{name: "no dirs", lookup: none},
		{
			name:   "fresh",
			lookup: none,
			dirs:   []string{"/workspace/projects/a", "/workspace/projects/b"},
			want: map[string]string{
				"GIT_CONFIG_COUNT":   "2",
				"GIT_CONFIG_KEY_0":   "safe.directory",
				"GIT_CONFIG_VALUE_0": "/workspace/projects/a",
				"GIT_CONFIG_KEY_1":   "safe.directory",
				"GIT_CONFIG_VALUE_1": "/workspace/projects/b",
			},
		},
		{
			name: "appends after the launch's own entries",
			lookup: func(k string) (string, bool) {
				if k == "GIT_CONFIG_COUNT" {
					return "1", true
				}
				return "", false
			},
			dirs: []string{"/workspace/projects/a"},
			want: map[string]string{
				"GIT_CONFIG_COUNT":   "2",
				"GIT_CONFIG_KEY_1":   "safe.directory",
				"GIT_CONFIG_VALUE_1": "/workspace/projects/a",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WorktreeGitEnv(tc.lookup, tc.dirs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("env = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAddWorktreeGitReachesTheContainer(t *testing.T) {
	root, wts := dataRootWithWorktrees(t)
	alphaGit := filepath.Join(root, "projects", "alpha", ".git")
	worktrees := map[string]string{"alpha": wts["alpha"]}

	t.Run("copy mode", func(t *testing.T) {
		p := LaunchParams{DataVolume: "octopus-data", WorkspacePaths: VolumeSubpaths("test", root, worktrees)}
		p.AddWorktreeGit("test", worktrees, root)
		var git *mount.Mount
		for _, m := range buildVolumeMounts(p) {
			if m.Target == alphaGit {
				git = &m
			}
		}
		if git == nil {
			t.Fatalf("no mount at %s", alphaGit)
		}
		if git.Type != mount.TypeVolume || git.Source != "octopus-data" || !git.ReadOnly ||
			git.VolumeOptions == nil || git.VolumeOptions.Subpath != "projects/alpha/.git" {
			t.Errorf("git mount = %+v / %+v", git, git.VolumeOptions)
		}
		if p.Env["GIT_CONFIG_KEY_0"] != "safe.directory" || p.Env["GIT_CONFIG_VALUE_0"] != "/workspace/projects/alpha" {
			t.Errorf("env = %v", p.Env)
		}
	})

	t.Run("host mode", func(t *testing.T) {
		p := LaunchParams{Env: map[string]string{"STAGE": "dev"}, WorkspacePaths: worktrees}
		p.AddWorktreeGit("test", worktrees, "")
		mounts, err := buildLaunchMounts(&ComposeServiceSpec{}, AgentType{}, p)
		if err != nil {
			t.Fatal(err)
		}
		var git *mount.Mount
		for _, m := range mounts {
			if m.Target == alphaGit {
				git = &m
			}
		}
		if git == nil || git.Type != mount.TypeBind || git.Source != alphaGit || !git.ReadOnly {
			t.Fatalf("git mount = %+v in %+v", git, mounts)
		}
		if p.Env["STAGE"] != "dev" || p.Env["GIT_CONFIG_COUNT"] != "1" {
			t.Errorf("env = %v", p.Env)
		}
	})

	t.Run("a plain clone is marked safe without a mount", func(t *testing.T) {
		p := LaunchParams{}
		p.AddWorktreeGit("test", map[string]string{"gamma": wts["gamma"]}, "")
		if p.ExtraMounts != nil || p.Env["GIT_CONFIG_VALUE_0"] != "/workspace/projects/gamma" {
			t.Errorf("params = %+v", p)
		}
	})

	t.Run("no git leaves params alone", func(t *testing.T) {
		p := LaunchParams{}
		p.AddWorktreeGit("test", map[string]string{"none": t.TempDir()}, "")
		if p.Env != nil || p.ExtraMounts != nil {
			t.Errorf("params changed: %+v", p)
		}
	})
}

func TestAddWorktreeGitMountsThePointerReadOnly(t *testing.T) {
	root, wts := dataRootWithWorktrees(t)
	pointerOf := func(p LaunchParams, name string) *ExtraMount {
		for i := range p.ExtraMounts {
			if p.ExtraMounts[i].Target == "/workspace/projects/"+name+"/.git" {
				return &p.ExtraMounts[i]
			}
		}
		return nil
	}

	t.Run("copy mode names it inside the volume", func(t *testing.T) {
		worktrees := map[string]string{"alpha": wts["alpha"]}
		p := LaunchParams{DataVolume: "octopus-data"}
		p.AddWorktreeGit("test", worktrees, root)
		got := pointerOf(p, "alpha")
		if got == nil || !got.ReadOnly {
			t.Fatalf("pointer mount = %+v", got)
		}
		rel, err := filepath.Rel(root, filepath.Join(wts["alpha"], ".git"))
		if err != nil || got.Source != rel {
			t.Fatalf("source = %q, want %q (%v)", got.Source, rel, err)
		}
	})

	t.Run("host mode binds the file", func(t *testing.T) {
		worktrees := map[string]string{"alpha": wts["alpha"]}
		var p LaunchParams
		p.AddWorktreeGit("test", worktrees, "")
		got := pointerOf(p, "alpha")
		if got == nil || !got.ReadOnly || got.Source != filepath.Join(wts["alpha"], ".git") {
			t.Fatalf("pointer mount = %+v", got)
		}
	})

	t.Run("a plain clone has no pointer", func(t *testing.T) {
		worktrees := map[string]string{"gamma": wts["gamma"]}
		var p LaunchParams
		p.AddWorktreeGit("test", worktrees, "")
		if got := pointerOf(p, "gamma"); got != nil {
			t.Fatalf("pointer mount on a clone = %+v", got)
		}
	})
}
