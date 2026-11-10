package schema

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/migrate"
)

func kinds(sql string) []string {
	var out []string
	for _, h := range classify(sql, nil) {
		out = append(out, h.kind)
	}
	return out
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want []string
	}{
		{"additive", `CREATE TABLE IF NOT EXISTS x (id TEXT PRIMARY KEY, n INT NOT NULL);
			ALTER TABLE y ADD COLUMN IF NOT EXISTS z TEXT NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS i ON y(z);`, nil},
		{"drop table", `DROP TABLE IF EXISTS github_tokens`, []string{"DROP TABLE"}},
		{"drop column", `ALTER TABLE t DROP COLUMN IF EXISTS c`, []string{"DROP COLUMN"}},
		{"rename column", `ALTER TABLE t RENAME COLUMN a TO b`, []string{"RENAME"}},
		{"rename table", `ALTER TABLE t RENAME TO u`, []string{"RENAME"}},
		{"type", `ALTER TABLE t ALTER COLUMN c TYPE BIGINT`, []string{"ALTER COLUMN TYPE"}},
		{"set data type", `ALTER TABLE t ALTER COLUMN c SET DATA TYPE BIGINT`, []string{"ALTER COLUMN TYPE"}},
		{"set not null", `ALTER TABLE t ALTER COLUMN c SET NOT NULL`, []string{"SET NOT NULL"}},
		{"not null without default", `ALTER TABLE t ADD COLUMN c TEXT NOT NULL`, []string{"NOT NULL WITHOUT DEFAULT"}},
		{"two columns, one bad", `ALTER TABLE t ADD COLUMN a TEXT NOT NULL DEFAULT 'x', ADD COLUMN b INT NOT NULL`,
			[]string{"NOT NULL WITHOUT DEFAULT"}},
		{"add constraint", `ALTER TABLE t ADD CONSTRAINT u UNIQUE (c)`, []string{"ADD CONSTRAINT"}},
		{"unique index on existing table", `CREATE UNIQUE INDEX IF NOT EXISTS u ON t(c)`, []string{"CREATE UNIQUE INDEX"}},
		{"update", `UPDATE tasks SET x = 1 WHERE y`, []string{"UPDATE"}},
		{"update with alias", `UPDATE llm_profiles AS p SET x = 1`, []string{"UPDATE"}},
		{"delete", `DELETE FROM usage_daily WHERE day < now()`, []string{"DELETE"}},
		{"truncate", `TRUNCATE t`, []string{"TRUNCATE"}},
		{"quoted identifiers", `ALTER TABLE "t" DROP COLUMN "c"`, []string{"DROP COLUMN"}},

		{"FK actions", `CREATE TABLE c (id TEXT, p TEXT REFERENCES p(id) ON DELETE CASCADE ON UPDATE CASCADE)`, nil},
		{"upsert", `INSERT INTO s (k, v) VALUES ('a', 'b') ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v`, nil},
		{"keyword in a literal", `ALTER TABLE t ADD COLUMN kind TEXT NOT NULL DEFAULT 'DROP TABLE x; DELETE FROM y'`, nil},
		{"keyword in a comment", "-- DROP TABLE x\n/* UPDATE t SET a = 1 */\nCREATE TABLE IF NOT EXISTS z (id TEXT)", nil},
		{"constraint on a table created here", `CREATE TABLE IF NOT EXISTS n (id TEXT);
			CREATE UNIQUE INDEX IF NOT EXISTS u ON n(id);
			ALTER TABLE n ADD CONSTRAINT k CHECK (id <> '');
			ALTER TABLE n ALTER COLUMN id SET NOT NULL;
			ALTER TABLE n ADD COLUMN m TEXT NOT NULL`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(tc.sql)
			if len(got) != len(tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("kinds = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestLint(t *testing.T) {
	shipped := entry("a", 1, "DROP TABLE old")
	drop := entry("a", 2, "ALTER TABLE t DROP COLUMN c")
	l := lockOf(t, []string{"0.1.0", "0.2.0"}, []Entry{shipped, drop}, map[string]string{"a/1": "0.1.0"})

	f := Lint(l, []Entry{shipped, drop})
	if len(f) != 1 || f[0].ID != 2 || f[0].Allowed {
		t.Fatalf("findings = %+v, want one blocked finding on a/2", f)
	}

	drop.Contract = &migrate.Contract{StoppedUsingIn: "0.2.0", Reason: "c moved to d in 0.2.0"}
	if f := Lint(l, []Entry{shipped, drop}); len(f) != 1 || !f[0].Allowed {
		t.Fatalf("findings = %+v, want allowed", f)
	}

	drop.Contract.StoppedUsingIn = "0.3.0"
	if f := Lint(l, []Entry{shipped, drop}); len(f) != 1 || f[0].Allowed {
		t.Fatalf("findings = %+v, want blocked", f)
	}
}

func TestLintTableFromAnotherUnreleasedMigration(t *testing.T) {
	shipped := entry("a", 1, "CREATE TABLE IF NOT EXISTS old (id TEXT)")
	create := entry("b", 1, "CREATE TABLE IF NOT EXISTS fresh (id TEXT, s TEXT)")
	index := entry("b", 2, "CREATE UNIQUE INDEX IF NOT EXISTS u ON fresh(id) WHERE s = 'running'")
	redeclare := entry("c", 1, "CREATE TABLE IF NOT EXISTS old (id TEXT)")
	indexOld := entry("c", 2, "CREATE UNIQUE INDEX IF NOT EXISTS v ON old(id)")
	all := []Entry{shipped, create, index, redeclare, indexOld}
	l := lockOf(t, []string{"0.1.0", "0.2.0"}, all, map[string]string{"a/1": "0.1.0"})

	f := Lint(l, all)
	if len(f) != 1 || f[0].Component != "c" || f[0].ID != 2 || f[0].Kind != "CREATE UNIQUE INDEX" || f[0].Allowed {
		t.Fatalf("findings = %+v, want one blocked unique index on c/2", f)
	}
}

func TestEnforcing(t *testing.T) {
	for releases, want := range map[string]bool{
		"":                  false,
		"0.1.0":             false,
		"0.1.0,0.2.0-rc.1":  false,
		"0.1.0,0.2.0":       true,
		"0.1.0,0.2.0,0.3.0": true,
	} {
		l := &Lock{}
		if releases != "" {
			for _, r := range strings.Split(releases, ",") {
				l.Releases = append(l.Releases, r)
			}
		}
		if got := Enforcing(l); got != want {
			t.Errorf("Enforcing(%s) = %v, want %v", releases, got, want)
		}
	}
}
