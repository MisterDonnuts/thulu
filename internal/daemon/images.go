package daemon

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

// imageCheckEvery re-reads the Docker host: an image pruned by hand, or one built after a warning,
// shows on Infra within this long.
const imageCheckEvery = 2 * time.Minute

type imageInspector interface {
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
}

// configuredImage is an image the Docker install will start without being asked: the sandbox's and
// the agent images a stored agent without its own image runs on. Fix is the command that puts it on
// the Docker host from a Thulu checkout.
type configuredImage struct {
	Ref string
	Env string
	Fix string
}

// configuredImages lists the images the launchers resolve from the environment, by the names they
// would ask Docker for. One that names a registry is pulled on first use, so only a local name can
// be missing for good; OCTOPUS_BUILD_MISSING_IMAGES never builds these (it builds agent images that
// declare a build context), so a missing one stays missing until someone builds it.
func configuredImages(getenv func(string) string) []configuredImage {
	registry := strings.TrimRight(strings.TrimSpace(getenv(imageref.RegistryEnv)), "/")
	var out []configuredImage
	add := func(ref, env, fix string) {
		ref = imageref.WithRegistry(registry, ref)
		if ref == "" || imageref.HasRegistry(ref) || slices.ContainsFunc(out, func(c configuredImage) bool { return c.Ref == ref }) {
			return
		}
		out = append(out, configuredImage{Ref: ref, Env: env, Fix: strings.ReplaceAll(fix, "{ref}", ref)})
	}
	add(sandbox.RuntimeImageFromEnv(getenv), sandbox.EnvSandboxImage,
		"docker build -t {ref} images/runtime/") //i18n:ignore shell command
	imgs := agentimage.FromEnv(getenv)
	if imgs.Override != "" {
		add(imgs.Override, agentimage.EnvOverride,
			"make agent-images && docker tag octopus-docker-agent:latest {ref}") //i18n:ignore shell command
	}
	for _, eng := range agentimage.Engines {
		if img := imgs.ByEngine[eng]; img != "" && imgs.Override == "" {
			add(img, agentimage.Env(eng),
				"docker build -t octopus-docker images/docker/ && docker build -f images/kube-agent/Dockerfile "+ //i18n:ignore shell command
					"--build-arg BASE=octopus-docker --build-arg ENGINES="+eng+" -t {ref} .") //i18n:ignore shell command
		}
	}
	return out
}

// imageCheck tells, before any run does, that an image the install is configured to start is not on
// the Docker host. It never blocks startup: the daemon logs each missing image once and Infra lists
// it with the command that fixes it.
type imageCheck struct {
	docker imageInspector
	images []configuredImage

	mu      sync.Mutex
	missing []configuredImage
}

func newImageCheck(docker imageInspector, getenv func(string) string) *imageCheck {
	return &imageCheck{docker: docker, images: configuredImages(getenv)}
}

func (c *imageCheck) run(ctx context.Context) {
	if len(c.images) == 0 {
		return
	}
	t := time.NewTicker(imageCheckEvery)
	defer t.Stop()
	for {
		c.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// check reads every configured image once. An image Docker could not be asked about keeps its last
// answer: an unreachable Docker host is Infra's own problem, not a missing image.
func (c *imageCheck) check(ctx context.Context) {
	c.mu.Lock()
	prev := slices.Clone(c.missing)
	c.mu.Unlock()
	var missing []configuredImage
	for _, img := range c.images {
		ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, err := c.docker.ImageInspectWithRaw(ictx, img.Ref)
		cancel()
		wasMissing := slices.Contains(prev, img)
		switch {
		case err == nil:
			if wasMissing {
				log.Printf("[daemon] %s", i18n.Log("daemon.log.image_found", "image", img.Ref))
			}
		case errdefs.IsNotFound(err):
			if !wasMissing {
				log.Printf("[daemon] %s", i18n.Log("daemon.log.image_missing", "image", img.Ref, "env", img.Env, "fix", img.Fix))
			}
			missing = append(missing, img)
		case wasMissing:
			missing = append(missing, img)
		}
	}
	c.mu.Lock()
	c.missing = missing
	c.mu.Unlock()
}

// Problems is what Infra lists for the missing images; fresh on every call.
func (c *imageCheck) Problems() []infra.Problem {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]infra.Problem, 0, len(c.missing))
	for _, img := range c.missing {
		out = append(out, infra.Problem{Source: "images",
			Err: apperr.Precondition("daemon.problems.image_missing", "image", img.Ref, "env", img.Env, "fix", img.Fix)})
	}
	return out
}
