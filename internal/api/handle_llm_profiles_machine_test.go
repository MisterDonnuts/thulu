package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type machineProfileFixture struct {
	srv     *Server
	h       http.Handler
	runners *runner.Store
	ws      string
	keyID   string
}

func newMachineProfileFixture(t *testing.T) *machineProfileFixture {
	t.Helper()
	srv, h := newIntegrationServer(t)
	rs, err := runner.NewStore(srv.secretsStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetRunners(rs)
	code, key, body := createProfile(t, h, map[string]any{"name": "with-key", "engine": "claude-code", "key": "sk-ant-api-1"})
	if code != http.StatusCreated {
		t.Fatalf("key profile = %d: %s", code, body)
	}
	return &machineProfileFixture{srv: srv, h: h, runners: rs, ws: key["workspace_id"].(string), keyID: key["id"].(string)}
}

func (f *machineProfileFixture) machine(t *testing.T, ws, name string) string {
	t.Helper()
	tok, _, err := f.runners.CreateRegistration(t.Context(), ws, name, "")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := f.runners.Register(t.Context(), runner.RegisterRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	return reg.RunnerID
}

// Cases 1 and 5: a machine profile is created without a key, gets no key check, and lists its
// machine with what that machine reported.
func TestMachineProfileCreateAndList(t *testing.T) {
	t.Parallel()
	f := newMachineProfileFixture(t)
	mac := f.machine(t, f.ws, "mac")
	inv := runner.Inventory{Engines: []runner.EngineState{{Engine: engine.DefaultName, Installed: true, LoggedIn: true}}}
	if err := f.runners.SaveInventory(t.Context(), mac, inv); err != nil {
		t.Fatal(err)
	}

	code, p, body := createProfile(t, f.h, map[string]any{"name": "mi mac", "engine": "claude-code", "runner_id": mac,
		"model": "opus", "is_default": true})
	if code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", code, body)
	}
	if p["runner_id"] != mac || p["has_key"] != false || p["is_default"] != true || p["key_state"] != nil {
		t.Errorf("machine profile = %s", body)
	}
	if cw := postJSON(t, f.h, "/llm-profiles/"+p["id"].(string)+"/check", nil); cw.Code != http.StatusOK ||
		strings.Contains(cw.Body.String(), "key_state") {
		t.Errorf("check = %d %s, want the profile without a key state", cw.Code, cw.Body.String())
	}

	lw := getJSON(t, f.h, "/llm-profiles", "")
	var list struct {
		Profiles []struct {
			ID          string              `json:"id"`
			RunnerName  string              `json:"runner_name"`
			EngineState *runner.EngineState `json:"engine_state"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, got := range list.Profiles {
		if got.ID == p["id"] && (got.RunnerName != "mac" || got.EngineState == nil || !got.EngineState.LoggedIn) {
			t.Errorf("listed machine profile = %+v", got)
		}
	}
}

// Cases 2-4: what a machine profile refuses.
func TestMachineProfileRefusals(t *testing.T) {
	t.Parallel()
	f := newMachineProfileFixture(t)
	mac := f.machine(t, f.ws, "mac")
	elsewhere := f.machine(t, "another-ws", "their-mac")
	revoked := f.machine(t, f.ws, "old-mac")
	if err := f.runners.Revoke(t.Context(), f.ws, revoked); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		body  map[string]any
		names string
	}{
		{"2 unknown machine", map[string]any{"runner_id": "rn-nope"}, "rn-nope"},
		{"2 another workspace's machine", map[string]any{"runner_id": elsewhere}, elsewhere},
		{"2 revoked machine", map[string]any{"runner_id": revoked}, "old-mac"},
		{"3 engine without a machine login", map[string]any{"runner_id": mac, "engine": "codex"}, "odex"},
		{"4 with a key", map[string]any{"runner_id": mac, "key": "sk-ant-api-2"}, ""},
		{"4 with a base url", map[string]any{"runner_id": mac, "base_url": "https://proxy.example.com"}, ""},
		{"4 on a cloud provider", map[string]any{"runner_id": mac, "provider": "bedrock", "region": "us-east-1"}, "bedrock"},
		{"4 with federation", map[string]any{"runner_id": mac, "federation": map[string]string{
			"federation_rule_id": "r", "organization_id": "o", "service_account_id": "s"}}, ""},
	}
	for _, tc := range cases {
		body := map[string]any{"name": "m " + tc.name, "engine": "claude-code"}
		for k, v := range tc.body {
			body[k] = v
		}
		code, _, raw := createProfile(t, f.h, body)
		if code < 400 || code >= 500 {
			t.Errorf("%s: %d %s, want refused", tc.name, code, raw)
			continue
		}
		if tc.names != "" && !strings.Contains(raw, tc.names) {
			t.Errorf("%s: %s does not name %q", tc.name, raw, tc.names)
		}
	}
}

// Case 5: the kind is locked both ways; the machine may change.
func TestMachineProfileKindIsLocked(t *testing.T) {
	t.Parallel()
	f := newMachineProfileFixture(t)
	mac, other := f.machine(t, f.ws, "mac"), f.machine(t, f.ws, "other")
	code, p, body := createProfile(t, f.h, map[string]any{"name": "m", "engine": "claude-code", "runner_id": mac})
	if code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", code, body)
	}
	id := p["id"].(string)

	if w := patchJSONAuth(t, f.h, "/llm-profiles/"+f.keyID, "", map[string]any{"runner_id": mac}); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "machine profile") {
		t.Errorf("key → machine = %d %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, f.h, "/llm-profiles/"+id, "", map[string]any{"runner_id": ""}); w.Code != http.StatusConflict {
		t.Errorf("machine → key = %d %s", w.Code, w.Body.String())
	}
	w := patchJSONAuth(t, f.h, "/llm-profiles/"+id, "", map[string]any{"runner_id": other, "model": "sonnet"})
	var patched map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &patched)
	if w.Code != http.StatusOK || patched["runner_id"] != other || patched["model"] != "sonnet" {
		t.Errorf("switch machine = %d %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, f.h, "/llm-profiles/"+id, "", map[string]any{"key": "sk-ant-api-3"}); w.Code != http.StatusBadRequest {
		t.Errorf("key on a machine profile = %d %s", w.Code, w.Body.String())
	}
}

// Cases 7 and 9: a machine profile binds, and effective-llm says where it runs and when it cannot.
func TestMachineProfileBindingAndEffectiveLLM(t *testing.T) {
	t.Parallel()
	f := newMachineProfileFixture(t)
	installAgentCompose(t, f.srv.agentsDir, "reviewer", "claude-code", "claude-code")
	mac := f.machine(t, f.ws, "mac")
	_, p, _ := createProfile(t, f.h, map[string]any{"name": "m", "engine": "claude-code", "runner_id": mac})
	if bw := putJSONWithHeaders(t, f.h, "/agents/reviewer/llm-binding", map[string]string{"profile_id": p["id"].(string)}, nil); bw.Code != http.StatusOK {
		t.Fatalf("PUT binding = %d: %s", bw.Code, bw.Body.String())
	}

	type effective struct {
		HasCred    bool     `json:"has_credential"`
		RunnerID   string   `json:"runner_id"`
		RunnerName string   `json:"runner_name"`
		Warnings   []string `json:"warnings"`
	}
	read := func() effective {
		ew := getJSON(t, f.h, "/agents/reviewer/effective-llm", "")
		var got effective
		if err := json.Unmarshal(ew.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read()
	if !got.HasCred || got.RunnerID != mac || got.RunnerName != "mac" || len(got.Warnings) == 0 {
		t.Errorf("offline machine never reported = %+v, want runnable with warnings", got)
	}
	if err := f.runners.Revoke(t.Context(), f.ws, mac); err != nil {
		t.Fatal(err)
	}
	if got = read(); got.HasCred || !strings.Contains(strings.Join(got.Warnings, " "), "revoked") {
		t.Errorf("revoked machine = %+v, want not runnable", got)
	}
}

// Case 8: GET /engines says which engines can run on a machine.
func TestEnginesExposeMachineLogin(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := getJSON(t, h, "/engines", "")
	var got struct {
		Engines []struct {
			Name         string `json:"name"`
			MachineLogin bool   `json:"machine_login"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Engines) == 0 {
		t.Fatalf("no engines: %s", w.Body.String())
	}
	for _, e := range got.Engines {
		if e.MachineLogin != (e.Name == engine.DefaultName) {
			t.Errorf("%s machine_login = %v", e.Name, e.MachineLogin)
		}
	}
}

// Case 5 through PUT /llm-key: the old key endpoint cannot turn a default machine profile into a key
// profile, and GET /engines does not report a key for it.
func TestLegacyKeyEndpointKeepsAMachineDefault(t *testing.T) {
	t.Parallel()
	f := newMachineProfileFixture(t)
	mac := f.machine(t, f.ws, "mac")
	code, p, body := createProfile(t, f.h, map[string]any{"name": "mi mac", "engine": "claude-code", "runner_id": mac,
		"is_default": true})
	if code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", code, body)
	}
	w := putJSONWithHeaders(t, f.h, "/llm-key", map[string]string{"key": "sk-ant-api-legacy"}, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "mi mac") {
		t.Errorf("PUT /llm-key over a machine default = %d %s, want 409 naming the profile", w.Code, w.Body.String())
	}
	got := getJSON(t, f.h, "/llm-profiles/"+p["id"].(string), "")
	if !strings.Contains(got.Body.String(), mac) {
		t.Errorf("the default lost its machine: %s", got.Body.String())
	}
	var engines struct {
		Engines []struct {
			Name   string `json:"name"`
			HasKey bool   `json:"has_key"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(getJSON(t, f.h, "/engines", "").Body.Bytes(), &engines); err != nil {
		t.Fatal(err)
	}
	for _, e := range engines.Engines {
		if e.Name == engine.DefaultName && e.HasKey {
			t.Errorf("GET /engines reports a key for a machine default")
		}
	}
}
