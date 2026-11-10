package initwizard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var ErrAborted error = apperr.New("initwizard.errors.aborted")

var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#5A4FCF", Dark: "#9D8CFF"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#D29922"}
	colorFail   = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F85149"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#656D76", Dark: "#8B949E"}
)

var (
	styleAccent = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleMuted  = lipgloss.NewStyle().Foreground(colorMuted)
	styleOK     = lipgloss.NewStyle().Foreground(colorOK)
	styleWarn   = lipgloss.NewStyle().Foreground(colorWarn)
	styleFail   = lipgloss.NewStyle().Foreground(colorFail)
	styleBold   = lipgloss.NewStyle().Bold(true)
)

type CheckState int

const (
	CheckOK CheckState = iota
	CheckWarn
	CheckFail
)

func (s CheckState) mark(tui bool) string {
	switch s {
	case CheckOK:
		return styleIf(tui, styleOK, "✓")
	case CheckWarn:
		return styleIf(tui, styleWarn, "!")
	case CheckFail:
		return styleIf(tui, styleFail, "✗")
	}
	return styleIf(tui, styleOK, "✓")
}

func styleIf(tui bool, s lipgloss.Style, text string) string {
	if !tui {
		return text
	}
	return s.Render(text)
}

func interactiveTerminal() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func (p *Prompter) width() int {
	w := 80
	if p.tui {
		if tw, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && tw > 20 {
			w = tw
		}
	}
	return min(w, 88)
}

func (p *Prompter) theme() *huh.Theme {
	t := huh.ThemeCharm()
	t.Focused.Title = t.Focused.Title.Foreground(colorAccent)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(colorAccent)
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(colorAccent)
	t.Focused.SelectedPrefix = t.Focused.SelectedPrefix.Foreground(colorOK)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(colorOK)
	t.Focused.Base = t.Focused.Base.BorderForeground(colorAccent)
	return t
}

func (p *Prompter) ask(field huh.Field) error {
	err := huh.NewForm(huh.NewGroup(field)).WithTheme(p.theme()).WithShowHelp(true).Run()
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, huh.ErrTimeout) {
		return ErrAborted
	}
	if err != nil {
		return apperr.Wrap(err, "initwizard.errors.read_input")
	}
	return nil
}

func (p *Prompter) answered(label, value string) {
	switch {
	case value == "" && p.listEnd:
		value = styleMuted.Render(i18n.Text("initwizard.tui.list_done"))
	case value == "":
		value = styleMuted.Render(i18n.Text("initwizard.tui.skipped"))
	default:
		value = styleBold.Render(shown(value))
	}
	fmt.Fprintf(p.out, "  %s %s %s %s\n", styleOK.Render("✓"), label, styleMuted.Render("›"), value)
}

func (p *Prompter) tuiText(label, def string, skippable bool) (string, error) {
	v := ""
	in := huh.NewInput().Title(label).Value(&v).Placeholder(shown(def))
	switch {
	case skippable && def != "":
		in = in.Description(i18n.Text("initwizard.tui.enter_default_or_skip", "skip", skipToken))
	case skippable && p.listEnd:
		in = in.Description(i18n.Text("initwizard.tui.enter_ends_list"))
	case skippable:
		in = in.Description(i18n.Text("initwizard.tui.enter_skip"))
	case def != "":
		in = in.Description(i18n.Text("initwizard.tui.enter_default"))
	}
	if err := p.ask(in); err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	switch {
	case skippable && strings.EqualFold(v, skipToken):
		p.answered(label, "")
		return "", ErrSkipped
	case v == "" && def == "" && skippable:
		p.answered(label, "")
		return "", ErrSkipped
	case v == "":
		v = def
	}
	p.answered(label, v)
	return v, nil
}

func (p *Prompter) tuiSecret(label string) (string, error) {
	v := ""
	in := huh.NewInput().Title(label).Value(&v).EchoMode(huh.EchoModePassword).
		Description(i18n.Text("initwizard.tui.secret_hint"))
	if err := p.ask(in); err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	if v == "" {
		p.answered(label, "")
		return "", ErrSkipped
	}
	p.answered(label, maskSecret(v))
	return v, nil
}

func maskSecret(v string) string {
	if len(v) < 20 {
		return "••••"
	}
	return "••••" + v[len(v)-4:]
}

func (p *Prompter) tuiYesNo(label string, def bool) (bool, error) {
	v := def
	c := huh.NewConfirm().Title(label).Value(&v).
		Affirmative(i18n.Text("initwizard.tui.yes")).Negative(i18n.Text("initwizard.tui.no"))
	if err := p.ask(c); err != nil {
		return false, err
	}
	answer := i18n.Text("initwizard.tui.no")
	if v {
		answer = i18n.Text("initwizard.tui.yes")
	}
	p.answered(label, answer)
	return v, nil
}

func (p *Prompter) tuiChoice(label string, opts []Option, def int) (string, error) {
	v := opts[def].Value
	options := make([]huh.Option[string], 0, len(opts))
	detail := map[string]string{}
	name := map[string]string{}
	for _, o := range opts {
		options = append(options, huh.NewOption(o.Label, o.Value))
		detail[o.Value] = wrap(o.Detail, p.width()-6)
		if p.fits(o.Diagram) {
			detail[o.Value] += "\n\n" + o.Diagram
		}
		name[o.Value] = o.Label
	}
	// Pad every description to the longest: huh sizes the list once, and a description that grows
	// pushes options out of it.
	lines := 0
	for _, d := range detail {
		lines = max(lines, strings.Count(d, "\n")+1)
	}
	for k, d := range detail {
		detail[k] = d + strings.Repeat("\n ", lines-1-strings.Count(d, "\n"))
	}
	s := huh.NewSelect[string]().Title(label).Options(options...).Value(&v).
		DescriptionFunc(func() string { return detail[v] }, &v)
	if err := p.ask(s); err != nil {
		return "", err
	}
	p.answered(label, name[v])
	return v, nil
}

func (p *Prompter) tuiMultiChoice(label string, opts []Option, checked []bool) ([]string, error) {
	options := make([]huh.Option[string], 0, len(opts))
	pad := 0
	for _, o := range opts {
		pad = max(pad, len([]rune(o.Label)))
	}
	room := p.width() - pad - 14
	for i, o := range opts {
		text := o.Label
		if o.Detail != "" && room > 20 {
			text = fmt.Sprintf("%-*s  %s", pad, o.Label, styleMuted.Render(truncate(o.Detail, room)))
		}
		options = append(options, huh.NewOption(text, o.Value).Selected(checked[i]))
	}
	var v []string
	m := huh.NewMultiSelect[string]().Title(label).Options(options...).Value(&v).
		Description(i18n.Text("initwizard.tui.multi_hint"))
	if err := p.ask(m); err != nil {
		return nil, err
	}
	out := []string{}
	names := []string{}
	for _, o := range opts {
		if containsName(v, o.Value) {
			out = append(out, o.Value)
			names = append(names, o.Label)
		}
	}
	p.answered(label, strings.Join(names, ", "))
	return out, nil
}

func (p *Prompter) tuiSection(n, total int, title, blurb string) {
	fmt.Fprintln(p.out)
	switch {
	case n > 0 && total > 0:
		fmt.Fprintf(p.out, "%s %s\n", styleMuted.Render(i18n.Text("initwizard.tui.step_of", "n", n, "total", total)), styleAccent.Render(title))
	case n > 0:
		fmt.Fprintf(p.out, "%s %s\n", styleMuted.Render(i18n.Text("initwizard.tui.step", "n", n)), styleAccent.Render(title))
	default:
		fmt.Fprintln(p.out, styleAccent.Render(title))
	}
	if blurb != "" {
		fmt.Fprintln(p.out, renderLines(styleMuted, indentLines("  ", wrap(blurb, p.width()-2))))
	}
	fmt.Fprintln(p.out)
}

func (p *Prompter) Box(title, body string) {
	if p.muted {
		return
	}
	if !p.tui {
		fmt.Fprintf(p.out, "   %s\n", title)
		fmt.Fprintln(p.out, indentLines("   ", body))
		fmt.Fprintln(p.out)
		return
	}
	w := p.width() - 4
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).
		Padding(0, 1).Width(w)
	fmt.Fprintln(p.out, box.Render(styleAccent.Render(title)+"\n\n"+wrapParagraphs(body, w-4)))
	fmt.Fprintln(p.out)
}

func (p *Prompter) Check(state CheckState, label, detail string) {
	if p.muted {
		return
	}
	if detail != "" {
		detail = "  " + styleIf(p.tui, styleMuted, detail)
	}
	fmt.Fprintf(p.out, "   %s %s%s\n", state.mark(p.tui), label, detail)
}

// Per line: rendering a block makes lipgloss pad every line to the longest.
func renderLines(st lipgloss.Style, text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = st.Render(l)
	}
	return strings.Join(lines, "\n")
}

func wrap(text string, width int) string {
	if width < 20 {
		return text
	}
	// Break only at spaces (a path cut at a hyphen reads as two words); a lipgloss Width style
	// would also pad every line.
	if strings.Contains(text, "\n") {
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			lines[i] = wrap(l, width)
		}
		return strings.Join(lines, "\n")
	}
	if indent := text[:len(text)-len(strings.TrimLeft(text, " "))]; indent != "" && len(indent) < width-10 {
		return indent + strings.ReplaceAll(wrap(text[len(indent):], width-len(indent)), "\n", "\n"+indent)
	}
	var b strings.Builder
	n := 0
	for i, word := range strings.Fields(text) {
		w := ansi.StringWidth(word)
		switch {
		case i == 0:
		case n+1+w > width:
			b.WriteByte('\n')
			n = 0
		default:
			b.WriteByte(' ')
			n++
		}
		b.WriteString(word)
		n += w
	}
	return b.String()
}

func wrapParagraphs(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if len([]rune(l)) <= width {
			continue
		}
		if rest, ok := strings.CutPrefix(l, "• "); ok {
			lines[i] = "• " + strings.ReplaceAll(wrap(rest, width-2), "\n", "\n  ")
			continue
		}
		lines[i] = wrap(l, width)
	}
	return strings.Join(lines, "\n")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

type spinner struct {
	out   io.Writer
	tui   bool
	title string
	start time.Time
	stop  chan struct{}
	wg    sync.WaitGroup
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func startSpinner(out io.Writer, tui bool, title string) *spinner {
	s := &spinner{out: out, tui: tui, title: title, start: time.Now(), stop: make(chan struct{})}
	if !tui {
		fmt.Fprintf(out, "   … %s\n", title)
		return s
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			fmt.Fprintf(out, "\r\033[K   %s %s %s", styleAccent.Render(spinnerFrames[i%len(spinnerFrames)]), title,
				styleMuted.Render(elapsed(time.Since(s.start))))
			select {
			case <-s.stop:
				return
			case <-t.C:
			}
		}
	}()
	return s
}

func (s *spinner) finish(err error) {
	if s.tui {
		close(s.stop)
		s.wg.Wait()
		fmt.Fprint(s.out, "\r\033[K")
	}
	mark := CheckOK.mark(s.tui)
	if err != nil {
		mark = CheckFail.mark(s.tui)
	}
	fmt.Fprintf(s.out, "   %s %s %s\n", mark, s.title, styleIf(s.tui, styleMuted, elapsed(time.Since(s.start))))
}

func elapsed(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	return d.Truncate(time.Second).String()
}
