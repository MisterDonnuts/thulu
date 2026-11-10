package agentscripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/mcp"
)

func snippet(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(root, "images", "snippets", name)
}

func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the MCP writer tests")
	}
	return node
}

func serversJSON(t *testing.T, entries ...mcp.ServerEntry) string {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func genMCP(t *testing.T, servers, apiKey string) string {
	t.Helper()
	node := nodeOrSkip(t)
	out := filepath.Join(t.TempDir(), "mcp.json")
	cmd := exec.Command(node, snippet(t, "mcp-json.js"), out)
	cmd.Env = append(os.Environ(), mcp.ServersEnv+"="+servers, "OCTOPUS_API_KEY="+apiKey)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node mcp-json.js: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func codexArgs(t *testing.T, servers, apiKey string, extra ...string) []string {
	t.Helper()
	node := nodeOrSkip(t)
	cmd := exec.Command(node, append([]string{snippet(t, "mcp-codex-args.js")}, extra...)...)
	cmd.Env = append(os.Environ(), mcp.ServersEnv+"="+servers, "OCTOPUS_API_KEY="+apiKey)
	cmd.Stderr = nil
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("node mcp-codex-args.js: %v", err)
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

var (
	proxiedSSE  = mcp.ServerEntry{Name: "octopus-api", URL: "http://d:8585/mcp-proxy/octopus-api", Transport: "sse", RunKey: true}
	proxiedHTTP = mcp.ServerEntry{Name: "jira", URL: "http://d:8585/mcp-proxy/jira", Transport: "http", RunKey: true}
	keyless     = mcp.ServerEntry{Name: "docs", URL: "https://docs.example/mcp", Transport: "http"}
)

func TestMCPJSONGolden(t *testing.T) {
	got := genMCP(t, serversJSON(t, proxiedSSE, proxiedHTTP, keyless), "oct_run")
	want := `{
  "mcpServers": {
    "octopus-api": {
      "type": "sse",
      "url": "http://d:8585/mcp-proxy/octopus-api",
      "headers": {
        "X-API-Key": "oct_run"
      }
    },
    "jira": {
      "type": "http",
      "url": "http://d:8585/mcp-proxy/jira",
      "headers": {
        "X-API-Key": "oct_run"
      }
    },
    "docs": {
      "type": "http",
      "url": "https://docs.example/mcp"
    }
  }
}`
	if got != want {
		t.Errorf("golden mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMCPJSONEmptyIsValidObject(t *testing.T) {
	for _, servers := range []string{"", "not json", `{"name":"x"}`} {
		got := genMCP(t, servers, "oct_run")
		want := `{
  "mcpServers": {}
}`
		if got != want {
			t.Errorf("%q: got\n%s", servers, got)
		}
	}
}

func TestMCPJSONSkipsBadEntries(t *testing.T) {
	raw := `[{"name":"ok","url":"http://d/mcp-proxy/ok","transport":"sse","run_key":true},` +
		`{"name":"bad name","url":"http://x","transport":"sse"},` +
		`{"name":"file","url":"file:///etc/passwd","transport":"sse"},` +
		`{"name":"ws","url":"http://x","transport":"ws"},` +
		`{"name":"ok","url":"http://evil","transport":"sse"}]`
	var cfg struct {
		MCPServers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(genMCP(t, raw, "")), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers["ok"].URL != "http://d/mcp-proxy/ok" {
		t.Errorf("servers = %+v", cfg.MCPServers)
	}
}

func TestMCPJSONNoKeyNoHeader(t *testing.T) {
	if strings.Contains(genMCP(t, serversJSON(t, proxiedSSE), ""), "X-API-Key") {
		t.Error("wrote an X-API-Key header with no key")
	}
}

func TestMCPJSONEscapesNastyURL(t *testing.T) {
	nasty := `http://d/mcp-proxy/x?q="a\b"`
	got := genMCP(t, serversJSON(t, mcp.ServerEntry{Name: "x", URL: nasty, Transport: "sse"}), "")
	var cfg map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}
	if cfg["mcpServers"]["x"]["url"] != nasty {
		t.Errorf("url = %v", cfg["mcpServers"]["x"]["url"])
	}
}

func TestCodexArgs(t *testing.T) {
	got := codexArgs(t, serversJSON(t, proxiedSSE, proxiedHTTP, keyless), "oct_run")
	want := []string{
		"-c", `mcp_servers.octopus-api.command="mcp-remote"`,
		"-c", `mcp_servers.octopus-api.args=["http://d:8585/mcp-proxy/octopus-api", "--transport", "sse-only", "--header", "X-API-Key: oct_run"]`,
		"-c", `mcp_servers.octopus-api.startup_timeout_sec=30`,
		"-c", `mcp_servers.jira.url="http://d:8585/mcp-proxy/jira"`,
		"-c", `mcp_servers.jira.env_http_headers={ "X-API-Key" = "OCTOPUS_API_KEY" }`,
		"-c", `mcp_servers.jira.startup_timeout_sec=30`,
		"-c", `mcp_servers.docs.url="https://docs.example/mcp"`,
		"-c", `mcp_servers.docs.startup_timeout_sec=30`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tokens:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, tok := range got {
		if strings.Contains(tok, "jira") && strings.Contains(tok, "oct_run") {
			t.Errorf("the key landed in a flag: %s", tok)
		}
	}
}

func TestCodexArgsWithoutMCPRemote(t *testing.T) {
	got := strings.Join(codexArgs(t, serversJSON(t, proxiedSSE, proxiedHTTP), "oct_run", "--no-mcp-remote"), "\n")
	if strings.Contains(got, "octopus-api") || !strings.Contains(got, "mcp_servers.jira.url") {
		t.Errorf("tokens:\n%s", got)
	}
}

func TestCodexArgsEmpty(t *testing.T) {
	if got := codexArgs(t, "", "oct_run"); got != nil {
		t.Errorf("tokens = %v, want none", got)
	}
}
