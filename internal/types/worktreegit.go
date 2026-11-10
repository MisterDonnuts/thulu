package types

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/mount"

	"github.com/MisterDonnuts/thulu/internal/gitworktree"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The clone's .git is mounted ALWAYS read-only: the daemon runs git on it trusting .git/config (see
// project/gitsafe.go),
// so a writable mount lets a run plant a filter driver or remote URL the daemon's next git add/push
// executes.

func WorktreeGitMounts(what string, worktrees map[string]string, copyMode bool, dataRoot string) ([]ExtraMount, []string) {
	if len(worktrees) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(worktrees))
	for name := range worktrees {
		names = append(names, name)
	}
	sort.Strings(names)

	var (
		mounts []ExtraMount
		ok     []string
		seen   = map[string]bool{}
	)
	for _, name := range names {
		wt := worktrees[name]
		if wt == "" {
			continue
		}
		common, err := gitworktree.CommonDir(wt)
		if errors.Is(err, gitworktree.ErrNotLinkedWorktree) {
			if isPlainClone(wt) {
				ok = append(ok, name)
			}
			continue
		}
		if err != nil {
			log.Printf("[%s] %s", what, i18n.Log("types.log.git_metadata_not_mounted", "name", name, "path", wt, "err", err))
			continue
		}
		source := common
		if copyMode {
			rel, relErr := filepath.Rel(dataRoot, common)
			if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
				log.Printf("[%s] %s", what, i18n.Log("types.log.git_dir_outside_data_root", "name", name, "path", common, "root", dataRoot))
				continue
			}
			source = rel
		}
		ok = append(ok, name)
		if seen[common] {
			continue
		}
		seen[common] = true
		mounts = append(mounts, ExtraMount{Source: source, Target: common, ReadOnly: true})
	}
	return mounts, ok
}

func isPlainClone(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && fi.IsDir()
}

// git ignores safe.directory in repo config, so it goes in GIT_CONFIG_* env (files are uid 10001,
// container git runs as root/node).
// Entries are appended after the launch's own GIT_CONFIG_<n>.
func WorktreeGitEnv(lookup func(string) (string, bool), dirs []string) map[string]string {
	if len(dirs) == 0 {
		return nil
	}
	base := 0
	if v, ok := lookup("GIT_CONFIG_COUNT"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			base = n
		}
	}
	env := make(map[string]string, 2*len(dirs)+1)
	for i, dir := range dirs {
		idx := strconv.Itoa(base + i)
		env["GIT_CONFIG_KEY_"+idx] = "safe.directory"
		env["GIT_CONFIG_VALUE_"+idx] = dir
	}
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(base + len(dirs))
	return env
}

func ProjectMountTarget(name string) string { return "/workspace/projects/" + name }

func ProjectMountTargets(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, ProjectMountTarget(n))
	}
	return out
}

// worktrees must be daemon-side absolute paths, taken before any VolumeSubpaths rewrite.
func (p *LaunchParams) AddWorktreeGit(what string, worktrees map[string]string, dataRoot string) {
	mounts, names := WorktreeGitMounts(what, worktrees, p.DataVolume != "", dataRoot)
	if len(mounts) == 0 && len(names) == 0 {
		return
	}
	p.ExtraMounts = append(p.ExtraMounts, mounts...)
	p.ExtraMounts = append(p.ExtraMounts, worktreePointerMounts(worktrees, names, p.DataVolume != "", dataRoot)...)
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	lookup := func(k string) (string, bool) { v, ok := p.Env[k]; return v, ok }
	for k, v := range WorktreeGitEnv(lookup, ProjectMountTargets(names)) {
		p.Env[k] = v
	}
}

// With the clone's .git read-only, a run told to commit has copied it and rewritten the worktree's
// .git file to point at the copy: its commit went nowhere, and git broke for the auditor and every
// later run on the worktree.
func worktreePointerMounts(worktrees map[string]string, names []string, copyMode bool, dataRoot string) []ExtraMount {
	var mounts []ExtraMount
	for _, name := range names {
		pointer := filepath.Join(worktrees[name], ".git")
		fi, err := os.Lstat(pointer)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		source := pointer
		if copyMode {
			rel, relErr := filepath.Rel(dataRoot, pointer)
			if relErr != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			source = rel
		}
		mounts = append(mounts, ExtraMount{Source: source, Target: ProjectMountTarget(name) + "/.git", ReadOnly: true})
	}
	return mounts
}

func (em ExtraMount) DockerMount(dataVolume string) mount.Mount {
	if dataVolume != "" {
		return mount.Mount{
			Type:          mount.TypeVolume,
			Source:        dataVolume,
			Target:        em.Target,
			ReadOnly:      em.ReadOnly,
			VolumeOptions: &mount.VolumeOptions{Subpath: em.Source},
		}
	}
	return mount.Mount{Type: mount.TypeBind, Source: em.Source, Target: em.Target, ReadOnly: em.ReadOnly}
}
