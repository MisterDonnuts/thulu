package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

type UninstallOptions struct {
	Root      string
	Yes       bool
	PurgeData bool
}

func Uninstall(opts UninstallOptions) error {
	api, err := updater.OpenDocker()
	if err != nil {
		return err
	}
	defer api.Close()
	root, err := resolveRoot(opts.Root)
	if err != nil {
		root = ""
	}
	runner := initwizard.ExecRunner{Dir: root, Out: os.Stdout}
	return runUninstall(context.Background(), os.Stdout, api, runner, opts)
}

var generatedFiles = []string{
	initwizard.AnswersFile,
	updater.StateFile,
	"deploy/docker-compose.nologin.yml",
}

func runUninstall(ctx context.Context, out io.Writer, api dockergc.API, runner initwizard.Runner, opts UninstallOptions) error {
	host, err := describeHost(ctx, out, api, opts.Root)
	if err != nil {
		return err
	}
	if host.in != nil && host.in.Mode == initwizard.ModeKubernetes {
		printHelmUninstall(out, host.in)
		return nil
	}
	if opts.PurgeData {
		printPurgeWarning(out, host)
	}
	down := composeDown(host)
	host.gc.Uninstall, host.gc.PurgeData = true, opts.PurgeData
	plan, err := dockergc.Scan(ctx, api, host.gc)
	if err != nil {
		return err
	}
	remove, keep := uninstallFiles(host, opts.PurgeData)
	remove, keep = withRunnerFiles(remove, keep, opts.PurgeData)
	svcFile := runnerServiceFile(ctx)

	if svcFile != "" {
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.uninstall_service", "file", svcFile))
	}
	if down != nil {
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.uninstall_stack", "command", strings.Join(down, " ")))
	}
	printPlan(out, plan, nil)
	printFiles(out, remove, keep)
	if !opts.Yes {
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.uninstall_dry_run"))
		printBinary(out)
		return nil
	}

	var errs []error
	// The runner service goes first, or it keeps claiming runs from the stopping daemon and writing
	// into the work dir being removed.
	if svcFile != "" {
		_, err = uninstallRunnerService(ctx)
		errs = append(errs, err)
	}
	if down != nil {
		// Down before Apply so the stack stops first; a failure is not fatal since the plan removes
		// the same objects by label.
		if downErr := runner.Run(ctx, "docker", down...); downErr != nil {
			fmt.Fprintln(out, i18n.Text("cli.cli.uninstall_compose_down_failed", "err", apperr.Text(downErr)))
		}
		if plan, err = dockergc.Scan(ctx, api, host.gc); err != nil {
			return err
		}
	}
	res := dockergc.Apply(ctx, api, plan)
	errs = append(errs, res.Errors...)
	for _, path := range remove {
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, apperr.Wrap(err, "cli.errors.remove_path", "path", path))
		}
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.uninstall_removed",
		"containers", res.Containers, "images", res.Images, "volumes", res.Volumes, "networks", res.Networks, "files", len(remove)))
	printBinary(out)
	if err := errors.Join(errs...); err != nil {
		return apperr.Wrap(err, "cli.errors.some_objects_not_removed")
	}
	return nil
}

// No -v: whether volumes go is the plan's decision, made with -purge-data.
func composeDown(host *hostView) []string {
	if host.in == nil || host.in.ComposeFile == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(host.in.Root, host.in.ComposeFile)); err != nil {
		return nil
	}
	return []string{"compose", "-f", host.in.ComposeFile, "down", "--remove-orphans"}
}

// deploy/.env and backups/ stay unless data is purged: .env holds the database password and the
// credentials decryption key the kept volumes need.
func uninstallFiles(host *hostView, purge bool) (remove, keep []string) {
	if host.in == nil {
		return nil, nil
	}
	root := host.in.Root
	env := filepath.Join(root, "deploy", ".env")
	backups := filepath.Join(root, "backups")
	if home, err := defaultHome(); err == nil && root == home && !hasSource(root) {
		if purge {
			return existing(root), nil
		}
		remove = existing(entriesExcept(root, "backups", "deploy")...)
		remove = append(remove, existing(entriesExcept(filepath.Join(root, "deploy"), ".env")...)...)
		return remove, existing(env, backups)
	}
	for _, rel := range generatedFiles {
		remove = append(remove, existing(filepath.Join(root, rel))...)
	}
	if values, err := filepath.Glob(filepath.Join(root, "deploy", "values-*.yaml")); err == nil {
		remove = append(remove, values...)
	}
	if purge {
		return append(remove, existing(env, backups)...), nil
	}
	return remove, existing(env, backups)
}

// Trust Installed even when Query errs: a service manager that cannot answer still leaves its file.
func runnerServiceFile(ctx context.Context) string {
	st, err := queryRunnerService(ctx)
	if st.Installed || (err == nil && st.Loaded) {
		return st.File
	}
	return ""
}

// The config (the credential the kept database knows) and work dir go only with -purge-data.
// A work_dir the config moved elsewhere is the person's folder: never touch it.
func withRunnerFiles(remove, keep []string, purge bool) (removeOut, keepOut []string) {
	config, err := hostrun.DefaultConfigPath()
	if err != nil {
		return remove, keep
	}
	if abs, absErr := filepath.Abs(config); absErr == nil {
		config = abs
	}
	data := existing(config, filepath.Join(filepath.Dir(config), "work"))
	// A home install in the runner's folder lists these among its entries; strip them so only
	// -purge-data removes them.
	remove = slices.DeleteFunc(remove, func(p string) bool { return slices.Contains(data, p) })
	add := hostrun.LogFiles(service.LogPath(config))
	if purge {
		add = append(add, data...)
	} else {
		keep = append(keep, data...)
	}
	for _, p := range add {
		if !slices.ContainsFunc(remove, func(r string) bool { return p == r || strings.HasPrefix(p, r+string(filepath.Separator)) }) {
			remove = append(remove, p)
		}
	}
	return remove, keep
}

func entriesExcept(dir string, skip ...string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		keep := false
		for _, s := range skip {
			keep = keep || e.Name() == s
		}
		if !keep {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func existing(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func printFiles(out io.Writer, remove, keep []string) {
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.section_files", "count", len(remove)))
	for _, p := range remove {
		fmt.Fprintf(out, "   %s\n", p)
	}
	if len(keep) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.section_kept_without_purge"))
	for _, p := range keep {
		fmt.Fprintf(out, "   %s\n", p)
	}
}

func printPurgeWarning(out io.Writer, host *hostView) {
	backups := "backups/"
	if host.in != nil {
		backups = filepath.Join(host.in.Root, "backups")
	}
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.purge_warning", "backups", backups))
}

func printHelmUninstall(out io.Writer, in *updater.Install) {
	fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.helm_uninstall_intro"))
	//i18n:ignore shell commands the operator copies, not a message
	fmt.Fprintf(out, "\n   helm uninstall %s --namespace %s\n", in.Release, in.Namespace)
	//i18n:ignore shell command the operator copies, not a message
	fmt.Fprintf(out, "   kubectl delete namespace %s octopus-sandboxes\n", in.Namespace)
	//i18n:ignore shell command the operator copies, not a message
	fmt.Fprintln(out, "   kubectl delete namespace -l thulu.dev/plane=data")
	values := filepath.Join(in.Root, "deploy", fmt.Sprintf("values-%s.yaml", in.Release))
	if _, err := os.Stat(values); err == nil {
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.helm_delete_values", "path", values))
	}
	fmt.Fprintln(out, i18n.Text("cli.cli.helm_external_database"))
}

func printBinary(out io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, evalErr := filepath.EvalSymlinks(exe); evalErr == nil {
		exe = resolved
	}
	fmt.Fprintln(out, i18n.Text("cli.cli.binary_stays", "path", exe))
}
