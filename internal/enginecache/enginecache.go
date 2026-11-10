// Package enginecache holds the Docker volumes where agent containers keep the engines their image
// installs on first use. Published agent images carry no copy of Claude Code; the shim
// images/snippets/claude-first-run.sh installs it into OCTOPUS_ENGINE_DIR, and in Docker mode that
// directory is one of these volumes so the install happens once per scope, not once per container.
//
// One volume per workspace (and one per person for personal-login containers): every agent of a
// scope runs the binary in it, so a scope that can write it must never be able to reach another
// scope's runs.
package enginecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

const (
	// VolumeLabel holds the scope; dockergc keeps these volumes on every sweep and removes them
	// only on uninstall.
	VolumeLabel = "octopus.engine-cache"
	// MountPath is where agent containers see the volume; EnvDir points the shim at it.
	MountPath = "/opt/thulu-engines"
	EnvDir    = "OCTOPUS_ENGINE_DIR"

	volumePrefix = "octopus-engines-"
)

var nameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// WorkspaceScope and UserScope name the two kinds of scope.
func WorkspaceScope(workspaceID string) string { return "ws-" + workspaceID }
func UserScope(userID string) string           { return "user-" + userID }

// VolumeName is the scope's volume on this install. The install id prefix keeps two installs on one
// Docker engine apart; the hash suffix keeps cleaned scopes from colliding.
func VolumeName(scope string) string {
	clean := nameUnsafe.ReplaceAllString(scope, "-")
	if clean != scope {
		sum := sha256.Sum256([]byte(scope))
		clean = strings.Trim(clean, "-") + "-" + hex.EncodeToString(sum[:4])
	}
	name := volumePrefix
	if id := nameUnsafe.ReplaceAllString(dockerx.InstallID(), "-"); id != "" {
		name += id[:min(8, len(id))] + "-"
	}
	return name + clean
}

type API interface {
	VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error)
	VolumeCreate(ctx context.Context, options volume.CreateOptions) (volume.Volume, error)
}

// Ensure returns the scope's volume, creating it when missing. Docker answers a create for an
// existing name with that volume: one this install did not label for the scope is refused, never
// adopted, because its contents would run in every agent of the scope.
func Ensure(ctx context.Context, api API, scope string) (string, error) {
	if strings.TrimPrefix(strings.TrimPrefix(scope, "ws-"), "user-") == "" {
		return "", apperr.Precondition("enginecache.errors.no_scope")
	}
	name := VolumeName(scope)
	v, err := api.VolumeInspect(ctx, name)
	if err != nil && !errdefs.IsNotFound(err) {
		return "", apperr.Wrap(err, "enginecache.errors.volume")
	}
	if err != nil {
		labels := map[string]string{VolumeLabel: scope}
		dockerx.StampInstall(labels)
		if v, err = api.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: labels}); err != nil {
			return "", apperr.Wrap(err, "enginecache.errors.volume")
		}
	}
	if v.Labels[VolumeLabel] != scope || dockerx.ForeignInstall(v.Labels) {
		return "", apperr.Conflict("enginecache.errors.volume_taken", "volume", name)
	}
	return name, nil
}
