package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

func perRunBrowser(t *testing.T) browserrun.Config {
	t.Helper()
	cfg, err := browserrun.ConfigFromEnv(func(k string) string {
		if k == browserrun.EnvMode {
			return string(browserrun.ModePerRun)
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func serversIn(t *testing.T, env map[string]string) []ServerEntry {
	t.Helper()
	raw, ok := env[ServersEnv]
	if !ok {
		return nil
	}
	var out []ServerEntry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", ServersEnv, err, raw)
	}
	return out
}

type recordedGrant struct {
	ref, agent, stage string
	g                 RunGrant
}

type fakeRecorder struct{ got []recordedGrant }

func (f *fakeRecorder) RecordRunGrant(_ context.Context, ref, agent, stage string, g RunGrant) error {
	f.got = append(f.got, recordedGrant{ref, agent, stage, g})
	return nil
}

func TestApplyLaunchEnv_ProxiesTableServers(t *testing.T) {
	f := NewForwarder()
	f.SetMany(map[string]string{
		"MCP_OCTOPUS_API_URL": "http://octopus-api:3013/sse",
		"MCP_JIRA_URL":        "https://mcp.atlassian.com/v1/mcp",
	})
	f.SetTransport("jira", TransportHTTP)
	rec := &fakeRecorder{}
	f.SetGrantRecorder(rec)

	env := map[string]string{
		"MCP_TOOLS":             "octopus-api@write,jira,thulu-sandbox,not-registered",
		"OCTOPUS_API":           "http://host.docker.internal:8585",
		"TASK_ID":               "t1",
		"AGENT_NAME":            "developer",
		"STAGE":                 "in_progress",
		"OCTOPUS_LOCALE":        "es",
		"MCP_THULU_SANDBOX_URL": "http://host.docker.internal:8585/sandbox/sse?task_id=t1&stage=in_progress",
	}
	ApplyLaunchEnv(context.Background(), env, f, browserrun.Config{})

	want := []ServerEntry{
		{Name: "octopus-api", URL: "http://host.docker.internal:8585/mcp-proxy/octopus-api", Transport: "sse", RunKey: true},
		{Name: "jira", URL: "http://host.docker.internal:8585/mcp-proxy/jira", Transport: "http", RunKey: true},
		{Name: "thulu-sandbox", URL: "http://host.docker.internal:8585/sandbox/sse?task_id=t1&stage=in_progress&locale=es&task=t1",
			Transport: "sse", RunKey: true},
	}
	if got := serversIn(t, env); !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n%+v\nwant\n%+v", ServersEnv, got, want)
	}
	for k, v := range env {
		if strings.Contains(v, "octopus-api:3013") || strings.Contains(v, "atlassian.com") {
			t.Errorf("%s=%q hands the container an upstream URL", k, v)
		}
	}
	if got := env["MCP_OCTOPUS_API_URL"]; got != want[0].URL {
		t.Errorf("legacy MCP_OCTOPUS_API_URL = %q, want the proxy URL", got)
	}
	if _, ok := env["MCP_JIRA_URL"]; ok {
		t.Error("an HTTP server was handed to the legacy SSE env")
	}
	wantGrant := []recordedGrant{{"t1", "developer", "in_progress", RunGrant{Tools: env["MCP_TOOLS"], Locale: "es"}}}
	if !reflect.DeepEqual(rec.got, wantGrant) {
		t.Errorf("recorded %+v, want %+v", rec.got, wantGrant)
	}
}

func TestApplyLaunchEnv_ChatGrantKeyedByChat(t *testing.T) {
	f := NewForwarder()
	rec := &fakeRecorder{}
	f.SetGrantRecorder(rec)
	env := map[string]string{"MCP_TOOLS": "octopus-api", "CHAT_ID": "chat-1", "TASK_ID": "t1",
		"AGENT_NAME": "pm", "STAGE": "chat"}
	ApplyLaunchEnv(context.Background(), env, f, browserrun.Config{})
	if len(rec.got) != 1 || rec.got[0].ref != "chat-1" || rec.got[0].stage != "chat" {
		t.Errorf("recorded %+v, want one grant for chat-1", rec.got)
	}
}

func TestApplyLaunchEnv_NilForwarder(t *testing.T) {
	env := map[string]string{"MCP_TOOLS": "octopus-api", "OCTOPUS_API": "http://d:8585"}
	ApplyLaunchEnv(context.Background(), env, nil, browserrun.Config{})
	if v, ok := env[ServersEnv]; ok {
		t.Errorf("%s = %q with no server reachable", ServersEnv, v)
	}
}

func TestApplyLaunchEnv_DropsInheritedList(t *testing.T) {
	env := map[string]string{"MCP_TOOLS": "octopus-api", "OCTOPUS_API": "http://d:8585",
		ServersEnv: "octopus-api,thulu-github"}
	ApplyLaunchEnv(context.Background(), env, nil, browserrun.Config{})
	if v, ok := env[ServersEnv]; ok {
		t.Errorf("%s = %q, want it dropped", ServersEnv, v)
	}
}

func TestApplyLaunchEnv_PerRunBrowser(t *testing.T) {
	f := NewForwarder()
	f.Set(browserrun.URLEnv, "http://thulu-browser:3000/sse")
	f.Withhold(browserrun.URLEnv)
	f.Set("MCP_OCTOPUS_API_URL", "http://octopus-api/sse")

	env := map[string]string{"MCP_TOOLS": "thulu-browser@screenshot+get_text,octopus-api", "OCTOPUS_API": "http://d:8585", "TASK_ID": "t1"}
	ApplyLaunchEnv(context.Background(), env, f, perRunBrowser(t))
	if got := env[browserrun.URLEnv]; got != "http://127.0.0.1:3010/sse" {
		t.Errorf("%s = %q, want the loopback URL", browserrun.URLEnv, got)
	}
	if !browserrun.Wanted(env) {
		t.Error("the env does not ask the launcher for a browser")
	}
	want := []ServerEntry{
		{Name: "thulu-browser", URL: "http://127.0.0.1:3010/sse?task=t1&tools=screenshot%2Cget_text", Transport: "sse", RunKey: true},
		{Name: "octopus-api", URL: "http://d:8585/mcp-proxy/octopus-api", Transport: "sse", RunKey: true},
	}
	if got := serversIn(t, env); !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n%+v\nwant\n%+v", ServersEnv, got, want)
	}
	if got := env[ForwardKeyEnv]; got != "thulu-browser" {
		t.Errorf("legacy %s = %q, want thulu-browser", ForwardKeyEnv, got)
	}
	if _, ok := f.Lookup(browserrun.ServerName); ok {
		t.Error("the withheld shared browser is reachable through the proxy")
	}

	env = map[string]string{"MCP_TOOLS": "thulu-browser,octopus-api", "OCTOPUS_API": "http://d:8585"}
	ApplyLaunchEnv(context.Background(), env, f, browserrun.Config{})
	if got := serversIn(t, env); len(got) != 1 || got[0].Name != "octopus-api" {
		t.Errorf("%s = %+v, want octopus-api alone", ServersEnv, got)
	}
}

func TestToolEntry(t *testing.T) {
	cases := []struct {
		raw        string
		restricted bool
		allows     map[string]bool
		query      string
	}{
		{raw: "thulu-github", allows: map[string]bool{"pr_view": true}, query: "task=t"},
		{raw: "octopus-api@write", allows: map[string]bool{"anything": true}, query: "scope=write&task=t"},
		{raw: "thulu-github@pr_view+pr_diff", restricted: true,
			allows: map[string]bool{"pr_view": true, "pr_diff": true, "pr_merge": false},
			query:  "task=t&tools=pr_view%2Cpr_diff"},
		{raw: "thulu-github@write+run_dispatch", allows: map[string]bool{"pr_merge": true},
			query: "task=t&tools=write%2Crun_dispatch"},
	}
	for _, tc := range cases {
		e := ParseToolEntry(tc.raw)
		if e.Restricted() != tc.restricted {
			t.Errorf("%s: Restricted = %v", tc.raw, e.Restricted())
		}
		for tool, want := range tc.allows {
			if e.Allows(tool) != want {
				t.Errorf("%s: Allows(%s) = %v", tc.raw, tool, !want)
			}
		}
		if got := RunQuery("t", "", e).Encode(); got != tc.query {
			t.Errorf("%s: query = %q, want %q", tc.raw, got, tc.query)
		}
	}
	if got := ParseToolList(" a, b@read ,a@write,,"); len(got) != 2 || got[0].Server != "a" || got[0].Scope != "" || got[1].Scope != "read" {
		t.Errorf("ParseToolList = %+v", got)
	}
}
