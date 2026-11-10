package daemon

import (
	"context"
	"log"
	"os"
	"strings"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/mount"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const dataVolumeEnv = "OCTOPUS_DATA_VOLUME"

type containerInspector interface {
	ContainerInspect(ctx context.Context, containerID string) (dtypes.ContainerJSON, error)
}

// A daemon in a container hands agents subpaths of the volume behind its data dir; its own paths mean
// nothing to the Docker host, and every agent fails with "bind source path does not exist". Compose
// prefixes the volume with the project name, so the daemon reads the real name from its own container.
// The rest of the daemon reads the env var.
func adoptDataVolume(ctx context.Context, docker containerInspector, dataDir string) {
	if strings.TrimSpace(os.Getenv(dataVolumeEnv)) != "" {
		return
	}
	hostname, err := os.Hostname()
	if err != nil {
		return
	}
	name, mounted := dataVolumeOf(ctx, docker, hostname, dataDir)
	switch {
	case name != "":
		if err := os.Setenv(dataVolumeEnv, name); err == nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.data_volume_detected", "volume", name, "path", dataDir))
		}
	case mounted:
		log.Printf("[daemon] %s", i18n.Log("daemon.log.data_dir_not_a_volume", "path", dataDir))
	}
}

// mounted says the daemon is in a container with dataDir mounted but not as a named volume. An error
// from inspect means no such container: a daemon on the host, whose paths agents can bind.
func dataVolumeOf(ctx context.Context, docker containerInspector, hostname, dataDir string) (name string, mounted bool) {
	info, err := docker.ContainerInspect(ctx, hostname)
	if err != nil || info.Config == nil || info.Config.Hostname != hostname {
		return "", false
	}
	for i := range info.Mounts {
		m := &info.Mounts[i]
		if m.Destination != dataDir {
			continue
		}
		if m.Type == mount.TypeVolume && m.Name != "" {
			return m.Name, true
		}
		return "", true
	}
	return "", false
}
