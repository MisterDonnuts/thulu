// Package logx provides small helpers for the project's error-handling policy.
//
// Thulu treats most ignored errors as a code smell. When an operation's
// failure is genuinely non-actionable but still worth a trace — best-effort
// cleanup in an error path, a fire-and-forget metadata update — use [Warn]
// instead of discarding the error with a blank assignment. This keeps a signal
// in the logs without forcing callers to handle something they cannot act on,
// and avoids scattering //nolint:errcheck directives across the codebase.
//
// If a caller can do something useful with the error, return it instead.
package logx

import (
	"io"
	"log"
	"strings"
	"unicode"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func Warn(err error, key string, args ...any) {
	if err == nil {
		return
	}
	log.Print(i18n.Log("common.log.warn", "message", i18n.Log(key, args...), "err", err))
}

func Safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func Close(c io.Closer, name string) {
	if c == nil {
		return
	}
	Warn(c.Close(), "common.log.close_failed", "name", name)
}
