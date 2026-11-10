package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"

	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

type EventType string

const (
	EventTaskCreated       EventType = "task.created"
	EventTaskStageChanged  EventType = "task.stage_changed"
	EventTaskDeleted       EventType = "task.deleted"
	EventChatMessage       EventType = "chat.message"
	EventStagesChanged     EventType = "stages.changed"
	EventBoardsChanged     EventType = "boards.changed"
	EventTaskTypesChanged  EventType = "task_types.changed"
	EventQuestionAsked     EventType = "question.asked"
	EventQuestionAnswered  EventType = "question.answered"
	EventQuestionCanceled  EventType = "question.canceled"
	EventApprovalRequested EventType = "approval.requested"
	EventApprovalDecided   EventType = "approval.decided"
)

func Events() []EventType {
	return []EventType{
		EventTaskCreated, EventTaskStageChanged, EventTaskDeleted,
		EventChatMessage,
		EventBoardsChanged, EventStagesChanged, EventTaskTypesChanged,
		EventQuestionAsked, EventQuestionAnswered, EventQuestionCanceled,
		EventApprovalRequested, EventApprovalDecided,
	}
}

func UnknownEventPatterns(patterns []string) []string {
	var unknown []string
	for _, p := range patterns {
		matched := false
		for _, e := range Events() {
			if matchesEvent([]string{p}, e) {
				matched = true
				break
			}
		}
		if !matched {
			unknown = append(unknown, p)
		}
	}
	return unknown
}

type Endpoint struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	URL             string `json:"url"`
	Secret          string `json:"secret,omitempty"`
	SecretEncrypted string `json:"-"`
	// Endpoints with no secret of their own are signed with this hash as the HMAC key until rotated;
	// their receivers verify with it.
	SecretHash    string    `json:"-"`
	Events        []string  `json:"events"`
	Active        bool      `json:"active"`
	LegacySigning bool      `json:"legacy_signing"`
	CreatedAt     time.Time `json:"created_at"`
}

// An error means the sealed secret did not open; the delivery must not go out unsigned or signed
// with anything else.
func (ep *Endpoint) signingKey() ([]byte, error) {
	if ep.SecretEncrypted != "" {
		secret, err := secretbox.Decrypt(secretbox.LabelWebhookSecret, ep.SecretEncrypted)
		if err != nil {
			return nil, apperr.Wrap(err, "webhook.errors.open_signing_secret")
		}
		return []byte(secret), nil
	}
	if ep.SecretHash != "" {
		return []byte(ep.SecretHash), nil
	}
	return nil, nil
}

type Payload struct {
	ID          string    `json:"id"`
	Event       EventType `json:"event"`
	WorkspaceID string    `json:"workspace_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	Data        any       `json:"data"`
}

func signPayload(body, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func generateSecret() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}

func matchesEvent(patterns []string, event EventType) bool {
	for _, p := range patterns {
		if p == "*" || strings.TrimSpace(p) == string(event) {
			return true
		}
		if strings.HasSuffix(p, ".*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(string(event), prefix) {
				return true
			}
		}
	}
	return false
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
