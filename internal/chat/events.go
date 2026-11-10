package chat

import "github.com/MisterDonnuts/thulu/internal/syncx"

// Forwarded verbatim as the SSE `event:` field; names are part of the public wire contract.
type EventType string

const (
	EventChatCreated   EventType = "chat_created"
	EventChatUpdated   EventType = "chat_updated"
	EventChatDeleted   EventType = "chat_deleted"
	EventMessageAdded  EventType = "message_added"
	EventTurnStarted   EventType = "turn_started"
	EventTurnFinished  EventType = "turn_finished"
	EventTurnFailed    EventType = "turn_failed"
	EventActionPending EventType = "action_pending"
	EventActionDecided EventType = "action_decided"
)

// WorkspaceID must always be set: the SSE hub filters per-subscriber visibility on it.
type Event struct {
	Type        EventType `json:"type"`
	ChatID      string    `json:"chat_id"`
	WorkspaceID string    `json:"workspace_id"`
	TaskID      string    `json:"task_id,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	Chat        *Chat     `json:"chat,omitempty"`
	Message     *Message  `json:"message,omitempty"`
	Err         string    `json:"error,omitempty"`
	Action      *Action   `json:"action,omitempty"`

	// Set only by For; nil reaches no subscriber, so an event published without its chat is dropped
	// rather than leaked.
	audience *audience
}

// For stamps the event with its chat: ids, and who may receive it.
func (e Event) For(c *Chat) Event {
	e.ChatID, e.WorkspaceID, e.TaskID = c.ID, c.WorkspaceID, c.TaskID
	a := audienceOf(c)
	e.audience = &a
	return e
}

func (e Event) VisibleTo(v Viewer) bool { return e.audience != nil && e.audience.includes(v) }

// Listed is Chat.Listed for the event's chat: what a chat list subscribed to the feed may receive.
func (e Event) Listed(v Viewer) bool { return e.VisibleTo(v) && !e.audience.shared }

type Bus = syncx.Bus[Event]

func NewBus() *Bus { return syncx.NewBus[Event]() }
