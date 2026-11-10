package updater

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

func nextKept(prev []KeptVersion, leaving, target KeptVersion, keep int) (list, dropped []KeptVersion) {
	byVersion := map[string]KeptVersion{}
	for _, k := range prev {
		byVersion[k.Version] = k
	}
	if _, ok := semver.Parse(leaving.Version); ok && leaving.Version != target.Version {
		k := byVersion[leaving.Version]
		k.Version = leaving.Version
		if len(k.Images) == 0 {
			k.Images = leaving.Images
		}
		if leaving.Snapshot != "" {
			k.Snapshot, k.SnapshotMethod = leaving.Snapshot, leaving.SnapshotMethod
		}
		byVersion[k.Version] = k
	}
	t := byVersion[target.Version]
	t.Version, t.Images = target.Version, target.Images
	byVersion[target.Version] = t

	var others []KeptVersion
	for v, k := range byVersion {
		if v != target.Version {
			others = append(others, k)
		}
	}
	sort.Slice(others, func(i, j int) bool { return semver.Newer(others[i].Version, others[j].Version) })

	list = []KeptVersion{t}
	room := keep - 1
	for _, k := range others {
		switch {
		case k.Pinned:
			list = append(list, k)
		case room > 0:
			list = append(list, k)
			room--
		default:
			dropped = append(dropped, k)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return semver.Newer(list[i].Version, list[j].Version) })
	return list, dropped
}

func keptEnv(list []KeptVersion) map[string]string {
	vs := make([]kept.Version, 0, len(list))
	seen := map[string]bool{}
	var images []string
	for _, k := range list {
		vs = append(vs, kept.Version{Version: k.Version, Snapshot: k.Snapshot})
		for _, img := range k.Images {
			if !seen[img] {
				seen[img] = true
				images = append(images, img)
			}
		}
	}
	versions, snapshots := kept.Encode(vs)
	return map[string]string{
		envKeptVersions:  versions,
		envKeptSnapshots: snapshots,
		envKeptImages:    strings.Join(images, ","),
	}
}

func snapshotName(path string) string {
	name := filepath.Base(path)
	if !kept.ValidSnapshot(name) {
		return ""
	}
	return name
}

func (u *updateRun) snapshotPath(arg string) (string, error) {
	p := arg
	if !strings.ContainsRune(arg, os.PathSeparator) {
		p = filepath.Join(u.in.Root, "backups", arg)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.snapshot", "name", arg)
	}
	if !info.Mode().IsRegular() {
		return "", apperr.Invalid("updater.errors.snapshot_not_file", "name", arg)
	}
	return p, nil
}

// Only the snapshot is removed here; the janitor removes the images once they are absent from
// OCTOPUS_KEPT_IMAGES.
func (u *updateRun) dropKept(dropped []KeptVersion) {
	for _, k := range dropped {
		if name := snapshotName(k.Snapshot); name != "" {
			p := filepath.Join(u.in.Root, "backups", name)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				u.say("updater.cli.snapshot_remove_failed", "version", k.Version, "err", apperr.Text(err))
				continue
			}
		}
		u.say("updater.cli.no_longer_kept", "version", k.Version)
	}
}

func keptSnapshots(root string, list []KeptVersion) []string {
	var out []string
	for _, k := range list {
		if name := snapshotName(k.Snapshot); name != "" {
			out = append(out, filepath.Join(root, "backups", name))
		}
	}
	return out
}

func (u *updateRun) listKept() error {
	state, err := LoadState(u.in.Root)
	if err != nil {
		return err
	}
	if state == nil || len(state.Kept) == 0 {
		u.say("updater.cli.nothing_kept_yet")
		return nil
	}
	current := u.installedVersion(state)
	u.say("updater.cli.kept_header", "env", kept.EnvKeep, "keep", kept.Keep(u.getenv))
	for _, k := range state.Kept {
		mark := " "
		if k.Version == current {
			mark = "*"
		}
		extra := ""
		if k.Pinned {
			extra = i18n.Text("updater.labels.pinned")
		}
		cmd := kept.Command(kept.Version{Version: k.Version, Snapshot: k.Snapshot}, current,
			state.To.Version == current && state.RollbackTo == k.Version)
		if cmd == "" {
			cmd = i18n.Text("updater.labels.running")
		}
		snap := k.Snapshot
		if snap == "" {
			snap = "-"
		}
		u.say("updater.cli.kept_row", "mark", mark, "version", k.Version, "snapshot", snap, "command", cmd, "extra", extra)
	}
	return nil
}

func (u *updateRun) pin(keepVersion, unkeepVersion string) error {
	state, err := LoadState(u.in.Root)
	if err != nil {
		return err
	}
	if state == nil {
		return apperr.Precondition("updater.errors.nothing_kept")
	}
	v, on := keepVersion, true
	if v == "" {
		v, on = unkeepVersion, false
	}
	for i := range state.Kept {
		if state.Kept[i].Version == v {
			state.Kept[i].Pinned = on
			if err := SaveState(u.in.Root, state); err != nil {
				return err
			}
			if on {
				u.say("updater.cli.pinned", "version", v)
			} else {
				u.say("updater.cli.unpinned", "version", v, "env", kept.EnvKeep, "keep", kept.Keep(u.getenv))
			}
			return nil
		}
	}
	return apperr.NotFound("updater.errors.not_kept", "version", v)
}

func (u *updateRun) previousVersion() (string, error) {
	state, err := LoadState(u.in.Root)
	if err != nil {
		return "", err
	}
	if state == nil {
		return "", apperr.NotFound("updater.errors.no_update_on_record")
	}
	current := u.installedVersion(state)
	if state.Result == ResultOK && state.To.Version == current && state.From.Version != "" {
		return state.From.Version, nil
	}
	for _, k := range state.Kept {
		if semver.Less(k.Version, current) {
			return k.Version, nil
		}
	}
	return "", apperr.NotFound("updater.errors.nothing_older_kept", "current", current)
}
