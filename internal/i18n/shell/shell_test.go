package shell_test

// A string that is words but not a message carries `# i18n-ignore <why>` on its line or the line
// above.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the generated i18n blocks in the scripts")

const (
	sourceLocale = "en"
	beginMark    = "# BEGIN i18n"
	endMark      = "# END i18n"
	ignoreMark   = "i18n-ignore"
)

type localeSource int

const (
	terminal localeSource = iota
	// The container's LANG/LC_* are the image's, not the reader's; only OCTOPUS_LOCALE counts.
	daemon
)

type target struct {
	path    string
	scripts []string
	locale  localeSource
	tabs    bool
}

var targets = []target{
	{path: "install.sh", locale: terminal, tabs: true},
	{path: "scripts/setup-auth.sh", locale: terminal},
	{path: "scripts/aws-observability-credential.sh", locale: terminal},
	{path: "scripts/gcp-observability-credential.sh", locale: terminal},
	{path: "deploy/backup.sh", locale: terminal},
	{path: "deploy/k8s-data-plane/render.sh", locale: terminal},
	{
		path:    "images/snippets/i18n.sh",
		scripts: []string{"images/entrypoint-base.sh", "images/kube-agent-entrypoint.sh", "images/snippets/"},
		locale:  daemon,
	},
}

var skipScripts = map[string]string{
	"scripts/ci/":                    "CI tooling, read by people debugging a workflow run",
	"scripts/compat.sh":              "developer tool",
	"scripts/dev-version.sh":         "developer tool",
	"scripts/third-party-notices.sh": "maintainer tool; its output is the English THIRD_PARTY_NOTICES file",
	"scripts/k8s-dev.sh":             "developer tool",
	"internal/api/scaffold/":         "a starter written into the user's agent repository, which they own and edit",
	"internal/i18n/shell/":           "this guard's own fixtures",
	"web/node_modules/":              "third-party code",
	"tools/thulu-browser/node_":      "third-party code",
}

var skipDirs = map[string]bool{".git": true, ".claude": true, "node_modules": true, "vendor": true, "testdata": true}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

var pluralForms = map[string]bool{"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true}

type message map[string]string

type catalog map[string]map[string]message

func loadCatalog(t *testing.T, dir string) catalog {
	t.Helper()
	cat := catalog{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		loc := e.Name()
		cat[loc] = map[string]message{}
		base := filepath.Join(dir, loc)
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
				return err
			}
			rel, _ := filepath.Rel(base, strings.TrimSuffix(p, ".json"))
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var tree map[string]any
			if err := json.Unmarshal(b, &tree); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			return flatten(cat[loc], strings.ReplaceAll(filepath.ToSlash(rel), "/", "."), tree)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if cat[sourceLocale] == nil {
		t.Fatalf("no %s catalog under %s", sourceLocale, dir)
	}
	return cat
}

func flatten(into map[string]message, prefix string, tree map[string]any) error {
	for k, v := range tree {
		key := prefix + "." + k
		switch v := v.(type) {
		case string:
			into[key] = message{"": v}
		case map[string]any:
			if m, ok := plural(v); ok {
				into[key] = m
				continue
			}
			if err := flatten(into, key, v); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: a message must be a string or a plural object", key)
		}
	}
	return nil
}

func plural(v map[string]any) (message, bool) {
	if _, ok := v["other"]; !ok {
		return nil, false
	}
	m := message{}
	for form, text := range v {
		s, ok := text.(string)
		if !ok || !pluralForms[form] {
			return nil, false
		}
		m[form] = s
	}
	return m, true
}

var placeholder = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

func (m message) vars() map[string]bool {
	vs := map[string]bool{}
	for _, text := range m {
		for _, sub := range placeholder.FindAllStringSubmatch(text, -1) {
			vs[sub[1]] = true
		}
	}
	return vs
}

func (m message) isPlural() bool { _, ok := m["other"]; return ok }

func (c catalog) locales() []string {
	var ls []string
	for l := range c {
		ls = append(ls, l)
	}
	sort.Strings(ls)
	return ls
}

type literal struct {
	line   int
	text   string
	cmd    string
	quoted bool
}

type call struct {
	line int
	fn   string
	args []string
}

type heredoc struct {
	delim  string
	quoted bool
	strip  bool
	line   int
	cmd    string
}

type lexer struct {
	src     string
	i       int
	line    int
	lits    []literal
	calls   []call
	pending []heredoc
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

func isNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

const metachars = " \t\n;&|()<>"

var (
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?\+?=`)
	keywords   = map[string]bool{
		"{": true, "}": true, "!": true, "if": true, "then": true, "else": true, "elif": true,
		"fi": true, "do": true, "done": true, "while": true, "until": true, "time": true,
	}
	paramOp = regexp.MustCompile(`(?s)^[#!]?([A-Za-z_][A-Za-z0-9_]*|[0-9@*#?$!-])(\[[^]]*\])?(:?[-=?+])(.*)$`)
)

func (lx *lexer) code(nested bool) {
	depth, cmdLine := 0, 0
	cmd, atStart := "", true
	var args []string
	finish := func() {
		if cmd == "msg" || cmd == "msg_en" {
			lx.calls = append(lx.calls, call{line: cmdLine, fn: cmd, args: args})
		}
		cmd, atStart, args = "", true, nil
	}
	for lx.i < len(lx.src) {
		c := lx.src[lx.i]
		switch {
		case c == '\n':
			finish()
			lx.newline()
		case isBlank(c):
			lx.i++
		case c == '\\' && lx.i+1 < len(lx.src) && lx.src[lx.i+1] == '\n':
			lx.i += 2
			lx.line++
		case c == '#':
			for lx.i < len(lx.src) && lx.src[lx.i] != '\n' {
				lx.i++
			}
		case c == ';' || c == '&' || c == '|':
			finish()
			lx.i++
		case c == '(':
			finish()
			if nested {
				depth++
			}
			lx.i++
		case c == ')':
			finish()
			lx.i++
			if nested {
				if depth == 0 {
					return
				}
				depth--
			}
		case c == '<' && strings.HasPrefix(lx.src[lx.i:], "<<") && !strings.HasPrefix(lx.src[lx.i:], "<<<"):
			lx.heredocStart(cmd)
		case c == '<' || c == '>':
			for lx.i < len(lx.src) && strings.IndexByte("<>&|", lx.src[lx.i]) >= 0 {
				lx.i++
			}
			for lx.i < len(lx.src) && isBlank(lx.src[lx.i]) {
				lx.i++
			}
			lx.word(cmd)
		default:
			line := lx.line
			w := lx.word(cmd)
			if lx.i < len(lx.src) && (lx.src[lx.i] == '<' || lx.src[lx.i] == '>') && strings.Trim(w, "0123456789") == "" {
				continue
			}
			switch {
			case !atStart:
				args = append(args, w)
			case assignment.MatchString(w) || keywords[w]:
			default:
				cmd, atStart, cmdLine = w, false, line
			}
		}
	}
	finish()
}

func (lx *lexer) word(cmd string) string {
	start := lx.i
	var plain strings.Builder
	line := lx.line
	for lx.i < len(lx.src) {
		c := lx.src[lx.i]
		switch {
		case strings.IndexByte(metachars, c) >= 0:
			lx.add(line, plain.String(), cmd, false)
			return lx.src[start:lx.i]
		case c == '\\' && lx.i+1 < len(lx.src) && lx.src[lx.i+1] == '\n':
			lx.add(line, plain.String(), cmd, false)
			return lx.src[start:lx.i]
		case c == '\\':
			if lx.i+1 < len(lx.src) {
				plain.WriteByte(lx.src[lx.i+1])
			}
			lx.i += 2
		case c == '\'':
			lx.single(cmd)
		case c == '"':
			lx.double(cmd)
		case c == '$':
			lx.dollar(cmd, nil)
		case c == '`':
			lx.backtick()
		default:
			plain.WriteByte(c)
			lx.i++
		}
	}
	lx.add(line, plain.String(), cmd, false)
	return lx.src[start:]
}

func (lx *lexer) addLit(line int, text, cmd string) { lx.add(line, text, cmd, true) }

func (lx *lexer) add(line int, text, cmd string, quoted bool) {
	if strings.TrimSpace(text) != "" {
		lx.lits = append(lx.lits, literal{line: line, text: text, cmd: cmd, quoted: quoted})
	}
}

func (lx *lexer) single(cmd string) {
	line := lx.line
	lx.i++
	end := strings.IndexByte(lx.src[lx.i:], '\'')
	if end < 0 {
		end = len(lx.src) - lx.i
	}
	text := lx.src[lx.i : lx.i+end]
	lx.line += strings.Count(text, "\n")
	lx.i += end + 1
	lx.addLit(line, text, cmd)
}

func (lx *lexer) double(cmd string) {
	line := lx.line
	lx.i++
	var buf strings.Builder
	lx.text(cmd, &buf, '"')
	lx.addLit(line, buf.String(), cmd)
}

func (lx *lexer) text(cmd string, buf *strings.Builder, end byte) {
	for lx.i < len(lx.src) {
		c := lx.src[lx.i]
		switch {
		case end != 0 && c == end:
			lx.i++
			return
		case c == '\\' && lx.i+1 < len(lx.src):
			if lx.src[lx.i+1] == '\n' {
				lx.line++
			} else {
				buf.WriteByte(lx.src[lx.i+1])
			}
			lx.i += 2
		case c == '$':
			lx.dollar(cmd, buf)
		case c == '`':
			lx.backtick()
		default:
			if c == '\n' {
				lx.line++
			}
			buf.WriteByte(c)
			lx.i++
		}
	}
}

func (lx *lexer) dollar(cmd string, buf *strings.Builder) {
	rest := lx.src[lx.i:]
	switch {
	case strings.HasPrefix(rest, "$(("):
		depth := 0
		for lx.i < len(lx.src) {
			switch lx.src[lx.i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			lx.i++
			if depth == 0 {
				return
			}
		}
	case strings.HasPrefix(rest, "$("):
		lx.i += 2
		lx.code(true)
		if buf != nil {
			buf.WriteByte(' ')
		}
	case strings.HasPrefix(rest, "${"):
		start := lx.i + 2
		depth := 0
		for lx.i < len(lx.src) {
			c := lx.src[lx.i]
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
			lx.i++
		}
		inner := lx.src[start:lx.i]
		lx.i++
		if m := paramOp.FindStringSubmatch(inner); m != nil && m[4] != "" {
			sub := &lexer{src: m[4], line: lx.line}
			var b strings.Builder
			sub.text(cmd, &b, 0)
			lx.calls = append(lx.calls, sub.calls...)
			lx.lits = append(lx.lits, sub.lits...)
			lx.addLit(lx.line, b.String(), cmd)
		}
	case strings.HasPrefix(rest, "$'"):
		lx.i++
		lx.single(cmd)
	default:
		lx.i++
		if lx.i < len(lx.src) && strings.IndexByte("@*#?$!-", lx.src[lx.i]) >= 0 {
			lx.i++
			return
		}
		for lx.i < len(lx.src) && isNameByte(lx.src[lx.i]) {
			lx.i++
		}
	}
}

func (lx *lexer) backtick() {
	lx.i++
	for lx.i < len(lx.src) && lx.src[lx.i] != '`' {
		if lx.src[lx.i] == '\n' {
			lx.line++
		}
		lx.i++
	}
	lx.i++
}

func (lx *lexer) heredocStart(cmd string) {
	lx.i += 2
	h := heredoc{line: lx.line, cmd: cmd}
	if lx.i < len(lx.src) && lx.src[lx.i] == '-' {
		h.strip = true
		lx.i++
	}
	for lx.i < len(lx.src) && isBlank(lx.src[lx.i]) {
		lx.i++
	}
	start := lx.i
	for lx.i < len(lx.src) && strings.IndexByte(metachars, lx.src[lx.i]) < 0 {
		lx.i++
	}
	raw := lx.src[start:lx.i]
	h.delim = strings.Trim(raw, `'"\`)
	h.quoted = h.delim != raw
	lx.pending = append(lx.pending, h)
}

func (lx *lexer) newline() {
	lx.i++
	lx.line++
	docs := lx.pending
	lx.pending = nil
	for _, h := range docs {
		var body strings.Builder
		for lx.i < len(lx.src) {
			end := strings.IndexByte(lx.src[lx.i:], '\n')
			if end < 0 {
				end = len(lx.src) - lx.i
			}
			l := lx.src[lx.i : lx.i+end]
			check := l
			if h.strip {
				check = strings.TrimLeft(l, "\t")
			}
			lx.i += end + 1
			lx.line++
			if check == h.delim {
				break
			}
			body.WriteString(l + "\n")
		}
		if h.quoted {
			lx.addLit(h.line, body.String(), h.cmd)
			continue
		}
		sub := &lexer{src: body.String(), line: h.line + 1}
		var b strings.Builder
		sub.text(h.cmd, &b, 0)
		lx.calls = append(lx.calls, sub.calls...)
		lx.lits = append(lx.lits, sub.lits...)
		lx.addLit(h.line, b.String(), h.cmd)
	}
}

var (
	tags     = regexp.MustCompile(`\b[A-Z][A-Z_-]+:|\[[a-z][a-z-]*\]|==>`)
	twoWords = regexp.MustCompile(`\p{L}{2,}[ \t]+\p{L}{2,}`)
	oneWord  = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_./=$\\-])(\p{L}{3,})(?:$|[^\p{L}\p{N}_./=-])`)
	printers = map[string]bool{
		"echo": true, "printf": true, "read": true, "say": true, "die": true, "warn": true, "unverified": true,
	}
	keyShape  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
	nameShape = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

type script struct {
	path    string
	src     string
	ignored map[int]bool
	block   [2]int
}

func readScript(t *testing.T, root, rel string) *script {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	s := &script{path: rel, src: string(b), ignored: map[int]bool{}}
	for i, l := range strings.Split(s.src, "\n") {
		n := i + 1
		if strings.Contains(l, "# "+ignoreMark) {
			s.ignored[n] = true
		}
		switch strings.TrimSpace(l) {
		case endMark:
			s.block[1] = n
		default:
			if strings.HasPrefix(strings.TrimSpace(l), beginMark) {
				s.block[0] = n
			}
		}
	}
	return s
}

func (s *script) code() string {
	if s.block[0] == 0 {
		return s.src
	}
	lines := strings.Split(s.src, "\n")
	for i := s.block[0] - 1; i < s.block[1] && i < len(lines); i++ {
		lines[i] = ""
	}
	return strings.Join(lines, "\n")
}

type guard struct {
	cat   catalog
	used  map[string]bool
	errs  []string
	byTgt map[string]map[string]bool
}

func (g *guard) problem(path string, line int, format string, args ...any) {
	g.errs = append(g.errs, fmt.Sprintf("%s:%d: %s", path, line, fmt.Sprintf(format, args...)))
}

func (g *guard) check(s *script, tgt *target) {
	lx := &lexer{src: s.code(), line: 1}
	lx.code(false)
	for _, l := range lx.lits {
		if s.ignored[l.line] || s.ignored[l.line-1] {
			continue
		}
		text := tags.ReplaceAllString(l.text, " ")
		if twoWords.MatchString(text) || l.quoted && printers[l.cmd] && hasWord(text) {
			g.problem(s.path, l.line, "text outside the catalog: %s", clip(l.text))
		}
	}
	for _, c := range lx.calls {
		g.checkCall(s, tgt, c)
	}
}

func hasWord(text string) bool {
	for _, m := range oneWord.FindAllStringSubmatch(text, -1) {
		if strings.ToUpper(m[1]) != m[1] {
			return true
		}
	}
	return false
}

func (g *guard) checkCall(s *script, tgt *target, c call) {
	if tgt == nil {
		g.problem(s.path, c.line, "%s is called but no generated block covers this script (see targets)", c.fn)
		return
	}
	if len(c.args) == 0 {
		g.problem(s.path, c.line, "%s without a key", c.fn)
		return
	}
	key := strings.Trim(c.args[0], `'"`)
	if !keyShape.MatchString(key) {
		g.problem(s.path, c.line, "%s takes a literal key, got %s", c.fn, c.args[0])
		return
	}
	g.used[key] = true
	g.byTgt[tgt.path][key] = true
	msg, ok := g.cat[sourceLocale][key]
	if !ok {
		g.problem(s.path, c.line, "key %s is not in the %s catalog", key, sourceLocale)
		return
	}
	rest := c.args[1:]
	if len(rest)%2 != 0 {
		g.problem(s.path, c.line, "%s %s: a variable without a value", c.fn, key)
		return
	}
	given := map[string]bool{}
	for i := 0; i < len(rest); i += 2 {
		if !nameShape.MatchString(rest[i]) {
			g.problem(s.path, c.line, "%s %s: variable name %s is not a literal name", c.fn, key, rest[i])
			continue
		}
		given[rest[i]] = true
	}
	want := msg.vars()
	if msg.isPlural() {
		want["count"] = true
	}
	for v := range want {
		if !given[v] {
			g.problem(s.path, c.line, "%s %s: missing variable %s", c.fn, key, v)
		}
	}
	for v := range given {
		if !want[v] {
			g.problem(s.path, c.line, "%s %s: the message has no variable %s", c.fn, key, v)
		}
	}
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

func (g *guard) checkLocales() {
	src := g.cat[sourceLocale]
	for _, loc := range g.cat.locales() {
		if loc == sourceLocale {
			continue
		}
		for key, m := range src {
			other, ok := g.cat[loc][key]
			switch {
			case !ok:
				g.errs = append(g.errs, fmt.Sprintf("(catalog %s) missing %s", loc, key))
			case m.isPlural() != other.isPlural():
				g.errs = append(g.errs, fmt.Sprintf("(catalog %s) %s: plural in one locale only", loc, key))
			case !sameVars(m.vars(), other.vars()):
				g.errs = append(g.errs, fmt.Sprintf("(catalog %s) %s: variables differ from %s", loc, key, sourceLocale))
			}
		}
		for key := range g.cat[loc] {
			if _, ok := src[key]; !ok {
				g.errs = append(g.errs, fmt.Sprintf("(catalog %s) %s is not in %s", loc, key, sourceLocale))
			}
		}
	}
}

func sameVars(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func covers(tgt target, rel string) bool {
	if len(tgt.scripts) == 0 {
		return rel == tgt.path
	}
	for _, s := range tgt.scripts {
		if rel == s || strings.HasSuffix(s, "/") && strings.HasPrefix(rel, s) {
			return true
		}
	}
	return false
}

func skipped(rel string) bool {
	for p := range skipScripts {
		if rel == p || strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

func shellScripts(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".sh") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !skipped(rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestShellCatalog(t *testing.T) {
	root := repoRoot(t)
	g := &guard{cat: loadCatalog(t, "locales"), used: map[string]bool{}, byTgt: map[string]map[string]bool{}}
	for _, tgt := range targets {
		g.byTgt[tgt.path] = map[string]bool{}
	}
	for _, rel := range shellScripts(t, root) {
		var tgt *target
		for i := range targets {
			if covers(targets[i], rel) {
				tgt = &targets[i]
				break
			}
		}
		g.check(readScript(t, root, rel), tgt)
	}
	g.checkLocales()
	var unused []string
	for key := range g.cat[sourceLocale] {
		if !g.used[key] {
			unused = append(unused, key)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		g.errs = append(g.errs, "(catalog) keys no script names: "+strings.Join(unused, ", "))
	}
	for _, tgt := range targets {
		g.checkBlock(t, root, tgt)
	}
	if len(g.errs) > 0 {
		sort.Strings(g.errs)
		t.Fatalf("%d problems:\n%s", len(g.errs), strings.Join(g.errs, "\n"))
	}
}

func (g *guard) checkBlock(t *testing.T, root string, tgt target) {
	t.Helper()
	path := filepath.Join(root, tgt.path)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, beginMark)
	end := strings.Index(src, endMark)
	if start < 0 || end < start {
		g.errs = append(g.errs, fmt.Sprintf("%s: no %q … %q block", tgt.path, beginMark, endMark))
		return
	}
	end += len(endMark)
	want := generate(tgt, g.cat, g.byTgt[tgt.path])
	if src[start:end] == want {
		return
	}
	if !*update {
		g.errs = append(g.errs, fmt.Sprintf("%s: the generated block is stale; run go test ./internal/i18n/shell -update", tgt.path))
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src[:start]+want+src[end:]), info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// literalShell is true when a message in the block holds a $ or a backtick:
// a command for the reader to run, which shellcheck (SC2016) reads as an
// expansion someone forgot to double-quote.
func literalShell(cat catalog, keys []string) bool {
	for _, loc := range cat.locales() {
		for _, key := range keys {
			for _, text := range cat[loc][key] {
				if strings.ContainsAny(text, "$`") {
					return true
				}
			}
		}
	}
	return false
}

func generate(tgt target, cat catalog, keys map[string]bool) string {
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var b strings.Builder
	w := func(depth int, line string) {
		if line == "" {
			b.WriteString("\n")
			return
		}
		ind := strings.Repeat("  ", depth)
		if tgt.tabs {
			ind = strings.Repeat("\t", depth)
		}
		b.WriteString(ind + line + "\n")
	}
	w(0, beginMark+": written by `go test ./internal/i18n/shell -update` from")
	w(0, "# internal/i18n/shell/locales. Edit the catalog, then regenerate.")
	w(0, "#")
	w(0, "# msg KEY [NAME VALUE]... prints a message in the reader's language;")
	w(0, "# msg_en prints it in English, for logs and for text kept outside Thulu.")
	w(0, "# A count chooses between a message's plural forms.")
	if tgt.locale == terminal {
		w(0, "# The language is OCTOPUS_LOCALE, else the terminal's (LC_ALL, LC_MESSAGES, LANG).")
		w(0, "_i18n_locale() {")
		w(1, `for _i18n_v in "${OCTOPUS_LOCALE:-}" "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do`)
	} else {
		w(0, "# The language is OCTOPUS_LOCALE, which the daemon passes to the container.")
		w(0, "_i18n_locale() {")
		w(1, "# shellcheck disable=SC2066 # one source here; the terminal variant loops over four")
		w(1, `for _i18n_v in "${OCTOPUS_LOCALE:-}"; do`)
	}
	w(2, "case $_i18n_v in")
	w(2, "'') ;;")
	for _, loc := range cat.locales() {
		if loc == sourceLocale {
			continue
		}
		w(2, fmt.Sprintf("%s | %s[_.@-]*) echo %s; return ;;", loc, loc, loc))
	}
	w(2, fmt.Sprintf("*) echo %s; return ;;", sourceLocale))
	w(2, "esac")
	w(1, "done")
	w(1, "echo "+sourceLocale)
	w(0, "}")
	if literalShell(cat, sorted) {
		w(0, "# shellcheck disable=SC2016 # literal text: a $ or ` in a message is for the reader to type")
	}
	w(0, "_i18n_tmpl() {")
	w(1, "case $1 in")
	for _, loc := range cat.locales() {
		for _, key := range sorted {
			m, ok := cat[loc][key]
			if !ok {
				continue
			}
			var forms []string
			for f := range m {
				forms = append(forms, f)
			}
			sort.Strings(forms)
			for _, f := range forms {
				label := loc + ":" + key
				if f != "" {
					label += ":" + f
				}
				w(1, fmt.Sprintf("%s) _i18n_t=%s ;;", quote(label), quote(m[f])))
			}
		}
	}
	w(1, "*) return 1 ;;")
	w(1, "esac")
	w(0, "}")
	w(0, "_i18n_render() {")
	w(1, "_i18n_loc=$1 _i18n_key=$2 _i18n_n='' _i18n_name=''")
	w(1, "shift 2")
	w(1, `for _i18n_a in "$@"; do`)
	w(2, `if [ -z "$_i18n_name" ]; then _i18n_name=$_i18n_a; continue; fi`)
	w(2, `if [ "$_i18n_name" = count ]; then _i18n_n=$_i18n_a; fi`)
	w(2, "_i18n_name=")
	w(1, "done")
	w(1, "_i18n_form=:other")
	w(1, `if [ "$_i18n_n" = 1 ]; then _i18n_form=:one; fi`)
	w(1, `_i18n_tmpl "$_i18n_loc:$_i18n_key$_i18n_form" || _i18n_tmpl "$_i18n_loc:$_i18n_key" ||`)
	w(2, `_i18n_tmpl "en:$_i18n_key$_i18n_form" || _i18n_tmpl "en:$_i18n_key" || _i18n_t=$_i18n_key`)
	// Single pass over the template so a value containing "{name}" is not expanded again.
	w(1, "_i18n_out=")
	w(1, "while :; do")
	w(2, "case $_i18n_t in *'{'*) ;; *) break ;; esac")
	w(2, "_i18n_out=$_i18n_out${_i18n_t%%'{'*}")
	w(2, "_i18n_t=${_i18n_t#*'{'}")
	w(2, "_i18n_ph=${_i18n_t%%'}'*} _i18n_hit=")
	w(2, "case $_i18n_t in")
	w(2, "*'}'*)")
	w(3, `for _i18n_a in "$@"; do`)
	w(4, `if [ -z "$_i18n_name" ]; then _i18n_name=$_i18n_a; continue; fi`)
	w(4, `if [ "$_i18n_name" = "$_i18n_ph" ]; then _i18n_hit=1 _i18n_val=$_i18n_a; fi`)
	w(4, "_i18n_name=")
	w(3, "done")
	w(3, ";;")
	w(2, "esac")
	w(2, `if [ -n "$_i18n_hit" ]; then`)
	w(3, "_i18n_out=$_i18n_out$_i18n_val")
	w(3, "_i18n_t=${_i18n_t#*'}'}")
	w(2, "else")
	w(3, "_i18n_out=$_i18n_out'{'")
	w(2, "fi")
	w(1, "done")
	w(1, `printf '%s\n' "$_i18n_out$_i18n_t"`)
	w(0, "}")
	w(0, `msg() { _i18n_render "$(_i18n_locale)" "$@"; }`)
	w(0, `msg_en() { _i18n_render en "$@"; }`)
	b.WriteString(endMark)
	return b.String()
}
