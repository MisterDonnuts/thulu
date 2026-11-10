package task

import (
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	maxPendingMentions    = 5
	maxMergedMentionBytes = 16 << 10
)

type mentionLane struct {
	mu   sync.Mutex
	live map[string][]*Comment
}

func newMentionLane() *mentionLane {
	return &mentionLane{live: map[string][]*Comment{}}
}

func mentionKey(taskID, threadID, agentName string) string {
	return taskID + "/" + threadID + "/" + agentName
}

func (l *mentionLane) begin(key string, c *Comment) (claimed bool, queued int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pending, live := l.live[key]
	if !live {
		l.live[key] = nil
		return true, 0
	}
	pending = append(pending, c)
	if len(pending) > maxPendingMentions {
		pending = pending[len(pending)-maxPendingMentions:]
	}
	l.live[key] = pending
	return false, len(pending)
}

// The lane stays claimed for the follow-up run, so a message arriving now queues instead of
// starting a third run.
func (l *mentionLane) finish(key string) *Comment {
	l.mu.Lock()
	defer l.mu.Unlock()
	pending := l.live[key]
	if len(pending) == 0 {
		delete(l.live, key)
		return nil
	}
	l.live[key] = nil
	return mergeMentions(pending)
}

func (l *mentionLane) abandon(key string) (dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	dropped = len(l.live[key])
	delete(l.live, key)
	return dropped
}

func mergeMentions(pending []*Comment) *Comment {
	if len(pending) == 0 {
		return nil
	}
	last := pending[len(pending)-1]
	if len(pending) == 1 {
		return last
	}
	bodies := make([]string, 0, len(pending))
	for _, c := range pending {
		if body := strings.TrimSpace(c.Body); body != "" {
			bodies = append(bodies, body)
		}
	}
	merged := strings.Join(bodies, "\n\n---\n\n")
	if len(merged) > maxMergedMentionBytes {
		merged = i18n.Text("task.prompts.earlier_messages_trimmed") + merged[len(merged)-maxMergedMentionBytes:]
	}
	out := *last
	out.Body = merged
	return &out
}

// Exit codes 90-93 come from the entrypoint (images/snippets/run-bundle.sh); 94 from
// ee/kuberuntime/waitnetwork.go.
func runFailureHint(exitCode int) error {
	switch exitCode {
	case 90:
		return apperr.New("task.comments.run_failure_no_credential")
	case 91:
		return apperr.New("task.comments.run_failure_bundle_denied")
	case 92:
		return apperr.New("task.comments.run_failure_bundle_unreadable")
	case 93:
		return apperr.New("task.comments.run_failure_no_prompt")
	case 94: // kuberuntime.ExitNetworkClosed
		return apperr.New("task.comments.run_failure_network_closed")
	default:
		return nil
	}
}
