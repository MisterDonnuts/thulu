package imageref

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type DockerImages interface {
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

type MissingError struct {
	Ref string
}

func (e *MissingError) Error() string { return e.Unwrap().Error() }

func (e *MissingError) Unwrap() error {
	return apperr.New("imageref.errors.image_missing", "ref", e.Ref, "env", RegistryEnv)
}

// A bare name such as "octopus-docker-agent:latest" is never pulled: Docker would fetch it from Docker Hub,
// where anyone can publish under a name Thulu builds locally. Pulls are anonymous; pre-pull for
// authed registries.
func EnsureLocal(ctx context.Context, api DockerImages, ref string) error {
	_, _, err := api.ImageInspectWithRaw(ctx, ref)
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return apperr.Wrap(err, "imageref.errors.inspect_failed", "ref", ref)
	}
	if !HasRegistry(ref) {
		return &MissingError{Ref: ref}
	}
	log.Printf("[image] %s", i18n.Log("imageref.log.pulling", "ref", ref))
	rc, err := api.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return apperr.Wrap(err, "imageref.errors.pull_failed", "ref", ref)
	}
	defer rc.Close()
	if err := drainPull(rc); err != nil {
		return apperr.Wrap(err, "imageref.errors.pull_failed", "ref", ref)
	}
	return nil
}

// The pull only completes once the progress stream is consumed; errors arrive inside the stream.
func drainPull(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		err := dec.Decode(&msg)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return apperr.Wrap(err, "imageref.errors.read_pull_progress")
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}
