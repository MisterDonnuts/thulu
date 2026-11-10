package i18n_test

// A string that is not a message carries `//i18n:ignore <why>` on its line or the line above.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	modulePath = "github.com/MisterDonnuts/thulu"
	i18nPath   = modulePath + "/internal/i18n"
	apperrPath = modulePath + "/internal/apperr"
	ignoreMark = "i18n:ignore"
)

var keyArg = map[string]int{
	i18nPath + ".T": 1, i18nPath + ".TCtx": 1, i18nPath + ".Text": 0, i18nPath + ".Log": 0,
	apperrPath + ".New": 0, apperrPath + ".Invalid": 0, apperrPath + ".Unauthenticated": 0,
	apperrPath + ".Forbidden": 0, apperrPath + ".NotFound": 0, apperrPath + ".Conflict": 0,
	apperrPath + ".Precondition": 0, apperrPath + ".Unprocessable": 0, apperrPath + ".TooLarge": 0, apperrPath + ".RateLimited": 0,
	apperrPath + ".Internal": 0, apperrPath + ".NotImplemented": 0, apperrPath + ".Unavailable": 0,
	apperrPath + ".Timeout": 0, apperrPath + ".Wrap": 1, apperrPath + ".WrapKind": 2,
}

var sinkArg = map[string]int{
	"errors.New": 0, "fmt.Errorf": 0, "net/http.Error": 1,
	"fmt.Print": 0, "fmt.Printf": 0, "fmt.Println": 0,
	"fmt.Fprint": 1, "fmt.Fprintf": 1, "fmt.Fprintln": 1,
	"log.Print": 0, "log.Printf": 0, "log.Println": 0, "log.Fatal": 0, "log.Fatalf": 0,
	"log.Fatalln": 0, "log.Panic": 0, "log.Panicf": 0, "log.Panicln": 0,
	"log/slog.Info": 0, "log/slog.Warn": 0, "log/slog.Error": 0, "log/slog.Debug": 0,
	"log/slog.InfoContext": 1, "log/slog.WarnContext": 1, "log/slog.ErrorContext": 1,
	"log/slog.DebugContext": 1,
}

var sinkMethods = map[string]bool{
	"Printf": true, "Println": true, "Print": true, "Fatalf": true, "Fatal": true,
	"Info": true, "Warn": true, "Error": true, "Debug": true,
	"Infof": true, "Warnf": true, "Errorf": true, "Debugf": true,
}

var wrapsCause = map[string]bool{}

var keyedMethods = map[string]bool{
	"Get": true, "Set": true, "Add": true, "Del": true, "Values": true, "Cookie": true,
	"FormValue": true, "PostFormValue": true, "PathValue": true, "Getenv": true,
	"LookupEnv": true, "Setenv": true, "Unsetenv": true, "Query": true,
}

var skipDirs = []string{".git", ".claude", "node_modules", "vendor", "testdata", "internal/i18n"}

type file struct {
	path    string
	pkg     string
	ast     *ast.File
	imports map[string]string
	ignored map[int]bool // lines a //i18n:ignore covers
}

type guard struct {
	root   string
	fset   *token.FileSet
	files  []*file
	cat    *i18n.Catalog
	groups map[string]bool
	used   map[string]bool
	errs   map[string][]string
}

func TestCatalogGuard(t *testing.T) {
	g := newGuard(t)
	g.collectForwarders()
	for _, f := range g.files {
		g.checkFile(f)
	}
	g.checkUnused()
	g.checkLocales()
	g.report(t)
}

func newGuard(t *testing.T) *guard {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	g := &guard{
		root: root, fset: token.NewFileSet(), cat: i18n.Default(),
		groups: map[string]bool{}, used: map[string]bool{}, errs: map[string][]string{},
	}
	for _, k := range g.cat.Keys(i18n.SourceLocale) {
		parts := strings.Split(k, ".")
		for i := 2; i < len(parts); i++ {
			g.groups[strings.Join(parts[:i], ".")] = true
		}
	}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			for _, s := range skipDirs {
				if rel == s || d.Name() == s {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		af, err := parser.ParseFile(g.fset, p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		g.files = append(g.files, g.newFile(rel, af))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func (g *guard) newFile(rel string, af *ast.File) *file {
	f := &file{
		path: rel, ast: af, imports: map[string]string{}, ignored: map[int]bool{},
		pkg: modulePath + "/" + filepath.ToSlash(filepath.Dir(rel)),
	}
	for _, imp := range af.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		f.imports[name] = p
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, ignoreMark) {
				line := g.fset.Position(c.Slash).Line
				f.ignored[line] = true
				f.ignored[line+1] = true
			}
		}
	}
	return f
}

func (g *guard) problem(f *file, pos token.Pos, msg string) {
	line := g.fset.Position(pos).Line
	g.errs[f.path] = append(g.errs[f.path], strconv.Itoa(line)+": "+msg)
}

func (f *file) callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return f.pkg + "." + fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			if p, ok := f.imports[x.Name]; ok {
				return p + "." + fn.Sel.Name
			}
		}
		return f.pkg + "." + fn.Sel.Name
	}
	return ""
}

func (g *guard) collectForwarders() {
	for changed := true; changed; {
		changed = false
		for _, f := range g.files {
			for _, decl := range f.ast.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				name := f.pkg + "." + fd.Name.Name
				if _, known := keyArg[name]; known {
					continue
				}
				idx := paramIndex(fd, "key")
				if idx < 0 {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if ki, ok := keyArg[f.callee(call)]; ok && ki < len(call.Args) {
						if id, ok := call.Args[ki].(*ast.Ident); ok && id.Name == "key" {
							keyArg[name] = idx
							callee := f.callee(call)
							if strings.HasSuffix(callee, ".Wrap") || strings.HasSuffix(callee, ".WrapKind") || wrapsCause[callee] {
								wrapsCause[name] = true
							}
							changed = true
							return false
						}
					}
					return true
				})
			}
		}
	}
}

func paramIndex(fd *ast.FuncDecl, want string) int {
	i := 0
	for _, field := range fd.Type.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, n := range field.Names {
			if n.Name == want {
				return i
			}
			i++
		}
	}
	return -1
}

func (g *guard) checkFile(f *file) {
	keyLits := map[*ast.BasicLit]bool{}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.Field:
			x.Tag = nil
		case *ast.CallExpr:
			g.checkCall(f, x, keyLits)
			g.checkSink(f, x, keyLits)
			markKeyedArg(x, keyLits)
		case *ast.BasicLit:
			if x.Kind == token.STRING && !keyLits[x] {
				g.checkLiteral(f, x)
			}
		}
		return true
	})
}

func (g *guard) checkCall(f *file, call *ast.CallExpr, keyLits map[*ast.BasicLit]bool) {
	ki, ok := keyArg[f.callee(call)]
	if !ok || ki >= len(call.Args) {
		return
	}
	lit, ok := call.Args[ki].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	keyLits[lit] = true
	key, _ := strconv.Unquote(lit.Value)
	g.used[key] = true
	if !g.cat.Has(i18n.SourceLocale, key) {
		g.problem(f, lit.Pos(), "key "+key+" is not in the "+i18n.SourceLocale+" catalog")
		return
	}
	if call.Ellipsis.IsValid() {
		return
	}
	rest := call.Args[ki+1:]
	if len(rest)%2 != 0 {
		g.problem(f, lit.Pos(), "key "+key+": arguments must be name/value pairs")
		return
	}
	given := map[string]bool{}
	for i := 0; i < len(rest); i += 2 {
		nameLit, ok := rest[i].(*ast.BasicLit)
		if !ok || nameLit.Kind != token.STRING {
			g.problem(f, rest[i].Pos(), "key "+key+": argument names must be string literals")
			return
		}
		keyLits[nameLit] = true
		name, _ := strconv.Unquote(nameLit.Value)
		given[name] = true
	}
	want := map[string]bool{}
	for _, tmpl := range g.cat.Templates(i18n.SourceLocale, key) {
		for _, p := range i18n.Placeholders(tmpl) {
			want[p] = true
		}
	}
	if strings.HasSuffix(f.callee(call), ".Wrap") || strings.HasSuffix(f.callee(call), ".WrapKind") || wrapsCause[f.callee(call)] {
		given["cause"] = true
		want["cause"] = true
	}
	for p := range want {
		if !given[p] {
			g.problem(f, lit.Pos(), "key "+key+": the message uses {"+p+"} but the call does not pass it")
		}
	}
	for p := range given {
		if !want[p] {
			g.problem(f, lit.Pos(), "key "+key+": the call passes "+p+" but the message does not use it")
		}
	}
}

func markKeyedArg(call *ast.CallExpr, seen map[*ast.BasicLit]bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !keyedMethods[sel.Sel.Name] || len(call.Args) == 0 {
		return
	}
	if lit, ok := call.Args[0].(*ast.BasicLit); ok {
		seen[lit] = true
	}
}

func (g *guard) checkSink(f *file, call *ast.CallExpr, seen map[*ast.BasicLit]bool) {
	idx, ok := -1, false
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if fn.Name == "panic" {
			idx, ok = 0, true
		}
	case *ast.SelectorExpr:
		if idx, ok = sinkArg[f.callee(call)]; !ok {
			if x, isIdent := fn.X.(*ast.Ident); !isIdent || f.imports[x.Name] == "" {
				idx, ok = 0, sinkMethods[fn.Sel.Name]
			}
		}
	}
	if !ok || idx >= len(call.Args) {
		return
	}
	lit, isLit := call.Args[idx].(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING || seen[lit] {
		return
	}
	seen[lit] = true
	s, _ := strconv.Unquote(lit.Value)
	if f.ignored[g.fset.Position(lit.Pos()).Line] {
		return
	}
	if anyWord.MatchString(logTag.ReplaceAllString(fmtVerb.ReplaceAllString(s, " "), "")) {
		g.problem(f, lit.Pos(), "message outside the catalog: "+clip(s))
	}
}

var (
	anyWord        = regexp.MustCompile(`[A-Za-z]{2,}`)
	sqlFragment    = regexp.MustCompile(`\b(IN|AND|OR|LIKE|ILIKE|IS|NOT|NULL|BETWEEN|SET|WHERE|ON|AS|DESC|ASC|BY)\b.*(\?|\$\d)|(\?|\$\d).*\b(IN|AND|OR|LIKE|ILIKE|IS|NOT|NULL|BETWEEN|SET|WHERE|ON|AS|DESC|ASC|BY)\b`)
	identList      = regexp.MustCompile(`^\s*[a-z0-9_.]+(\s*,\s*[a-z0-9_.]+)+,?\s*$`)
	fmtVerb        = regexp.MustCompile(`%[-+# 0-9.*\[\]]*[a-zA-Z%]`)
	logTag         = regexp.MustCompile(`^\[[a-z0-9_ -]+\]`)
	words          = regexp.MustCompile(`[A-Za-z][a-z']+[ ,;:!?]+[A-Za-z]{2,}`)
	sentence       = regexp.MustCompile(`^[A-Z][a-z']+[.!?…]?$`)
	keyShape       = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)
	sqlPrefix      = regexp.MustCompile(`(?s)^\s*\(?\s*(` + sqlKeywords + `)\b`)
	sqlPrefixLower = regexp.MustCompile(`(?s)^\s*\(?\s*(` + strings.ToLower(sqlKeywords) + `)\b`)
	sqlSyntax      = regexp.MustCompile(`[=()*?$_;]|\b(from|set|into|table|index|where|values|returning|exists|not null|primary key)\b`)
)

const sqlKeywords = `SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|WITH|BEGIN|COMMIT|ROLLBACK|SET|FROM|WHERE|AND|OR|ORDER|GROUP|LIMIT|` +
	`OFFSET|LEFT|INNER|JOIN|ON|GRANT|REVOKE|TRUNCATE|VALUES|RETURNING|CASE|COALESCE|LOCK|DO|UNION|HAVING|NOT|IS|NULL|EXISTS|USING|ADD|` +
	`PRIMARY|REFERENCES|DEFAULT`

func isSQL(s string) bool {
	return sqlPrefix.MatchString(s) || (sqlPrefixLower.MatchString(s) && sqlSyntax.MatchString(s))
}

func (g *guard) checkLiteral(f *file, lit *ast.BasicLit) {
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return
	}
	if keyShape.MatchString(s) {
		if parts := strings.Split(s, "."); len(parts) > 2 && g.groups[parts[0]+"."+parts[1]] {
			g.used[s] = true
			if !g.cat.Has(i18n.SourceLocale, s) {
				g.problem(f, lit.Pos(), "key "+s+" is not in the "+i18n.SourceLocale+" catalog")
			}
			return
		}
	}
	if f.ignored[g.fset.Position(lit.Pos()).Line] || isSQL(s) || sqlFragment.MatchString(s) || identList.MatchString(s) {
		return
	}
	text := logTag.ReplaceAllString(fmtVerb.ReplaceAllString(s, " "), "")
	if words.MatchString(text) || sentence.MatchString(strings.TrimSpace(text)) {
		g.problem(f, lit.Pos(), "text outside the catalog: "+clip(s))
	}
}

func (g *guard) checkUnused() {
	var unused []string
	for _, k := range g.cat.Keys(i18n.SourceLocale) {
		if !g.used[k] {
			unused = append(unused, k)
		}
	}
	if len(unused) > 0 && scope() == "" {
		g.errs["(catalog)"] = append(g.errs["(catalog)"], "keys no code names: "+strings.Join(unused, ", "))
	}
}

func (g *guard) checkLocales() {
	src := g.cat.Keys(i18n.SourceLocale)
	for _, loc := range g.cat.Locales() {
		if loc == i18n.SourceLocale {
			continue
		}
		where := "(catalog " + loc + ")"
		have := map[string]bool{}
		for _, k := range g.cat.Keys(loc) {
			have[k] = true
		}
		for _, k := range src {
			if !have[k] {
				g.errs[where] = append(g.errs[where], "missing "+k)
				continue
			}
			delete(have, k)
			if a, b := placeholderSet(g.cat.Templates(i18n.SourceLocale, k)), placeholderSet(g.cat.Templates(loc, k)); a != b {
				g.errs[where] = append(g.errs[where], k+": variables {"+b+"}, the source has {"+a+"}")
			}
		}
		for k := range have {
			g.errs[where] = append(g.errs[where], "not in the source: "+k)
		}
	}
}

func placeholderSet(tmpls []string) string {
	set := map[string]bool{}
	for _, t := range tmpls {
		for _, p := range i18n.Placeholders(t) {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func scope() string { return os.Getenv("I18N_GUARD_PATH") }

func (g *guard) report(t *testing.T) {
	var paths []string
	total := 0
	for p, errs := range g.errs {
		if s := scope(); s != "" && !strings.HasPrefix(p, s) {
			continue
		}
		paths = append(paths, p)
		total += len(errs)
	}
	if total == 0 {
		return
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		for _, e := range g.errs[p] {
			b.WriteString(p + ":" + e + "\n")
		}
	}
	t.Errorf("%d i18n problems:\n%s", total, b.String())
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	if len(s) > 100 {
		return s[:100] + "…"
	}
	return s
}
