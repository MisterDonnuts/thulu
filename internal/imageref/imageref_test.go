package imageref

import "testing"

func TestWithRegistry(t *testing.T) {
	const reg = "registry.corp.example/octopus"
	cases := []struct {
		registry, ref, want string
	}{
		{reg, "octopus-docker-agent:latest", reg + "/octopus-docker-agent:latest"},
		{reg, "library/ubuntu:24.04", reg + "/library/ubuntu:24.04"},
		{reg + "/", " octopus-sandbox:local ", reg + "/octopus-sandbox:local"},
		{reg, "ghcr.io/acme/agent:1.2", "ghcr.io/acme/agent:1.2"},
		{reg, "localhost:5000/agent", "localhost:5000/agent"},
		{reg, "localhost/agent", "localhost/agent"},
		{reg, "agent@sha256:abc", reg + "/agent@sha256:abc"},
		{"", "octopus-docker-agent:latest", "octopus-docker-agent:latest"},
		{reg, "", ""},
	}
	for _, c := range cases {
		if got := WithRegistry(c.registry, c.ref); got != c.want {
			t.Errorf("WithRegistry(%q, %q) = %q, want %q", c.registry, c.ref, got, c.want)
		}
	}
}

func TestResolveReadsEnv(t *testing.T) {
	t.Setenv(RegistryEnv, "harbor.internal:8443/mirror/")
	if got := Resolve("octopus-runtime:latest"); got != "harbor.internal:8443/mirror/octopus-runtime:latest" {
		t.Errorf("Resolve = %q", got)
	}
	t.Setenv(RegistryEnv, "")
	if got := Resolve("octopus-runtime:latest"); got != "octopus-runtime:latest" {
		t.Errorf("Resolve without registry = %q", got)
	}
}
