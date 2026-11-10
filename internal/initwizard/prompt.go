package initwizard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var ErrSkipped error = apperr.New("initwizard.errors.step_skipped")

const skipToken = "s"

type Prompter struct {
	in  *bufio.Reader
	out io.Writer

	NonInteractive bool

	// Once input ends every prompt returns its default, so a step that refuses it must stop instead
	// of re-asking forever.
	inputClosed bool

	readSecret func(prompt string) (string, error)

	tui bool

	muted bool

	listEnd bool

	Total int
}

func NewPrompter(plain bool) *Prompter {
	p := &Prompter{in: bufio.NewReader(os.Stdin), out: os.Stdout, tui: !plain && interactiveTerminal()}
	p.readSecret = p.secretFromTTY
	return p
}

func (p *Prompter) Interactive() bool { return p.tui && !p.NonInteractive }

func NewTestPrompter(in io.Reader, out io.Writer) *Prompter {
	p := &Prompter{in: bufio.NewReader(in), out: out}
	p.readSecret = func(prompt string) (string, error) {
		fmt.Fprint(p.out, prompt)
		return p.readLine()
	}
	return p
}

// Without a TTY, reuse p.in: a second buffered reader on the same pipe would swallow the scripted
// answers.
func (p *Prompter) secretFromTTY(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return p.readLine()
	}
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(p.out)
	if err != nil {
		return "", apperr.Wrap(err, "initwizard.errors.read_secret")
	}
	return strings.TrimSpace(string(raw)), nil
}

func (p *Prompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", apperr.Wrap(err, "initwizard.errors.read_input")
	}
	if errors.Is(err, io.EOF) && line == "" {
		p.inputClosed = true
	}
	return strings.TrimSpace(line), nil
}

func (p *Prompter) cannotAskAgain() bool {
	return p.NonInteractive || p.inputClosed
}

func (p *Prompter) Printf(format string, args ...any) {
	if p.muted {
		return
	}
	fmt.Fprintf(p.out, format+"\n", args...)
}

const narrationIndent = "   "

func (p *Prompter) narrate(key string, args ...any) {
	if p.muted {
		return
	}
	text := i18n.Text(key, args...)
	if p.tui {
		text = wrapParagraphs(text, p.width()-len(narrationIndent))
	}
	fmt.Fprintln(p.out, indentLines(narrationIndent, text))
}

func indentLines(indent, text string) string {
	if indent == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

func (p *Prompter) Section(n int, title, blurb string) {
	if p.muted {
		return
	}
	if p.tui {
		p.tuiSection(n, p.Total, title, blurb)
		return
	}
	fmt.Fprintf(p.out, "\n%s\n", strings.Repeat("─", 72))
	if n > 0 {
		fmt.Fprintf(p.out, "%d. %s\n", n, title)
	} else {
		fmt.Fprintf(p.out, "%s\n", title)
	}
	if blurb != "" {
		fmt.Fprintf(p.out, "   %s\n", blurb)
	}
	fmt.Fprintln(p.out)
}

func (p *Prompter) Text(label, def string) (string, error) {
	if p.NonInteractive {
		return def, nil
	}
	if p.tui {
		return p.tuiText(label, def, false)
	}
	fmt.Fprint(p.out, promptLabel(label, def, false))
	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

func (p *Prompter) TextOrSkip(label, def string) (string, error) {
	if p.NonInteractive {
		if def == "" {
			return "", ErrSkipped
		}
		return def, nil
	}
	if p.tui {
		return p.tuiText(label, def, true)
	}
	fmt.Fprint(p.out, promptLabel(label, def, true))
	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	switch {
	case strings.EqualFold(answer, skipToken):
		return "", ErrSkipped
	case answer == "" && def == "":
		return "", ErrSkipped
	case answer == "":
		return def, nil
	}
	return answer, nil
}

func (p *Prompter) Secret(label string) (string, error) {
	if p.NonInteractive {
		return "", ErrSkipped
	}
	if p.tui {
		return p.tuiSecret(label)
	}
	answer, err := p.readSecret(fmt.Sprintf("   %s %s: ", label, i18n.Text("initwizard.prompts.enter_skip")))
	if err != nil {
		return "", err
	}
	if answer == "" {
		return "", ErrSkipped
	}
	return answer, nil
}

func (p *Prompter) YesNo(label string, def bool) (bool, error) {
	if p.NonInteractive {
		return def, nil
	}
	if p.tui {
		return p.tuiYesNo(label, def)
	}
	hint := i18n.Text("initwizard.prompts.yes_no_default_no")
	if def {
		hint = i18n.Text("initwizard.prompts.yes_no_default_yes")
	}
	// Re-ask on an answer that is neither yes nor no: reading it as "no" turned a mistyped "yes"
	// into a decision nobody made.
	for {
		fmt.Fprintf(p.out, "   %s [%s]: ", label, hint)
		answer, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes", "s", "si", "sí":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.narrate("initwizard.prompts.yes_no_invalid")
		if p.cannotAskAgain() {
			return def, nil
		}
	}
}

type Option struct {
	Value   string
	Label   string
	Detail  string
	Diagram string
}

func (p *Prompter) Choice(label string, opts []Option, def int) (string, error) {
	if len(opts) == 0 {
		return "", apperr.Internal("initwizard.errors.choice_no_options")
	}
	fallback := opts[0]
	if def > 0 && def < len(opts) {
		fallback = opts[def]
	} else {
		def = 0
	}
	if p.NonInteractive {
		return fallback.Value, nil
	}
	if p.tui {
		return p.tuiChoice(label, opts, def)
	}
	for i, o := range opts {
		fmt.Fprintf(p.out, "     %d) %s\n", i+1, o.Label)
		if o.Detail != "" {
			fmt.Fprintf(p.out, "        %s\n", o.Detail)
		}
	}
	for {
		fmt.Fprintf(p.out, "   %s [%d]: ", label, def+1)
		answer, err := p.readLine()
		if err != nil {
			return "", err
		}
		if answer == "" {
			return fallback.Value, nil
		}
		var n int
		if _, scanErr := fmt.Sscanf(answer, "%d", &n); scanErr == nil && n >= 1 && n <= len(opts) {
			return opts[n-1].Value, nil
		}
		p.narrate("initwizard.prompts.pick_number", "max", len(opts))
	}
}

func (p *Prompter) MultiChoice(label string, opts []Option, def []int) ([]string, error) {
	if len(opts) == 0 {
		return nil, apperr.Internal("initwizard.errors.multi_choice_no_options")
	}
	checked := make([]bool, len(opts))
	for _, i := range def {
		if i >= 0 && i < len(opts) {
			checked[i] = true
		}
	}
	if p.NonInteractive {
		return checkedValues(opts, checked), nil
	}
	if p.tui {
		return p.tuiMultiChoice(label, opts, checked)
	}
	for {
		p.drawChecklist(opts, checked)
		fmt.Fprintf(p.out, "   %s: ", i18n.Text("initwizard.prompts.checklist", "label", label))
		answer, err := p.readLine()
		if err != nil {
			return nil, err
		}
		if answer == "" {
			return checkedValues(opts, checked), nil
		}
		if !applyChecklistAnswer(answer, checked) {
			p.narrate("initwizard.prompts.checklist_invalid", "max", len(opts))
		}
	}
}

func (p *Prompter) drawChecklist(opts []Option, checked []bool) {
	for i, o := range opts {
		mark := " "
		if checked[i] {
			mark = "x"
		}
		fmt.Fprintf(p.out, "     [%s] %d) %s\n", mark, i+1, o.Label)
		if o.Detail != "" {
			fmt.Fprintf(p.out, "            %s\n", o.Detail)
		}
	}
}

func applyChecklistAnswer(answer string, checked []bool) bool {
	switch strings.ToLower(answer) {
	case "a", "all", "n", "none":
		all := strings.HasPrefix(strings.ToLower(answer), "a")
		for i := range checked {
			checked[i] = all
		}
		return true
	}
	toggles, ok := parseIndexes(answer, len(checked))
	if !ok {
		return false
	}
	for _, i := range toggles {
		checked[i] = !checked[i]
	}
	return true
}

// Never nil: nil means the step was never asked (see Config.MCPServers).
func checkedValues(opts []Option, checked []bool) []string {
	out := []string{}
	for i, o := range opts {
		if checked[i] {
			out = append(out, o.Value)
		}
	}
	return out
}

func parseIndexes(answer string, n int) ([]int, bool) {
	fields := strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' })
	if len(fields) == 0 {
		return nil, false
	}
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		var i int
		if _, err := fmt.Sscanf(f, "%d", &i); err != nil || i < 1 || i > n || fmt.Sprint(i) != f {
			return nil, false
		}
		out = append(out, i-1)
	}
	return out, true
}

// Redacts a URL's password so a DSN default or answer never lands in the scrollback.
func shown(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.User == nil {
		return v
	}
	if _, ok := u.User.Password(); !ok {
		return v
	}
	return u.Redacted()
}

func promptLabel(label, def string, skippable bool) string {
	var b strings.Builder
	b.WriteString("   ")
	b.WriteString(label)
	switch {
	case def != "" && skippable:
		fmt.Fprintf(&b, " %s", i18n.Text("initwizard.prompts.default_or_skip", "default", shown(def)))
	case def != "":
		fmt.Fprintf(&b, " [%s]", shown(def))
	case skippable:
		b.WriteString(" " + i18n.Text("initwizard.prompts.enter_skip"))
	}
	b.WriteString(": ")
	return b.String()
}
