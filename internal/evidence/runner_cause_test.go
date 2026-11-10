package evidence

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func TestRunnerCause(t *testing.T) {
	src := func(key string, args ...any) string { return i18n.T(i18n.SourceLocale, key, args...) }
	for text, want := range map[string]string{
		src("api.launcher.errors.browser_image", "image", "ghcr.io/x/thulu-browser:1") + ": pull access denied": CauseBrowserImage,
		src("api.evidence.errors.runner_start", "batch", "b1") + ": no such network":                            CauseRunnerStart,
		src("api.evidence.reasons.runner_exit", "code", "137"):                                                  CauseRunnerExit,
		src("api.evidence.reasons.deadline"):                                                                    CauseDeadline,
		src("evidence.reasons.daemon_restarted"):                                                                CauseDaemonRestart,
		src("evidence.reasons.runner_lost"):                                                                     CauseRunnerLost,
		"net::ERR_NAME_NOT_RESOLVED at https://staging.example.com":                                             "",
		"": "",
	} {
		if got := RunnerCause(text); got != want {
			t.Errorf("RunnerCause(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestLocalizeRunnerError(t *testing.T) {
	src := func(key string, args ...any) string { return i18n.T(i18n.SourceLocale, key, args...) }
	es := func(key string, args ...any) string { return i18n.T("es", key, args...) }
	for text, want := range map[string]string{
		src("api.evidence.reasons.deadline"):                            es("api.evidence.reasons.deadline"),
		src("evidence.reasons.runner_lost"):                             es("evidence.reasons.runner_lost"),
		src("api.evidence.reasons.runner_exit", "code", "137"):          es("api.evidence.reasons.runner_exit", "code", "137"),
		src("evidence.reasons.daemon_restarted") + ": context canceled": es("evidence.reasons.daemon_restarted") + ": context canceled",
		"net::ERR_NAME_NOT_RESOLVED at https://staging.example.com":     "net::ERR_NAME_NOT_RESOLVED at https://staging.example.com",
	} {
		if got := LocalizeRunnerError(text, "es"); got != want {
			t.Errorf("LocalizeRunnerError(%q) = %q, want %q", text, got, want)
		}
	}
	if got := LocalizeRunnerError(src("api.evidence.reasons.deadline"), i18n.SourceLocale); got != src("api.evidence.reasons.deadline") {
		t.Errorf("source-locale reader got %q", got)
	}
}
