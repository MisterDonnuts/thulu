package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
)

type EventKind string

const (
	EventTaskCreated        EventKind = "task_created"
	EventStageEntered       EventKind = "stage_entered"
	EventAgentRunStarted    EventKind = "agent_run_started"
	EventAgentPromptInit    EventKind = "agent_prompt"
	EventAgentRunFinished   EventKind = "agent_run_finished"
	EventMentionStarted     EventKind = "mention_started"
	EventMentionFinished    EventKind = "mention_finished"
	EventAuditorPrompt      EventKind = "auditor_prompt"
	EventAuditorVerdict     EventKind = "auditor_verdict"
	EventAuditorRunStarted  EventKind = "auditor_run_started"
	EventAuditorRunFinished EventKind = "auditor_run_finished"
	EventFlagRaised         EventKind = "flag_raised"
	EventFlagResolved       EventKind = "flag_resolved"
	EventApprovalRequested  EventKind = "approval_requested"
	EventApprovalDecided    EventKind = "approval_decided"
	EventApprovalReset      EventKind = "approval_reset"
	EventApprovalWaived     EventKind = "approval_waived"
	EventApprovalDeleted    EventKind = "approval_deleted"
	EventThreadResolved     EventKind = "thread_resolved"
	EventThreadApplied      EventKind = "thread_applied"
	EventBranchCreated      EventKind = "branch_created"
	EventPROpened           EventKind = "pr_opened"
	EventBranchMerged       EventKind = "branch_merged"

	EventSectionUpdated  EventKind = "section_updated"
	EventChecklistToggle EventKind = "checklist_toggled"

	EventDepAdded    EventKind = "dep_added"
	EventDepRemoved  EventKind = "dep_removed"
	EventDepResolved EventKind = "dep_resolved"

	EventSubtasksReordered EventKind = "subtasks_reordered"

	EventTestCaseAdded   EventKind = "test_case_added"
	EventTestCaseUpdated EventKind = "test_case_updated"
	EventTestCaseDeleted EventKind = "test_case_deleted"
	EventTestCaseRun     EventKind = "test_case_run"

	EventQuestionAsked    EventKind = "question_asked"
	EventQuestionAnswered EventKind = "question_answered"
	EventQuestionCanceled EventKind = "question_canceled"
	EventRunAwaitingInput EventKind = "run_awaiting_input"

	EventConversationMessage EventKind = "conversation_message"

	EventCredentialBlocked EventKind = "credential_blocked" //nolint:gosec // G101: event name

	EventRunQueued EventKind = "run_queued"

	EventRunNotStarted EventKind = "run_not_started"

	EventRunQueueCanceled EventKind = "run_queue_canceled"

	EventPreviewURL EventKind = "preview_url"
)

type WorkflowEvent struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	Kind      EventKind       `json:"kind"`
	RefID     string          `json:"ref_id,omitempty"`
	Actor     string          `json:"actor,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func (s *Store) RecordEvent(ctx context.Context, taskID string, kind EventKind, refID, actor, summary string, payload any) error {
	return s.recordEvent(ctx, taskID, kind, refID, actor, summary, payload, "")
}

// The user id goes in its own column, never the payload: payloads travel in export bundles and a
// user id means nothing on another install.
func (s *Store) recordRunEvent(ctx context.Context, taskID string, kind EventKind, refID, actor, summary string,
	payload any, triggeredBy string,
) error {
	return s.recordEvent(ctx, taskID, kind, refID, actor, summary, payload, triggeredBy)
}

func (s *Store) recordEvent(ctx context.Context, taskID string, kind EventKind, refID, actor, summary string, payload any, triggeredBy string) error {
	var raw string
	switch v := payload.(type) {
	case nil:
		raw = "{}"
	case string:
		raw = v
	case []byte:
		raw = string(v)
	case json.RawMessage:
		raw = string(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	if raw == "" {
		raw = "{}"
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO workflow_events (id, task_id, kind, ref_id, actor, summary, payload, created_at, triggered_by)
		 VALUES (?,?,?,?,?,?,?,?,?)`),
		mustGenerateID(), taskID, kind, refID, actor, summary, raw, time.Now().UTC(), triggeredBy,
	)
	return err
}

func (s *Store) RunTriggeredBy(ctx context.Context, taskID, instanceID string) (string, error) {
	var who string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT triggered_by FROM workflow_events WHERE task_id = ? AND kind = ? AND ref_id = ?
		 ORDER BY created_at DESC LIMIT 1`), taskID, EventAgentRunStarted, instanceID).Scan(&who)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return who, err
}

func (s *Store) HasFinishEventForInstance(ctx context.Context, instanceID string) bool {
	return s.hasEventForInstance(ctx, EventAgentRunFinished, instanceID)
}

func (s *Store) HasMentionFinishEventForInstance(ctx context.Context, instanceID string) bool {
	return s.hasEventForInstance(ctx, EventMentionFinished, instanceID)
}

func (s *Store) HasAuditFinishEventForInstance(ctx context.Context, instanceID string) bool {
	return s.hasEventForInstance(ctx, EventAuditorRunFinished, instanceID)
}

// A caller deciding whether to remove a container must read an error as "don't know", not as "not
// started".
func (s *Store) RunStartedForInstance(ctx context.Context, taskID, instanceID string) (bool, error) {
	if instanceID == "" {
		return false, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events WHERE task_id = ? AND ref_id = ? AND kind IN (?, ?, ?)`),
		taskID, instanceID, EventAgentRunStarted, EventAuditorRunStarted, EventMentionStarted,
	).Scan(&count)
	return count > 0, err
}

func (s *Store) hasEventForInstance(ctx context.Context, kind EventKind, instanceID string) bool {
	if instanceID == "" {
		return false
	}
	var count int
	if err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events WHERE kind = ? AND ref_id = ?`),
		kind, instanceID,
	).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func (s *Store) EventPayloadForInstance(ctx context.Context, kind EventKind, instanceID string) (json.RawMessage, bool) {
	if instanceID == "" {
		return nil, false
	}
	var payload string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT payload FROM workflow_events WHERE kind = ? AND ref_id = ? ORDER BY created_at ASC LIMIT 1`),
		kind, instanceID,
	).Scan(&payload)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(payload), true
}

// Runs recorded before launch_kind existed have no kind and read as stage runs.
func (s *Store) RunLaunchKind(ctx context.Context, instanceID string) LaunchKind {
	raw, ok := s.EventPayloadForInstance(ctx, EventAgentRunStarted, instanceID)
	if !ok {
		return LaunchStage
	}
	var p struct {
		LaunchKind string `json:"launch_kind"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return LaunchStage
	}
	return ParseLaunchKind(p.LaunchKind)
}

func (s *Store) CountRunsForStage(ctx context.Context, taskID string, stage Stage) int {
	var count int
	stageExpr := `(payload::jsonb ->> 'stage')` //i18n:ignore SQL expression
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events
		 WHERE task_id = ? AND kind = ?
		   AND `+stageExpr+` = ?`),
		taskID, EventAgentRunStarted, string(stage),
	).Scan(&count), "task.log.count_stage_runs", "task", taskID, "stage", stage)
	return count
}

func (s *Store) ListEvents(ctx context.Context, taskID string) ([]WorkflowEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, task_id, kind, ref_id, actor, summary, payload, created_at
		 FROM workflow_events WHERE task_id = ? ORDER BY created_at ASC`), taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []WorkflowEvent{}
	for rows.Next() {
		var e WorkflowEvent
		var payload string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Kind, &e.RefID, &e.Actor, &e.Summary, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		if payload != "" && payload != "{}" {
			e.Payload = json.RawMessage(payload)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
