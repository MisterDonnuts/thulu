package initwizard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
)

type Options struct {
	Root           string
	DryRun         bool
	NonInteractive bool

	Prompter *Prompter
	Runner   Runner
	Out      io.Writer

	NoSource      bool
	BinaryVersion string

	LogPath string

	Stack string

	Detect func()
}

func Run(ctx context.Context, opts Options) error {
	w := &run{opts: opts, p: opts.Prompter, out: opts.Out, cfg: &Config{}}
	return w.execute(ctx)
}

type run struct {
	opts Options
	p    *Prompter
	out  io.Writer
	cfg  *Config

	sessionKey, sessionKeyID string
	wroteEnv                 bool

	env          *EnvFile
	envBefore    string
	envPath      string
	fromTemplate bool
	bootstrapped bool
}

func (w *run) execute(ctx context.Context) error {
	w.banner()
	if err := w.loadDefaults(); err != nil {
		return err
	}
	iv := &Interviewer{
		P: w.p, Cfg: w.cfg, Ctx: ctx, Existing: w.defaults(),
		NoSource: w.opts.NoSource, BinaryVersion: w.opts.BinaryVersion,
		HostArch: func() string { return DockerArch(ctx) },
		PortFree: func(port int) bool { return portFree(ctx, port) },
		Detect:   w.opts.Detect,
		Stack:    w.opts.Stack,
	}
	w.loadPrevious(iv)
	if err := iv.Run(); err != nil {
		if errors.Is(err, ErrAborted) {
			w.printf("")
			w.say("", "initwizard.cli.aborted")
		}
		return err
	}
	if err := w.ensureSecret(); err != nil {
		return err
	}

	ok, err := w.confirm()
	if errors.Is(err, ErrAborted) || (err == nil && !ok) {
		w.printf("")
		w.say("", "initwizard.cli.aborted")
		return err
	}
	if err != nil {
		return err
	}
	if w.opts.DryRun {
		w.printf("")
		w.say("", "initwizard.cli.dry_run")
		return nil
	}
	w.p.Total = 0
	w.p.Section(0, i18n.Text("initwizard.sections.install.title"), i18n.Text("initwizard.sections.install.blurb"))

	// Write the config before anything starts: the stacks refuse to interpolate without the
	// Postgres
	// password and socket group, even for the lone `compose up pg` the bootstrap runs.
	if err := w.writeConfig(); err != nil {
		return err
	}
	if err := w.prepareDocker(ctx); err != nil {
		return err
	}
	if err := w.bootstrapDatabase(ctx); err != nil {
		return err
	}
	// Revoke even when the stack fails to start: nobody holds this key and it would stay live for
	// 90 days.
	defer w.revokeSessionKey(ctx)
	if err := w.writeConfig(); err != nil {
		return err
	}
	// Save before the stack starts: `thulu update` reads this file to identify the stack,
	// and a failed `compose up` is exactly the install someone has to repair.
	if err := SaveAnswers(w.opts.Root, w.cfg); err != nil {
		return err
	}
	if err := w.startStack(ctx); err != nil {
		return err
	}
	w.postBoot(ctx)
	w.summary()
	return SaveAnswers(w.opts.Root, w.cfg)
}

func (w *run) loadPrevious(iv *Interviewer) {
	previous, err := LoadAnswers(w.opts.Root)
	if err != nil {
		return
	}
	iv.PreviousMCP = previous.MCPServers
	iv.PreviousEngines = previous.Engines
	iv.PreviousRunner = previous.Runner
	iv.PreviousQuick = previous.Quick
	iv.PreviousMode = previous.Mode
	iv.PreviousCompose = previous.ComposeFile
	iv.PreviousWorkspace.Name = previous.WorkspaceName
	iv.PreviousWorkspace.Slug = previous.WorkspaceSlug
	iv.PreviousWorkspace.ID = previous.WorkspaceID
	if previous.Mode != ModeKubernetes {
		return
	}
	// Kubernetes answers live in the values file, not deploy/.env: without them a rerun offers no
	// DSN, drops the license and loses the OIDC secret.
	iv.PreviousCluster.Namespace, iv.PreviousCluster.Release = previous.Namespace, previous.Release
	iv.PreviousCluster.RuntimeClass, iv.PreviousCluster.AgentImage = previous.RuntimeClass, previous.AgentImage
	if values := loadHelmEnv(w.opts.Root, previous.Release); values != nil {
		iv.Existing = values
	}
}

func (w *run) needsAPI() bool {
	return w.cfg.GitToken != "" || len(w.cfg.Credentials) > 0 || w.cfg.Runner.Enabled
}

func (w *run) revokeSessionKey(ctx context.Context) {
	if w.sessionKeyID == "" {
		return
	}
	boot, err := OpenBootstrap(w.cfg.BootstrapDSN)
	if err == nil {
		defer boot.Close()
		err = boot.RevokeKey(ctx, w.sessionKeyID)
	}
	if err != nil {
		w.warn(i18n.Text("initwizard.cli.session_key_left", "id", w.sessionKeyID, "err", apperr.Text(err)))
	}
}

func (w *run) printf(format string, args ...any) { fmt.Fprintf(w.out, format+"\n", args...) }

func (w *run) say(indent, key string, args ...any) {
	fmt.Fprintln(w.out, indentLines(indent, i18n.Text(key, args...)))
}

func (w *run) banner() {
	if w.p != nil && w.p.Interactive() {
		lines := strings.Split(strings.TrimSpace(i18n.Text("initwizard.cli.banner_tui")), "\n")
		if !w.p.Diagram(bannerArt(lines[0], lines[1:])) {
			w.say("", "initwizard.cli.banner_tui")
		}
		return
	}
	w.say("", "initwizard.cli.banner")
}

func (w *run) loadDefaults() error {
	w.envPath = filepath.Join(w.opts.Root, "deploy", ".env")
	env, err := LoadEnvFile(w.envPath)
	if err != nil {
		return err
	}
	// With no deploy/.env, edit .env.example (it keeps every variable's explanation),
	// but its values are placeholders and must never become this run's defaults.
	if env.Render() == "" {
		if seed, seedErr := LoadEnvFile(filepath.Join(w.opts.Root, "deploy", ".env.example")); seedErr == nil {
			env = seed
			w.fromTemplate = true
		}
	}
	w.env = env
	w.envBefore = env.Render()
	return nil
}

func (w *run) defaults() *EnvFile {
	if w.fromTemplate {
		return nil
	}
	return w.env
}

// Never regenerate existing secrets: a new JWT secret signs out every session,
// a new credential key makes every stored Git token and LLM key unreadable.
func (w *run) ensureSecret() error {
	jwt, box := w.previousSecrets()
	if jwt == "" {
		var err error
		if jwt, err = GenerateSecret(); err != nil {
			return err
		}
	}
	if box == "" {
		var err error
		if box, err = GenerateSecret(); err != nil {
			return err
		}
	}
	w.cfg.JWTSecret = jwt
	w.cfg.SecretboxKey = box
	return w.ensureDevToken()
}

func (w *run) ensureDevToken() error {
	if !w.fromTemplate && w.env != nil {
		if tok := w.env.Get(auth.DevTokenEnv); tok != "" {
			w.cfg.DevToken = tok
			return nil
		}
	}
	tok, err := GenerateSecret()
	w.cfg.DevToken = tok
	return err
}

func (w *run) previousSecrets() (jwt, box string) {
	if w.cfg.Mode == ModeKubernetes {
		return LoadHelmSecrets(w.opts.Root, w.cfg.Release)
	}
	if w.fromTemplate {
		return "", ""
	}
	return w.env.Get("OCTOPUS_JWT_SECRET"), w.env.Get("OCTOPUS_SECRETBOX_KEY")
}

func (w *run) confirm() (bool, error) {
	w.p.Total = 0
	w.p.Section(0, i18n.Text("initwizard.sections.review.title"), "")
	w.review()
	if !w.p.Interactive() {
		w.printf("")
		w.details()
		w.printf("")
		ok, err := w.p.YesNo(i18n.Text("initwizard.prompts.proceed"), true)
		// Input that ended early took every default unread; end of input is not consent to install.
		// -non-interactive is how a script opts in.
		if err == nil && ok && w.p.inputClosed && !w.p.NonInteractive && !w.opts.DryRun {
			return false, apperr.Precondition("initwizard.errors.input_ended")
		}
		return ok, err
	}
	for {
		w.printf("")
		choice, err := w.p.Choice(i18n.Text("initwizard.prompts.proceed"), []Option{
			{Value: "go", Label: i18n.Text("initwizard.options.review_go.label"), Detail: w.goDetail()},
			{Value: "details", Label: i18n.Text("initwizard.options.review_details.label"),
				Detail: i18n.Text("initwizard.options.review_details.detail")},
			{Value: "cancel", Label: i18n.Text("initwizard.options.review_cancel.label"),
				Detail: i18n.Text("initwizard.options.review_cancel.detail")},
		}, 0)
		if err != nil {
			return false, err
		}
		switch choice {
		case "go":
			return true, nil
		case "cancel":
			return false, nil
		}
		w.printf("")
		w.details()
	}
}

func (w *run) goDetail() string {
	if w.opts.DryRun {
		return i18n.Text("initwizard.options.review_go.dry_run")
	}
	return i18n.Text("initwizard.options.review_go.detail")
}

func (w *run) details() {
	preview := w.previewEnv()
	if w.cfg.Mode == ModeDocker {
		w.printf("  deploy/.env")
		w.printf("%s", Diff(w.envBefore, preview))
		w.printf("  $ docker %s", strings.Join(ComposeArgs(w.cfg, w.composeFiles()), " "))
		return
	}
	w.say("  ", "initwizard.cli.helm_values_file", "release", w.cfg.Release)
	w.printf("  $ helm %s", strings.Join(HelmArgs(w.cfg, "deploy/values-"+w.cfg.Release+".yaml"), " "))
}

func (w *run) review() {
	for _, row := range w.reviewRows() {
		w.reviewRow(row[0], row[1])
	}
	if len(w.cfg.Deferred) > 0 {
		w.printf("")
		w.say("  ", "initwizard.cli.skipped_list", "steps", strings.Join(deferredNames(w.cfg.Deferred), ", "))
	}
}

func (w *run) reviewRow(label, value string) {
	if w.p.Interactive() {
		w.printf("  %s %s", styleMuted.Render(fmt.Sprintf("%-14s", label)), value)
		return
	}
	w.printf("  %-14s %s", label, value)
}

func (w *run) reviewRows() [][2]string {
	c := w.cfg
	var rows [][2]string
	add := func(label, value string) { rows = append(rows, [2]string{label, value}) }

	switch {
	case c.Mode == ModeKubernetes:
		add(i18n.Text("initwizard.review.install"), i18n.Text("initwizard.review.install_kubernetes", "release", c.Release, "namespace", c.Namespace))
	case c.Quick:
		add(i18n.Text("initwizard.review.install"), i18n.Text("initwizard.review.install_quick"))
	default:
		add(i18n.Text("initwizard.review.install"), i18n.Text("initwizard.review.install_docker", "file", c.ComposeFile))
	}
	if c.Image != "" {
		add(i18n.Text("initwizard.review.image"), c.Image)
	}
	switch c.Identity {
	case IdentityOIDC:
		add(i18n.Text("initwizard.review.access"), i18n.Text("initwizard.review.access_oidc", "issuer", c.OIDC.Issuer))
	case IdentityGitHub:
		add(i18n.Text("initwizard.review.access"), i18n.Text("initwizard.review.access_github"))
	default:
		add(i18n.Text("initwizard.review.access"), i18n.Text("initwizard.review.access_none"))
	}
	if !c.Quick {
		add(i18n.Text("initwizard.review.address"), c.PublicOrigin)
		database := i18n.Text("initwizard.review.database_external")
		if c.Mode == ModeDocker && strings.Contains(c.BootstrapDSN, bundledHostAddrOn(c.PGHostPort)) {
			database = i18n.Text("initwizard.review.database_bundled")
		}
		add(i18n.Text("initwizard.review.database"), database)
	}
	mcp := i18n.Text("initwizard.review.none")
	if len(c.MCPServers) > 0 {
		mcp = strings.Join(c.MCPServers, ", ")
	}
	add(i18n.Text("initwizard.review.mcp"), mcp)
	add(i18n.Text("initwizard.review.engines"), w.enginesRow())
	if c.Mode == ModeDocker {
		add(i18n.Text("initwizard.review.runner"), w.runnerRow())
	}
	add(i18n.Text("initwizard.review.workspace"), i18n.Text("initwizard.review.workspace_value", "name", c.WorkspaceName, "email", c.OperatorEmail))
	github := i18n.Text("initwizard.review.github_skipped")
	if c.GitToken != "" {
		github = i18n.Text("initwizard.review.github_token")
	}
	if c.GitHubURL != "" {
		github += " · " + c.GitHubURL
	}
	add(i18n.Text("initwizard.review.github"), github)
	return rows
}

func (w *run) enginesRow() string {
	parts := make([]string, 0, len(w.cfg.Engines))
	for _, e := range w.cfg.Engines {
		state := i18n.Text("initwizard.review.no_credential")
		for _, c := range w.cfg.Credentials {
			if c.Engine == e {
				state = i18n.Text("initwizard.review.with_credential")
			}
		}
		parts = append(parts, engineName(e)+" · "+state)
	}
	if len(parts) == 0 {
		return i18n.Text("initwizard.review.none")
	}
	return strings.Join(parts, "; ")
}

func (w *run) runnerRow() string {
	r := w.cfg.Runner
	if !r.Enabled {
		return i18n.Text("initwizard.review.runner_no")
	}
	parts := []string{i18n.Text("initwizard.review.runner_yes")}
	if r.InstallClaude {
		parts = append(parts, i18n.Text("initwizard.review.runner_install_claude", "version", hostrun.ClaudeCodeVersion))
	}
	if len(r.Folders) > 0 {
		parts = append(parts, i18n.Text("initwizard.review.runner_folders", "count", len(r.Folders)))
	}
	if r.Service {
		parts = append(parts, i18n.Text("initwizard.review.runner_service"))
	} else {
		parts = append(parts, i18n.Text("initwizard.review.runner_manual"))
	}
	return strings.Join(parts, " · ")
}

func (w *run) previewEnv() string {
	clone, err := LoadEnvFile(w.envPath)
	if err != nil || clone.Render() == "" {
		seed, seedErr := LoadEnvFile(filepath.Join(w.opts.Root, "deploy", ".env.example"))
		if seedErr != nil {
			seed = &EnvFile{}
		}
		clone = seed
	}
	w.cfg.ApplyToEnv(clone)
	return clone.Render()
}

// The loopback override names services `api` and `web`, which exist only in the dev stack;
// layering it on any other stack fails with "no such service".
func (w *run) overrides() []string {
	if w.cfg.DevMode() && w.cfg.ComposeFile == devComposeFile {
		return []string{devOverrideFile}
	}
	return nil
}

// No release writes this file, so operator edits survive `thulu apply`; init and apply must both
// layer it when it exists.
const OperatorOverrideFile = deployfiles.OverrideFile

func (w *run) composeFiles() []string {
	files := w.overrides()
	if _, err := os.Stat(filepath.Join(w.opts.Root, OperatorOverrideFile)); err == nil {
		files = append(files, OperatorOverrideFile)
	}
	return files
}

func (w *run) writeConfig() error {
	if w.cfg.Mode != ModeDocker {
		path, err := WriteHelmValues(w.opts.Root, w.cfg)
		if err != nil {
			return err
		}
		w.done(i18n.Text("initwizard.cli.wrote_file", "path", path))
		return nil
	}
	w.cfg.ApplyToEnv(w.env)
	if err := w.env.Save(w.envPath); err != nil {
		return err
	}
	if !w.wroteEnv {
		w.done(i18n.Text("initwizard.cli.wrote_env"))
		w.wroteEnv = true
	}
	if len(w.overrides()) > 0 {
		path, err := WriteDevOverride(w.opts.Root)
		if err != nil {
			return err
		}
		w.done(i18n.Text("initwizard.cli.wrote_override", "path", path))
	}
	return nil
}

// Runs before the daemon starts so OCTOPUS_OPERATOR_WORKSPACE lands in the config it boots with,
// avoiding a second restart.
func (w *run) bootstrapDatabase(ctx context.Context) error {
	if w.cfg.BootstrapDSN == "" {
		w.deferBootstrap(i18n.Text("initwizard.cli.no_bootstrap_dsn"))
		return nil
	}
	if err := w.ensureDatabaseUp(ctx); err != nil {
		w.deferBootstrap(apperr.Text(err))
		return nil
	}

	boot, err := OpenBootstrap(w.cfg.BootstrapDSN)
	if err != nil {
		w.deferBootstrap(apperr.Text(err))
		return nil
	}
	defer boot.Close()

	wsID, created, err := boot.EnsureWorkspace(ctx, w.cfg.OperatorEmail, i18n.Text("initwizard.labels.operator_name"),
		w.cfg.WorkspaceName, w.cfg.WorkspaceSlug)
	if err != nil {
		return apperr.Wrap(err, "initwizard.errors.bootstrap_workspace")
	}
	w.cfg.WorkspaceID = wsID
	if created {
		w.done(i18n.Text("initwizard.cli.workspace_created", "name", w.cfg.WorkspaceName, "id", wsID))
	} else {
		w.done(i18n.Text("initwizard.cli.workspace_exists", "slug", w.cfg.WorkspaceSlug, "id", wsID))
	}
	w.bootstrapped = true
	if w.cfg.AdminEmail != "" {
		if err := boot.InviteOwner(ctx, wsID, w.cfg.AdminEmail); err != nil {
			return err
		}
		w.done(i18n.Text("initwizard.cli.admin_invited", "email", w.cfg.AdminEmail, "name", w.cfg.WorkspaceName))
	}
	return w.mintKey(ctx, boot, wsID)
}

func (w *run) mintKey(ctx context.Context, boot *Bootstrap, wsID string) error {
	has, err := boot.HasAPIKey(ctx, wsID)
	if err != nil {
		return err
	}
	if has {
		w.done(i18n.Text("initwizard.cli.key_exists", "workspace", wsID))
		if !w.needsAPI() {
			return nil
		}
		var raw, id string
		raw, id, err = boot.MintSessionKey(ctx, i18n.Text("initwizard.labels.session_key"), wsID)
		if err != nil {
			return err
		}
		w.sessionKey, w.sessionKeyID = raw, id
		return nil
	}
	// The daemon reads the key TTL cap from deploy/.env, so the minted key must use that value, not
	// the wizard's shell env.
	if adoptErr := AdoptEnv(w.env, auth.MaxKeyTTLEnv); adoptErr != nil {
		return adoptErr
	}
	key, expires, err := boot.MintOperatorKey(ctx, i18n.Text("initwizard.labels.operator_key"), wsID, auth.RoleOwner, "full")
	if err != nil {
		return err
	}
	w.cfg.MintedKey = key
	w.cfg.MintedKeyExpires = expires
	// Print the key now: only its hash is stored, and if a later step fails the summary never
	// prints,
	// and a rerun sees a live key and refuses to mint another, locking the operator out.
	w.printf("")
	w.say("   ", "initwizard.cli.operator_key_once")
	w.printf("")
	w.printf("     %s\n", key)
	w.sayKeyExpiry("   ")
	return nil
}

func (w *run) sayKeyExpiry(indent string) {
	if w.cfg.MintedKeyExpires == nil {
		w.say(indent, "initwizard.summary.key_never_expires")
		return
	}
	w.say(indent, "initwizard.summary.key_expires", "date", w.cfg.MintedKeyExpires.Local().Format("2006-01-02"))
}

func (w *run) composeArgs(rest ...string) []string {
	args := []string{"compose", "-f", w.cfg.ComposeFile}
	for _, f := range w.composeFiles() {
		args = append(args, "-f", f)
	}
	return append(args, rest...)
}

func (w *run) ensureDatabaseUp(ctx context.Context) error {
	if err := ProbePostgres(ctx, w.cfg.BootstrapDSN); err == nil {
		return nil
	}
	if w.cfg.Mode != ModeDocker || !strings.Contains(w.cfg.BootstrapDSN, bundledHostAddrOn(w.cfg.PGHostPort)) {
		return apperr.Unavailable("initwizard.errors.database_unreachable")
	}
	return w.task(ctx, i18n.Text("initwizard.tasks.postgres"), func(ctx context.Context) error {
		if err := w.opts.Runner.Run(ctx, "docker", w.composeArgs("up", "-d", "pg")...); err != nil {
			return apperr.Wrap(err, "initwizard.errors.start_postgres")
		}
		return WaitForPostgres(ctx, w.cfg.BootstrapDSN, 90*time.Second, nil)
	})
}

func (w *run) deferBootstrap(why string) {
	w.warn(i18n.Text("initwizard.cli.skipping_bootstrap", "reason", why))
	create := fmt.Sprintf("-name '%s' -slug %s -owner-email %s", w.cfg.WorkspaceName, w.cfg.WorkspaceSlug, w.cfg.OperatorEmail)
	// /app/octopus is off the PATH: compose exec runs in WORKDIR /app so ./octopus works; kubectl
	// exec needs the full path.
	//i18n:ignore shell command, not a message
	finish := fmt.Sprintf("docker compose -f %s exec octopus ./octopus workspace create %s", w.cfg.ComposeFile, create)
	if w.cfg.Mode == ModeKubernetes {
		//i18n:ignore shell command, not a message
		finish = fmt.Sprintf("kubectl exec -n %s deploy/%s -- /app/octopus workspace create %s", w.cfg.Namespace, w.cfg.DaemonDeployment(), create)
	}
	w.cfg.Defer(i18n.Text("initwizard.deferred.workspace.step"), i18n.Text("initwizard.deferred.workspace.effect"), finish)
}

func (w *run) prepareDocker(ctx context.Context) error {
	if w.cfg.Mode != ModeDocker {
		return nil
	}
	if err := w.pullImage(ctx); err != nil {
		return err
	}
	w.pullAgentImages(ctx)
	return w.alignDockerGroup(ctx)
}

// A failed pull only warns: the daemon pulls the same image on that engine's first run.
// Checkout-built stacks (no Image) have no published agent images.
func (w *run) pullAgentImages(ctx context.Context) {
	if w.cfg.Image == "" || w.cfg.ImageTag == "" {
		return
	}
	registry := w.cfg.ImageRegistry
	if registry == "" {
		registry = ImageRegistry(ReleaseRepo())
	}
	for _, eng := range w.cfg.Engines {
		ref := strings.TrimRight(registry, "/") + "/" + agentimage.Name(eng) + ":" + w.cfg.ImageTag
		if ImagePresent(ctx, ref) {
			w.done(i18n.Text("initwizard.cli.image_present", "image", ref))
			continue
		}
		err := w.task(ctx, i18n.Text("initwizard.cli.pulling", "image", ref), func(ctx context.Context) error {
			return w.opts.Runner.Run(ctx, "docker", "pull", ref)
		})
		if err != nil {
			w.warn(i18n.Text("initwizard.cli.agent_image_later", "image", ref))
		}
	}
}

func (w *run) startStack(ctx context.Context) error {
	if w.cfg.Mode == ModeKubernetes {
		values := fmt.Sprintf("deploy/values-%s.yaml", w.cfg.Release)
		return w.task(ctx, i18n.Text("initwizard.tasks.helm"), func(ctx context.Context) error {
			return apperr.Wrap(w.opts.Runner.Run(ctx, "helm", HelmArgs(w.cfg, values)...), "initwizard.errors.start_stack")
		})
	}
	if err := w.task(ctx, i18n.Text("initwizard.tasks.compose_up"), func(ctx context.Context) error {
		return apperr.Wrap(w.opts.Runner.Run(ctx, "docker", ComposeArgs(w.cfg, w.composeFiles())...), "initwizard.errors.start_stack")
	}); err != nil {
		return err
	}
	return w.task(ctx, i18n.Text("initwizard.tasks.wait_daemon"), func(ctx context.Context) error {
		//i18n:ignore shell command, not a message
		logs := "docker " + strings.Join(w.composeArgs("logs", w.daemonService()), " ")
		return apperr.Wrap(WaitForDaemon(ctx, w.daemonURL()), "initwizard.errors.daemon_not_ready", "command", logs)
	})
}

func (w *run) daemonService() string {
	if w.cfg.ComposeFile == devComposeFile {
		return "api"
	}
	return "octopus"
}

// Pull before compose so an auth failure shows next to the login hint, not as a "failed to start"
// service buried in compose output.
func (w *run) pullImage(ctx context.Context) error {
	if w.cfg.Image == "" {
		return nil
	}
	if ImagePresent(ctx, w.cfg.Image) {
		w.done(i18n.Text("initwizard.cli.image_present", "image", w.cfg.Image))
		return nil
	}
	return w.task(ctx, i18n.Text("initwizard.cli.pulling", "image", w.cfg.Image), func(ctx context.Context) error {
		if err := w.opts.Runner.Run(ctx, "docker", "pull", w.cfg.Image); err != nil {
			return apperr.Wrap(err, "initwizard.errors.pull_needs_login")
		}
		return nil
	})
}

// The dev container runs as root and needs no group. The self-hosted daemon image is built during
// `compose up`,
// so it does not exist yet on a first run; the Postgres image sees the same socket.
func (w *run) probeImage() string {
	if w.cfg.Mode != ModeDocker || w.cfg.ComposeFile == devComposeFile {
		return ""
	}
	if w.cfg.Image != "" {
		return w.cfg.Image
	}
	if w.cfg.ComposeFile == prodComposeFile {
		return bundledPostgresImage
	}
	return ""
}

const bundledPostgresImage = "postgres:16-alpine"

var socketGID = DockerSocketGID

// The image runs unprivileged and the socket is mode 660: without this GID every Docker call is
// denied and compose restart-loops the daemon.
// The GID varies by host (991 under Colima, 999 on most Linux), so read it, never hardcode it.
func (w *run) alignDockerGroup(ctx context.Context) error {
	image := w.probeImage()
	if image == "" {
		return nil
	}
	gid := socketGID(ctx, image)
	if gid == "" {
		w.warn(i18n.Text("initwizard.cli.socket_group_unknown"))
		w.cfg.Defer(i18n.Text("initwizard.deferred.docker_socket.step"),
			i18n.Text("initwizard.deferred.docker_socket.effect"),
			i18n.Text("initwizard.deferred.docker_socket.finish"))
		return nil
	}
	w.env.Set("OCTOPUS_DOCKER_GID", gid)
	if err := w.env.Save(w.envPath); err != nil {
		return err
	}
	w.done(i18n.Text("initwizard.cli.socket_group", "gid", gid))
	return nil
}

// Compose stacks use the published port, not the domain: the domain may point at a proxy that is
// not up yet, or resolve to nothing locally.
func (w *run) daemonURL() string {
	if w.cfg.Mode == ModeKubernetes {
		scheme := "https"
		if isLocalHost(w.cfg.Domain) {
			scheme = "http"
		}
		return scheme + "://" + w.cfg.Domain
	}
	return "http://localhost:8585"
}

func (w *run) postBoot(ctx context.Context) {
	if !w.needsAPI() {
		return
	}
	key := w.cfg.MintedKey
	if key == "" {
		key = w.sessionKey
	}
	if key == "" {
		w.cfg.Defer(i18n.Text("initwizard.deferred.git_and_llm.step"),
			i18n.Text("initwizard.deferred.git_and_llm.no_key_effect"),
			i18n.Text("initwizard.deferred.git_and_llm.no_key_finish"))
		return
	}
	client := NewAPIClient(w.daemonURL(), key)
	if err := client.Reachable(ctx); err != nil {
		w.warn(i18n.Text("initwizard.cli.api_unreachable", "err", apperr.Text(err)))
		w.cfg.Defer(i18n.Text("initwizard.deferred.git_and_llm.step"),
			i18n.Text("initwizard.deferred.git_and_llm.unreachable_effect"),
			i18n.Text("initwizard.deferred.git_and_llm.unreachable_finish"))
		return
	}
	w.storeGitCredential(ctx, client)
	w.storeCredentials(ctx, client)
	w.setupRunner(ctx, client)
}

func (w *run) storeGitCredential(ctx context.Context, client *APIClient) {
	if w.cfg.GitToken == "" {
		return
	}
	res, err := client.CreateGitCredential(ctx, i18n.Text("initwizard.labels.git_credential_name"), w.cfg.GitToken)
	if err != nil {
		w.warn(i18n.Text("initwizard.cli.git_credential_rejected", "err", apperr.Text(err)))
		w.cfg.Defer(i18n.Text("initwizard.deferred.git_credential.step"),
			i18n.Text("initwizard.deferred.git_credential.rejected_effect"),
			i18n.Text("initwizard.deferred.git_credential.rejected_finish"))
		return
	}
	w.done(i18n.Text("initwizard.cli.git_credential_stored", "identity", res.Identity, "state", res.State))
	switch res.IntrospectError {
	case "":
	case credentials.IntrospectUnreachable:
		w.say("     ", "initwizard.cli.git_provider_unreachable")
	default:
		w.say("     ", "initwizard.cli.provider_said", "message", res.IntrospectError)
	}
}

func (w *run) storeCredentials(ctx context.Context, client *APIClient) {
	for _, cred := range w.cfg.Credentials {
		label := engineLabel(cred.Engine)
		if err := client.CreateLLMProfile(ctx, label, cred); err != nil {
			w.warn(i18n.Text("initwizard.cli.llm_key_rejected", "engine", engineName(cred.Engine), "err", apperr.Text(err)))
			w.cfg.Defer(i18n.Text("initwizard.deferred.llm_key.step"),
				i18n.Text("initwizard.deferred.llm_key.rejected_effect"),
				i18n.Text("initwizard.deferred.llm_key.rejected_finish"))
			continue
		}
		w.done(i18n.Text("initwizard.cli.llm_key_stored", "engine", engineName(cred.Engine)))
	}
}

func (w *run) task(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	sp := startSpinner(w.out, w.p != nil && w.p.Interactive(), title)
	err := fn(ctx)
	sp.finish(err)
	if err != nil && w.opts.LogPath != "" {
		w.printf("%s", indentLines("     ", apperr.Text(err)))
		if tail := logTail(w.opts.LogPath, 15); tail != "" {
			w.printf("")
			w.printf("%s", indentLines("     ", tail))
		}
		w.say("     ", "initwizard.cli.full_log", "path", w.opts.LogPath)
	}
	return err
}

func (w *run) done(text string) { w.check(CheckOK, text) }

func (w *run) warn(text string) { w.check(CheckWarn, text) }

func (w *run) check(state CheckState, text string) {
	if w.p == nil {
		fmt.Fprintf(w.out, "   %s %s\n", state.mark(false), text)
		return
	}
	w.p.Check(state, text, "")
}

func logTail(path string, n int) string {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the wizard's own log
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func deferredNames(items []Deferred) []string {
	names := make([]string, 0, len(items))
	for _, d := range items {
		names = append(names, d.Step)
	}
	return names
}
