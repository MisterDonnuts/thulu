package initwizard

import (
	"bytes"
	"strings"
	"testing"
)

func multiChoice(t *testing.T, answers string, def []int) ([]string, string) {
	t.Helper()
	var out bytes.Buffer
	p := NewTestPrompter(strings.NewReader(answers), &out)
	opts := []Option{{Value: "a1", Label: "one"}, {Value: "b2", Label: "two"}, {Value: "c3", Label: "three"}}
	got, err := p.MultiChoice("Pick", opts, def)
	if err != nil {
		t.Fatalf("multi-choice: %v", err)
	}
	return got, out.String()
}

func TestMultiChoiceEnterKeepsTheDefaults(t *testing.T) {
	got, out := multiChoice(t, "\n", []int{0, 2})
	if strings.Join(got, ",") != "a1,c3" {
		t.Errorf("got %v, want the checked defaults", got)
	}
	if !strings.Contains(out, "[x] 1) one") || !strings.Contains(out, "[ ] 2) two") {
		t.Errorf("checklist does not show what is checked:\n%s", out)
	}
}

func TestMultiChoiceTogglesUntilConfirmed(t *testing.T) {
	got, _ := multiChoice(t, "1 2\n3\n\n", []int{0, 2})
	if strings.Join(got, ",") != "b2" {
		t.Errorf("got %v, want only the second entry", got)
	}
}

func TestMultiChoiceAllAndNone(t *testing.T) {
	if got, _ := multiChoice(t, "a\n\n", nil); len(got) != 3 {
		t.Errorf("a → %v, want everything", got)
	}
	got, _ := multiChoice(t, "n\n\n", []int{0, 1, 2})
	if got == nil || len(got) != 0 {
		t.Errorf("n → %#v, want an empty, non-nil selection", got)
	}
}

func TestMultiChoiceRejectsOutOfRangeWithoutApplyingAnything(t *testing.T) {
	got, out := multiChoice(t, "2 9\n\n", []int{0})
	if strings.Join(got, ",") != "a1" {
		t.Errorf("got %v: an invalid answer changed the selection", got)
	}
	if !strings.Contains(out, "between 1 and 3") {
		t.Errorf("no re-ask after an invalid answer:\n%s", out)
	}
}

func TestMultiChoiceNonInteractiveTakesTheDefaults(t *testing.T) {
	p := NewTestPrompter(strings.NewReader(""), &bytes.Buffer{})
	p.NonInteractive = true
	got, err := p.MultiChoice("Pick", []Option{{Value: "x"}, {Value: "y"}}, []int{1})
	if err != nil || strings.Join(got, ",") != "y" {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestInterviewMCPDefaultsComeFromThePreviousRun(t *testing.T) {
	existing := &EnvFile{lines: []string{mcpServersKey + "=thulu-cloud"}}
	cfg, _ := transcript(t, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n", existing)
	if got := strings.Join(cfg.MCPServers, ","); got != "thulu-cloud" {
		t.Errorf("MCP servers = %q, want the previous answer", got)
	}

	cfg, _ = runInterview(t, &Interviewer{PreviousMCP: []string{"octopus-api"}}, "2\n2\n\n3\n\n\n\ns\n\n\n\n\n")
	if got := strings.Join(cfg.MCPServers, ","); got != "octopus-api" {
		t.Errorf("MCP servers = %q, want the answers file's selection", got)
	}
}

func TestInterviewMCPNoneIsRecordedAsSkipped(t *testing.T) {
	cfg, _ := transcript(t, "2\n2\n\n3\n\n\nn\n\ns\n\n\n\n\n\n", nil)
	if cfg.MCPServers == nil || len(cfg.MCPServers) != 0 {
		t.Fatalf("MCP servers = %#v, want an explicit empty selection", cfg.MCPServers)
	}
	if !strings.Contains(strings.Join(deferredNames(cfg.Deferred), ","), "MCP servers") {
		t.Errorf("installing no MCP server left no trail: %v", cfg.Deferred)
	}
	f := &EnvFile{}
	cfg.JWTSecret = "x"
	cfg.ApplyToEnv(f)
	if f.Get(mcpServersKey) != noMCPServers {
		t.Errorf("%s = %q: an empty answer would read back as 'never asked'", mcpServersKey, f.Get(mcpServersKey))
	}
	if f.Get("COMPOSE_PROFILES") != "" {
		t.Errorf("COMPOSE_PROFILES = %q with nothing selected", f.Get("COMPOSE_PROFILES"))
	}
}

func TestApplyMCPToEnvWiresProfilesURLsAndCredential(t *testing.T) {
	f := &EnvFile{lines: []string{
		"OCTOPUS_MCP_FORWARD_CREDENTIAL=my-tracker,thulu-cloud",
		"MCP_THULU_CLOUD_URL=http://thulu-cloud:3000/sse",
		"MCP_THULU_BROWSER_URL=http://browser.corp:3000/sse",
	}}
	cfg := &Config{JWTSecret: "x", MCPServers: []string{"octopus-api", "thulu-github"}}
	cfg.ApplyToEnv(f)

	if got := f.Get("COMPOSE_PROFILES"); got != "octopus-api,thulu-github" {
		t.Errorf("COMPOSE_PROFILES = %q", got)
	}
	if got := f.Get("MCP_OCTOPUS_API_URL"); got != "http://octopus-api:3000/sse" {
		t.Errorf("MCP_OCTOPUS_API_URL = %q, want the compose service", got)
	}
	if got := f.Get("MCP_THULU_GITHUB_URL"); got != "http://thulu-github:3000/sse" {
		t.Errorf("MCP_THULU_GITHUB_URL = %q", got)
	}
	if got := f.Get("MCP_THULU_CLOUD_URL"); got != "" {
		t.Errorf("MCP_THULU_CLOUD_URL = %q, want removed with the service", got)
	}
	if got := f.Get("MCP_THULU_BROWSER_URL"); got != "http://browser.corp:3000/sse" {
		t.Errorf("MCP_THULU_BROWSER_URL = %q: the operator's external server was dropped", got)
	}
	if got := f.Get("OCTOPUS_MCP_FORWARD_CREDENTIAL"); got != "my-tracker,octopus-api,thulu-github" {
		t.Errorf("OCTOPUS_MCP_FORWARD_CREDENTIAL = %q", got)
	}
}

func TestApplyMCPToEnvWithoutAnAnswerTouchesNothing(t *testing.T) {
	f := &EnvFile{lines: []string{"COMPOSE_PROFILES=thulu-github", "MCP_THULU_GITHUB_URL=http://thulu-github:3000/sse"}}
	(&Config{JWTSecret: "x"}).ApplyToEnv(f)
	if f.Get("COMPOSE_PROFILES") != "thulu-github" || f.Get("MCP_THULU_GITHUB_URL") == "" {
		t.Errorf("a config with no MCP answer rewrote the MCP keys:\n%s", f.Render())
	}
}

func TestHelmValuesEnableExactlyTheCheckedServers(t *testing.T) {
	cfg := &Config{Mode: ModeKubernetes, Release: "octopus", Domain: "o.example.com",
		JWTSecret: "x", MCPServers: []string{"thulu-browser"}}
	mcp, ok := cfg.HelmValues()["mcp"].(map[string]any)
	if !ok {
		t.Fatal("no mcp section in the Helm values")
	}
	for _, s := range NativeMCPServers {
		entry, _ := mcp[s.HelmKey].(map[string]any)
		want := s.Name == "thulu-browser"
		if entry["enabled"] != want {
			t.Errorf("mcp.%s.enabled = %v, want %v", s.HelmKey, entry["enabled"], want)
		}
	}
	if _, has := mcp["imageRegistry"]; has {
		t.Error("imageRegistry written without an override")
	}
}

func TestHelmValuesFollowTheMirroredRegistry(t *testing.T) {
	t.Setenv(ImageRegistryEnv, "registry.corp.example/octopus/")
	values := (&Config{Mode: ModeKubernetes, JWTSecret: "x", MCPServers: []string{}}).HelmValues()
	mcp, _ := values["mcp"].(map[string]any)
	if mcp["imageRegistry"] != "registry.corp.example/octopus" {
		t.Errorf("mcp.imageRegistry = %v", mcp["imageRegistry"])
	}
	image, _ := values["image"].(map[string]any)
	if image["repository"] != "registry.corp.example/octopus/octopus" {
		t.Errorf("image.repository = %v, want the daemon on the same mirror", image["repository"])
	}
}

func TestMCPEnvKeyMatchesTheAgentEncoding(t *testing.T) {
	if got := MCPEnvKey("thulu-github"); got != "MCP_THULU_GITHUB_URL" {
		t.Errorf("MCPEnvKey = %q", got)
	}
}

func TestParseMCPSelectionDropsUnknownNames(t *testing.T) {
	if got := parseMCPSelection("octopus-api, retired-server ,thulu-cloud"); strings.Join(got, ",") != "octopus-api,thulu-cloud" {
		t.Errorf("got %v", got)
	}
	if got := parseMCPSelection("none"); got == nil || len(got) != 0 {
		t.Errorf("none → %#v", got)
	}
}
