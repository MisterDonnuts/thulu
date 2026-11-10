package release

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

const (
	ManifestFile = "octopus-release.json"
	// base64 ASN.1 ECDSA P-256 over sha256(manifest bytes), as `cosign sign-blob --key` writes it.
	SignatureFile = ManifestFile + ".sig"
	BundleFile    = ManifestFile + ".sigstore.json"
)

const (
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
	ChannelPreview    = "preview"
)

const SchemaVersion = 1

type Manifest struct {
	Schema      int       `json:"schema"`
	Version     string    `json:"version"`
	Channel     string    `json:"channel"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`

	Images     map[string]string `json:"images"`
	ThirdParty map[string]string `json:"third_party"`

	Chart        Chart               `json:"chart"`
	DeployBundle Artifact            `json:"deploy_bundle"`
	Binaries     map[string]Artifact `json:"binaries"`

	UpgradeFrom string `json:"upgrade_from"`
	RollbackTo  string `json:"rollback_to,omitempty"`

	Migrations  Migrations `json:"migrations"`
	Requires    []string   `json:"requires"`
	ManualSteps []string   `json:"manual_steps"`
	Advisories  []Advisory `json:"advisories"`
	NotesURL    string     `json:"notes_url,omitempty"`
}

type Chart struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type Migrations struct {
	Added    int `json:"added"`
	Contract int `json:"contract"`
}

type Advisory struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Affected string `json:"affected"`
	URL      string `json:"url,omitempty"`
}

var (
	digestRef  = regexp.MustCompile(`^[a-z0-9]+(?:[._/-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)
	digestOnly = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hexSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	envName    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	safeFile   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Unknown fields are refused: a signed document must not be half-read.
func Parse(b []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "release.errors.manifest_decode")
	}
	if dec.More() {
		return nil, apperr.Invalid("release.errors.manifest_trailing_data")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) Validate() error {
	var bad complaints
	versionOK := m.validateIdentity(&bad)
	m.validateContent(&bad)
	m.validatePolicy(&bad, versionOK)
	if len(bad) > 0 {
		sort.Slice(bad, func(i, j int) bool { return bad[i].Error() < bad[j].Error() })
		return apperr.Invalid("release.errors.manifest_invalid", "version", m.Version,
			"problems", apperr.List{Errs: bad, Sep: "; "})
	}
	return nil
}

type complaints []error

func (c *complaints) add(key string, args ...any) { *c = append(*c, apperr.New(key, args...)) }

func (m *Manifest) validateIdentity(bad *complaints) bool {
	if m.Schema != SchemaVersion {
		bad.add("release.manifest.schema_unsupported", "schema", m.Schema, "supported", SchemaVersion)
	}
	v, ok := semver.Parse(m.Version)
	if !ok || v.String() != m.Version || v.Build != "" {
		bad.add("release.manifest.version_not_release", "version", m.Version)
	}
	switch m.Channel {
	case ChannelStable:
		if ok && v.Prerelease() {
			bad.add("release.manifest.stable_prerelease", "version", m.Version)
		}
	case ChannelPrerelease, ChannelPreview:
	default:
		bad.add("release.manifest.channel_unknown", "channel", m.Channel)
	}
	if !commitSHA.MatchString(m.Commit) {
		bad.add("release.manifest.commit_not_sha", "commit", m.Commit)
	}
	if m.PublishedAt.IsZero() {
		bad.add("release.manifest.published_at_missing")
	}
	return ok
}

func (m *Manifest) validateContent(bad *complaints) {
	if _, found := m.Images["octopus"]; !found {
		bad.add("release.manifest.octopus_image_missing")
	}
	for name, ref := range m.Images {
		if !digestRef.MatchString(ref) {
			bad.add("release.manifest.image_not_digest", "name", name, "ref", ref)
		}
	}
	for name, ref := range m.ThirdParty {
		if !digestRef.MatchString(ref) && !strings.Contains(ref, "@sha256:") {
			bad.add("release.manifest.third_party_not_pinned", "name", name, "ref", ref)
		}
	}
	if !strings.HasPrefix(m.Chart.Ref, "oci://") || !digestOnly.MatchString(m.Chart.Digest) {
		bad.add("release.manifest.chart_not_pinned")
	}
	checkArtifact := func(field string, a Artifact) {
		if !safeFile.MatchString(a.File) || !hexSHA256.MatchString(a.SHA256) {
			bad.add("release.manifest.artifact_not_pinned", "field", field)
		}
	}
	checkArtifact("deploy_bundle", m.DeployBundle)
	for platform, a := range m.Binaries {
		checkArtifact("binaries."+platform, a)
	}
}

func (m *Manifest) validatePolicy(bad *complaints, versionOK bool) {
	if strings.TrimSpace(m.UpgradeFrom) == "" {
		bad.add("release.manifest.upgrade_from_missing")
	} else if versionOK {
		if err := semver.ValidConstraint(m.UpgradeFrom); err != nil {
			bad.add("release.manifest.upgrade_from_invalid", "err", err)
		}
	}
	if m.RollbackTo != "" && !semver.Less(m.RollbackTo, m.Version) {
		bad.add("release.manifest.rollback_not_older", "rollback", m.RollbackTo, "version", m.Version)
	}
	for _, r := range m.Requires {
		if !envName.MatchString(r) {
			bad.add("release.manifest.requires_not_env", "name", r)
		}
	}
	for _, a := range m.Advisories {
		if a.ID == "" || a.Affected == "" {
			bad.add("release.manifest.advisory_incomplete")
		}
	}
}

func (m *Manifest) CanUpgradeFrom(current string) bool {
	v, ok := semver.Parse(current)
	return ok && semver.Satisfies(v, m.UpgradeFrom)
}

func (m *Manifest) RollbackWithoutRestore(target string) bool {
	return m.RollbackTo != "" && m.RollbackTo == target
}

func (m *Manifest) AdvisoriesFor(current string) []Advisory {
	v, ok := semver.Parse(current)
	if !ok {
		return nil
	}
	var out []Advisory
	for _, a := range m.Advisories {
		if semver.Satisfies(v, a.Affected) {
			out = append(out, a)
		}
	}
	return out
}

func (m *Manifest) ImageNames() []string {
	names := make([]string, 0, len(m.Images))
	for n := range m.Images {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *Manifest) Encode() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
