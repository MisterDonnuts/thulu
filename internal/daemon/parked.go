package daemon

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

const envQuestionsSandboxGrace = "OCTOPUS_QUESTIONS_SANDBOX_GRACE"

const defaultQuestionsSandboxGrace = 30 * time.Minute

// Mirrors retention.stuck_worktree_days's default; the Kubernetes janitor has no settings store to
// read it.
const parkedClaimTTL = 14 * 24 * time.Hour

func questionsSandboxGrace() time.Duration {
	v := strings.TrimSpace(os.Getenv(envQuestionsSandboxGrace))
	if v == "" {
		return defaultQuestionsSandboxGrace
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.bad_questions_grace", "env", envQuestionsSandboxGrace,
			"default", defaultQuestionsSandboxGrace))
		return defaultQuestionsSandboxGrace
	}
	return d
}

// Parked tasks only in Docker mode: in Kubernetes, releasing the sandbox also drops the worktree
// claim the resume needs.
func sandboxReleasable(t *task.Task, now time.Time, failedTTL, grace time.Duration, dockerMode bool) bool {
	switch {
	case isJanitorReleasableStatus(t.Status):
		return !t.UpdatedAt.After(now.Add(-failedTTL))
	case isParkedStatus(t.Status):
		return dockerMode && !t.UpdatedAt.After(now.Add(-grace))
	}
	return false
}

func isParkedStatus(s task.RunStatus) bool {
	return s == task.StatusAwaitingInput || s == task.StatusAwaitingApproval
}

func isParkableWorktreeStatus(s task.RunStatus) bool {
	return isJanitorReleasableStatus(s) || isParkedStatus(s)
}
