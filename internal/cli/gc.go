package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

type GCOptions struct {
	Root      string
	Apply     bool
	MinAge    time.Duration
	Unclaimed bool
}

func GC(opts GCOptions) error {
	api, err := updater.OpenDocker()
	if err != nil {
		return err
	}
	defer api.Close()
	return runGC(context.Background(), os.Stdout, api, opts)
}

func runGC(ctx context.Context, out io.Writer, api dockergc.API, opts GCOptions) error {
	host, err := describeHost(ctx, out, api, opts.Root)
	if err != nil {
		return err
	}
	host.gc.ContainerMinAge = opts.MinAge
	host.gc.PruneUnclaimed = opts.Unclaimed
	plan, err := dockergc.Scan(ctx, api, host.gc)
	if err != nil {
		return err
	}
	backups, err := host.oldBackups()
	if err != nil {
		return err
	}
	printPlan(out, plan, backups)
	printKept(out, plan)
	if plan.Empty() && len(backups) == 0 {
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.gc_nothing_to_remove"))
		return nil
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.gc_reclaimable", "amount", reclaimable(plan, backups)))
	if !opts.Apply {
		fmt.Fprintln(out, i18n.Text("cli.cli.gc_dry_run"))
		return nil
	}
	res := dockergc.Apply(ctx, api, plan)
	removedBackups, backupErr := host.pruneBackups()
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.gc_removed",
		"containers", res.Containers, "images", res.Images, "volumes", res.Volumes, "networks", res.Networks,
		"backups", len(removedBackups), "freed", dockergc.HumanBytes(res.ImageBytes+backupBytes(removedBackups))))
	if err := res.Err(); err != nil {
		return apperr.Wrap(err, "cli.errors.some_objects_not_removed")
	}
	return backupErr
}

type hostView struct {
	in     *updater.Install
	gc     dockergc.Options
	pinned string
}

func describeHost(ctx context.Context, out io.Writer, api dockergc.API, rootFlag string) (*hostView, error) {
	root, err := resolveRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.host_no_install"))
		return &hostView{gc: dockergc.Options{
			Keep:       dockergc.KeepFromEnv(os.Getenv),
			Registries: dockergc.RegistriesFromEnv(os.Getenv),
		}}, nil
	}
	in, err := updater.Detect(ctx, root)
	if err != nil {
		return nil, err
	}
	gcOpts, err := in.GCOptions(ctx, api)
	if err != nil {
		return nil, err
	}
	h := &hostView{in: in, gc: gcOpts}
	if state, stateErr := updater.LoadState(root); stateErr == nil && state != nil {
		h.pinned = state.BackupPath
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.host_install", "install", in, "root", root))
	projects := i18n.Text("cli.cli.host_projects_none")
	if len(gcOpts.ComposeProjects) > 0 {
		projects = strings.Join(gcOpts.ComposeProjects, ", ")
	}
	fmt.Fprintln(out, i18n.Text("cli.cli.host_projects", "projects", projects))
	if len(gcOpts.Rollback) > 0 {
		images, until := strings.Join(gcOpts.Rollback, ", "), gcOpts.RollbackUntil.Format(time.DateOnly)
		line := i18n.Text("cli.cli.host_rollback_kept", "images", images, "date", until)
		if time.Now().After(gcOpts.RollbackUntil) {
			line = i18n.Text("cli.cli.host_rollback_expired", "images", images, "date", until)
		}
		fmt.Fprintln(out, line)
	}
	return h, nil
}

func (h *hostView) backupDir() string {
	if h.in == nil {
		return ""
	}
	return filepath.Join(h.in.Root, "backups")
}

func (h *hostView) backupKeep() int {
	return updater.BackupKeep(h.in.Getenv())
}

func (h *hostView) oldBackups() ([]updater.BackupFile, error) {
	if h.in == nil {
		return nil, nil
	}
	old, err := updater.BackupsBeyondRetention(h.backupDir(), h.backupKeep(), h.pinned)
	if err != nil {
		return nil, apperr.Wrap(err, "cli.errors.list_backups")
	}
	return old, nil
}

func (h *hostView) pruneBackups() ([]updater.BackupFile, error) {
	if h.in == nil {
		return nil, nil
	}
	removed, err := updater.PruneBackups(h.backupDir(), h.backupKeep(), h.pinned)
	if err != nil {
		return removed, apperr.Wrap(err, "cli.errors.prune_backups")
	}
	return removed, nil
}

func printPlan(out io.Writer, plan *dockergc.Plan, backups []updater.BackupFile) {
	section := func(title string, n int, extra string) *tabwriter.Writer {
		fmt.Fprintf(out, "\n%s (%d%s)\n", title, n, extra)
		return tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	}
	w := section(i18n.Text("cli.cli.section_containers"), len(plan.Containers), "")
	for _, c := range plan.Containers {
		fmt.Fprintf(w, "   %s\t%s\t%s\t%s\n", c.Name, c.Kind.Label(), c.State, c.Image)
	}
	flush(w)
	extra := ""
	if len(plan.Images) > 0 {
		extra = i18n.Text("cli.cli.section_images_up_to", "size", dockergc.HumanBytes(plan.ImageBytes()))
	}
	w = section(i18n.Text("cli.cli.section_images"), len(plan.Images), extra)
	for _, img := range plan.Images {
		size := dockergc.HumanBytes(img.Size)
		if img.Shared {
			size = i18n.Text("cli.cli.image_untag_only")
		}
		fmt.Fprintf(w, "   %s\t%s\t%s\n", imageLabel(img), size, img.Reason)
	}
	flush(w)
	w = section(i18n.Text("cli.cli.section_volumes"), len(plan.Volumes), "")
	for _, v := range plan.Volumes {
		fmt.Fprintf(w, "   %s\t%s\n", v.Name, v.Reason)
	}
	flush(w)
	w = section(i18n.Text("cli.cli.section_networks"), len(plan.Networks), "")
	for _, n := range plan.Networks {
		fmt.Fprintf(w, "   %s\n", n.Name)
	}
	flush(w)
	if backups == nil {
		return
	}
	w = section(i18n.Text("cli.cli.section_backups"), len(backups), ", "+dockergc.HumanBytes(backupBytes(backups)))
	for _, b := range backups {
		fmt.Fprintf(w, "   %s\t%s\t%s\n", b.Path, dockergc.HumanBytes(b.Size), b.ModTime.Format(time.DateOnly))
	}
	flush(w)
}

func printKept(out io.Writer, plan *dockergc.Plan) {
	if len(plan.Kept) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.section_keeping"))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, img := range plan.Kept {
		fmt.Fprintf(w, "   %s\t%s\t%s\n", imageLabel(img), dockergc.HumanBytes(img.Size), img.Reason)
	}
	flush(w)
}

func imageLabel(img dockergc.ImageItem) string {
	if len(img.Refs) == 0 {
		return "<none> " + img.Name()
	}
	return strings.Join(img.Refs, ", ")
}

func reclaimable(plan *dockergc.Plan, backups []updater.BackupFile) string {
	return i18n.Text("cli.cli.gc_reclaimable_amount", "size", dockergc.HumanBytes(plan.ImageBytes()+backupBytes(backups)))
}

func backupBytes(files []updater.BackupFile) int64 {
	var n int64
	for _, b := range files {
		n += b.Size
	}
	return n
}

func flush(w *tabwriter.Writer) { w.Flush() }
