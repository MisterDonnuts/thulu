package sandbox

import "testing"

func TestRuntimeImageFromEnv(t *testing.T) {
	for env, want := range map[string]string{
		"":    DefaultRuntimeImage,
		"   ": DefaultRuntimeImage,
		"ghcr.io/misterdonnuts/octopus-runtime@sha256:abc": "ghcr.io/misterdonnuts/octopus-runtime@sha256:abc",
		" ghcr.io/misterdonnuts/octopus-runtime:0.2.0 ":    "ghcr.io/misterdonnuts/octopus-runtime:0.2.0",
	} {
		got := RuntimeImageFromEnv(func(k string) string {
			if k == EnvSandboxImage {
				return env
			}
			return ""
		})
		if got != want {
			t.Errorf("OCTOPUS_SANDBOX_IMAGE=%q: image %q, want %q", env, got, want)
		}
	}
}
