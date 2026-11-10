package release

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

const (
	EnvSource      = "OCTOPUS_UPDATE_SOURCE"
	EnvChannels    = "OCTOPUS_UPDATE_CHANNELS"
	EnvGitHubToken = "OCTOPUS_UPDATE_GITHUB_TOKEN"

	SourceGitHub = "github"
	SourceOff    = "off"

	DefaultRepo = "MisterDonnuts/thulu"
	// `octopus mirror` writes this file at the mirror root; keep the name in sync.
	IndexFile = "releases.json"
)

var ErrSourceOff error = apperr.Precondition("release.errors.source_off", "env", EnvSource)

// Binaries and bundles bypass this bound: callers stream them to disk and check them against the
// manifest.
const maxAsset = 4 << 20

type Listing struct {
	Version     string    `json:"version"`
	Channel     string    `json:"channel"`
	PublishedAt time.Time `json:"published_at"`
	Notes       string    `json:"notes,omitempty"`
}

type Source interface {
	Name() string
	List(ctx context.Context) ([]Listing, error)
	Open(ctx context.Context, version, file string) (io.ReadCloser, error)
}

func SourceFromEnv(getenv func(string) string, client *http.Client) (Source, error) {
	v := strings.TrimSpace(getenv(EnvSource))
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	switch {
	case v == "" || v == SourceGitHub:
		return &GitHub{Repo: DefaultRepo, Token: strings.TrimSpace(getenv(EnvGitHubToken)), Client: client}, nil
	case v == SourceOff:
		return nil, ErrSourceOff
	case strings.HasPrefix(v, "file://"):
		root := strings.TrimPrefix(v, "file://")
		if !strings.HasPrefix(root, "/") {
			return nil, apperr.Invalid("release.errors.file_source_not_absolute", "env", EnvSource, "value", v)
		}
		return &Dir{Root: strings.TrimRight(root, "/")}, nil
	default:
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.User != nil {
			// The value is echoed into the daemon log and Settings → Updates, so credentials (user
			// info, presigned query) must not reach the message.
			return nil, apperr.Invalid("release.errors.source_invalid", "env", EnvSource, "value", sourceLabel(v))
		}
		return &Mirror{Base: strings.TrimRight(u.String(), "/"), Client: client}, nil
	}
}

func sourceLabel(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return i18n.Log("release.labels.unparseable_url")
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	if u.RawQuery != "" {
		u.RawQuery = "REDACTED"
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

func ChannelsFromEnv(getenv func(string) string) map[string]bool {
	allowed := map[string]bool{ChannelStable: true}
	for _, c := range strings.FieldsFunc(getenv(EnvChannels), func(r rune) bool { return r == ',' || r == ' ' }) {
		switch c = strings.TrimSpace(c); c {
		case ChannelPrerelease, ChannelPreview:
			allowed[c] = true
		}
	}
	return allowed
}

func ReadAll(ctx context.Context, s Source, version, file string) ([]byte, error) {
	rc, err := s.Open(ctx, version, file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxAsset+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxAsset {
		return nil, apperr.TooLarge("release.errors.asset_too_large", "file", file, "version", version, "limit", maxAsset)
	}
	return b, nil
}

func FetchManifest(ctx context.Context, s Source, keys Keys, version string) (*Manifest, []byte, error) {
	body, err := ReadAll(ctx, s, version, ManifestFile)
	if err != nil {
		return nil, nil, err
	}
	var m *Manifest
	if IsPreview(version) {
		bundle, err := ReadAll(ctx, s, version, BundleFile)
		if err != nil {
			return nil, nil, apperr.Wrap(err, "release.errors.no_sigstore_bundle", "version", version, "file", BundleFile)
		}
		if m, err = VerifyPreviewManifest(ctx, body, bundle); err != nil {
			return nil, nil, err
		}
	} else {
		sig, err := ReadAll(ctx, s, version, SignatureFile)
		if err != nil {
			return nil, nil, apperr.Wrap(err, "release.errors.no_kms_signature", "version", version, "file", SignatureFile)
		}
		if m, err = keys.VerifyManifest(body, sig); err != nil {
			return nil, nil, err
		}
		if m.Channel == ChannelPreview {
			return nil, nil, apperr.Unprocessable("release.errors.release_key_on_preview", "version", version)
		}
	}
	if m.Version != version {
		// A correctly signed manifest of another version served under this name is a downgrade
		// dressed as an upgrade.
		return nil, nil, apperr.Unprocessable("release.errors.manifest_version_mismatch", "asked", version, "signed", m.Version)
	}
	return m, body, nil
}

func sortListings(ls []Listing) {
	sort.SliceStable(ls, func(i, j int) bool { return semver.Newer(ls[i].Version, ls[j].Version) })
}

func get(ctx context.Context, client *http.Client, rawURL string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", "octopus-updater")
	resp, err := client.Do(req) // #nosec G107 -- the URL is the operator's configured source
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, apperr.Unavailable("release.errors.http_get_failed", "url", redact(rawURL), "status", resp.Status)
	}
	return resp, nil
}

// A presigned URL carries its credential in the query.
func redact(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

type GitHub struct {
	Repo   string
	Token  string
	Client *http.Client
	API    string
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"assets"`
}

func (g *GitHub) Name() string { return "github " + g.Repo }

func (g *GitHub) api() string {
	if g.API != "" {
		return strings.TrimRight(g.API, "/")
	}
	return "https://api.github.com"
}

func (g *GitHub) header(accept string) http.Header {
	h := http.Header{"Accept": {accept}, "X-Github-Api-Version": {"2022-11-28"}} //i18n:ignore HTTP header names
	if g.Token != "" {
		h.Set("Authorization", "Bearer "+g.Token) //i18n:ignore HTTP auth scheme
	}
	return h
}

func (g *GitHub) releases(ctx context.Context) ([]ghRelease, error) {
	//i18n:ignore GitHub API path
	resp, err := get(ctx, g.Client, g.api()+"/repos/"+g.Repo+"/releases?per_page=50", g.header("application/vnd.github+json"))
	if err != nil {
		return nil, apperr.Wrap(err, "release.errors.list_github_releases", "repo", g.Repo)
	}
	defer func() { _ = resp.Body.Close() }()
	var rs []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAsset)).Decode(&rs); err != nil {
		return nil, apperr.Wrap(err, "release.errors.list_github_releases", "repo", g.Repo)
	}
	return rs, nil
}

func (g *GitHub) List(ctx context.Context) ([]Listing, error) {
	rs, err := g.releases(ctx)
	if err != nil {
		return nil, err
	}
	var out []Listing
	for _, r := range rs {
		if r.Draft {
			continue
		}
		v, ch := strings.TrimPrefix(r.TagName, "v"), ChannelStable
		if after, ok := strings.CutPrefix(r.TagName, PreviewTagPrefix); ok {
			v, ch = after, ChannelPreview
		} else if r.Prerelease {
			ch = ChannelPrerelease
		}
		if _, ok := semver.Parse(v); !ok || IsPreview(v) != (ch == ChannelPreview) {
			continue
		}
		out = append(out, Listing{Version: v, Channel: ch, PublishedAt: r.PublishedAt, Notes: r.Body})
	}
	sortListings(out)
	return out, nil
}

func (g *GitHub) Open(ctx context.Context, version, file string) (io.ReadCloser, error) {
	if !safeFile.MatchString(file) {
		return nil, apperr.Invalid("release.errors.bad_asset_name", "file", file)
	}
	tag := "v" + version
	if IsPreview(version) {
		tag = PreviewTagPrefix + version
	}
	resp, err := get(ctx, g.Client, g.api()+"/repos/"+g.Repo+"/releases/tags/"+url.PathEscape(tag), g.header("application/vnd.github+json"))
	if err != nil {
		return nil, apperr.Wrap(err, "release.errors.release_lookup", "version", version)
	}
	var r ghRelease
	err = json.NewDecoder(io.LimitReader(resp.Body, maxAsset)).Decode(&r)
	_ = resp.Body.Close()
	if err != nil {
		return nil, apperr.Wrap(err, "release.errors.release_lookup", "version", version)
	}
	for _, a := range r.Assets {
		if a.Name == file {
			// The asset URL redirects to storage; the client drops Authorization on the new host,
			// so the token does not leak.
			resp, err := get(ctx, g.Client, a.URL, g.header("application/octet-stream"))
			if err != nil {
				return nil, apperr.Wrap(err, "release.errors.asset_download", "file", file, "version", version)
			}
			return resp.Body, nil
		}
	}
	return nil, apperr.NotFound("release.errors.asset_missing", "version", version, "file", file)
}

// Nothing a mirror serves is trusted: manifests are verified with the release key, everything else
// against a verified manifest.
// Layout <base>/releases.json and <base>/<version>/<file> must match what `octopus mirror` writes.
type Mirror struct {
	Base   string
	Client *http.Client
}

type mirrorIndex struct {
	Releases []Listing `json:"releases"`
}

func (m *Mirror) Name() string { return m.Base }

func (m *Mirror) List(ctx context.Context) ([]Listing, error) {
	resp, err := get(ctx, m.Client, m.Base+"/"+IndexFile, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var idx mirrorIndex
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAsset)).Decode(&idx); err != nil {
		return nil, apperr.Wrap(err, "release.errors.mirror_index", "base", m.Base, "file", IndexFile)
	}
	var out []Listing
	for _, l := range idx.Releases {
		if _, ok := semver.Parse(l.Version); ok {
			out = append(out, l)
		}
	}
	sortListings(out)
	return out, nil
}

func (m *Mirror) Open(ctx context.Context, version, file string) (io.ReadCloser, error) {
	if !safeFile.MatchString(file) {
		return nil, apperr.Invalid("release.errors.bad_asset_name", "file", file)
	}
	if _, ok := semver.Parse(version); !ok {
		return nil, apperr.Invalid("release.errors.bad_version", "version", version)
	}
	resp, err := get(ctx, m.Client, m.Base+"/"+url.PathEscape(version)+"/"+file, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

type Dir struct{ Root string }

func (d *Dir) Name() string { return "file://" + d.Root }

func (d *Dir) List(context.Context) ([]Listing, error) {
	b, err := os.ReadFile(d.Root + "/" + IndexFile) // #nosec G304 -- operator-configured directory
	if err != nil {
		return nil, err
	}
	var idx mirrorIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	sortListings(idx.Releases)
	return idx.Releases, nil
}

func (d *Dir) Open(_ context.Context, version, file string) (io.ReadCloser, error) {
	if !safeFile.MatchString(file) {
		return nil, apperr.Invalid("release.errors.bad_asset_name", "file", file)
	}
	if _, ok := semver.Parse(version); !ok {
		return nil, apperr.Invalid("release.errors.bad_version", "version", version)
	}
	return os.Open(d.Root + "/" + version + "/" + file) // #nosec G304 -- names validated above
}
