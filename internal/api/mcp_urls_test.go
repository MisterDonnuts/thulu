package api

import "testing"

func TestCollectMCPURLs(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"MCP_THULU_GITHUB_URL=http://thulu-github.octopus.svc:3000/sse",
		"MCP_THULU_BROWSER_URL=http://thulu-browser.octopus.svc:3000/sse",
		"MCP_OCTOPUS_API_URL=http://octopus-api.octopus.svc:3000/sse",
		"MCP_THULU_SANDBOX_URL=http://octopus.svc:8585/sandbox/sse",
		"MCP_THULU_GITHUB_TOKEN=secret",
		"SOME_URL=http://nope",
		"MCP_EMPTY_URL=",
	}
	got := collectMCPURLs(environ)

	want := map[string]string{
		"MCP_THULU_GITHUB_URL":  "http://thulu-github.octopus.svc:3000/sse",
		"MCP_THULU_BROWSER_URL": "http://thulu-browser.octopus.svc:3000/sse",
		"MCP_OCTOPUS_API_URL":   "http://octopus-api.octopus.svc:3000/sse",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["MCP_THULU_SANDBOX_URL"]; ok {
		t.Error("MCP_THULU_SANDBOX_URL must be excluded (dispatcher computes it per task)")
	}
	if _, ok := got["MCP_EMPTY_URL"]; ok {
		t.Error("empty value must be dropped")
	}
}
