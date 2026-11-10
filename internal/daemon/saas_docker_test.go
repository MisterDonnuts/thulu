package daemon

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestCheckDockerRuntimeSaaS(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		refused bool
	}{
		"self-hosted docker":    {env: map[string]string{}},
		"self-hosted explicit":  {env: map[string]string{"OCTOPUS_AGENT_RUNTIME": "docker"}},
		"saas docker":           {env: map[string]string{"OCTOPUS_SAAS_MODE": "1"}, refused: true},
		"saas true docker":      {env: map[string]string{"OCTOPUS_SAAS_MODE": "true", "OCTOPUS_AGENT_RUNTIME": "docker"}, refused: true},
		"saas bad override":     {env: map[string]string{"OCTOPUS_SAAS_MODE": "1", "OCTOPUS_ALLOW_DOCKER_SAAS": "yes please"}, refused: true},
		"saas kubernetes":       {env: map[string]string{"OCTOPUS_SAAS_MODE": "1", "OCTOPUS_AGENT_RUNTIME": " Kubernetes "}},
		"saas docker allowed":   {env: map[string]string{"OCTOPUS_SAAS_MODE": "1", "OCTOPUS_ALLOW_DOCKER_SAAS": "1"}},
		"saas mode off ignored": {env: map[string]string{"OCTOPUS_SAAS_MODE": "0"}},
	} {
		err := checkDockerRuntimeSaaS(func(k string) string { return tc.env[k] })
		if tc.refused != (err != nil) {
			t.Errorf("%s: err = %v, refused want %v", name, err, tc.refused)
		}
		if err != nil && apperr.KindOf(err) != apperr.KindForbidden {
			t.Errorf("%s: kind = %v", name, apperr.KindOf(err))
		}
	}
}

func TestRunnersEnabled(t *testing.T) {
	for name, tc := range map[string]struct {
		runtime, runners string
		want             bool
	}{
		"docker default":     {want: true},
		"docker off":         {runners: "0", want: false},
		"docker false":       {runners: "false", want: false},
		"kubernetes default": {runtime: "kubernetes", want: false},
		"kubernetes on":      {runtime: "kubernetes", runners: "1", want: true},
		"kubernetes true":    {runtime: "Kubernetes", runners: "true", want: true},
		"kubernetes garbage": {runtime: "kubernetes", runners: "yes", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OCTOPUS_AGENT_RUNTIME", tc.runtime)
			t.Setenv("OCTOPUS_RUNNERS", tc.runners)
			if got := runnersEnabled(); got != tc.want {
				t.Errorf("runnersEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
