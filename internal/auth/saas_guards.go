package auth

import (
	"log"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type saasRefusal struct {
	env string
	bad func(v string) bool
	key string
}

// Deliberately broader than the readers of these switches: a guard that misses "enabled"
// stops guarding the day a reader starts accepting it.
func valueOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func valueSet(v string) bool { return strings.TrimSpace(v) != "" }

var saasRefusals = []saasRefusal{
	{"OCTOPUS_DEV_MODE", valueOn, "auth.labels.saas_guard_dev_mode"},
	{DevModeContainerEnv, valueOn, "auth.labels.saas_guard_dev_mode"},
	{"OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL", valueOn, "auth.labels.saas_guard_host_llm_credential"},
	{"OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY", valueOn, "auth.labels.saas_guard_ambient_cloud_identity"},
	{"OCTOPUS_ALLOW_SSO_AUTOLINK", valueOn, "auth.labels.saas_guard_sso_autolink"},
	{"OCTOPUS_SSO_DEFAULT_WORKSPACE", valueSet, "auth.labels.saas_guard_sso_default_workspace"},
	{"OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT", valueOn, "auth.labels.saas_guard_secretbox_plaintext"},
	{"OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", valueOn, "auth.labels.saas_guard_git_local_remotes"},
	{"OCTOPUS_GIT_LOCAL_REMOTE_ROOT", valueSet, "auth.labels.saas_guard_git_local_remotes"},
	{"OCTOPUS_GIT_ALLOW_SSH", valueOn, "auth.labels.saas_guard_git_ssh"},
}

func saasConfigProblems(getenv func(string) string) (problems, warnings []string) {
	if !valueOn(getenv("OCTOPUS_SAAS_MODE")) {
		return nil, nil
	}
	line := func(env, key string) string { return "  - " + env + ": " + i18n.Text(key) }
	logLine := func(env, key string) string { return env + ": " + i18n.Log(key) }
	for _, c := range saasRefusals {
		if c.bad(getenv(c.env)) {
			problems = append(problems, line(c.env, c.key))
		}
	}
	if !valueSet(getenv("OCTOPUS_SECRETBOX_KEY")) && !valueSet(getenv("OCTOPUS_SECRETBOX_KEY_FILE")) {
		problems = append(problems, line("OCTOPUS_SECRETBOX_KEY", "auth.labels.saas_guard_secretbox_key"))
	}
	if !valueSet(getenv("OCTOPUS_OIDC_ISSUER")) && !valueSet(getenv("OCTOPUS_GITHUB_CLIENT_ID")) {
		problems = append(problems, line("OCTOPUS_OIDC_ISSUER", "auth.labels.saas_guard_no_sso"))
	}

	if !valueSet(getenv("OCTOPUS_OPERATOR_WORKSPACE")) {
		warnings = append(warnings, logLine("OCTOPUS_OPERATOR_WORKSPACE", "auth.labels.saas_guard_no_operator"))
	}
	if strings.EqualFold(strings.TrimSpace(getenv("OCTOPUS_SIGNUP_MODE")), SignupOpen) {
		warnings = append(warnings, logLine("OCTOPUS_SIGNUP_MODE", "auth.labels.saas_guard_signup_open"))
	}
	if v := strings.TrimSpace(getenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES")); v != "" && !valueOn(v) {
		warnings = append(warnings, logLine("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "auth.labels.saas_guard_no_personal_workspaces"))
	}
	return problems, warnings
}

func validateSaaSConfig() error {
	if devModeRequested() && valueSet(os.Getenv("OCTOPUS_OIDC_ISSUER")) && !valueOn(os.Getenv("OCTOPUS_SAAS_MODE")) {
		return apperr.Precondition("auth.errors.dev_mode_with_oidc")
	}
	problems, warnings := saasConfigProblems(os.Getenv)
	for _, w := range warnings {
		log.Printf("[auth] %s", i18n.Log("auth.log.saas_config_warning", "line", w))
	}
	if len(problems) > 0 {
		return apperr.Precondition("auth.errors.saas_unsafe_config", "problems", strings.Join(problems, "\n"))
	}
	return nil
}
