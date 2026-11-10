package initwizard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
)

type RunnerPlan struct {
	Enabled       bool     `json:"enabled"`
	Service       bool     `json:"service,omitempty"`
	Folders       []string `json:"folders,omitempty"`
	InstallClaude bool     `json:"-"`
}

type hostTool struct {
	name    string
	present bool
	version string
}

var lookTools = func(ctx context.Context) []hostTool {
	names := []string{"git", "bash", "curl", "node", "claude"}
	out := make([]hostTool, 0, len(names))
	for _, n := range names {
		t := hostTool{name: n}
		if _, err := exec.LookPath(n); err == nil {
			t.present = true
			if n == "claude" || n == "node" {
				vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				b, verr := exec.CommandContext(vctx, n, "--version").Output() //nolint:gosec // G204: n is one of the fixed tool names above
				cancel()
				if verr == nil {
					t.version = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
				}
			}
		}
		out = append(out, t)
	}
	return out
}

var claudeLogin = func(ctx context.Context) (loggedIn bool, email string, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, "claude", "auth", "status").Output()
	if err != nil {
		return false, "", false
	}
	var st struct {
		LoggedIn bool   `json:"loggedIn"`
		Email    string `json:"email"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false, "", false
	}
	return st.LoggedIn, st.Email, true
}

var npmPresent = func() bool {
	_, err := exec.LookPath("npm")
	return err == nil
}

// Docker only: a Kubernetes daemon has runners off (OCTOPUS_RUNNERS).
func (iv *Interviewer) askRunner() error {
	if iv.Cfg.Mode != ModeDocker {
		return nil
	}
	iv.section(i18n.Text("initwizard.sections.runner.title"), i18n.Text("initwizard.sections.runner.blurb"))
	iv.P.Box(i18n.Text("initwizard.runner.box_title"), i18n.Text("initwizard.runner.box_body"))
	iv.P.Diagram(runnerDiagram())

	def := 0
	if iv.PreviousRunner.Enabled {
		def = 1
	}
	answer, err := iv.P.Choice(i18n.Text("initwizard.prompts.runner"), []Option{
		{Value: "no", Label: i18n.Text("initwizard.options.runner_no.label"),
			Detail: i18n.Text("initwizard.options.runner_no.detail")},
		{Value: "yes", Label: i18n.Text("initwizard.options.runner_yes.label"),
			Detail: i18n.Text("initwizard.options.runner_yes.detail")},
	}, def)
	if err != nil {
		return err
	}
	if answer != "yes" {
		return nil
	}
	iv.Cfg.Runner.Enabled = true
	iv.P.Printf("")
	if err := iv.checkRunnerHost(); err != nil {
		return err
	}
	if slices.ContainsFunc(iv.Cfg.Engines, func(e string) bool { return e != engineClaude }) {
		iv.P.narrate("initwizard.cli.runner_claude_only")
		iv.P.Printf("")
	}
	if err := iv.askRunnerFolders(); err != nil {
		return err
	}
	return iv.askRunnerService()
}

func (iv *Interviewer) checkRunnerHost() error {
	iv.P.narrate("initwizard.cli.runner_host_check")
	var missing []string
	claudeMissing := false
	for _, t := range lookTools(iv.Ctx) {
		switch {
		case t.present && t.version != "":
			iv.P.Check(CheckOK, t.name, t.version)
		case t.present:
			iv.P.Check(CheckOK, t.name, "")
		case t.name == "claude":
			claudeMissing = true
			iv.P.Check(CheckFail, t.name, i18n.Text("initwizard.cli.runner_tool_missing"))
		default:
			missing = append(missing, t.name)
			iv.P.Check(CheckFail, t.name, i18n.Text("initwizard.cli.runner_tool_missing"))
		}
	}
	if !claudeMissing {
		switch in, email, ok := claudeLogin(iv.Ctx); {
		case ok && in && email != "":
			iv.P.Check(CheckOK, i18n.Text("initwizard.cli.runner_claude_session"), email)
		case ok && in:
			iv.P.Check(CheckOK, i18n.Text("initwizard.cli.runner_claude_session"), "")
		default:
			iv.P.Check(CheckWarn, i18n.Text("initwizard.cli.runner_claude_session"), i18n.Text("initwizard.cli.runner_claude_no_session"))
			iv.Cfg.Defer(i18n.Text("initwizard.deferred.runner_login.step"),
				i18n.Text("initwizard.deferred.runner_login.effect"),
				i18n.Text("initwizard.deferred.runner_login.finish"))
		}
	}
	iv.P.Printf("")
	if len(missing) > 0 {
		iv.P.narrate("initwizard.cli.runner_install_tools", "tools", strings.Join(missing, ", "))
		iv.P.Printf("")
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.runner_tools.step"),
			i18n.Text("initwizard.deferred.runner_tools.effect", "tools", strings.Join(missing, ", ")),
			i18n.Text("initwizard.deferred.runner_tools.finish"))
	}
	if !claudeMissing {
		return nil
	}
	//i18n:ignore shell command, not a message
	install := "npm install -g " + hostrun.ClaudeCodePackage + "@" + hostrun.ClaudeCodeVersion
	if !npmPresent() {
		iv.P.narrate("initwizard.cli.runner_no_npm", "command", install)
		iv.P.Printf("")
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.runner_claude.step"),
			i18n.Text("initwizard.deferred.runner_claude.effect"),
			i18n.Text("initwizard.deferred.runner_claude.finish", "command", install))
		return nil
	}
	yes, err := iv.P.YesNo(i18n.Text("initwizard.prompts.install_claude", "version", hostrun.ClaudeCodeVersion), true)
	if err != nil {
		return err
	}
	iv.Cfg.Runner.InstallClaude = yes
	if !yes {
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.runner_claude.step"),
			i18n.Text("initwizard.deferred.runner_claude.effect"),
			i18n.Text("initwizard.deferred.runner_claude.finish", "command", install))
	}
	return nil
}

func (iv *Interviewer) askRunnerFolders() error {
	iv.P.narrate("initwizard.cli.runner_folders_intro")
	iv.P.listEnd = true
	defer func() { iv.P.listEnd = false }()
	previous := iv.PreviousRunner.Folders
	for {
		def := ""
		if n := len(iv.Cfg.Runner.Folders); n < len(previous) {
			def = previous[n]
		}
		answer, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.runner_folder"), def)
		switch {
		case errors.Is(err, ErrSkipped):
			return nil
		case err != nil:
			return err
		}
		dir, ok := gitCheckout(answer)
		if !ok {
			iv.P.narrate("initwizard.cli.runner_not_a_checkout", "path", answer)
			if iv.P.cannotAskAgain() {
				return nil
			}
			continue
		}
		if slices.Contains(iv.Cfg.Runner.Folders, dir) {
			if iv.P.cannotAskAgain() {
				return nil
			}
			continue
		}
		iv.Cfg.Runner.Folders = append(iv.Cfg.Runner.Folders, dir)
		iv.P.Check(CheckOK, filepath.Base(dir), dir)
	}
}

func gitCheckout(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return "", false
	}
	return abs, true
}

func (iv *Interviewer) askRunnerService() error {
	if !service.Supported() {
		iv.P.narrate("initwizard.cli.runner_service_unsupported")
		return nil
	}
	iv.P.Printf("")
	yes, err := iv.P.YesNo(i18n.Text("initwizard.prompts.runner_service"), true)
	if err != nil {
		return err
	}
	iv.Cfg.Runner.Service = yes
	return nil
}

func (w *run) setupRunner(ctx context.Context, client *APIClient) {
	plan := w.cfg.Runner
	if !plan.Enabled {
		return
	}
	if plan.InstallClaude {
		install := []string{"install", "-g", hostrun.ClaudeCodePackage + "@" + hostrun.ClaudeCodeVersion}
		if err := w.task(ctx, i18n.Text("initwizard.tasks.install_claude", "version", hostrun.ClaudeCodeVersion), func(ctx context.Context) error {
			return w.opts.Runner.Run(ctx, "npm", install...)
		}); err != nil {
			w.cfg.Defer(i18n.Text("initwizard.deferred.runner_claude.step"),
				i18n.Text("initwizard.deferred.runner_claude.effect"),
				i18n.Text("initwizard.deferred.runner_claude.finish", "command", "npm "+strings.Join(install, " ")))
		}
	}
	path, err := hostrun.DefaultConfigPath()
	if err != nil {
		w.deferRunner(apperr.Text(err))
		return
	}
	cfg, err := w.registerRunner(ctx, client, path)
	if err != nil {
		w.deferRunner(apperr.Text(err))
		return
	}
	for _, dir := range plan.Folders {
		w.addRunnerFolder(ctx, client, path, cfg, dir)
	}
	if plan.Service {
		w.startRunnerService(ctx, path)
	}
}

func (w *run) registerRunner(ctx context.Context, client *APIClient, path string) (*hostrun.Config, error) {
	if cfg, err := hostrun.LoadConfig(path); err == nil && cfg.Credential != "" &&
		strings.TrimRight(cfg.DaemonURL, "/") == w.daemonURL() && cfg.WorkspaceID == w.cfg.WorkspaceID {
		w.done(i18n.Text("initwizard.tasks.runner_kept", "name", cfg.Name))
		return cfg, nil
	}
	var cfg *hostrun.Config
	err := w.task(ctx, i18n.Text("initwizard.tasks.register_runner"), func(ctx context.Context) error {
		token, err := client.CreateRunnerToken(ctx, runnerName())
		if err != nil {
			return err
		}
		cfg, err = hostrun.Register(ctx, path, w.daemonURL(), token, w.opts.BinaryVersion)
		return err
	})
	return cfg, err
}

func (w *run) addRunnerFolder(ctx context.Context, client *APIClient, path string, cfg *hostrun.Config, dir string) {
	for id, mapped := range cfg.Projects {
		if mapped == dir {
			w.done(i18n.Text("initwizard.tasks.runner_project_kept", "name", filepath.Base(dir), "id", id))
			return
		}
	}
	name := filepath.Base(dir)
	err := w.task(ctx, i18n.Text("initwizard.tasks.runner_project", "name", name), func(ctx context.Context) error {
		id, err := client.CreateRunnerProject(ctx, cfg.RunnerID, name)
		if err != nil {
			return err
		}
		return hostrun.AddProject(path, id, dir)
	})
	if err != nil {
		w.cfg.Defer(i18n.Text("initwizard.deferred.runner_project.step", "name", name),
			i18n.Text("initwizard.deferred.runner_project.effect"),
			i18n.Text("initwizard.deferred.runner_project.finish", "dir", dir))
	}
}

func (w *run) startRunnerService(ctx context.Context, path string) {
	var st service.Status
	err := w.task(ctx, i18n.Text("initwizard.tasks.runner_service"), func(ctx context.Context) error {
		spec, err := service.SpecFor(path)
		if err != nil {
			return err
		}
		st, err = service.Install(ctx, spec)
		return err
	})
	switch {
	case err != nil:
		w.cfg.Defer(i18n.Text("initwizard.deferred.runner_service.step"),
			i18n.Text("initwizard.deferred.runner_service.effect"),
			i18n.Text("initwizard.deferred.runner_service.finish"))
	case !st.Running:
		w.warn(i18n.Text("initwizard.cli.runner_service_not_running", "file", st.File))
	}
	if err == nil && st.NoLinger {
		w.warn(i18n.Text("service.cli.no_linger"))
	}
}

func runnerName() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	//i18n:ignore fallback runner name, an identifier
	return "runner"
}

func (w *run) deferRunner(why string) {
	w.warn(i18n.Text("initwizard.cli.runner_failed", "reason", why))
	w.cfg.Defer(i18n.Text("initwizard.deferred.runner.step"),
		i18n.Text("initwizard.deferred.runner.effect"),
		i18n.Text("initwizard.deferred.runner.finish"))
}
