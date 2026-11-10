package initwizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

//i18n:ignore drawing, not a message
var octopusArt = strings.Join([]string{
	"   ╭──────╮",
	"  ╭╯ ◕  ◕ ╰╮",
	"  │   ‿‿   │",
	"  ╰┬─┬──┬─┬╯",
	"  ╭╯╭╯  ╰╮╰╮",
	"  ╰ ╰    ╯ ╯",
}, "\n")

func bannerArt(title string, lines []string) string {
	text := make([]string, 0, 2+len(lines))
	text = append(text, "", styleAccent.Render(title))
	for _, l := range lines {
		text = append(text, styleMuted.Render(l))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, styleAccent.Render(octopusArt), "   ", strings.Join(text, "\n"))
}

func flow(steps ...string) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = styleBold.Render(s)
	}
	return strings.Join(parts, styleMuted.Render(" ──▶ "))
}

const (
	diagramOctopus = "Thulu"       //i18n:ignore product name
	diagramPort    = "Thulu :8585" //i18n:ignore product name and port
	diagramCaddy   = "Caddy :443"  //i18n:ignore product name and port
	diagramIngress = "ingress"     //i18n:ignore Kubernetes term
)

func modeDiagram(mode string) string {
	switch mode {
	case modeQuick:
		return flow(i18n.Text("initwizard.diagram.browser"), "127.0.0.1:8585", diagramOctopus, i18n.Text("initwizard.diagram.agents_docker"))
	case modeCustom:
		return flow(i18n.Text("initwizard.diagram.browser"), i18n.Text("initwizard.diagram.domain_or_local"),
			diagramOctopus, i18n.Text("initwizard.diagram.agents_docker"))
	case string(ModeKubernetes):
		return flow(diagramIngress, diagramOctopus, i18n.Text("initwizard.diagram.agents_pods"))
	}
	return ""
}

func stackDiagram(file string) string {
	switch file {
	case caddyComposeFile:
		return flow(i18n.Text("initwizard.diagram.internet"), diagramCaddy, diagramPort, i18n.Text("initwizard.diagram.agents_docker"))
	case standaloneComposeFile:
		return flow(i18n.Text("initwizard.diagram.browser_or_proxy"), diagramPort, i18n.Text("initwizard.diagram.agents_docker"))
	}
	return ""
}

func runnerDiagram() string {
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(0, 1)
	server := box.Render(styleAccent.Render(diagramOctopus) + "\n" +
		i18n.Text("initwizard.diagram.runner_server"))
	machine := box.Render(styleAccent.Render(i18n.Text("initwizard.diagram.runner_machine")) + "\n" +
		i18n.Text("initwizard.diagram.runner_local"))
	run := i18n.Text("initwizard.diagram.runner_run")
	result := i18n.Text("initwizard.diagram.runner_result")
	width := max(lipgloss.Width(run), lipgloss.Width(result)) + 7
	arrows := strings.Join([]string{
		"",
		styleMuted.Render(" " + run + " " + strings.Repeat("─", width-lipgloss.Width(run)-3) + "▶ "),
		styleMuted.Render(" ◀" + strings.Repeat("─", width-lipgloss.Width(result)-3) + " " + result + " "),
	}, "\n")
	row := lipgloss.JoinHorizontal(lipgloss.Top, server, arrows, machine)
	caption := lipgloss.PlaceHorizontal(lipgloss.Width(row), lipgloss.Center,
		styleMuted.Render(i18n.Text("initwizard.diagram.runner_code_stays")))
	return row + "\n" + caption
}

func (p *Prompter) fits(drawing string) bool {
	return p.tui && !p.muted && drawing != "" && lipgloss.Width(drawing)+3 <= p.width()
}

func (p *Prompter) Diagram(drawing string) bool {
	if !p.fits(drawing) {
		return false
	}
	fmt.Fprint(p.out, indentLines("   ", drawing)+"\n\n")
	return true
}
