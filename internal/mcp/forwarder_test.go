package mcp

import "testing"

func TestForwarder_SetManyMergesAndSkipsEmpty(t *testing.T) {
	f := NewForwarder()

	f.SetMany(nil)
	if got := f.Snapshot(); len(got) != 0 {
		t.Fatalf("nil override populated the table: %v", got)
	}

	f.SetMany(map[string]string{
		"MCP_THULU_GITHUB_URL":  "http://thulu-github.octopus.svc:3000/sse",
		"MCP_THULU_BROWSER_URL": "http://thulu-browser.octopus.svc:3000/sse",
		"":                      "http://ignored",
		"MCP_EMPTY_URL":         "",
	})
	snap := f.Snapshot()
	if got := snap["MCP_THULU_GITHUB_URL"]; got != "http://thulu-github.octopus.svc:3000/sse" {
		t.Errorf("github URL = %q", got)
	}
	if _, ok := snap[""]; ok {
		t.Error("empty key was kept")
	}
	if _, ok := snap["MCP_EMPTY_URL"]; ok {
		t.Error("empty value was kept")
	}

	f.SetMany(map[string]string{"MCP_OCTOPUS_API_URL": "http://octopus-api.octopus.svc:3000/sse"})
	if got := f.Snapshot(); len(got) != 3 {
		t.Errorf("merge produced %d entries, want 3: %v", len(got), got)
	}
}

func TestForwarder_Lookup(t *testing.T) {
	f := NewForwarder()
	f.Set("MCP_JIRA_URL", "https://mcp.atlassian.com/v1/mcp")
	f.SetTransport("jira", TransportHTTP)
	f.SetForwardsCredential("jira", false)
	f.Set("MCP_OCTOPUS_API_URL", "http://octopus-api:3013/sse")
	f.SetForwardsCredential("octopus-api", true)

	if got, ok := f.Lookup("jira"); !ok || got.Transport != TransportHTTP || got.ForwardsCredential {
		t.Errorf("jira = %+v, %v", got, ok)
	}
	if got, ok := f.Lookup("octopus-api"); !ok || got.Transport != TransportSSE || !got.ForwardsCredential {
		t.Errorf("octopus-api = %+v, %v", got, ok)
	}
	if _, ok := f.Lookup("nobody"); ok {
		t.Error("an unknown server was found")
	}
	f.SetTransport("jira", "")
	if got, _ := f.Lookup("jira"); got.Transport != TransportSSE {
		t.Errorf("transport after reset = %q, want sse", got.Transport)
	}
}

func TestForwarder_SetAddsAndReplaces(t *testing.T) {
	f := NewForwarder()

	f.Set("MCP_TICKET_READER_URL", "http://ticket-reader.internal:3000/sse")
	if got := f.Snapshot()["MCP_TICKET_READER_URL"]; got != "http://ticket-reader.internal:3000/sse" {
		t.Fatalf("Set first insert: %q", got)
	}

	f.Set("MCP_TICKET_READER_URL", "http://10.0.0.1:3000/sse")
	if got := f.Snapshot()["MCP_TICKET_READER_URL"]; got != "http://10.0.0.1:3000/sse" {
		t.Fatalf("Set overwrite: %q", got)
	}

	f.Set("", "http://x")
	f.Set("MCP_X_URL", "")
	snap := f.Snapshot()
	if _, ok := snap[""]; ok {
		t.Error("empty key inserted")
	}
	if _, ok := snap["MCP_X_URL"]; ok {
		t.Error("empty value inserted")
	}
}

func TestForwarder_Delete(t *testing.T) {
	f := NewForwarder()
	f.Set("MCP_TICKET_READER_URL", "http://x")
	f.Delete("MCP_TICKET_READER_URL")
	if _, ok := f.Snapshot()["MCP_TICKET_READER_URL"]; ok {
		t.Fatal("Delete left the key in place")
	}
	f.Delete("MCP_UNKNOWN_URL")
	f.Delete("")
}

func TestForwarder_SnapshotIsCopy(t *testing.T) {
	f := NewForwarder()
	f.Set("MCP_A_URL", "http://a")
	snap := f.Snapshot()
	snap["MCP_A_URL"] = "http://tampered"
	if got := f.Snapshot()["MCP_A_URL"]; got != "http://a" {
		t.Fatalf("snapshot mutation leaked into internal table: %q", got)
	}
}

func TestForwarder_WithholdKeepsAKeyOutOfTheProxy(t *testing.T) {
	f := NewForwarder()
	f.Withhold("MCP_THULU_BROWSER_URL")
	f.Set("MCP_THULU_BROWSER_URL", "http://thulu-browser:3000/sse")
	f.Set("MCP_THULU_GITHUB_URL", "http://thulu-github:3000/sse")

	if _, ok := f.Lookup("thulu-browser"); ok {
		t.Error("the withheld server is reachable")
	}
	if _, ok := f.Lookup("thulu-github"); !ok {
		t.Error("withholding one key dropped another")
	}
	if f.Snapshot()["MCP_THULU_BROWSER_URL"] == "" {
		t.Error("Snapshot hides the withheld registration")
	}
}
