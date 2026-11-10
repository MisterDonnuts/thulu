package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

// Unsigned and informational only: apply must never trust it; references come from the signed
// manifest plus OCTOPUS_IMAGE_REGISTRY.
const MirrorFile = "octopus-release.mirror.json"

var cosignSuffixes = []string{".sig", ".att", ".sbom"}

type MirrorOptions struct {
	Version       string
	Registry      string
	ChartRegistry string
	Dir           string
	Out           io.Writer
	Getenv        func(string) string
	Deps          Deps
}

type MirrorRecord struct {
	Schema        int           `json:"schema"`
	Version       string        `json:"version"`
	MirroredAt    time.Time     `json:"mirrored_at"`
	Source        string        `json:"source"`
	Registry      string        `json:"registry,omitempty"`
	ChartRegistry string        `json:"chart_registry,omitempty"`
	Images        []MirroredRef `json:"images,omitempty"`
	Chart         *MirroredRef  `json:"chart,omitempty"`
	Note          string        `json:"note"`
}

type MirroredRef struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

func Mirror(ctx context.Context, opts MirrorOptions) error {
	if opts.Registry == "" && opts.Dir == "" {
		return apperr.Invalid("updater.errors.nothing_to_mirror_into")
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	opts.Registry = strings.TrimRight(strings.TrimSpace(opts.Registry), "/")
	deps := &opts.Deps
	src, err := mirrorSource(deps, getenv)
	if err != nil {
		return err
	}
	p := printer{w: opts.Out}

	version := strings.TrimPrefix(strings.TrimSpace(opts.Version), "v")
	m, body, err := release.FetchManifest(ctx, src, deps.Keys, version)
	if err != nil {
		if errors.Is(err, release.ErrNoCosign) {
			return apperr.Wrap(err, "updater.errors.mirror_preview_needs_cosign")
		}
		return apperr.Wrap(err, "updater.errors.manifest_of", "version", version)
	}
	p.say("updater.cli.mirroring", "version", m.Version, "source", src.Name())
	rec := &MirrorRecord{
		Schema: 1, Version: m.Version, MirroredAt: deps.now(), Source: src.Name(),
		Registry: opts.Registry,
		Note:     i18n.Text("updater.labels.mirror_note", "manifest", ManifestName(m.Version)),
	}
	if opts.Registry != "" {
		if err := mirrorImages(ctx, m, opts, deps, rec, p); err != nil {
			return err
		}
	}
	if opts.Dir != "" {
		if err := mirrorFiles(ctx, src, m, body, opts.Dir, rec, p); err != nil {
			return err
		}
	}
	p.say("updater.cli.mirror_done")
	if opts.Dir != "" {
		p.say("updater.cli.mirror_hint_source", "dir", opts.Dir)
	}
	if opts.Registry != "" {
		p.say("updater.cli.mirror_hint_registry", "registry", opts.Registry)
	}
	if rec.ChartRegistry != "" {
		p.say("updater.cli.mirror_hint_chart_registry", "registry", rec.ChartRegistry)
	}
	p.say("updater.cli.mirror_hint_update", "version", m.Version)
	return nil
}

func mirrorSource(deps *Deps, getenv func(string) string) (release.Source, error) {
	if deps.Keys == nil {
		keys, err := release.TrustedKeys()
		if err != nil {
			return nil, err
		}
		deps.Keys = keys
	}
	if deps.Source != nil {
		return deps.Source, nil
	}
	return release.SourceFromEnv(getenv, deps.http())
}

func ManifestName(version string) string { return version + "/" + release.ManifestFile }

func mirrorImages(ctx context.Context, m *release.Manifest, opts MirrorOptions, deps *Deps, rec *MirrorRecord, p printer) error {
	verify := deps.imageVerifier(m)
	names := m.ImageNames()
	// Verify at the source before copying and again at the mirror: the second check proves the
	// signatures traveled.
	for _, n := range names {
		if err := verify(ctx, m.Images[n]); err != nil {
			return apperr.Wrap(err, "updater.errors.at_source", "name", n)
		}
	}
	tag := initwizard.ImageTag(m.Version)
	for _, n := range names {
		to, err := copyImage(ctx, m.Images[n], opts.Registry, tag, true)
		if err != nil {
			return apperr.Wrap(err, "updater.errors.copy", "name", n)
		}
		if err := verify(ctx, to); err != nil {
			return apperr.Wrap(err, "updater.errors.at_mirror", "name", n)
		}
		rec.Images = append(rec.Images, MirroredRef{Name: n, From: m.Images[n], To: to})
		fmt.Fprintf(p.w, "   %s → %s\n", n, to)
	}
	third := make([]string, 0, len(m.ThirdParty))
	for n := range m.ThirdParty {
		third = append(third, n)
	}
	sort.Strings(third)
	for _, n := range third {
		from := m.ThirdParty[n]
		tag := tagOf(from)
		if tag == "" {
			tag = initwizard.ImageTag(m.Version)
		}
		to, err := copyImage(ctx, from, opts.Registry, tag, false)
		if err != nil {
			return apperr.Wrap(err, "updater.errors.copy", "name", n)
		}
		rec.Images = append(rec.Images, MirroredRef{Name: n, From: from, To: to})
		fmt.Fprintf(p.w, "   %s → %s\n", n, to)
	}

	if m.Chart.Ref == "" {
		return nil
	}
	chartFrom := repository(strings.TrimPrefix(m.Chart.Ref, "oci://")) + "@" + m.Chart.Digest
	chartRepo := mirrorChartRepo(opts.ChartRegistry, opts.Registry, chartFrom)
	if err := verify(ctx, chartFrom); err != nil {
		return apperr.Wrap(err, "updater.errors.chart_at_source")
	}
	to, err := copyTo(ctx, chartFrom, chartRepo, m.Version, true)
	if err != nil {
		return apperr.Wrap(err, "updater.errors.copy_chart")
	}
	if err := verify(ctx, to); err != nil {
		return apperr.Wrap(err, "updater.errors.chart_at_mirror")
	}
	rec.ChartRegistry = "oci://" + strings.TrimSuffix(chartRepo, "/"+repoName(chartRepo))
	rec.Chart = &MirroredRef{Name: "chart", From: chartFrom, To: to}
	p.say("updater.cli.mirrored_chart", "ref", to)
	return nil
}

// chartRef (helm.go) must resolve the same repository name when installing.
func mirrorChartRepo(chartRegistry, registry, ref string) string {
	n := repoName(repository(ref))
	if chartRegistry != "" {
		return strings.TrimRight(strings.TrimPrefix(chartRegistry, "oci://"), "/") + "/" + n
	}
	return registry + "/charts/" + n
}

func copyImage(ctx context.Context, ref, registry, tag string, signed bool) (string, error) {
	return copyTo(ctx, ref, registry+"/"+repoName(repository(ref)), tag, signed)
}

func copyTo(ctx context.Context, ref, repo, tag string, signed bool) (string, error) {
	src, err := name.NewDigest(ref)
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.source_ref", "ref", ref)
	}
	dst, err := name.NewRepository(repo)
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.mirror_repository", "repo", repo)
	}
	if err := copyManifest(ctx, src, dst.Tag(tag)); err != nil {
		return "", err
	}
	for _, suffix := range cosignSuffixes {
		t := strings.Replace(src.DigestStr(), ":", "-", 1) + suffix
		cerr := copyManifest(ctx, src.Context().Tag(t), dst.Tag(t))
		switch {
		case cerr == nil:
		case isNotFound(cerr) && (suffix != ".sig" || !signed):
		case isNotFound(cerr):
			return "", apperr.NotFound("updater.errors.no_cosign_signature", "ref", ref)
		default:
			return "", apperr.Wrap(cerr, "updater.errors.part_of", "part", suffix, "whole", ref)
		}
	}
	return dst.Digest(src.DigestStr()).String(), nil
}

// The manifest is written byte-for-byte so the digest the release pins (and its cosign signature)
// stays valid.
func copyManifest(ctx context.Context, src, dst name.Reference) error {
	opts := release.RemoteOptions(ctx)
	desc, err := remote.Get(src, opts...)
	if err != nil {
		return apperr.Wrap(err, "updater.errors.get_ref", "ref", src.String())
	}
	if d, ok := src.(name.Digest); ok && desc.Digest.String() != d.DigestStr() {
		return apperr.Unprocessable("updater.errors.served_digest", "ref", src.String(), "digest", desc.Digest.String())
	}
	if desc.MediaType.IsIndex() {
		idx, ierr := desc.ImageIndex()
		if ierr != nil {
			return apperr.Wrap(ierr, "updater.errors.read_path", "path", src.String())
		}
		if ierr := remote.WriteIndex(dst, idx, opts...); ierr != nil {
			return apperr.Wrap(ierr, "updater.errors.write_path", "path", dst.String())
		}
		return nil
	}
	img, err := desc.Image()
	if err != nil {
		return apperr.Wrap(err, "updater.errors.read_path", "path", src.String())
	}
	if err := remote.Write(dst, img, opts...); err != nil {
		return apperr.Wrap(err, "updater.errors.write_path", "path", dst.String())
	}
	return nil
}

func isNotFound(err error) bool {
	var terr *transport.Error
	return errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound
}

func tagOf(ref string) string {
	base, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		return base[i+1:]
	}
	return ""
}

// Write <version>/ before adding it to releases.json, so a mirror never lists a release it does not
// hold in full.
func mirrorFiles(ctx context.Context, src release.Source, m *release.Manifest, body []byte, dir string,
	rec *MirrorRecord, p printer,
) error {
	vdir := filepath.Join(dir, m.Version)
	if err := os.MkdirAll(vdir, 0o755); err != nil { //nolint:gosec // a mirror is served to every install that reads it
		return err
	}
	put := func(file string, b []byte) error {
		return fsx.WriteFile(filepath.Join(vdir, file), b, 0o644)
	}
	if err := put(release.ManifestFile, body); err != nil {
		return err
	}
	for _, f := range []string{release.SignatureFile, release.BundleFile} {
		b, err := release.ReadAll(ctx, src, m.Version, f)
		switch {
		case err == nil:
			if serr := put(f, b); serr != nil {
				return serr
			}
		case f == release.SignatureFile && !release.IsPreview(m.Version),
			f == release.BundleFile && release.IsPreview(m.Version):
			return apperr.Wrap(err, "updater.errors.part_of", "part", f, "whole", m.Version)
		}
	}
	arts := []release.Artifact{m.DeployBundle}
	platforms := make([]string, 0, len(m.Binaries))
	for p := range m.Binaries {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	for _, p := range platforms {
		arts = append(arts, m.Binaries[p])
	}
	for _, a := range arts {
		if a.File == "" {
			continue
		}
		if err := copyArtifact(ctx, src, m.Version, a, vdir); err != nil {
			return err
		}
		p.say("updater.cli.sha256_checked", "file", a.File)
	}
	recBody, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := put(MirrorFile, append(recBody, '\n')); err != nil {
		return err
	}
	return addToIndex(ctx, src, dir, m)
}

func copyArtifact(ctx context.Context, src release.Source, version string, a release.Artifact, vdir string) error {
	final := filepath.Join(vdir, filepath.Base(a.File))
	if sum, err := fileSHA256(final); err == nil && sum == a.SHA256 {
		return nil
	}
	rc, err := src.Open(ctx, version, a.File)
	if err != nil {
		return apperr.Wrap(err, "updater.errors.download", "file", a.File)
	}
	defer func() { _ = rc.Close() }()
	tmp, err := os.CreateTemp(vdir, ".download-*")
	if err != nil {
		return err
	}
	defer warnRemove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(rc, maxBinary+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return apperr.Wrap(err, "updater.errors.download", "file", a.File)
	}
	if n > maxBinary {
		return apperr.TooLarge("updater.errors.file_too_large", "file", a.File, "limit", maxBinary)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return apperr.Unprocessable("updater.errors.sha_mismatch", "file", a.File, "got", got, "want", a.SHA256)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // served to every install reading the mirror
		return err
	}
	return os.Rename(tmp.Name(), final)
}

func addToIndex(ctx context.Context, src release.Source, dir string, m *release.Manifest) error {
	path := filepath.Join(dir, release.IndexFile)
	var idx struct {
		Releases []release.Listing `json:"releases"`
	}
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the mirror directory the operator named
		if uerr := json.Unmarshal(b, &idx); uerr != nil {
			return apperr.Wrap(uerr, "updater.errors.parse_path", "path", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	entry := release.Listing{Version: m.Version, Channel: m.Channel, PublishedAt: m.PublishedAt}
	if ls, err := src.List(ctx); err == nil {
		for _, l := range ls {
			if l.Version == m.Version {
				entry.Notes = l.Notes
			}
		}
	}
	others := make([]release.Listing, 0, len(idx.Releases)+1)
	for _, l := range idx.Releases {
		if l.Version != m.Version {
			others = append(others, l)
		}
	}
	others = append(others, entry)
	idx.Releases = others
	sort.SliceStable(idx.Releases, func(i, j int) bool { return semver.Newer(idx.Releases[i].Version, idx.Releases[j].Version) })
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFile(path, append(b, '\n'), 0o644)
}
