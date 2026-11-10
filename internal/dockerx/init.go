package dockerx

import (
	"context"
	"path"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// Thulu images start under tini; adding Docker's init (HostConfig.Init) on top nests two inits,
// so Docker's is requested only when the container would otherwise have none as PID 1.

var initPrograms = map[string]bool{
	"tini": true, "tini-static": true, "docker-init": true, "dumb-init": true, "catatonit": true,
}

func StartsWithInit(entrypoint []string) bool {
	return len(entrypoint) > 0 && initPrograms[path.Base(entrypoint[0])]
}

type ImageInspector interface {
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
}

// An unreadable image gets Docker's init: a nested init only logs a warning, a missing one leaks
// zombies.
func NeedsInit(ctx context.Context, cli ImageInspector, image string, entrypoint []string) bool {
	if len(entrypoint) == 0 {
		insp, _, err := cli.ImageInspectWithRaw(ctx, image)
		if err != nil || insp.Config == nil {
			return true
		}
		entrypoint = insp.Config.Entrypoint
	}
	return !StartsWithInit(entrypoint)
}

func ApplyInit(ctx context.Context, cli ImageInspector, image string, entrypoint []string, hc *container.HostConfig) {
	if NeedsInit(ctx, cli, image, entrypoint) {
		on := true
		hc.Init = &on
	}
}
