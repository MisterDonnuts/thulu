package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/mcp"
)

func TestEnvMCPServices(t *testing.T) {
	env := map[string]string{
		"MCP_OCTOPUS_API_URL":  "http://octopus-api:3000/sse",
		"MCP_THULU_GITHUB_URL": "http://thulu-github:3000/sse",
		"MCP__URL":             "http://nowhere/sse",
	}
	got := envMCPServices(env, map[string]bool{"octopus-api": true})
	want := []mcp.Service{
		{Name: "octopus-api", Source: "env", URL: "http://octopus-api:3000/sse", Status: "configured", ForwardsCredential: true},
		{Name: "thulu-github", Source: "env", URL: "http://thulu-github:3000/sse", Status: "configured"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("envMCPServices =\n%+v\nwant\n%+v", got, want)
	}
}

func TestMergeMCPList(t *testing.T) {
	envRow := func(name, u string, forwards bool) mcp.Service {
		return mcp.Service{Name: name, Source: "env", URL: u, Status: "configured", ForwardsCredential: forwards}
	}
	runtimeRow := func(name string) mcp.Service {
		return mcp.Service{Name: name, Source: "runtime", Image: "img/" + name, Status: "running", Health: "healthy", HostPort: 3000}
	}
	reg := func(name, u string) mcp.Registration {
		return mcp.Registration{Name: name, URL: u, Description: "note", ForwardsCredential: true}
	}

	cases := []struct {
		name    string
		env     []mcp.Service
		runtime []mcp.Service
		regs    []mcp.Registration
		want    []mcp.Service
	}{
		{
			name: "nothing configured",
			want: []mcp.Service{},
		},
		{
			name: "env only (a standalone compose install)",
			env:  []mcp.Service{envRow("thulu-github", "http://thulu-github:3000/sse", true)},
			want: []mcp.Service{envRow("thulu-github", "http://thulu-github:3000/sse", true)},
		},
		{
			name:    "runtime wins over env and takes its URL and credential flag",
			env:     []mcp.Service{envRow("thulu-github", "http://thulu-github.octopus.svc:3000/sse", true)},
			runtime: []mcp.Service{runtimeRow("thulu-github")},
			want: []mcp.Service{{
				Name: "thulu-github", Source: "runtime", Image: "img/thulu-github", Status: "running",
				Health: "healthy", HostPort: 3000,
				URL: "http://thulu-github.octopus.svc:3000/sse", ForwardsCredential: true,
			}},
		},
		{
			name: "registered replaces an env-only row whole",
			env:  []mcp.Service{envRow("octopus-api", "http://octopus-api:3000/sse", true)},
			regs: []mcp.Registration{{Name: "octopus-api", URL: "https://elsewhere/sse"}},
			want: []mcp.Service{{
				Name: "octopus-api", Source: "registered", URL: "https://elsewhere/sse", Status: "external",
			}},
		},
		{
			name:    "registered over runtime keeps the container state",
			runtime: []mcp.Service{runtimeRow("thulu-browser")},
			regs:    []mcp.Registration{reg("thulu-browser", "https://browser.example/sse")},
			want: []mcp.Service{{
				Name: "thulu-browser", Source: "registered", Image: "img/thulu-browser", Status: "running",
				Health: "healthy", HostPort: 3000,
				URL: "https://browser.example/sse", Description: "note", ForwardsCredential: true,
			}},
		},
		{
			name:    "all three: registered on top, runtime state kept",
			env:     []mcp.Service{envRow("thulu-cloud", "http://thulu-cloud:3000/sse", false)},
			runtime: []mcp.Service{runtimeRow("thulu-cloud")},
			regs:    []mcp.Registration{reg("thulu-cloud", "https://cloud.example/sse")},
			want: []mcp.Service{{
				Name: "thulu-cloud", Source: "registered", Image: "img/thulu-cloud", Status: "running",
				Health: "healthy", HostPort: 3000,
				URL: "https://cloud.example/sse", Description: "note", ForwardsCredential: true,
			}},
		},
		{
			name:    "distinct names: runtime, then env, then registered",
			env:     []mcp.Service{envRow("octopus-api", "http://octopus-api:3000/sse", false)},
			runtime: []mcp.Service{runtimeRow("thulu-browser")},
			regs:    []mcp.Registration{{Name: "ticket-reader", URL: "https://tickets/sse"}},
			want: []mcp.Service{
				runtimeRow("thulu-browser"),
				envRow("octopus-api", "http://octopus-api:3000/sse", false),
				{Name: "ticket-reader", Source: "registered", URL: "https://tickets/sse", Status: "external"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeMCPList(tc.env, tc.runtime, tc.regs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeMCPList =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

type fakeMCPProber struct {
	t      *testing.T
	result map[string]error
}

func (f *fakeMCPProber) Tools(context.Context, string) ([]mcp.ToolInfo, error) {
	return nil, errors.New("not used")
}

func (f *fakeMCPProber) Ping(_ context.Context, u string) error {
	err, ok := f.result[u]
	if !ok {
		f.t.Errorf("unexpected ping of %q", u)
	}
	return err
}

func TestPingMCPs(t *testing.T) {
	cases := []struct {
		name       string
		in         mcp.Service
		pingErr    error
		wantHealth string
		wantDetail string
	}{
		{
			name:       "env server that answers",
			in:         mcp.Service{Name: "a", Source: "env", Status: "configured", URL: "http://a/sse"},
			wantHealth: "healthy",
		},
		{
			name:       "env server that does not",
			in:         mcp.Service{Name: "b", Source: "env", Status: "configured", URL: "http://b/sse"},
			pingErr:    errors.New("mcp ping: connect: refused"),
			wantHealth: "unhealthy",
			wantDetail: "mcp ping: connect: refused",
		},
		{
			name:       "registered external server",
			in:         mcp.Service{Name: "c", Source: "registered", Status: "external", URL: "https://c/sse"},
			wantHealth: "healthy",
		},
		{
			name:       "runtime row keeps its container health",
			in:         mcp.Service{Name: "d", Source: "runtime", Status: "exited", Health: "unhealthy", URL: "http://d/sse"},
			wantHealth: "unhealthy",
		},
		{
			name:       "registration over a runtime row keeps the container health",
			in:         mcp.Service{Name: "e", Source: "registered", Status: "running", Health: "starting", URL: "https://e/sse"},
			wantHealth: "starting",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &fakeMCPProber{t: t, result: map[string]error{}}
			if tc.in.Status == "configured" || tc.in.Status == "external" {
				probe.result[tc.in.URL] = tc.pingErr
			}
			s := &Server{}
			s.mcp.prober = probe
			list := []mcp.Service{tc.in}
			s.pingMCPs(context.Background(), list)
			if list[0].Health != tc.wantHealth || list[0].HealthDetail != tc.wantDetail {
				t.Errorf("health = %q (%q), want %q (%q)", list[0].Health, list[0].HealthDetail, tc.wantHealth, tc.wantDetail)
			}
		})
	}

	s := &Server{}
	list := []mcp.Service{{Name: "a", Source: "env", Status: "configured", URL: "http://a/sse"}}
	s.pingMCPs(context.Background(), list)
	if list[0].Health != "" {
		t.Errorf("health without a prober = %q, want empty", list[0].Health)
	}
}

func sseEndpointServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: endpoint\ndata: /messages?sessionId=x\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/sse"
}

func TestIntegration_ListMCPs_EnvServers(t *testing.T) {
	live := sseEndpointServer(t)
	t.Setenv("MCP_THULU_GITHUB_URL", live)
	t.Setenv("MCP_THULU_CLOUD_URL", "http://127.0.0.1:1/sse")
	t.Setenv("OCTOPUS_MCP_FORWARD_CREDENTIAL", "thulu-github")
	rig := newOperatorRig(t)

	list := func() map[string]mcp.Service {
		t.Helper()
		w := callAs(t, rig.h, http.MethodGet, "/mcps", rig.opToken, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /mcps = %d: %s", w.Code, w.Body.String())
		}
		var got []mcp.Service
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := map[string]mcp.Service{}
		for _, sv := range got {
			out[sv.Name] = sv
		}
		return out
	}

	got := list()
	gh, ok := got["thulu-github"]
	if !ok {
		t.Fatalf("thulu-github missing from /mcps: %+v", got)
	}
	if gh.Source != "env" || gh.Status != "configured" || gh.URL != live || !gh.ForwardsCredential {
		t.Errorf("thulu-github = %+v, want an env row forwarding the credential", gh)
	}
	if gh.Health != "healthy" {
		t.Errorf("thulu-github health = %q (%s), want healthy", gh.Health, gh.HealthDetail)
	}
	cloud := got["thulu-cloud"]
	if cloud.Source != "env" || cloud.Health != "unhealthy" || cloud.HealthDetail == "" {
		t.Errorf("thulu-cloud = %+v, want an env row that failed its check with a reason", cloud)
	}
	if cloud.ForwardsCredential {
		t.Error("thulu-cloud is not in OCTOPUS_MCP_FORWARD_CREDENTIAL")
	}

	fwd := mcp.NewForwarder()
	fwd.SetMany(rig.srv.taskDispatcher.MCPURLs())
	fwd.SetForwardsCredential("thulu-github", true)
	rig.srv.taskDispatcher.WithMCPForwarder(fwd)

	override := "https://github-mcp.example.com/sse"
	if w := callAs(t, rig.h, http.MethodPost, "/mcps/register", rig.opToken, "",
		map[string]any{"name": "thulu-github", "url": override}); w.Code != http.StatusCreated {
		t.Fatalf("register = %d: %s", w.Code, w.Body.String())
	}
	if gh = list()["thulu-github"]; gh.Source != "registered" || gh.URL != override {
		t.Errorf("after register: %+v, want the registration", gh)
	}
	if u := fwd.Snapshot()["MCP_THULU_GITHUB_URL"]; u != override {
		t.Errorf("agents get %q while registered, want %q", u, override)
	}
	if up, _ := fwd.Lookup("thulu-github"); up.ForwardsCredential {
		t.Error("the run key goes to thulu-github while a non-forwarding registration holds the name")
	}

	if w := callAs(t, rig.h, http.MethodDelete, "/mcps/thulu-github", rig.opToken, "", nil); w.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}
	if gh = list()["thulu-github"]; gh.Source != "env" || gh.URL != live || !gh.ForwardsCredential {
		t.Errorf("after delete: %+v, want the env row back", gh)
	}
	if u := fwd.Snapshot()["MCP_THULU_GITHUB_URL"]; u != live {
		t.Errorf("agents get %q after unregistering, want the env URL %q", u, live)
	}
	if up, _ := fwd.Lookup("thulu-github"); !up.ForwardsCredential {
		t.Error("the run key no longer goes to thulu-github after unregistering, want the env's flag back")
	}
}
