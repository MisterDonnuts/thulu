package updatecheck

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

const (
	EnvInterval     = "OCTOPUS_UPDATE_INTERVAL"
	DefaultInterval = 12 * time.Hour
	// Floor: shorter intervals hit the GitHub API rate limit.
	MinInterval         = time.Hour
	DefaultMaxManifests = 10

	checkTimeout = 2 * time.Minute
	maxErrorLen  = 500
)

// Notes comes from the unsigned listing: the UI must render it as untrusted markdown.
type Release struct {
	Version     string    `json:"version"`
	Channel     string    `json:"channel"`
	PublishedAt time.Time `json:"published_at"`
	Notes       string    `json:"notes,omitempty"`

	Manifest *release.Manifest `json:"-"`
}

func (r Release) Verified() bool { return r.Manifest != nil }

type Rejection struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

type State struct {
	Current     string
	DevBuild    bool
	Source      string
	Off         bool
	Channels    []string
	ConfigError string
	Interval    time.Duration

	LastCheck       time.Time
	LastError       string
	Releases        []Release
	Rejected        []Rejection
	CurrentManifest *release.Manifest
}

type Config struct {
	Source       release.Source
	SourceErr    error
	Channels     map[string]bool
	Keys         release.Keys
	Current      string
	Interval     time.Duration
	MaxManifests int
}

type Checker struct {
	cfg Config

	checkMu sync.Mutex

	mu       sync.RWMutex
	state    State
	verified map[string]*release.Manifest // by version; never invalidated because a published release never changes
}

func New(cfg Config) *Checker {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.MaxManifests <= 0 {
		cfg.MaxManifests = DefaultMaxManifests
	}
	if cfg.Channels == nil {
		cfg.Channels = map[string]bool{release.ChannelStable: true}
	}
	st := State{
		Current:  cfg.Current,
		DevBuild: devBuild(cfg.Current),
		Channels: sortedChannels(cfg.Channels),
		Interval: cfg.Interval,
	}
	switch {
	case errors.Is(cfg.SourceErr, release.ErrSourceOff):
		st.Off = true
	case cfg.SourceErr != nil:
		st.ConfigError = cfg.SourceErr.Error()
	case cfg.Source != nil:
		st.Source = cfg.Source.Name()
	default:
		st.Off = true
	}
	return &Checker{cfg: cfg, state: st, verified: map[string]*release.Manifest{}}
}

func FromEnv(getenv func(string) string, current string) (*Checker, error) {
	keys, err := release.TrustedKeys()
	if err != nil {
		return nil, apperr.Wrap(err, "updatecheck.errors.release_key")
	}
	// Keep the default transport: it honors HTTPS_PROXY, which enterprise networks need to reach
	// the source.
	src, srcErr := release.SourceFromEnv(getenv, &http.Client{Timeout: 60 * time.Second})
	return New(Config{
		Source:    src,
		SourceErr: srcErr,
		Channels:  release.ChannelsFromEnv(getenv),
		Keys:      keys,
		Current:   current,
		Interval:  IntervalFromEnv(getenv),
	}), nil
}

func IntervalFromEnv(getenv func(string) string) time.Duration {
	v := strings.TrimSpace(getenv(EnvInterval))
	if v == "" {
		return DefaultInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("[updates] %s", i18n.Log("updatecheck.log.interval_not_duration", "env", EnvInterval, "value", v, "fallback", DefaultInterval))
		return DefaultInterval
	}
	if d < MinInterval {
		log.Printf("[updates] %s", i18n.Log("updatecheck.log.interval_below_minimum", "env", EnvInterval, "value", d, "minimum", MinInterval))
		return MinInterval
	}
	return d
}

func (c *Checker) Run(ctx context.Context) {
	if c.cfg.Source == nil {
		return
	}
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		if err := c.Check(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[updates] %s", i18n.Log("updatecheck.log.check_failed", "err", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Checker) Enabled() bool { return c.cfg.Source != nil }

func (c *Checker) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.state
	st.Channels = append([]string(nil), st.Channels...)
	st.Releases = append([]Release(nil), st.Releases...)
	st.Rejected = append([]Rejection(nil), st.Rejected...)
	return st
}

// Only releases the last check verified: never fetch a manifest for a version a caller typed.
func (c *Checker) Release(version string) (Release, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, r := range c.state.Releases {
		if r.Version == version && r.Verified() {
			return r, true
		}
	}
	return Release{}, false
}

func (c *Checker) Check(ctx context.Context) error {
	if c.cfg.Source == nil {
		if c.cfg.SourceErr != nil && !errors.Is(c.cfg.SourceErr, release.ErrSourceOff) {
			return c.cfg.SourceErr
		}
		return release.ErrSourceOff
	}
	c.checkMu.Lock()
	defer c.checkMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	found, err := c.lookup(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.LastCheck = time.Now().UTC()
	if err != nil {
		c.state.LastError = truncate(err.Error())
		return err
	}
	c.state.LastError = ""
	c.state.Releases = found.releases
	c.state.Rejected = found.rejected
	if found.current != nil {
		c.state.CurrentManifest = found.current
	}
	return nil
}

type lookupResult struct {
	releases []Release
	rejected []Rejection
	current  *release.Manifest
}

func (c *Checker) lookup(ctx context.Context) (lookupResult, error) {
	var res lookupResult
	listings, err := c.cfg.Source.List(ctx)
	if err != nil {
		return res, apperr.Wrap(err, "updatecheck.errors.list_releases", "source", c.cfg.Source.Name())
	}
	fetched := 0
	for _, l := range candidates(listings, c.cfg.Current, c.cfg.Channels) {
		if fetched >= c.cfg.MaxManifests {
			break
		}
		rel, rej, wasFetched, err := c.offer(ctx, l)
		if wasFetched {
			fetched++
		}
		switch {
		case err != nil:
			return res, err
		case rej != nil:
			res.rejected = append(res.rejected, *rej)
		default:
			res.releases = append(res.releases, rel)
		}
	}
	for _, l := range listings {
		if l.Version != c.cfg.Current {
			continue
		}
		m, _, err := c.manifest(ctx, l.Version)
		if err == nil {
			res.current = m
		} else if ctx.Err() == nil {
			res.rejected = append(res.rejected, Rejection{Version: l.Version, Reason: truncate(err.Error())})
		}
		break
	}
	return res, nil
}

func (c *Checker) manifest(ctx context.Context, version string) (m *release.Manifest, fetched bool, err error) {
	c.mu.RLock()
	m, ok := c.verified[version]
	c.mu.RUnlock()
	if ok {
		return m, false, nil
	}
	m, _, err = release.FetchManifest(ctx, c.cfg.Source, c.cfg.Keys, version)
	if err != nil {
		return nil, true, err
	}
	c.mu.Lock()
	c.verified[version] = m
	c.mu.Unlock()
	return m, true, nil
}

func (c *Checker) offer(ctx context.Context, l release.Listing) (Release, *Rejection, bool, error) {
	m, fetched, err := c.manifest(ctx, l.Version)
	if err != nil {
		if ctx.Err() != nil {
			return Release{}, nil, fetched, ctx.Err()
		}
		// Previews are signed keylessly and the daemon image has no cosign: list them unverified,
		// read nothing from the manifest;
		// `thulu update` verifies before installing.
		if errors.Is(err, release.ErrNoCosign) && l.Channel == release.ChannelPreview && release.IsPreview(l.Version) {
			return Release{
				Version:     l.Version,
				Channel:     release.ChannelPreview,
				PublishedAt: l.PublishedAt,
				Notes:       l.Notes,
			}, nil, fetched, nil
		}
		return Release{}, &Rejection{Version: l.Version, Reason: truncate(apperr.Text(err))}, fetched, nil
	}
	// The listing's channel is the source's word, the manifest's is the release key's: trust only
	// the manifest.
	if !c.cfg.Channels[m.Channel] {
		return Release{}, &Rejection{
			Version: l.Version,
			Reason:  i18n.Text("updatecheck.rejections.channel_not_allowed", "channel", m.Channel, "env", release.EnvChannels),
		}, fetched, nil
	}
	published := m.PublishedAt
	if published.IsZero() {
		published = l.PublishedAt
	}
	return Release{Version: m.Version, Channel: m.Channel, PublishedAt: published, Notes: l.Notes, Manifest: m}, nil, fetched, nil
}

func candidates(ls []release.Listing, current string, channels map[string]bool) []release.Listing {
	cur, curOK := semver.Parse(current)
	var out []release.Listing
	seen := map[string]bool{}
	for _, l := range ls {
		v, ok := semver.Parse(l.Version)
		if !ok || seen[l.Version] || !channels[l.Channel] {
			continue
		}
		if curOK && semver.Compare(v, cur) <= 0 {
			continue
		}
		seen[l.Version] = true
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return semver.Newer(out[i].Version, out[j].Version) })
	return out
}

func devBuild(v string) bool {
	p, ok := semver.Parse(v)
	if !ok {
		return true
	}
	return len(p.Pre) > 0 && p.Pre[0] == "dev"
}

func sortedChannels(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for ch, on := range m {
		if on {
			out = append(out, ch)
		}
	}
	sort.Strings(out)
	return out
}

func truncate(s string) string {
	if len(s) <= maxErrorLen {
		return s
	}
	return s[:maxErrorLen] + "…"
}
