// Thulu must never read, copy, log or export a personal login volume: the engine's terms allow
// only the CLI's own login flow inside its container.
package personallogin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

// dockergc keeps volumes with these labels on every sweep and removes them only on uninstall.
const (
	VolumeLabel = "octopus.personal-login"
	UserLabel   = "octopus.personal-login.user"
)

const volumePrefix = "octopus-login-"

var nameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// The install id prefix keeps two installs on one Docker engine from sharing a person's volume; the
// hash suffix keeps cleaned ids from colliding.
func VolumeName(engineName, userID string) string {
	clean := nameUnsafe.ReplaceAllString(userID, "-")
	if clean != userID || clean == "" {
		sum := sha256.Sum256([]byte(userID))
		clean = strings.Trim(clean, "-") + "-" + hex.EncodeToString(sum[:4])
	}
	name := volumePrefix
	if id := nameUnsafe.ReplaceAllString(dockerx.InstallID(), "-"); id != "" {
		name += id[:min(8, len(id))] + "-"
	}
	return name + nameUnsafe.ReplaceAllString(engineName, "-") + "-" + clean
}

func volumeLabels(engineName, userID string) map[string]string {
	l := map[string]string{VolumeLabel: engineName, UserLabel: userID}
	dockerx.StampInstall(l)
	return l
}

func ownVolume(v volume.Volume, engineName, userID string) bool {
	return v.Labels[VolumeLabel] == engineName && v.Labels[UserLabel] == userID && !dockerx.ForeignInstall(v.Labels)
}

type volumeAPI interface {
	VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error)
}

func lookupVolume(ctx context.Context, api volumeAPI, engineName, userID string) (string, bool, error) {
	name := VolumeName(engineName, userID)
	v, err := api.VolumeInspect(ctx, name)
	if errdefs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !ownVolume(v, engineName, userID) {
		return "", false, nil
	}
	return name, true, nil
}
