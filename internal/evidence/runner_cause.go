package evidence

import (
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	CauseBrowserImage   = "browser_image"
	CauseRunnerStart    = "runner_start"
	CauseRunnerExit     = "runner_exit"
	CauseDeadline       = "deadline"
	CauseDaemonRestart  = "daemon_restarted"
	CauseRunnerLost     = "runner_lost"
	causeArgPlaceholder = "\x00"
)

// Matched against stored errors, which CloseBatch writes in i18n.SourceLocale whatever the reader's
// locale.
var causePrefixes = []struct {
	cause, key, arg string
}{
	{CauseBrowserImage, "api.launcher.errors.browser_image", "image"},
	{CauseRunnerStart, "api.evidence.errors.runner_create", "batch"},
	{CauseRunnerStart, "api.evidence.errors.runner_start", "batch"},
	{CauseRunnerExit, "api.evidence.reasons.runner_exit", "code"},
	{CauseDeadline, "api.evidence.reasons.deadline", ""},
	{CauseDaemonRestart, "evidence.reasons.daemon_restarted", ""},
	{CauseRunnerLost, "evidence.reasons.runner_lost", ""},
}

func RunnerCause(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for _, p := range causePrefixes {
		var prefix string
		if p.arg == "" {
			prefix = i18n.T(i18n.SourceLocale, p.key)
		} else {
			prefix, _, _ = strings.Cut(i18n.T(i18n.SourceLocale, p.key, p.arg, causeArgPlaceholder), causeArgPlaceholder)
		}
		if prefix != "" && strings.HasPrefix(text, prefix) {
			return p.cause
		}
	}
	return ""
}

var runnerTexts = sync.OnceValue(func() *i18n.Retranslator {
	keys := map[string]bool{}
	for _, p := range causePrefixes {
		keys[p.key] = true
	}
	return i18n.NewRetranslator(i18n.Default(), func(key string) bool { return keys[key] })
})

func LocalizeRunnerError(text, locale string) string {
	rt := runnerTexts()
	if out, ok := rt.Translate(text, locale); ok {
		return out
	}
	for i := strings.Index(text, ": "); i >= 0; {
		if out, ok := rt.Translate(text[:i], locale); ok {
			return out + text[i:]
		}
		next := strings.Index(text[i+2:], ": ")
		if next < 0 {
			break
		}
		i += 2 + next
	}
	return text
}
