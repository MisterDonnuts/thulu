package api

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func nativeToolsServer(t *testing.T) (*Server, *agentstore.Store) {
	t.Helper()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir()}
	srv.SetAgentStore(store)
	return srv, store
}

func catalogByName(t *testing.T, srv *Server) map[string]ToolDef {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/tools", nil)
	defs, err := srv.catalogTools(r)
	if err != nil {
		t.Fatalf("catalogTools: %v", err)
	}
	out := map[string]ToolDef{}
	for _, d := range defs {
		if _, dup := out[d.Name]; dup {
			t.Fatalf("%s is in the catalog twice", d.Name)
		}
		out[d.Name] = d
	}
	return out
}

func toolScope(def ToolDef, name string) string {
	for _, tl := range def.Tools {
		if tl.Name == name {
			return tl.Scope
		}
	}
	return "<absent>"
}

func TestCatalogTools_FreshInstallDescribesTheShippedServers(t *testing.T) {
	t.Parallel()
	srv, _ := nativeToolsServer(t)
	got := catalogByName(t, srv)

	for _, name := range []string{"octopus-api", "thulu-github", "thulu-browser", "thulu-sandbox", "thulu-cloud"} {
		if got[name].Source != "builtin" {
			t.Errorf("%s: source %q, want builtin (catalog %v)", name, got[name].Source, got)
		}
	}
	api := got["octopus-api"]
	if api.DefaultScope != "read" {
		t.Errorf("octopus-api default_scope = %q, want read: a bare grant is read-only", api.DefaultScope)
	}
	if s := toolScope(api, "task_get"); s != "read" {
		t.Errorf("octopus-api task_get scope = %q, want read", s)
	}
	if s := toolScope(api, "task_create"); s != "write" {
		t.Errorf("octopus-api task_create scope = %q, want write", s)
	}
	if got["thulu-github"].DefaultScope != "write" {
		t.Errorf("thulu-github default_scope = %q, want write", got["thulu-github"].DefaultScope)
	}
}

func TestCatalogTools_OneImportedToolDoesNotHideTheShippedOnes(t *testing.T) {
	t.Parallel()
	srv, store := nativeToolsServer(t)
	ctx := context.Background()
	if _, err := store.PutFile(ctx, agentstore.GlobalTenant, agentstore.KindTool, "ticket-reader",
		"tool.yaml", []byte("name: ticket-reader\ndescription: Read tickets\n"), "test"); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	got := catalogByName(t, srv)

	if got["ticket-reader"].Source != "store" {
		t.Errorf("ticket-reader: %+v, want the stored descriptor", got["ticket-reader"])
	}
	if got["octopus-api"].Source != "builtin" || got["octopus-api"].DefaultScope != "read" {
		t.Errorf("octopus-api: %+v, want the embedded descriptor beside the imported tool", got["octopus-api"])
	}
}

func TestCatalogTools_StoredCopyWinsOverTheEmbeddedOne(t *testing.T) {
	t.Parallel()
	srv, store := nativeToolsServer(t)
	ctx := context.Background()
	hotfix := "name: octopus-api\ndescription: operator hotfix\ndefault_scope: write\n" +
		"tools:\n  - name: task_get\n    scope: read\n"
	if _, err := store.PutFile(ctx, agentstore.GlobalTenant, agentstore.KindTool, "octopus-api",
		"tool.yaml", []byte(hotfix), "test"); err != nil {
		t.Fatalf("put tool: %v", err)
	}
	api := catalogByName(t, srv)["octopus-api"]
	if api.Source != "store" || api.Description != "operator hotfix" || api.DefaultScope != "write" {
		t.Errorf("octopus-api = %+v, want the stored copy whole", api)
	}
	if len(api.Tools) != 1 {
		t.Errorf("octopus-api has %d tools, want the stored copy's 1: no mixing with the embedded list", len(api.Tools))
	}
}

func TestRunBundle_CarriesTheEmbeddedManualOfAWiredShippedServer(t *testing.T) {
	t.Parallel()
	srv, store := nativeToolsServer(t)
	ctx := context.Background()
	at := types.AgentType{
		Name: "developer",
		MCPByStage: map[string][]string{
			"in_progress": {"thulu-github@write", "thulu-browser", "octopus-api@write"},
		},
	}
	bundle := func() map[string]string {
		t.Helper()
		var buf bytes.Buffer
		if err := writeRunBundle(ctx, &buf, srv.content(ctx, "ws-native"), at, "in_progress"); err != nil {
			t.Fatalf("write bundle: %v", err)
		}
		return entries(t, buf.Bytes())
	}

	want, err := fs.ReadFile(builtin.NativeTools(), "thulu-github/skill.md")
	if err != nil {
		t.Fatalf("embedded manual: %v", err)
	}
	got := bundle()
	if got["tools/thulu-github/skill.md"] != string(want) {
		t.Errorf("thulu-github manual missing or not the embedded one; bundle has %v", keysOf(got))
	}
	if _, ok := got["tools/thulu-browser/skill.md"]; !ok {
		t.Errorf("thulu-browser manual missing; bundle has %v", keysOf(got))
	}
	for name := range got {
		if name == "tools/octopus-api/skill.md" || name == "tools/thulu-github/tool.yaml" {
			t.Errorf("bundle carries %s", name)
		}
	}

	if _, err := store.PutFile(ctx, agentstore.GlobalTenant, agentstore.KindTool, "thulu-github",
		agentstore.ToolManualFile, []byte("hotfix manual\n"), "test"); err != nil {
		t.Fatalf("put manual: %v", err)
	}
	if got := bundle(); got["tools/thulu-github/skill.md"] != "hotfix manual\n" {
		t.Errorf("thulu-github manual = %q, want the stored hotfix", got["tools/thulu-github/skill.md"])
	}
}
