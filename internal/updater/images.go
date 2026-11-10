package updater

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

type ImageOps interface {
	Pull(ctx context.Context, ref string) error
	// The repo:version tag is what the janitor orders versions by.
	Tag(ctx context.Context, ref, tag string) error
}

// The docker CLI, so pulls use the operator's credentials and context.
type cliImages struct{ runner initwizard.Runner }

func (c cliImages) Pull(ctx context.Context, ref string) error {
	return c.runner.Run(ctx, "docker", "pull", "--quiet", ref)
}

func (c cliImages) Tag(ctx context.Context, ref, tag string) error {
	return c.runner.Run(ctx, "docker", "tag", ref, tag)
}

func (d *Deps) imageVerifier(m *release.Manifest) func(context.Context, string) error {
	if d.VerifyImage != nil {
		return d.VerifyImage
	}
	if m.Channel == release.ChannelPreview {
		return release.VerifyPreviewImage
	}
	keys := d.Keys
	return func(ctx context.Context, ref string) error { return keys.VerifyImage(ctx, ref) }
}

// Keeps only the last path component; `octopus mirror` copies to exactly these names.
func MirrorRef(registry, ref string) (string, error) {
	registry = strings.TrimRight(strings.TrimSpace(registry), "/")
	base, digest, ok := strings.Cut(ref, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") {
		return "", apperr.Invalid("updater.errors.not_digest_ref", "ref", ref)
	}
	if registry == "" {
		return ref, nil
	}
	return registry + "/" + repoName(base) + "@" + digest, nil
}

func repoName(base string) string {
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.Index(base, ":"); i >= 0 {
		base = base[:i]
	}
	return base
}

func repository(ref string) string {
	base, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		base = base[:i]
	}
	return base
}

type dockerImages struct {
	Env  map[string]string
	Ours map[string]string
	Pull []string
}

// Images the stack does not start are pinned but not pulled; their signatures are still checked so
// a later `compose up` with the profile on starts a verified image.
func planDockerImages(m *release.Manifest, registry string, env *initwizard.EnvFile, engines []string,
	composeFile string, externalDB bool,
) (*dockerImages, error) {
	out := &dockerImages{Env: map[string]string{}, Ours: map[string]string{}}
	pull := map[string]bool{}
	add := func(key, ref string, ours, wanted bool) error {
		if ref == "" {
			return nil
		}
		moved, err := MirrorRef(registry, ref)
		if err != nil {
			return err
		}
		out.Env[key] = moved
		if ours {
			out.Ours[key] = moved
		}
		if wanted {
			pull[moved] = true
		}
		return nil
	}
	if m.Images["octopus"] == "" {
		return nil, apperr.Invalid("updater.errors.no_daemon_image")
	}
	if err := add(envDaemonImage, m.Images["octopus"], true, true); err != nil {
		return nil, err
	}
	if err := addAgentImages(add, m, engines, registry != ""); err != nil {
		return nil, err
	}
	if err := add(envSandboxImage, m.Images["octopus-runtime"], true, true); err != nil {
		return nil, err
	}
	installed := map[string]bool{}
	for _, name := range initwizard.InstalledMCPServers(env) {
		installed[name] = true
	}
	for _, name := range dockergc.NativeMCPServers {
		if err := add(dockergc.MCPImageEnv(name), m.Images[dockergc.MCPImage(name)], true, installed[name]); err != nil {
			return nil, err
		}
	}
	if err := add(envPGImage, m.ThirdParty["postgres"], false, !externalDB); err != nil {
		return nil, err
	}
	caddy := strings.HasSuffix(composeFile, "deploy/docker-compose.yml") ||
		strings.HasSuffix(composeFile, "deploy/docker-compose.caddy.yml")
	if err := add(envCaddyImage, m.ThirdParty["caddy"], false, caddy); err != nil {
		return nil, err
	}
	for ref := range pull {
		out.Pull = append(out.Pull, ref)
	}
	sort.Strings(out.Pull)
	return out, nil
}

// The daemon pulls another engine's image on its first run, without credentials; behind a mirror
// (usually behind a login) every engine is pulled here with the host's.
func addAgentImages(add func(key, ref string, ours, wanted bool) error, m *release.Manifest, engines []string, mirrored bool) error {
	for _, e := range agentimage.Engines {
		wanted := mirrored || len(engines) == 0 || slices.Contains(engines, e)
		if err := add(agentimage.Env(e), m.Images[agentimage.Name(e)], true, wanted); err != nil {
			return err
		}
	}
	return nil
}

func verifyAll(ctx context.Context, verify func(context.Context, string) error, refs map[string]string) error {
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := verify(ctx, refs[k]); err != nil {
			if errors.Is(err, release.ErrNoCosign) {
				return err
			}
			return apperr.Wrap(err, "updater.errors.image_signature")
		}
	}
	return nil
}

func pullAndTag(ctx context.Context, ops ImageOps, imgs *dockerImages, version string) error {
	ours := map[string]bool{}
	for _, ref := range imgs.Ours {
		ours[ref] = true
	}
	for _, ref := range imgs.Pull {
		if err := ops.Pull(ctx, ref); err != nil {
			return apperr.Wrap(err, "updater.errors.pull", "ref", ref)
		}
		if ours[ref] {
			if err := ops.Tag(ctx, ref, repository(ref)+":"+version); err != nil {
				return apperr.Wrap(err, "updater.errors.tag", "ref", ref)
			}
		}
	}
	return nil
}
