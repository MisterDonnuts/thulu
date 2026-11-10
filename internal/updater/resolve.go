package updater

import (
	"context"
	"errors"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

func (u *updateRun) source() (release.Source, error) {
	if u.deps.Source != nil {
		return u.deps.Source, nil
	}
	s, err := release.SourceFromEnv(u.getenv, nil)
	if errors.Is(err, release.ErrSourceOff) {
		return nil, apperr.Wrap(err, "updater.errors.source_off_hint", "env", release.EnvSource)
	}
	if err != nil {
		return nil, err
	}
	u.deps.Source = s
	return s, nil
}

// Errors are swallowed: a Dir mirror may list nothing while its manifest still verifies.
func (u *updateRun) listing(ctx context.Context, version string) release.Listing {
	src, err := u.source()
	if err != nil {
		return release.Listing{Version: version}
	}
	ls, err := src.List(ctx)
	if err != nil {
		return release.Listing{Version: version}
	}
	for _, l := range ls {
		if l.Version == version {
			return l
		}
	}
	return release.Listing{Version: version}
}

func (u *updateRun) latest(ctx context.Context) (string, error) {
	src, err := u.source()
	if err != nil {
		return "", err
	}
	ls, err := src.List(ctx)
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.list_releases", "source", src.Name())
	}
	allowed := release.ChannelsFromEnv(u.getenv)
	for _, l := range ls {
		if allowed[l.Channel] {
			return l.Version, nil
		}
	}
	return "", apperr.NotFound("updater.errors.no_release_on_channels", "source", src.Name(), "env", release.EnvChannels)
}

// Nothing about a release may be read before this verification succeeds.
func (u *updateRun) manifest(ctx context.Context, version string) (*release.Manifest, error) {
	if _, ok := semver.Parse(version); !ok {
		return nil, apperr.Invalid("updater.errors.not_release_version", "version", version)
	}
	src, err := u.source()
	if err != nil {
		return nil, err
	}
	m, _, err := release.FetchManifest(ctx, src, u.deps.Keys, version)
	if errors.Is(err, release.ErrNoCosign) {
		return nil, apperr.Wrap(err, "updater.errors.preview_build", "version", version)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.release_from", "version", version, "source", src.Name())
	}
	if !release.ChannelsFromEnv(u.getenv)[m.Channel] {
		return nil, apperr.Precondition("updater.errors.channel_not_allowed", "version", version, "channel", m.Channel,
			"env", release.EnvChannels, "value", u.getenv(release.EnvChannels))
	}
	return m, nil
}

func (u *updateRun) targetVersion(ctx context.Context, latestIfUnset bool) (string, error) {
	if v := strings.TrimPrefix(strings.TrimSpace(u.opts.Version), "v"); v != "" {
		return v, nil
	}
	if latestIfUnset {
		return u.latest(ctx)
	}
	if u.in.Mode == initwizard.ModeKubernetes {
		return "", apperr.Invalid("updater.errors.kube_needs_version")
	}
	v := strings.TrimSpace(u.getenv(envVersion))
	if v == "" {
		return "", apperr.Precondition("updater.errors.version_not_set", "env", envVersion)
	}
	return strings.TrimPrefix(v, "v"), nil
}

func (u *updateRun) installedVersion(state *State) string {
	if v := u.in.RunningVersion; v != "" {
		return v
	}
	if state != nil && state.To.Version != "" {
		if state.Result == ResultOK || state.Result == "" {
			return state.To.Version
		}
		if state.From.Version != "" {
			return state.From.Version
		}
	}
	if u.in.Mode != initwizard.ModeKubernetes {
		return strings.TrimPrefix(strings.TrimSpace(u.getenv(envVersion)), "v")
	}
	return ""
}

func (u *updateRun) externalDatabase() bool {
	if u.in.Mode == initwizard.ModeKubernetes {
		return true
	}
	dsn := strings.TrimSpace(u.getenv("OCTOPUS_DATABASE_URL"))
	return dsn != "" && !strings.Contains(dsn, "@pg:5432")
}
