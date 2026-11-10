package daemon

import "testing"

func TestGatewayConfigFromEnv(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	cases := []struct {
		name string
		kv   map[string]string
		on   bool
	}{
		{"auto without config stays off", nil, false},
		{"auto with a listener (the helm chart)", map[string]string{"OCTOPUS_LLM_GATEWAY_ADDR": ":8586"}, true},
		{"auto with an agent URL", map[string]string{"OCTOPUS_LLM_GATEWAY_URL": "http://octopus:8585/llm-gateway"}, true},
		{"on (docker)", map[string]string{"OCTOPUS_LLM_GATEWAY": "on"}, true},
		{"off wins over a listener", map[string]string{"OCTOPUS_LLM_GATEWAY": "off", "OCTOPUS_LLM_GATEWAY_ADDR": ":8586"}, false},
	}
	for _, c := range cases {
		cfg, err := gatewayConfigFromEnv(env(c.kv))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if cfg.enabled != c.on {
			t.Errorf("%s: enabled = %v, want %v", c.name, cfg.enabled, c.on)
		}
	}

	cfg, err := gatewayConfigFromEnv(env(map[string]string{"OCTOPUS_LLM_UPSTREAM": "https://anthropic.proxy.corp/"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.upstreams["anthropic"] != "https://anthropic.proxy.corp" || cfg.upstreams["openai"] != "https://api.openai.com/v1" {
		t.Errorf("upstreams = %v", cfg.upstreams)
	}

	for _, bad := range []map[string]string{
		{"OCTOPUS_LLM_GATEWAY": "yes please"},
		{"OCTOPUS_LLM_UPSTREAM": "ftp://x"},
		{"OCTOPUS_LLM_GATEWAY_URL": "/llm-gateway"},
	} {
		if _, err := gatewayConfigFromEnv(env(bad)); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}
