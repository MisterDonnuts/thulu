package updater

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/changepkg"
	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

const maxReport = 8 << 20

func (u *updateRun) check(ctx context.Context, latest bool) error {
	version, err := u.targetVersion(ctx, latest)
	if err != nil {
		return err
	}
	m, err := u.manifest(ctx, version)
	if err != nil {
		return err
	}
	state, err := LoadState(u.in.Root)
	if err != nil {
		return err
	}
	pkg := changepkg.Build(m, u.facts(ctx, m, u.installedVersion(state)))

	switch ext := strings.ToLower(filepath.Ext(u.opts.Output)); {
	case u.opts.Output == "":
		_, err = u.out.Write(pkg.Markdown())
		return err
	case ext == ".json":
		body, err := pkg.JSON()
		if err != nil {
			return err
		}
		return u.writeReport(body)
	case ext == ".md" || ext == ".markdown":
		return u.writeReport(pkg.Markdown())
	default:
		return apperr.Invalid("updater.errors.report_format", "path", u.opts.Output)
	}
}

// 0600: a diff of an edited deploy file may carry secrets.
func (u *updateRun) writeReport(body []byte) error {
	if len(body) > maxReport {
		return apperr.TooLarge("updater.errors.report_too_large", "size", len(body), "limit", maxReport)
	}
	if err := fsx.WriteFile(u.opts.Output, body, 0o600); err != nil {
		return apperr.Wrap(err, "updater.errors.write_path", "path", u.opts.Output)
	}
	u.say("updater.cli.report_written", "path", u.opts.Output)
	return nil
}

func (u *updateRun) facts(ctx context.Context, m *release.Manifest, current string) changepkg.Facts {
	f := changepkg.Facts{
		From:             current,
		Mode:             changepkg.ModeDocker,
		ExternalDatabase: u.externalDatabase(),
		Notes:            u.listing(ctx, m.Version).Notes,
		GeneratedAt:      u.deps.now(),
	}
	if src, err := u.source(); err == nil {
		f.Source = src.Name()
	}
	if u.in.Mode == initwizard.ModeKubernetes {
		f.Mode = changepkg.ModeKubernetes
		return f
	}
	f.MissingEnv = []string{}
	for _, k := range m.Requires {
		if strings.TrimSpace(u.getenv(k)) == "" {
			f.MissingEnv = append(f.MissingEnv, k)
		}
	}
	if u.in.HasSource() {
		return f
	}
	tree, err := u.bundle(ctx, m)
	if err != nil {
		u.say("updater.cli.files_not_compared", "err", apperr.Text(err))
		return f
	}
	rec, err := deployfiles.LoadRecord(u.in.Root)
	if err != nil {
		return f
	}
	var baseline map[string]string
	if rec == nil && isVersion(current) && current != m.Version {
		baseline = u.baselineHashes(ctx, current)
	}
	if plan, err := deployfiles.NewPlan(u.in.Root, rec, baseline, tree, m.Version); err == nil {
		f.Files = plan.Changes
	}
	return f
}
