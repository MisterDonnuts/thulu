package initwizard

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
)

func (w *run) summary() {
	w.printf("")
	if !w.p.Interactive() {
		w.printf("%s", strings.Repeat("═", 72))
	}
	w.p.Section(0, i18n.Text("initwizard.summary.up"), "")

	dashboard := i18n.Text("initwizard.summary.dashboard")
	switch {
	case w.cfg.Mode == ModeKubernetes:
		w.summaryRow(dashboard, i18n.Text("initwizard.summary.dashboard_after_dns", "domain", w.cfg.Domain))
		//i18n:ignore shell command, not a message
		rollout := "kubectl -n " + w.cfg.Namespace + " rollout status deploy/" + w.cfg.DaemonDeployment()
		w.summaryRow(i18n.Text("initwizard.summary.rollout"), rollout)
	case w.cfg.DevMode():
		w.summaryRow(dashboard, auth.DevDashboardURL("http://localhost:8585", w.cfg.DevToken))
		w.summaryRow("", i18n.Text("initwizard.summary.dev_link_note"))
	default:
		w.summaryRow(dashboard, w.dashboardURL())
	}
	if w.cfg.Runner.Enabled {
		path, err := hostrun.DefaultConfigPath()
		if err == nil {
			w.summaryRow(i18n.Text("initwizard.summary.runner"), path)
		}
	}

	if w.cfg.MintedKey != "" {
		w.printf("")
		w.say("  ", "initwizard.cli.operator_key_once")
		w.printf("")
		w.printf("    %s\n", w.cfg.MintedKey)
		w.say("  ", "initwizard.summary.use_key", "prefix", w.cfg.MintedKey[:min(8, len(w.cfg.MintedKey))])
		w.sayKeyExpiry("  ")
	}

	w.nextSteps()
	w.summaryDeferred()
}

func (w *run) nextSteps() {
	w.printf("")
	w.say("  ", "initwizard.summary.next_header")
	steps := []string{i18n.Text("initwizard.summary.next_open"), i18n.Text("initwizard.summary.next_agent")}
	switch {
	case w.cfg.Runner.Enabled && len(w.cfg.Runner.Folders) > 0:
		steps = append(steps, i18n.Text("initwizard.summary.next_runner_projects"))
	default:
		steps = append(steps, i18n.Text("initwizard.summary.next_project"))
	}
	steps = append(steps, i18n.Text("initwizard.summary.next_task"))
	if w.cfg.Runner.Enabled {
		if w.cfg.Runner.Service {
			steps = append(steps, i18n.Text("initwizard.summary.next_runner_status"))
		} else {
			steps = append(steps, i18n.Text("initwizard.summary.next_runner_run"))
		}
	}
	for i, s := range steps {
		w.printf("     %d. %s", i+1, s)
	}
}

func (w *run) summaryRow(label, value string) {
	if w.p.Interactive() {
		w.printf("  %s %s", styleMuted.Render(padRight(label, 11)), value)
		return
	}
	w.printf("  %-11s %s", label, value)
}

func padRight(s string, n int) string {
	if pad := n - len([]rune(s)); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func (w *run) summaryDeferred() {
	w.printf("")
	if len(w.cfg.Deferred) == 0 {
		w.say("  ", "initwizard.summary.nothing_skipped")
		return
	}
	w.say("  ", "initwizard.summary.skipped_header")
	w.printf("")
	for _, d := range w.cfg.Deferred {
		w.printf("  · %s", d.Step)
		w.say("      ", "initwizard.summary.effect", "text", d.Effect)
		w.say("      ", "initwizard.summary.finish", "text", d.Finish)
	}
}

// Behind Caddy, login callbacks and allowed origins name the public domain, so the loopback port
// would sign nobody in.
func (w *run) dashboardURL() string {
	if behindCaddy(w.cfg.ComposeFile) && w.cfg.Domain != "" && !isLocalHost(w.cfg.Domain) {
		return "https://" + w.cfg.Domain
	}
	return w.daemonURL()
}
