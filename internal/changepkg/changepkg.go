package changepkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

const SchemaVersion = 1

const (
	ModeDocker     = "docker"
	ModeKubernetes = "kubernetes"
)

const RestoreExternal = kept.RestoreExternal

type Facts struct {
	From   string
	Mode   string
	Source string
	// Not covered by the manifest signature: render as quoted, untrusted text.
	Notes            string
	ExternalDatabase bool
	MissingEnv       []string
	Files            []deployfiles.Change
	GeneratedAt      time.Time
	Locale           string
}

type Image struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type Package struct {
	Schema      int       `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`

	From        string    `json:"from"`
	To          string    `json:"to"`
	Channel     string    `json:"channel"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
	Source      string    `json:"source,omitempty"`
	Mode        string    `json:"mode"`

	Images     []Image       `json:"images"`
	ThirdParty []Image       `json:"third_party"`
	Chart      release.Chart `json:"chart"`
	Verify     []string      `json:"verify"`

	Advisories             []release.Advisory `json:"advisories"`
	Migrations             release.Migrations `json:"migrations"`
	RollbackWithoutRestore bool               `json:"rollback_without_restore"`

	Requires    []string             `json:"requires"`
	MissingEnv  []string             `json:"missing_env,omitempty"`
	ManualSteps []string             `json:"manual_steps"`
	Files       []deployfiles.Change `json:"files,omitempty"`

	Backup   string   `json:"backup"`
	Downtime string   `json:"downtime"`
	Plan     []string `json:"plan"`
	Rollback []string `json:"rollback"`
	Blockers []string `json:"blockers"`

	NotesURL string `json:"notes_url,omitempty"`
	Notes    string `json:"notes,omitempty"`

	locale string
}

func text(locale, key string, args ...any) string {
	if locale == "" {
		return i18n.Text(key, args...)
	}
	return i18n.T(locale, key, args...)
}

func Build(m *release.Manifest, f Facts) *Package {
	p := &Package{
		Schema:      SchemaVersion,
		GeneratedAt: f.GeneratedAt.UTC(),
		From:        f.From,
		To:          m.Version,
		Channel:     m.Channel,
		Commit:      m.Commit,
		PublishedAt: m.PublishedAt,
		Source:      f.Source,
		Mode:        f.Mode,
		Chart:       m.Chart,
		Migrations:  m.Migrations,
		Requires:    nonNil(m.Requires),
		MissingEnv:  f.MissingEnv,
		ManualSteps: nonNil(m.ManualSteps),
		Files:       f.Files,
		NotesURL:    m.NotesURL,
		Notes:       strings.TrimSpace(f.Notes),
		Advisories:  m.AdvisoriesFor(f.From),
		Blockers:    []string{},
		locale:      f.Locale,
	}
	if p.GeneratedAt.IsZero() {
		p.GeneratedAt = time.Now().UTC()
	}
	if p.Mode == "" {
		p.Mode = ModeDocker
	}
	if p.Advisories == nil {
		p.Advisories = []release.Advisory{}
	}
	external := f.ExternalDatabase || p.Mode == ModeKubernetes
	p.RollbackWithoutRestore = f.From != "" && m.RollbackWithoutRestore(f.From)

	for _, name := range m.ImageNames() {
		p.Images = append(p.Images, Image{Name: name, Ref: m.Images[name]})
	}
	for _, name := range sortedKeys(m.ThirdParty) {
		p.ThirdParty = append(p.ThirdParty, Image{Name: name, Ref: m.ThirdParty[name]})
	}
	if p.ThirdParty == nil {
		p.ThirdParty = []Image{}
	}
	p.Verify = verifyCommands(m)
	p.Blockers = blockers(m, f)
	p.Backup, p.Downtime = backupAndDowntime(f.Locale, p.Mode, external)
	p.Plan = planCommands(m, p.Mode, external)
	p.Rollback = rollbackPlan(f.Locale, m, f.From, external)
	return p
}

func blockers(m *release.Manifest, f Facts) []string {
	out := []string{}
	switch {
	case f.From == "":
		out = append(out, text(f.Locale, "changepkg.blockers.from_unknown"))
	case f.From == m.Version:
		// Re-applying the same version is allowed: it repairs drift.
	case !isVersion(f.From):
		out = append(out, text(f.Locale, "changepkg.blockers.unstamped_build", "from", f.From))
	case semver.Newer(f.From, m.Version):
		out = append(out, text(f.Locale, "changepkg.blockers.downgrade", "to", m.Version, "from", f.From))
	case !m.CanUpgradeFrom(f.From):
		out = append(out, text(f.Locale, "changepkg.blockers.needs_intermediate",
			"to", m.Version, "upgrade_from", m.UpgradeFrom, "from", f.From))
	}
	for _, k := range f.MissingEnv {
		out = append(out, text(f.Locale, "changepkg.blockers.missing_env", "name", k))
	}
	for _, c := range f.Files {
		if c.Kind == deployfiles.Edited {
			out = append(out, text(f.Locale, "changepkg.blockers.edited_by_hand",
				"path", c.Path, "override", deployfiles.OverrideFile))
		}
	}
	return out
}

func verifyCommands(m *release.Manifest) []string {
	//i18n:ignore shell command, not a message
	const verify = "cosign verify --key octopus-release.pub "
	cmds := []string{
		//i18n:ignore shell command, not a message
		"cosign verify-blob --key octopus-release.pub --signature " + release.SignatureFile + " " + release.ManifestFile,
	}
	for _, name := range m.ImageNames() {
		cmds = append(cmds, verify+m.Images[name])
	}
	if m.Chart.Ref != "" && m.Chart.Digest != "" {
		repo := strings.TrimPrefix(m.Chart.Ref, "oci://")
		if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
			repo = repo[:i]
		}
		cmds = append(cmds, verify+repo+"@"+m.Chart.Digest)
	}
	return cmds
}

func backupAndDowntime(locale, mode string, external bool) (backup, downtime string) {
	switch {
	case mode == ModeKubernetes:
		backup = text(locale, "changepkg.backup.kubernetes")
		downtime = text(locale, "changepkg.downtime.kubernetes")
	case external:
		backup = text(locale, "changepkg.backup.external")
		downtime = text(locale, "changepkg.downtime.recreate")
	default:
		backup = text(locale, "changepkg.backup.bundled")
		downtime = text(locale, "changepkg.downtime.recreate")
	}
	return backup, downtime
}

func planCommands(m *release.Manifest, mode string, external bool) []string {
	//i18n:ignore shell command, not a message
	check := fmt.Sprintf("thulu update -check -version %s -o change-%s.md", m.Version, m.Version)
	//i18n:ignore shell command, not a message
	apply := "thulu update -version " + m.Version
	if len(m.ManualSteps) > 0 || (mode == ModeKubernetes && len(m.Requires) > 0) {
		apply += " -ack-manual-steps"
	}
	if external {
		apply += " -backup-confirmed"
	}
	return []string{check, apply}
}

func rollbackPlan(locale string, m *release.Manifest, from string, external bool) []string {
	switch {
	case from == "" || from == m.Version:
		return []string{text(locale, "changepkg.rollback.nothing")}
	case m.RollbackWithoutRestore(from):
		return []string{
			text(locale, "changepkg.rollback.images_auto", "from", from),
			text(locale, "changepkg.rollback.images_by_hand", "from", from),
		}
	case external:
		return []string{
			text(locale, "changepkg.rollback.external_restore", "from", from),
			text(locale, "changepkg.rollback.external_by_hand", "from", from, "restore", RestoreExternal),
		}
	default:
		return []string{
			text(locale, "changepkg.rollback.snapshot_auto", "from", from),
			text(locale, "changepkg.rollback.snapshot_by_hand", "from", from),
		}
	}
}

func (p *Package) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type md struct {
	b      bytes.Buffer
	locale string
}

func (m *md) w(format string, args ...any) { fmt.Fprintf(&m.b, format+"\n", args...) }

func (m *md) t(key string, args ...any) {
	m.b.WriteString(text(m.locale, key, args...))
	m.b.WriteByte('\n')
}

func (p *Package) Markdown() []byte {
	m := &md{locale: p.locale}
	from := p.From
	if from == "" {
		from = text(p.locale, "changepkg.doc.unknown_version")
	}
	p.mdHeader(m, from)
	p.mdBlockers(m)
	p.mdAdvisories(m, from)
	p.mdMigrations(m, from)
	p.mdConfig(m)
	p.mdFiles(m)
	p.mdPlan(m)
	p.mdImages(m)
	p.mdNotes(m)
	return m.b.Bytes()
}

func (p *Package) mdHeader(m *md, from string) {
	m.t("changepkg.doc.title", "from", from, "to", p.To)
	m.w("")
	m.w("| | |")
	m.w("|---|---|")
	m.t("changepkg.doc.row_generated", "at", p.GeneratedAt.Format(time.RFC3339))
	m.t("changepkg.doc.row_mode", "mode", p.Mode)
	m.t("changepkg.doc.row_from", "from", from)
	m.t("changepkg.doc.row_to", "to", p.To, "channel", p.Channel, "published", p.PublishedAt.Format("2006-01-02"))
	m.t("changepkg.doc.row_commit", "commit", p.Commit)
	if p.Source != "" {
		m.t("changepkg.doc.row_source", "source", cell(p.Source))
	}
	m.t("changepkg.doc.row_backup", "backup", p.Backup)
	m.t("changepkg.doc.row_downtime", "downtime", p.Downtime)
	m.w("")
}

func (p *Package) mdBlockers(m *md) {
	if len(p.Blockers) == 0 {
		return
	}
	m.t("changepkg.doc.blockers_heading")
	m.w("")
	m.t("changepkg.doc.blockers_intro")
	m.w("")
	for _, s := range p.Blockers {
		m.w("- %s", s)
	}
	m.w("")
}

func (p *Package) mdAdvisories(m *md, from string) {
	m.t("changepkg.doc.advisories_heading")
	m.w("")
	if len(p.Advisories) == 0 {
		m.t("changepkg.doc.advisories_none", "from", from)
	} else {
		m.t("changepkg.doc.advisories_columns")
		m.w("|---|---|---|")
		for _, a := range p.Advisories {
			m.w("| %s | %s | %s |", cell(a.ID), cell(a.Severity), cell(a.Summary))
		}
	}
	m.w("")
}

func (p *Package) mdMigrations(m *md, from string) {
	m.t("changepkg.doc.migrations_heading")
	m.w("")
	m.t("changepkg.doc.migrations_added", "count", p.Migrations.Added)
	m.t("changepkg.doc.migrations_contract", "count", p.Migrations.Contract)
	if p.RollbackWithoutRestore {
		m.t("changepkg.doc.migrations_back_images", "from", from)
	} else if p.From != "" && p.From != p.To {
		m.t("changepkg.doc.migrations_back_snapshot", "from", from)
	}
	m.w("")
}

func (p *Package) mdConfig(m *md) {
	m.t("changepkg.doc.config_heading")
	m.w("")
	if len(p.Requires) == 0 {
		m.t("changepkg.doc.requires_none")
	} else {
		m.t("changepkg.doc.requires", "names", code(p.Requires))
		if len(p.MissingEnv) > 0 {
			m.w("")
			m.t("changepkg.doc.missing_env", "names", code(p.MissingEnv))
		}
	}
	m.w("")
	if len(p.ManualSteps) == 0 {
		m.t("changepkg.doc.manual_steps_none")
	} else {
		m.t("changepkg.doc.manual_steps")
		m.w("")
		for i, s := range p.ManualSteps {
			m.w("%d. %s", i+1, s)
		}
	}
	m.w("")
}

func (p *Package) mdFiles(m *md) {
	if p.Files == nil {
		return
	}
	m.t("changepkg.doc.files_heading")
	m.w("")
	changed := false
	for _, c := range p.Files {
		if c.Kind == deployfiles.Unchanged {
			continue
		}
		changed = true
		m.w("- `deploy/%s`: %s", c.Path, c.Kind)
	}
	if !changed {
		m.t("changepkg.doc.files_none")
	}
	for _, c := range p.Files {
		if c.Diff == "" {
			continue
		}
		m.w("")
		m.w("````diff")
		m.b.WriteString(strings.TrimRight(c.Diff, "\n"))
		m.w("")
		m.w("````")
	}
	m.w("")
}

func (p *Package) mdPlan(m *md) {
	m.t("changepkg.doc.plan_heading")
	m.w("")
	m.w("```sh")
	for _, c := range p.Plan {
		m.w("%s", c)
	}
	m.w("```")
	m.w("")
	m.t("changepkg.doc.plan_explained", "version", p.To)
	m.w("")

	m.t("changepkg.doc.rollback_heading")
	m.w("")
	for _, s := range p.Rollback {
		m.w("%s", s)
	}
	m.w("")
}

func (p *Package) mdImages(m *md) {
	m.t("changepkg.doc.images_heading")
	m.w("")
	m.t("changepkg.doc.images_columns")
	m.w("|---|---|")
	for _, img := range p.Images {
		m.w("| %s | `%s` |", img.Name, img.Ref)
	}
	for _, img := range p.ThirdParty {
		m.t("changepkg.doc.image_third_party", "name", img.Name, "ref", img.Ref)
	}
	if p.Chart.Ref != "" {
		m.t("changepkg.doc.image_chart", "ref", p.Chart.Ref, "digest", p.Chart.Digest)
	}
	m.w("")
	m.t("changepkg.doc.verify_intro")
	m.w("")
	m.w("```sh")
	for _, c := range p.Verify {
		m.w("%s", c)
	}
	m.w("```")
	m.w("")
}

func (p *Package) mdNotes(m *md) {
	if p.Notes == "" && p.NotesURL == "" {
		return
	}
	m.t("changepkg.doc.notes_heading")
	m.w("")
	if p.NotesURL != "" {
		m.w("%s", p.NotesURL)
		m.w("")
	}
	if p.Notes != "" {
		m.t("changepkg.doc.notes_unsigned")
		m.w("")
		for _, line := range strings.Split(p.Notes, "\n") {
			m.w("> %s", line)
		}
	}
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

func code(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return strings.Join(out, ", ")
}

func isVersion(v string) bool {
	_, ok := semver.Parse(v)
	return ok
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
