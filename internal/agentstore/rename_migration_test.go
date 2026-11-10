package agentstore_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/schema/schematest"
)

// The data migration of 1.0.0 (agentstore 18) against rows written with the names used before 1.0.
func TestRenameMigrationRewritesStoredNames(t *testing.T) {
	db := schematest.NewDB(t)
	ctx := context.Background()
	st, err := agentstore.New(db)
	if err != nil {
		t.Fatal(err)
	}

	prompt := "Push with mcp__argus-github__branch_push, film with argus-browser.\n" +
		"Labels: argus.mcp.default.review; prompts in /workspace/projects/shop/.argus/prompts.\n" +
		"MCP_ARGUS_CLOUD_URL and argus-notes stay as the operator wrote them only where they are not ours.\n"
	if _, err := st.PutFile(ctx, "ws1", agentstore.KindAgent, "dev", "prompts/main.md", []byte(prompt), "t"); err != nil {
		t.Fatal(err)
	}
	binary := []byte{0xff, 0xfe, 'a', 'r', 'g', 'u', 's', '-', 'g', 'i', 't', 'h', 'u', 'b'}
	if _, err := st.PutFile(ctx, "ws1", agentstore.KindAgent, "dev", "prompts/blob.bin", binary, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutFile(ctx, agentstore.GlobalTenant, agentstore.KindTool, "argus-github", "skill.md", []byte("manual"), "t"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	exec(t, db, `INSERT INTO agents (tenant_id, name, mcp_by_stage, created_at, updated_at)
		VALUES ('ws1', 'dev', '{"review": ["argus-github@write+tool", "argus-notes"]}', $1, $1)`, now)
	exec(t, db, `INSERT INTO system_skills (name, version, mcp, updated_at) VALUES ('octopus-x', 1, 'argus-browser', $1)`, now)
	exec(t, db, `INSERT INTO mcp_registrations (name, url, created_at, updated_at)
		VALUES ('argus-figma', 'http://argus-figma.octopus.svc:3000/sse', $1, $1)`, now)
	exec(t, db, `INSERT INTO credential_tool_policy (credential_id, server, tool, updated_at)
		VALUES ('c1', 'argus-github', 'pr_merge', $1)`, now)
	exec(t, db, `INSERT INTO session_tool_events (session_id, ts, tool_name, mcp_server)
		VALUES ('s1', $1, 'mcp__argus-github__pr_view', 'argus-github')`, now)

	rerun := func() {
		t.Helper()
		exec(t, db, `DELETE FROM schema_migrations WHERE component = 'agentstore' AND id = 18`)
		list, err := agentstore.Migrations(migrate.DBInspector(ctx, db))
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Run(ctx, db, agentstore.MigrationComponent, list); err != nil {
			t.Fatalf("rerun: %v", err)
		}
	}
	check := func() {
		t.Helper()
		f, err := st.ReadFile(ctx, "ws1", agentstore.KindAgent, "dev", "prompts/main.md")
		if err != nil {
			t.Fatal(err)
		}
		got := string(f.Body)
		for _, want := range []string{"mcp__thulu-github__branch_push", "thulu-browser", "thulu.mcp.default.review",
			"/workspace/projects/shop/.thulu/prompts", "MCP_THULU_CLOUD_URL", "argus-notes"} {
			if !strings.Contains(got, want) {
				t.Errorf("prompt lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "argus-browser") || strings.Contains(got, "argus.mcp") {
			t.Errorf("prompt keeps an old name:\n%s", got)
		}
		b, err := st.ReadFile(ctx, "ws1", agentstore.KindAgent, "dev", "prompts/blob.bin")
		if err != nil || string(b.Body) != string(binary) {
			t.Errorf("binary file changed: %v %q", err, b.Body)
		}
		if _, err := st.ReadFile(ctx, agentstore.GlobalTenant, agentstore.KindTool, "thulu-github", "skill.md"); err != nil {
			t.Errorf("tool manual not moved to thulu-github: %v", err)
		}
		want := map[string]string{
			`SELECT mcp_by_stage::text FROM agents WHERE name = 'dev'`:       `{"review": ["thulu-github@write+tool", "argus-notes"]}`,
			`SELECT mcp FROM system_skills WHERE name = 'octopus-x'`:         "thulu-browser",
			`SELECT name || ' ' || url FROM mcp_registrations`:               "thulu-figma http://thulu-figma.octopus.svc:3000/sse",
			`SELECT server FROM credential_tool_policy`:                      "thulu-github",
			`SELECT tool_name || ' ' || mcp_server FROM session_tool_events`: "mcp__thulu-github__pr_view thulu-github",
		}
		for q, w := range want {
			var got string
			if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if got != w {
				t.Errorf("%s = %q, want %q", q, got, w)
			}
		}
	}

	rerun()
	check()
	rerun() // a second run changes nothing
	check()
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
