package task

import (
	"github.com/MisterDonnuts/thulu/internal/engine"
)

// Claude Code's result event carries the whole final message and the entrypoint logs lines after
// it; 256 KiB covers a long report.
const sessionLogTail int64 = 256 * 1024

// No engine is named: a profile may have run this on another engine than the agent's, and each
// parser only recognizes its own stream.
func runHitMaxTurns(outputDir string) bool {
	buf := readSessionLogTail(outputDir)
	if buf == nil {
		return false
	}
	return engine.ResultOutcome(buf, "") == engine.OutcomeMaxTurns
}

func runOutcome(outputDir string) (string, int) {
	buf := readSessionLogTail(outputDir)
	if buf == nil {
		return "", 0
	}
	ev, ok := engine.LastResult(buf, "")
	if !ok {
		return "", 0
	}
	return ev.Outcome, ev.NumTurns
}
