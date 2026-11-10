package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/cli"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	configureLocale(os.Args[1])

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])

	case "open":
		err = runOpen(os.Args[2:])

	case "status", "ps":
		err = cli.Status()

	case "logs":
		err = runLogs(os.Args[2:])

	case "stop":
		err = runStop(os.Args[2:])

	case "agents":
		err = runAgents(os.Args[2:])

	case "export":
		err = runExport(os.Args[2:])

	case "import":
		err = runImport(os.Args[2:])

	case "init":
		err = runInit(os.Args[2:])

	case "update", "apply", "mirror":
		err = runChange(os.Args[1], os.Args[2:])

	case "gc":
		err = runGC(os.Args[2:])

	case "uninstall":
		err = runUninstall(os.Args[2:])

	case "workspace":
		err = runWorkspace(os.Args[2:])

	case "keys":
		err = runKeys(os.Args[2:])

	case "runner":
		err = runRunner(os.Args[2:])

	case "version", "-v", "--version":
		fmt.Println(buildinfo.Version)
		return

	case "help", "-h", "--help":
		usage()
		return

	default:
		fmt.Fprintf(os.Stderr, "%s\n\n", i18n.Text("main.cli.unknown_command", "command", os.Args[1]))
		usage()
		os.Exit(1)
	}

	exitOn(err)
}

func exitOn(err error) {
	switch {
	case err == nil:
	case errors.Is(err, initwizard.ErrAborted):
		os.Exit(130)
	default:
		fmt.Fprintln(os.Stderr, i18n.Text("main.cli.error", "err", apperr.Text(err)))
		os.Exit(1)
	}
}

func runOpen(args []string) error {
	openCmd := flag.NewFlagSet("open", flag.ExitOnError)
	root := openCmd.String("root", "", i18n.Text("main.flags.root"))
	if err := openCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "open")
	}
	return cli.Open(*root)
}

func runLogs(args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, i18n.Text("main.cli.usage_logs"))
		os.Exit(1)
	}
	follow := len(args) > 1 && args[1] == "-f"
	return cli.Logs(args[0], follow)
}

func runStop(args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, i18n.Text("main.cli.usage_stop"))
		os.Exit(1)
	}
	return cli.Stop(args[0])
}

func runAgents(args []string) error {
	if len(args) == 0 {
		return apperr.Invalid("main.errors.usage_agents")
	}
	switch args[0] {
	case "import":
		//i18n:ignore flag set name: the subcommand as typed, not a message
		importCmd := flag.NewFlagSet("agents import", flag.ExitOnError)
		templates := importCmd.Bool("templates", false, i18n.Text("main.flags.agents_templates"))
		ws := importCmd.String("workspace", "", i18n.Text("main.flags.agents_workspace"))
		if err := importCmd.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "import")
		}
		root := importCmd.Arg(0)
		if root == "" {
			return apperr.Invalid("main.errors.usage_agents_import")
		}
		return cli.ImportAgents(root, *ws, *templates)
	case "verify":
		//i18n:ignore flag set name: the subcommand as typed, not a message
		verifyCmd := flag.NewFlagSet("agents verify", flag.ExitOnError)
		ws := verifyCmd.String("workspace", "", i18n.Text("main.flags.agents_workspace"))
		if err := verifyCmd.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "verify")
		}
		root := verifyCmd.Arg(0)
		if root == "" {
			return apperr.Invalid("main.errors.usage_agents_verify")
		}
		return cli.VerifyAgents(root, *ws)
	case "gc":
		//i18n:ignore flag set name: the subcommand as typed, not a message
		gcCmd := flag.NewFlagSet("agents gc", flag.ExitOnError)
		keep := gcCmd.Int("keep", agentstore.DefaultKeepPerFile, i18n.Text("main.flags.agents_gc_keep"))
		if err := gcCmd.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "gc")
		}
		return cli.GCAgentContent(*keep)
	case "export":
		//i18n:ignore flag set name: the subcommand as typed, not a message
		exportCmd := flag.NewFlagSet("agents export", flag.ExitOnError)
		ws := exportCmd.String("workspace", "", i18n.Text("main.flags.agents_workspace"))
		if err := exportCmd.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "export")
		}
		return cli.ExportAgents(*ws)
	}
	return apperr.Invalid("main.errors.unknown_agents_subcommand", "name", args[0])
}

func runInit(args []string) error {
	initCmd := flag.NewFlagSet("init", flag.ExitOnError)
	root := initCmd.String("root", "", i18n.Text("main.flags.init_root"))
	dryRun := initCmd.Bool("dry-run", false, i18n.Text("main.flags.init_dry_run"))
	nonInteractive := initCmd.Bool("non-interactive", false, i18n.Text("main.flags.init_non_interactive"))
	plain := initCmd.Bool("plain", false, i18n.Text("main.flags.init_plain"))
	stack := initCmd.String("stack", "", i18n.Text("main.flags.init_stack"))
	if err := initCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "init")
	}
	return cli.Init(cli.InitFlags{Root: *root, DryRun: *dryRun, NonInteractive: *nonInteractive, Plain: *plain, Stack: *stack})
}

type changeFlags struct {
	root, version, registry, chartRegistry, apiKeyFile, operator, values *string
	ackManual, backupConfirmed, noFetchCLI, delegated, yes, reuseValues  *bool
}

func runChange(cmd string, args []string) error {
	switch cmd {
	case "apply":
		return runApply(args)
	case "mirror":
		return runMirror(args)
	}
	return runUpdate(args)
}

func addChangeFlags(fs *flag.FlagSet) *changeFlags {
	return &changeFlags{
		root:            fs.String("root", "", i18n.Text("main.flags.root")),
		version:         fs.String("version", "", i18n.Text("main.flags.change_version")),
		registry:        fs.String("registry", "", i18n.Text("main.flags.change_registry")),
		chartRegistry:   fs.String("chart-registry", "", i18n.Text("main.flags.change_chart_registry")),
		apiKeyFile:      fs.String("api-key-file", "", i18n.Text("main.flags.change_api_key_file")),
		operator:        fs.String("operator", "", i18n.Text("main.flags.change_operator")),
		ackManual:       fs.Bool("ack-manual-steps", false, i18n.Text("main.flags.change_ack_manual_steps")),
		backupConfirmed: fs.Bool("backup-confirmed", false, i18n.Text("main.flags.change_backup_confirmed")),
		noFetchCLI:      fs.Bool("no-fetch-cli", false, i18n.Text("main.flags.change_no_fetch_cli")),
		delegated:       fs.Bool("delegated", false, i18n.Text("main.flags.change_delegated")),
		yes:             fs.Bool("yes", false, i18n.Text("main.flags.change_yes")),
		values:          fs.String("values", "", i18n.Text("main.flags.change_values")),
		reuseValues:     fs.Bool("reuse-values", false, i18n.Text("main.flags.change_reuse_values")),
	}
}

func (f *changeFlags) options() (updater.Options, error) {
	opts := updater.Options{
		Version:         *f.version,
		Registry:        *f.registry,
		ChartRegistry:   *f.chartRegistry,
		Operator:        *f.operator,
		AckManualSteps:  *f.ackManual,
		BackupConfirmed: *f.backupConfirmed,
		NoFetchCLI:      *f.noFetchCLI,
		Delegated:       *f.delegated,
		Yes:             *f.yes,
		Values:          *f.values,
		ReuseValues:     *f.reuseValues,
	}
	if *f.apiKeyFile != "" {
		// A file, never a flag value: argv is readable by every user on the host through ps.
		b, err := os.ReadFile(*f.apiKeyFile)
		if err != nil {
			return opts, apperr.Wrap(err, "main.errors.read_api_key_file")
		}
		opts.APIKey = strings.TrimSpace(string(b))
	}
	return opts, nil
}

// Runs from the host: the process that replaces the container cannot be the one being replaced.
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	common := addChangeFlags(fs)
	check := fs.Bool("check", false, i18n.Text("main.flags.update_check"))
	out := fs.String("o", "", i18n.Text("main.flags.change_output"))
	to := fs.String("to", "", i18n.Text("main.flags.update_to"))
	restore := fs.String("restore", "", i18n.Text("main.flags.update_restore"))
	rollback := fs.Bool("rollback", false, i18n.Text("main.flags.update_rollback"))
	branch := fs.String("branch", "", i18n.Text("main.flags.update_branch"))
	keep := fs.String("keep", "", i18n.Text("main.flags.update_keep"))
	unkeep := fs.String("unkeep", "", i18n.Text("main.flags.update_unkeep"))
	listKept := fs.Bool("kept", false, i18n.Text("main.flags.update_kept"))
	if err := fs.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "update")
	}
	opts, err := common.options()
	if err != nil {
		return err
	}
	opts.Check, opts.Output = *check, *out
	opts.To, opts.Restore, opts.Rollback = *to, *restore, *rollback
	opts.Branch, opts.Keep, opts.Unkeep, opts.ListKept = *branch, *keep, *unkeep, *listKept
	return cli.Update(*common.root, opts)
}

func runApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	common := addChangeFlags(fs)
	check := fs.Bool("check", false, i18n.Text("main.flags.apply_check"))
	out := fs.String("o", "", i18n.Text("main.flags.change_output"))
	if err := fs.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "apply")
	}
	opts, err := common.options()
	if err != nil {
		return err
	}
	opts.Check, opts.Output = *check, *out
	return cli.Apply(*common.root, opts)
}

func runMirror(args []string) error {
	fs := flag.NewFlagSet("mirror", flag.ExitOnError)
	to := fs.String("to", "", i18n.Text("main.flags.mirror_to"))
	chartTo := fs.String("chart-to", "", i18n.Text("main.flags.mirror_chart_to"))
	dir := fs.String("dir", "", i18n.Text("main.flags.mirror_dir"))
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), i18n.Text("main.cli.usage_mirror"))
		fs.PrintDefaults()
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fs.Usage()
		return apperr.Invalid("main.errors.mirror_needs_version")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "mirror")
	}
	return cli.Mirror(updater.MirrorOptions{Version: args[0], Registry: *to, ChartRegistry: *chartTo, Dir: *dir})
}

func runGC(args []string) error {
	opts, err := parseGCArgs(args)
	if err != nil {
		return err
	}
	return cli.GC(opts)
}

func parseGCArgs(args []string) (cli.GCOptions, error) {
	gcCmd := flag.NewFlagSet("gc", flag.ExitOnError)
	root := gcCmd.String("root", "", i18n.Text("main.flags.root_or_home"))
	apply := gcCmd.Bool("apply", false, i18n.Text("main.flags.gc_apply"))
	minAge := gcCmd.Duration("min-age", time.Hour, i18n.Text("main.flags.gc_min_age"))
	unclaimed := gcCmd.Bool("unclaimed", false, i18n.Text("main.flags.gc_unclaimed"))
	gcCmd.Usage = func() {
		fmt.Fprint(gcCmd.Output(), i18n.Text("main.cli.usage_gc"))
		gcCmd.PrintDefaults()
	}
	if err := gcCmd.Parse(args); err != nil {
		return cli.GCOptions{}, apperr.Wrap(err, "main.errors.parse_flags", "command", "gc")
	}
	return cli.GCOptions{Root: *root, Apply: *apply, MinAge: *minAge, Unclaimed: *unclaimed}, nil
}

func runUninstall(args []string) error {
	opts, err := parseUninstallArgs(args)
	if err != nil {
		return err
	}
	return cli.Uninstall(opts)
}

func parseUninstallArgs(args []string) (cli.UninstallOptions, error) {
	unCmd := flag.NewFlagSet("uninstall", flag.ExitOnError)
	root := unCmd.String("root", "", i18n.Text("main.flags.root_or_home"))
	yes := unCmd.Bool("yes", false, i18n.Text("main.flags.uninstall_yes"))
	purge := unCmd.Bool("purge-data", false, i18n.Text("main.flags.uninstall_purge_data"))
	unCmd.Usage = func() {
		fmt.Fprint(unCmd.Output(), i18n.Text("main.cli.usage_uninstall"))
		unCmd.PrintDefaults()
	}
	if err := unCmd.Parse(args); err != nil {
		return cli.UninstallOptions{}, apperr.Wrap(err, "main.errors.parse_flags", "command", "uninstall")
	}
	return cli.UninstallOptions{Root: *root, Yes: *yes, PurgeData: *purge}, nil
}

func runWorkspace(args []string) error {
	if len(args) == 0 {
		return apperr.Invalid("main.errors.usage_workspace")
	}
	switch args[0] {
	case "list":
		return cli.ListWorkspaces()
	case "create":
		//i18n:ignore flag set name: the subcommand as typed, not a message
		createCmd := flag.NewFlagSet("workspace create", flag.ExitOnError)
		name := createCmd.String("name", "", i18n.Text("main.flags.workspace_name"))
		slug := createCmd.String("slug", "", i18n.Text("main.flags.workspace_slug"))
		owner := createCmd.String("owner-email", "", i18n.Text("main.flags.workspace_owner_email"))
		if err := createCmd.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "workspace")
		}
		return cli.CreateWorkspace(context.Background(), *name, *slug, *owner)
	}
	return apperr.Invalid("main.errors.unknown_workspace_subcommand", "name", args[0])
}

func runKeys(args []string) error {
	if len(args) == 0 {
		return apperr.Invalid("main.errors.usage_keys")
	}
	switch args[0] {
	case "workspaces":
		// Alias of `workspace list`; scripts still call it here.
		return cli.ListWorkspaces()
	case "mint":
		//i18n:ignore flag set name: the subcommand as typed, not a message
		mintCmd := flag.NewFlagSet("keys mint", flag.ExitOnError)
		name := mintCmd.String("name", i18n.Text("main.labels.default_key_name"), i18n.Text("main.flags.keys_name"))
		workspace := mintCmd.String("workspace", "", i18n.Text("main.flags.keys_workspace"))
		role := mintCmd.String("role", "developer", i18n.Text("main.flags.keys_role"))
		perms := mintCmd.String("permissions", "", i18n.Text("main.flags.keys_permissions"))
		days := mintCmd.Int("expires-in-days", 0, i18n.Text("main.flags.keys_expires_in_days"))
		if err := mintCmd.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "mint")
		}
		list := []string{}
		for _, p := range strings.Split(*perms, ",") {
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, p)
			}
		}
		return cli.MintKey(context.Background(), *name, *workspace, *role, list, *days)
	}
	return apperr.Invalid("main.errors.unknown_keys_subcommand", "name", args[0])
}

func runRunner(args []string) error {
	if len(args) == 0 {
		return apperr.Invalid("main.errors.usage_runner")
	}
	//i18n:ignore flag set name: the subcommand as typed, not a message
	fs := flag.NewFlagSet("runner "+args[0], flag.ExitOnError)
	defPath, err := hostrun.DefaultConfigPath()
	if err != nil {
		return err
	}
	config := fs.String("config", defPath, i18n.Text("main.flags.runner_config"))
	switch args[0] {
	case "register":
		url := fs.String("url", envOr("OCTOPUS_URL", "http://localhost:8585"), i18n.Text("main.flags.runner_url"))
		token := fs.String("token", "", i18n.Text("main.flags.runner_token"))
		if err := fs.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner register") //i18n:ignore the subcommand as typed
		}
		cfg, err := hostrun.Register(context.Background(), *config, *url, *token, buildinfo.Version)
		if err != nil {
			return err
		}
		fmt.Println(i18n.Text("main.cli.runner_registered", "name", cfg.Name, "id", cfg.RunnerID, "path", *config))
		return nil
	case "add-project":
		if err := fs.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner add-project") //i18n:ignore the subcommand as typed
		}
		if fs.NArg() != 2 {
			return apperr.Invalid("main.errors.usage_runner")
		}
		return hostrun.AddProject(*config, fs.Arg(0), fs.Arg(1))
	case "service":
		if err := fs.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner service") //i18n:ignore the subcommand as typed
		}
		return cli.RunnerService(context.Background(), os.Stdout, fs.Arg(0), *config)
	case "run":
		logPath := fs.String("log", "", i18n.Text("main.flags.runner_log"))
		if err := fs.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner run") //i18n:ignore the subcommand as typed
		}
		return runHostRunner(*config, *logPath)
	}
	return apperr.Invalid("main.errors.usage_runner")
}

func runHostRunner(config, logPath string) (err error) {
	if logPath != "" {
		l, lerr := hostrun.OpenLog(logPath)
		if lerr != nil {
			return lerr
		}
		log.SetOutput(l)
		// launchd discards stderr, where main prints the error the runner stops with.
		defer func() {
			if err != nil {
				log.Print(i18n.Log("main.cli.error", "err", err))
			}
		}()
	}
	cfg, err := hostrun.LoadConfig(config)
	if err != nil {
		return err
	}
	if err := hostrun.Preflight(cfg); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return hostrun.New(cfg, buildinfo.Version).Run(ctx)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Println(i18n.Text("main.cli.usage"))
}

// The daemon ignores the terminal locale: its locale is a deployment setting.
func configureLocale(cmd string) {
	if v := os.Getenv("OCTOPUS_LOCALE"); v != "" {
		if !i18n.SetDaemonLocale(v) {
			log.Print(i18n.Log("cli.log.unknown_locale", "fallback", i18n.SourceLocale))
		}
		return
	}
	if startsDaemon(cmd) {
		return
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			i18n.SetDaemonLocale(i18n.Negotiate(v))
			return
		}
	}
}
