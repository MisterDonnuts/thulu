package engine

import "testing"

func TestCloudSecretRoundTrip(t *testing.T) {
	enc, err := CloudSecret{}.Encode()
	if err != nil || enc != "" {
		t.Fatalf("empty secret must encode to \"\", got %q, %v", enc, err)
	}
	in := CloudSecret{AWSAccessKeyID: "AKIA", AWSSecretAccessKey: "x"}
	enc, err = in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeCloudSecret(enc)
	if err != nil || out != in {
		t.Fatalf("round trip = %+v, %v", out, err)
	}
	if _, err := DecodeCloudSecret("sk-ant-api-not-json"); err == nil {
		t.Error("a non-JSON credential must not decode as a cloud secret")
	}
}

func TestCustomEndpointKind(t *testing.T) {
	caps := Capabilities{
		Provider:         ProviderAnthropic,
		AuthorizationEnv: "ANTHROPIC_AUTH_TOKEN",
		KeyKinds: []KeyKind{
			{ID: "api", Prefix: "sk-ant-api", EnvVars: []string{"ANTHROPIC_API_KEY"}, Clears: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
			{ID: "oauth", Prefix: "sk-ant-oat", EnvVars: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
		},
	}
	k, ok := caps.CustomEndpointKind(AuthHeaderBearer)
	if !ok || len(k.EnvVars) != 1 || k.EnvVars[0] != "ANTHROPIC_AUTH_TOKEN" {
		t.Fatalf("bearer kind = %+v", k)
	}
	if !contains(k.Clears, "ANTHROPIC_API_KEY") || !contains(k.Clears, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("bearer kind must clear every other credential var: %v", k.Clears)
	}
	k, ok = caps.CustomEndpointKind("")
	if !ok || k.EnvVars[0] != "ANTHROPIC_API_KEY" || !contains(k.Clears, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("default kind = %+v", k)
	}
	if contains(caps.KeyKinds[0].Clears, "ANTHROPIC_AUTH_TOKEN") {
		t.Error("CustomEndpointKind mutated the engine's KeyKind")
	}
	if !caps.SupportsProvider(ProviderAnthropic) || caps.SupportsProvider(ProviderBedrock) {
		t.Error("SupportsProvider: own provider only when no cloud providers are declared")
	}
}

func TestGatewayCanPresent(t *testing.T) {
	caps := Capabilities{
		Provider:   ProviderAnthropic,
		BaseURLEnv: "ANTHROPIC_BASE_URL",
		KeyKinds: []KeyKind{
			{ID: "api", Prefix: "sk-ant-api", EnvVars: []string{"ANTHROPIC_API_KEY"}},
			{ID: "oauth", Prefix: "sk-ant-oat", EnvVars: []string{"CLAUDE_CODE_OAUTH_TOKEN"}, NoGateway: true},
		},
	}
	cases := []struct {
		name, provider, cred, base string
		want                       bool
	}{
		{"api key", "", "sk-ant-api-x", "", true},
		{"oauth token stays direct", "", "sk-ant-oat-x", "", false},
		{"custom endpoint ignores the prefix", "", "sk-ant-oat-looking-virtual-key", "https://llm.corp", true},
		{"unrecognized key still proxied", "", "litellm-123", "", true},
		{"cloud profile", ProviderBedrock, "{}", "", false},
		{"no credential", "", "", "", false},
	}
	for _, c := range cases {
		if got := caps.GatewayCanPresent(c.provider, c.cred, c.base); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if (Capabilities{Provider: "gemini", BaseURLEnv: "X"}).GatewayCanPresent("", "k", "") {
		t.Error("a provider the gateway does not speak must stay direct")
	}
	if (Capabilities{Provider: ProviderOpenAI}).GatewayCanPresent("", "k", "") {
		t.Error("a CLI that takes no base URL cannot be pointed at the gateway")
	}
}

func TestAPIProviders(t *testing.T) {
	multi := Capabilities{
		Provider:     ProviderOpenAICompatible,
		APIProviders: []string{ProviderAnthropic, ProviderOpenAI},
		BaseURLEnv:   "OCTOPUS_LLM_BASE_URL",
		KeyKinds:     []KeyKind{{ID: "api", EnvVars: []string{"LLM_API_KEY"}}},
	}
	if got := multi.Providers(); len(got) != 3 || got[0] != ProviderOpenAICompatible {
		t.Errorf("Providers() = %v, own first", got)
	}
	for _, p := range []string{"", ProviderOpenAICompatible, ProviderAnthropic, ProviderOpenAI} {
		if !multi.SupportsProvider(p) || !multi.GatewayCanPresent(p, "any-key", "") {
			t.Errorf("provider %q: supported=%v gateway=%v", p, multi.SupportsProvider(p), multi.GatewayCanPresent(p, "any-key", ""))
		}
	}
	if multi.ProfileProvider("") != ProviderOpenAICompatible || multi.ProfileProvider(ProviderOpenAI) != ProviderOpenAI {
		t.Error("ProfileProvider: the profile's own, or the engine's when it names none")
	}

	single := Capabilities{Provider: ProviderAnthropic, BaseURLEnv: "ANTHROPIC_BASE_URL"}
	if single.SupportsProvider(ProviderOpenAICompatible) || single.GatewayCanPresent(ProviderOpenAI, "k", "") {
		t.Error("a provider the engine does not declare is neither offered nor proxied")
	}
	if !ProviderNeedsBaseURL(ProviderOpenAICompatible) || ProviderNeedsBaseURL(ProviderAnthropic) {
		t.Error("only openai-compatible has no default address")
	}
}
