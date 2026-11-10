package schema

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

// Expand/contract is enforced from this release on.
const EnforceFrom = "0.2.0"

func Enforcing(l *Lock) bool {
	for _, r := range l.Releases {
		if !semver.Less(r, EnforceFrom) {
			return true
		}
	}
	return false
}

type Finding struct {
	Component string
	ID        int
	Kind      string
	Excerpt   string
	Allowed   bool
	Why       string
}

func (f Finding) String() string {
	state := i18n.Text("schema.lint.blocked")
	if f.Allowed {
		state = i18n.Text("schema.lint.contract")
	}
	s := fmt.Sprintf("%s/%d  %-22s %s  %q", f.Component, f.ID, f.Kind, state, f.Excerpt)
	if f.Why != "" {
		s += "\n    " + f.Why
	}
	return s
}

func Lint(l *Lock, all []Entry) []Finding {
	released := map[string]bool{}
	for _, e := range l.Entries {
		if e.ReleasedIn != "" {
			released[e.Key()] = true
		}
	}
	unreleased := make([]Entry, 0, len(all))
	for _, e := range all {
		if !released[entryKey(e.Component, e.ID)] {
			unreleased = append(unreleased, e)
		}
	}
	fresh := freshTables(all, released)
	var out []Finding
	for _, e := range unreleased {
		for _, hit := range classify(e.SQL, fresh) {
			f := Finding{Component: e.Component, ID: e.ID, Kind: hit.kind, Excerpt: hit.excerpt}
			switch {
			case e.Contract == nil:
				f.Why = i18n.Text("schema.lint.no_contract")
			case !l.hasRelease(e.Contract.StoppedUsingIn):
				f.Why = i18n.Text("schema.lint.contract_release_not_cut", "release", e.Contract.StoppedUsingIn)
			default:
				f.Allowed = true
			}
			out = append(out, f)
		}
	}
	return out
}

type hit struct{ kind, excerpt string }

var (
	lineComment   = regexp.MustCompile(`--[^\n]*`)
	blockComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	stringLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)
	spaces        = regexp.MustCompile(`\s+`)

	createTable = regexp.MustCompile(`create table (?:if not exists )?([a-z_][a-z0-9_.]*)`)
	alterTable  = regexp.MustCompile(`alter table (?:if exists )?(?:only )?([a-z_][a-z0-9_.]*)`)
	addColumn   = regexp.MustCompile(`add column (?:if not exists )?[a-z_][a-z0-9_]* [^,]*`)

	rules = []struct {
		kind       string
		re         *regexp.Regexp
		newTableOK bool
	}{
		//i18n:ignore SQL pattern, not a message
		{"DROP TABLE", regexp.MustCompile(`\bdrop table\b[^;]*`), false},
		//i18n:ignore SQL pattern, not a message
		{"DROP COLUMN", regexp.MustCompile(`\bdrop column\b[^,;]*`), false},
		//i18n:ignore SQL pattern, not a message
		{"RENAME", regexp.MustCompile(`\brename (?:column |constraint )?[a-z_][a-z0-9_]* to [a-z_][a-z0-9_]*|\brename to [a-z_][a-z0-9_]*`), false},
		//i18n:ignore SQL pattern, not a message
		{"ALTER COLUMN TYPE", regexp.MustCompile(`\balter column [a-z_][a-z0-9_]* (?:set data )?type\b[^,;]*`), true},
		//i18n:ignore SQL pattern, not a message
		{"SET NOT NULL", regexp.MustCompile(`\balter column [a-z_][a-z0-9_]* set not null`), true},
		//i18n:ignore SQL pattern, not a message
		{"ADD CONSTRAINT", regexp.MustCompile(`\badd constraint\b[^,;]*`), true},
		//i18n:ignore SQL pattern, not a message
		{"CREATE UNIQUE INDEX", regexp.MustCompile(`\bcreate unique index\b[^;]*`), true},
		{"UPDATE", regexp.MustCompile(`\bupdate (?:only )?[a-z_][a-z0-9_.]* (?:as [a-z_][a-z0-9_]* )?(?:[a-z_][a-z0-9_]* )?set\b`), false},
		//i18n:ignore SQL pattern, not a message
		{"DELETE", regexp.MustCompile(`\bdelete from\b[^;]*`), false},
		{"TRUNCATE", regexp.MustCompile(`\btruncate\b[^;]*`), false},
	}
)

func freshTables(all []Entry, released map[string]bool) map[string]bool {
	shipped := map[string]bool{}
	fresh := map[string]bool{}
	for _, e := range all {
		into := fresh
		if released[entryKey(e.Component, e.ID)] {
			into = shipped
		}
		for _, m := range createTable.FindAllStringSubmatch(normalize(e.SQL), -1) {
			into[m[1]] = true
		}
	}
	for t := range shipped {
		delete(fresh, t)
	}
	return fresh
}

func normalize(sql string) string {
	s := blockComment.ReplaceAllString(sql, " ")
	s = lineComment.ReplaceAllString(s, " ")
	s = stringLiteral.ReplaceAllString(s, "''")
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, `"`, "")
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

func classify(sql string, fresh map[string]bool) []hit {
	norm := normalize(sql)
	created := map[string]bool{}
	for t := range fresh {
		created[t] = true
	}
	for _, m := range createTable.FindAllStringSubmatch(norm, -1) {
		created[m[1]] = true
	}
	var out []hit
	for _, stmt := range strings.Split(norm, ";") {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			out = append(out, classifyStatement(stmt, created)...)
		}
	}
	return out
}

func classifyStatement(stmt string, created map[string]bool) []hit {
	var out []hit
	target := statementTable(stmt)
	for _, r := range rules {
		if r.newTableOK && created[target] {
			continue
		}
		for _, m := range r.re.FindAllString(stmt, -1) {
			if r.kind == "UPDATE" && strings.HasPrefix(m, "update set") {
				continue
			}
			out = append(out, hit{r.kind, excerpt(m)})
		}
	}
	if !strings.HasPrefix(stmt, "alter table") || created[target] {
		return out
	}
	for _, m := range addColumn.FindAllString(stmt, -1) {
		if strings.Contains(m, "not null") && !strings.Contains(m, "default") {
			out = append(out, hit{"NOT NULL WITHOUT DEFAULT", excerpt(m)})
		}
	}
	return out
}

func statementTable(stmt string) string {
	if m := alterTable.FindStringSubmatch(stmt); m != nil {
		return m[1]
	}
	if i := strings.Index(stmt, " on "); i >= 0 && strings.HasPrefix(stmt, "create") {
		f := strings.Fields(stmt[i+4:])
		if len(f) > 0 && f[0] == "only" {
			f = f[1:]
		}
		if len(f) > 0 {
			return strings.TrimRight(strings.SplitN(f[0], "(", 2)[0], " ")
		}
	}
	return ""
}

func excerpt(s string) string {
	const limit = 80
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
