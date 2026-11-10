package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func saasBase() map[string]string {
	return map[string]string{
		"OCTOPUS_SAAS_MODE":          "1",
		"OCTOPUS_SECRETBOX_KEY":      strings.Repeat("ab", 32),
		"OCTOPUS_OIDC_ISSUER":        "https://id.example.com",
		"OCTOPUS_OPERATOR_WORKSPACE": "a1b2c3d4e5f60718",
	}
}

func TestSaaSConfigProblems(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		wantProblems []string
		wantWarnings []string
	}{
		{name: "the base configuration is accepted"},
		{name: "self-hosted is never checked", env: map[string]string{
			"OCTOPUS_SAAS_MODE": "", "OCTOPUS_DEV_MODE": "1", "OCTOPUS_GIT_ALLOW_SSH": "1", "OCTOPUS_SECRETBOX_KEY": "",
		}},
		{name: "SAAS_MODE=0 is off", env: map[string]string{"OCTOPUS_SAAS_MODE": "0", "OCTOPUS_ALLOW_SSO_AUTOLINK": "1"}},
		{name: "dev mode", env: map[string]string{"OCTOPUS_DEV_MODE": "1"}, wantProblems: []string{"OCTOPUS_DEV_MODE"}},
		{name: "dev mode container escape", env: map[string]string{DevModeContainerEnv: "1"}, wantProblems: []string{DevModeContainerEnv}},
		{name: "host LLM credential", env: map[string]string{"OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL": "1"},
			wantProblems: []string{"OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL"}},
		{name: "ambient cloud identity, any truthy spelling", env: map[string]string{"OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY": "yes"},
			wantProblems: []string{"OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY"}},
		{name: "SSO autolink", env: map[string]string{"OCTOPUS_ALLOW_SSO_AUTOLINK": "1"}, wantProblems: []string{"OCTOPUS_ALLOW_SSO_AUTOLINK"}},
		{name: "shared default workspace", env: map[string]string{"OCTOPUS_SSO_DEFAULT_WORKSPACE": "acme"},
			wantProblems: []string{"OCTOPUS_SSO_DEFAULT_WORKSPACE"}},
		{name: "plaintext secrets", env: map[string]string{"OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT": "true"},
			wantProblems: []string{"OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT"}},
		{name: "no secretbox key", env: map[string]string{"OCTOPUS_SECRETBOX_KEY": ""}, wantProblems: []string{"OCTOPUS_SECRETBOX_KEY"}},
		{name: "secretbox key from a file is enough", env: map[string]string{
			"OCTOPUS_SECRETBOX_KEY": "", "OCTOPUS_SECRETBOX_KEY_FILE": "/run/secrets/box",
		}},
		{name: "local git remotes", env: map[string]string{"OCTOPUS_GIT_ALLOW_LOCAL_REMOTES": "on", "OCTOPUS_GIT_LOCAL_REMOTE_ROOT": "/srv"},
			wantProblems: []string{"OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "OCTOPUS_GIT_LOCAL_REMOTE_ROOT"}},
		{name: "SSH git remotes", env: map[string]string{"OCTOPUS_GIT_ALLOW_SSH": "1"}, wantProblems: []string{"OCTOPUS_GIT_ALLOW_SSH"}},
		{name: "explicit off values pass", env: map[string]string{
			"OCTOPUS_GIT_ALLOW_SSH": "0", "OCTOPUS_ALLOW_SSO_AUTOLINK": "false", "OCTOPUS_DEV_MODE": "off",
		}},
		{name: "no identity provider", env: map[string]string{"OCTOPUS_OIDC_ISSUER": ""}, wantProblems: []string{"OCTOPUS_OIDC_ISSUER"}},
		{name: "GitHub sign-in counts as a provider", env: map[string]string{"OCTOPUS_OIDC_ISSUER": "", "OCTOPUS_GITHUB_CLIENT_ID": "gh"}},
		{name: "everything at once, every line listed", env: map[string]string{
			"OCTOPUS_DEV_MODE": "1", "OCTOPUS_ALLOW_SSO_AUTOLINK": "1", "OCTOPUS_SECRETBOX_KEY": "",
		}, wantProblems: []string{"OCTOPUS_DEV_MODE", "OCTOPUS_ALLOW_SSO_AUTOLINK", "OCTOPUS_SECRETBOX_KEY"}},
		{name: "no operator is a warning", env: map[string]string{"OCTOPUS_OPERATOR_WORKSPACE": ""},
			wantWarnings: []string{"OCTOPUS_OPERATOR_WORKSPACE"}},
		{name: "open signup is a warning", env: map[string]string{"OCTOPUS_SIGNUP_MODE": "open"},
			wantWarnings: []string{"OCTOPUS_SIGNUP_MODE"}},
		{name: "personal workspaces off is a warning", env: map[string]string{"OCTOPUS_ALLOW_PERSONAL_WORKSPACES": "0"},
			wantWarnings: []string{"OCTOPUS_ALLOW_PERSONAL_WORKSPACES"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := saasBase()
			for k, v := range c.env {
				if v == "" {
					delete(env, k)
				} else {
					env[k] = v
				}
			}
			problems, warnings := saasConfigProblems(func(k string) string { return env[k] })
			check := func(kind string, got, want []string, prefix string) {
				t.Helper()
				if len(got) != len(want) {
					t.Fatalf("%s = %q, want lines for %v", kind, got, want)
				}
				for i := range want {
					if !strings.HasPrefix(got[i], prefix+want[i]+": ") {
						t.Errorf("%s[%d] = %q, want it to name %s", kind, i, got[i], want[i])
					}
				}
			}
			check("problems", problems, c.wantProblems, "  - ")
			check("warnings", warnings, c.wantWarnings, "")
		})
	}
}

func TestValidateStartupConfig_SaaSRefusalListsEveryProblem(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", strings.Repeat("s", 32))
	for k, v := range saasBase() {
		t.Setenv(k, v)
	}
	t.Setenv("OCTOPUS_ALLOW_SSO_AUTOLINK", "1")
	t.Setenv("OCTOPUS_GIT_ALLOW_SSH", "1")
	t.Setenv("OCTOPUS_DEV_MODE", "")
	t.Setenv(DevModeContainerEnv, "")

	err := ValidateStartupConfig("0.0.0.0:8585")
	if err == nil {
		t.Fatal("unsafe SaaS configuration accepted")
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindPrecondition {
		t.Fatalf("err = %v, want a precondition error", err)
	}
	msg := err.Error()
	for _, env := range []string{"OCTOPUS_ALLOW_SSO_AUTOLINK", "OCTOPUS_GIT_ALLOW_SSH"} {
		if !strings.Contains(msg, "\n  - "+env+": ") {
			t.Errorf("error does not list %s on its own line:\n%s", env, msg)
		}
	}

	t.Setenv("OCTOPUS_ALLOW_SSO_AUTOLINK", "")
	t.Setenv("OCTOPUS_GIT_ALLOW_SSH", "")
	if err := ValidateStartupConfig("0.0.0.0:8585"); err != nil {
		t.Errorf("safe SaaS configuration refused: %v", err)
	}
}

func TestPersonalWorkspacesAllowed_SaaSDefault(t *testing.T) {
	t.Setenv("OCTOPUS_OIDC_ISSUER", "https://id.example.com")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "")
	t.Setenv("OCTOPUS_SAAS_MODE", "")
	if PersonalWorkspacesAllowed() {
		t.Error("self-hosted with an issuer: personal workspaces on by default")
	}
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	if !PersonalWorkspacesAllowed() {
		t.Error("SaaS mode: personal workspaces off by default")
	}
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "0")
	if PersonalWorkspacesAllowed() {
		t.Error("SaaS mode: explicit 0 ignored")
	}
}

func TestValidateStartupConfig_DevModeWithOIDC(t *testing.T) {
	t.Setenv("OCTOPUS_SAAS_MODE", "")
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("OCTOPUS_OIDC_ISSUER", "https://id.example")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	err := ValidateStartupConfig("127.0.0.1:8585")
	if err == nil || !strings.Contains(err.Error(), "OCTOPUS_OIDC_ISSUER") {
		t.Errorf("dev mode next to an issuer: err = %v", err)
	}
	t.Setenv("OCTOPUS_OIDC_ISSUER", "")
	if err := ValidateStartupConfig("127.0.0.1:8585"); err != nil {
		t.Errorf("plain dev mode on loopback refused: %v", err)
	}
}
