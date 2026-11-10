// Command release-manifest writes octopus-release.json for a release, from
// what the release workflow already produced: the image digests its image
// jobs pushed, the chart digest, the deploy bundle and binaries on disk, and
// the migrations lock in the tagged tree.
//
//	go run ./cmd/release-manifest \
//	  -version 0.3.0 -commit $GITHUB_SHA \
//	  -images dist/images \
//	  -third-party postgres=postgres:16-alpine@sha256:… -third-party caddy=caddy:2-alpine@sha256:… \
//	  -chart-ref oci://ghcr.io/misterdonnuts/charts/octopus:0.3.0 -chart-digest sha256:… \
//	  -bundle dist/octopus_0.3.0_deploy.tar.gz -binaries dist \
//	  -o dist/octopus-release.json
//
// Release-specific facts nobody can derive (new required variables, manual
// steps, advisories fixed) come from releases/<version>.json when present.
// The workflow signs the output; this command never sees a key.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/schema"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	configureLocale(os.Getenv)
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, i18n.Text("releasemanifest.cli.failed", "err", apperr.Text(err)))
		os.Exit(1)
	}
}

func configureLocale(getenv func(string) string) {
	if v := getenv("OCTOPUS_LOCALE"); v != "" {
		i18n.SetDaemonLocale(v)
		return
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(env); v != "" {
			i18n.SetDaemonLocale(i18n.Negotiate(v))
			return
		}
	}
}

type inputs struct {
	Version     string
	Channel     string
	Commit      string
	PublishedAt time.Time
	Images      map[string]string
	ThirdParty  map[string]string
	ChartRef    string
	ChartDigest string
	Bundle      release.Artifact
	Binaries    map[string]release.Artifact
	Lock        *schema.Lock
	All         []schema.Entry
	Meta        meta
	NotesURL    string
}

type meta struct {
	Requires    []string           `json:"requires"`
	ManualSteps []string           `json:"manual_steps"`
	Advisories  []release.Advisory `json:"advisories"`
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("release-manifest", flag.ContinueOnError)
	var (
		verify      = fs.String("verify", "", i18n.Text("releasemanifest.flags.verify"))
		version     = fs.String("version", "", i18n.Text("releasemanifest.flags.version"))
		channel     = fs.String("channel", "", i18n.Text("releasemanifest.flags.channel"))
		commit      = fs.String("commit", "", i18n.Text("releasemanifest.flags.commit"))
		imagesDir   = fs.String("images", "", i18n.Text("releasemanifest.flags.images"))
		chartRef    = fs.String("chart-ref", "", i18n.Text("releasemanifest.flags.chart_ref"))
		chartDigest = fs.String("chart-digest", "", i18n.Text("releasemanifest.flags.chart_digest"))
		bundle      = fs.String("bundle", "", i18n.Text("releasemanifest.flags.bundle"))
		binaries    = fs.String("binaries", "", i18n.Text("releasemanifest.flags.binaries"))
		lockPath    = fs.String("lock", schema.LockPath, i18n.Text("releasemanifest.flags.lock"))
		metaDir     = fs.String("meta", "releases", i18n.Text("releasemanifest.flags.meta"))
		notesURL    = fs.String("notes-url", "", i18n.Text("releasemanifest.flags.notes_url"))
		output      = fs.String("o", "", i18n.Text("releasemanifest.flags.output"))
		thirdParty  multi
	)
	fs.Var(&thirdParty, "third-party", i18n.Text("releasemanifest.flags.third_party"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *verify != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		keys, err := release.TrustedKeys()
		return verifyRelease(ctx, *verify, keys, err, out, release.RemoteOptions(ctx)...)
	}

	in := inputs{
		Version:     strings.TrimPrefix(*version, "v"),
		Channel:     *channel,
		Commit:      *commit,
		PublishedAt: time.Now().UTC().Truncate(time.Second),
		ThirdParty:  map[string]string{},
		ChartRef:    *chartRef,
		ChartDigest: *chartDigest,
		NotesURL:    *notesURL,
	}
	var err error
	if in.Images, err = readImages(*imagesDir); err != nil {
		return err
	}
	for _, kv := range thirdParty {
		name, ref, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return apperr.Invalid("releasemanifest.errors.third_party_format", "value", kv)
		}
		in.ThirdParty[name] = ref
	}
	if in.Bundle, err = artifact(*bundle); err != nil {
		return err
	}
	if in.Binaries, err = readBinaries(*binaries, in.Version); err != nil {
		return err
	}
	if in.Lock, err = readLock(*lockPath); err != nil {
		return err
	}
	if in.All, err = schema.All(); err != nil {
		return err
	}
	if in.Meta, err = readMeta(*metaDir, in.Version); err != nil {
		return err
	}

	m, err := build(in)
	if err != nil {
		return err
	}
	b, err := m.Encode()
	if err != nil {
		return err
	}
	if *output == "" {
		_, err = out.Write(b)
		return err
	}
	return os.WriteFile(*output, b, 0o644) // #nosec G306 -- a public release asset
}

func build(in inputs) (*release.Manifest, error) {
	v, ok := semver.Parse(in.Version)
	if !ok {
		return nil, apperr.Invalid("releasemanifest.errors.bad_version", "version", in.Version)
	}
	channel := in.Channel
	if channel == "" {
		switch {
		case release.IsPreview(in.Version):
			channel = release.ChannelPreview
		case v.Prerelease():
			channel = release.ChannelPrerelease
		default:
			channel = release.ChannelStable
		}
	}
	if (channel == release.ChannelPreview) != release.IsPreview(in.Version) {
		return nil, apperr.Invalid("releasemanifest.errors.channel_mismatch", "version", in.Version, "channel", channel)
	}
	m := &release.Manifest{
		Schema:       release.SchemaVersion,
		Version:      in.Version,
		Channel:      channel,
		Commit:       in.Commit,
		PublishedAt:  in.PublishedAt,
		Images:       in.Images,
		ThirdParty:   in.ThirdParty,
		Chart:        release.Chart{Ref: in.ChartRef, Digest: in.ChartDigest},
		DeployBundle: in.Bundle,
		Binaries:     in.Binaries,
		UpgradeFrom:  upgradeFrom(v),
		Requires:     nonNil(in.Meta.Requires),
		ManualSteps:  nonNil(in.Meta.ManualSteps),
		Advisories:   in.Meta.Advisories,
		NotesURL:     in.NotesURL,
	}
	if m.Advisories == nil {
		m.Advisories = []release.Advisory{}
	}
	if channel != release.ChannelPreview {
		if err := schema.CheckReady(in.Lock, in.Version).Err(); err != nil {
			return nil, apperr.Wrap(err, "releasemanifest.errors.lock_not_ready", "version", in.Version)
		}
		if err := schema.Check(in.Lock, in.All).Err(); err != nil {
			return nil, apperr.Wrap(err, "releasemanifest.errors.lock_mismatch")
		}
		m.Migrations = migrationsOf(in.Lock, in.All, in.Version)
		m.RollbackTo = rollbackTo(in.Lock, in.Version)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Support policy: upgrades allowed from the previous minor version onward.
func upgradeFrom(v semver.Version) string {
	minor := v.Minor - 1
	if minor < 0 {
		minor = 0
	}
	return fmt.Sprintf(">=%d.%d.0", v.Major, minor)
}

func rollbackTo(l *schema.Lock, version string) string {
	prev := ""
	for _, r := range l.Releases {
		if semver.Less(r, version) && (prev == "" || semver.Newer(r, prev)) {
			prev = r
		}
	}
	if prev == "" || semver.Less(prev, schema.EnforceFrom) {
		return ""
	}
	// upgradeFrom keeps upgrades inside a major, and the compat gate only covers that span.
	if semver.MustParse(prev).Major != semver.MustParse(version).Major {
		return ""
	}
	return prev
}

func migrationsOf(l *schema.Lock, all []schema.Entry, version string) release.Migrations {
	contract := map[string]bool{}
	for _, e := range all {
		if e.Contract != nil {
			contract[fmt.Sprintf("%s/%d", e.Component, e.ID)] = true
		}
	}
	var out release.Migrations
	for _, e := range l.Entries {
		if e.ReleasedIn != version {
			continue
		}
		out.Added++
		if contract[e.Key()] {
			out.Contract++
		}
	}
	return out
}

func readImages(dir string) (map[string]string, error) {
	if dir == "" {
		return nil, apperr.Invalid("releasemanifest.errors.flag_required", "flag", "-images")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.ref"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, apperr.NotFound("releasemanifest.errors.no_ref_files", "dir", dir)
	}
	sort.Strings(paths)
	images := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p) // #nosec G304 -- workflow-provided directory
		if err != nil {
			return nil, err
		}
		images[strings.TrimSuffix(filepath.Base(p), ".ref")] = strings.TrimSpace(string(b))
	}
	return images, nil
}

func artifact(path string) (release.Artifact, error) {
	if path == "" {
		return release.Artifact{}, apperr.Invalid("releasemanifest.errors.flag_required", "flag", "-bundle")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- workflow-provided path
	if err != nil {
		return release.Artifact{}, err
	}
	return release.Artifact{File: filepath.Base(path), SHA256: release.SHA256Hex(b)}, nil
}

// Keyed "<os>_<arch>" because the CLI looks up runtime.GOOS + "_" + runtime.GOARCH.
func readBinaries(dir, version string) (map[string]release.Artifact, error) {
	if dir == "" {
		return nil, apperr.Invalid("releasemanifest.errors.flag_required", "flag", "-binaries")
	}
	prefix := "octopus_" + version + "_"
	paths, err := filepath.Glob(filepath.Join(dir, prefix+"*"))
	if err != nil {
		return nil, err
	}
	out := map[string]release.Artifact{}
	for _, p := range paths {
		platform := strings.TrimPrefix(filepath.Base(p), prefix)
		if strings.Contains(platform, ".") || strings.Count(platform, "_") != 1 {
			continue
		}
		a, err := artifact(p)
		if err != nil {
			return nil, err
		}
		out[platform] = a
	}
	if len(out) == 0 {
		return nil, apperr.NotFound("releasemanifest.errors.no_binaries", "prefix", prefix, "dir", dir)
	}
	return out, nil
}

func readLock(path string) (*schema.Lock, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- repository file
	if err != nil {
		return nil, err
	}
	return schema.ParseLock(bytes.NewReader(b))
}

func readMeta(dir, version string) (meta, error) {
	var m meta
	b, err := os.ReadFile(filepath.Join(dir, version+".json")) // #nosec G304 -- repository file
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, apperr.Wrap(err, "releasemanifest.errors.meta_file", "dir", dir, "version", version)
	}
	return m, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
