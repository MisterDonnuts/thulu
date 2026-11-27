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

	// The stage's own agent judged its run (verdict.md); the auditor's judgement is
	// EventAuditorVerdict.
	EventStageVerdict EventKind = "stage_verdict"
	// A send-back the board's max_kick_backs refused; the task stayed blocked where it was.
	EventKickBackLimit EventKind = "kick_back_limit"
	// A person relaunched a blocked task: send-backs before it no longer count against the cap.
	EventKickBackReset EventKind = "kick_back_reset"
	// Someone asked to run the task's stage again ({stage, turns, comment_id}); comment_id is the
	// decision that carries the reason, absent when none was given.
	EventRelaunchRequested EventKind = "relaunch_requested"
	// flags.yaml set what the change touches (surface → flow_flags), its risk and size.
	EventFlowFlagsSet EventKind = "flow_flags_set"
	// Auto-advance passed over a stage whose skip_unless the task's flow flags do not meet.
	EventStageSkipped EventKind = "stage_skipped"
	// A task now skips a column ({stage, reason, by, source: "flags_yaml"|"api"}).
	EventStageSkipAdded EventKind = "stage_skip_added"
	// A task no longer skips a column ({stage, reason, by, skipped_by, cause, passed, deferred}).
	// cause is one of the SkipCause values; deferred marks a move back that waits for the end of
	// the run that asked for it.
	EventStageSkipRemoved EventKind = "stage_skip_removed"
	// Why the task is blocked (BlockInfo); the card's label reads the latest one.
	EventTaskBlocked EventKind = "task_blocked"
	// A stage with release_resources freed the task's sandbox and worktrees.
	EventResourcesReleased EventKind = "resources_released"
	// A lock_tests stage recorded the blobs of the test files it wrote.
	EventTestsLocked EventKind = "tests_locked"
	// The pull request left draft for review.
	EventPRReady              EventKind = "pr_ready"
	EventLockedTestsUnchecked EventKind = "locked_tests_unchecked"
	// A run finished after the task left its stage or moved on to another run: its work stayed,
	// its result (auditor, verdict, advance, status) did not apply.
	EventRunOutlived EventKind = "run_outlived"
	// A stage's agent waits for another stage's run still working on the task (ref_id: that run).
	EventStageLaunchDeferred EventKind = "stage_launch_deferred"
	// Something a runner reported about a run that a person should see on its card ({run_id,
	// message}); an outputs upload that lost files also carries outputs and instance_id.
	EventRunnerWarning EventKind = "runner_warning"
	// An acceptance criterion changed after the tests were locked ({keys, changed, added, removed,
	// cases}); cases are the ones that cover a key that changed.
	EventCriteriaChanged EventKind = "criteria_changed"
	// A person gave the agent access to blocked cases ({cases, secrets, environment_id,
	// comment_id}): secret names only, never values.
	EventTestCasesAccessGiven EventKind = "test_cases_access_given"
	// The agent's auditor is in signals mode and the finished run showed none, so the stage went on
	// without it ({stage, agent, instance_id, checks}); checks lists every check done (AuditCheck).
	EventAuditorSkipped EventKind = "auditor_skipped"
)

// Values of a stage_entered payload's "via". A manual move also carries by_person.
const (
	ViaKickBack         = "kick_back"
	ViaChangesRequested = "changes_requested"
	ViaVerdict          = "verdict"
	ViaManual           = "manual"
	ViaMerge            = "merge"
	ViaAutomation       = "automation"
	ViaReleaseBatch     = "release_batch"
	// The run of the stage ended and the board moved the task on by itself.
	ViaAutoAdvance = "auto_advance"
	// Someone took away the skip of a column the task had passed, so it went back there. Not a
	// send-back: CountKickBacks leaves it out and max_kick_backs never refuses it.
	ViaSkipRemoved = "skip_removed"
)

type WorkflowEvent struct {
	ID      string          `json:"id"`
	TaskID  string          `json:"task_id"`
	Kind    EventKind       `json:"kind"`
	RefID   string          `json:"ref_id,omitempty"`
	Actor   string          `json:"actor,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// The task's round when the event was recorded; 0 on events from before rounds were stamped.
	Round     int       `json:"round,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
	var round int
	if kind == EventStageEntered {
		raw, round = s.stampRound(ctx, taskID, raw)
	} else {
		round = s.TaskRound(ctx, taskID)
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO workflow_events (id, task_id, kind, ref_id, actor, summary, payload, round, created_at, triggered_by)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`),
		mustGenerateID(), taskID, kind, refID, actor, summary, raw, round, time.Now().UTC(), triggeredBy,
	)
	if err == nil && kind == EventStageEntered && round > 0 {
		s.setRound(ctx, taskID, round)
	}
	if err == nil && kind == EventStageEntered && leftStage(raw) {
		// A stop waits for a person in the stage it was raised in; once the task leaves that
		// column, whoever moved it decided.
		s.ResolveStopThreads(ctx, taskID, "octopus")
		// Access was given for the stage it left: a case still queued will not run there.
		s.ReleaseQueuedTestCases(ctx, taskID, "octopus")
	}
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

func (s *Store) hasTaskEvent(ctx context.Context, taskID string, kind EventKind, refID string) bool {
	var count int
	if err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events WHERE task_id = ? AND kind = ? AND ref_id = ?`),
		taskID, kind, refID,
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

// RunTaskStage is the task's stage when the run was launched, or "" for runs recorded before the
// run-started event carried it.
func (s *Store) RunTaskStage(ctx context.Context, instanceID string) Stage {
	raw, ok := s.EventPayloadForInstance(ctx, EventAgentRunStarted, instanceID)
	if !ok {
		return ""
	}
	var p struct {
		TaskStage string `json:"task_stage"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	return Stage(p.TaskStage)
}

// RelaunchedWithReason reports whether a person's relaunch with a reason started the run: the
// latest relaunch of the stage carries a decision (comment_id), and no other run of the stage
// finished after it. The relaunch is recorded once the run has launched, so it comes after the
// run's own start and before its end.
func (s *Store) RelaunchedWithReason(ctx context.Context, taskID string, stage Stage, instanceID string) bool {
	var kind, payload string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT kind, payload FROM workflow_events
		 WHERE task_id = ? AND (payload::jsonb ->> 'stage') = ?
		   AND (kind = ? OR (kind = ? AND ref_id <> ?))
		 ORDER BY created_at DESC LIMIT 1`),
		taskID, string(stage), EventRelaunchRequested, EventAgentRunFinished, instanceID,
	).Scan(&kind, &payload)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logx.Warn(err, "task.log.read_relaunch", "task", taskID)
		}
		return false
	}
	if EventKind(kind) != EventRelaunchRequested {
		return false
	}
	var p struct {
		CommentID string `json:"comment_id"`
	}
	return json.Unmarshal([]byte(payload), &p) == nil && p.CommentID != ""
}

// When the send-back count last went to zero: a person moving the task by hand (by_person) or
// relaunching it out of blocked (EventKickBackReset). Takes task id, EventKickBackReset and
// EventStageEntered.
const kickBackResetSQL = `COALESCE((
     SELECT max(created_at) FROM workflow_events
     WHERE task_id = ? AND (kind = ? OR (kind = ? AND (payload::jsonb ->> 'by_person') = 'true'))
   ), '-infinity'::timestamptz)`

// Send-backs since the last reset. A move an agent makes through the API never resets.
func (s *Store) CountKickBacks(ctx context.Context, taskID string) int {
	var count int
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events
		 WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'via') IN (?, ?, ?)
		   AND created_at > `+kickBackResetSQL),
		taskID, EventStageEntered, ViaKickBack, ViaChangesRequested, ViaVerdict,
		taskID, EventKickBackReset, EventStageEntered,
	).Scan(&count), "task.log.count_kick_backs", "task", taskID)
	return count
}

// kick_back verdicts stage's agent gave with reason since the same reset as CountKickBacks,
// including one refused by the cap.
func (s *Store) CountVerdictKickBacks(ctx context.Context, taskID string, stage Stage, reason string) int {
	var count int
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events
		 WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'verdict') = ?
		   AND (payload::jsonb ->> 'stage') = ? AND (payload::jsonb ->> 'reason') = ?
		   AND created_at > `+kickBackResetSQL),
		taskID, EventStageVerdict, verdictKickBack, string(stage), reason,
		taskID, EventKickBackReset, EventStageEntered,
	).Scan(&count), "task.log.count_verdict_kick_backs", "task", taskID)
	return count
}

// A stage_launch_deferred for stage since the task last entered it, with no run of stage started
// after it: the stage's agent still waits for the run a move left on the task.
func (s *Store) DeferredLaunchPending(ctx context.Context, taskID string, stage Stage) bool {
	var pending bool
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT EXISTS (
		   SELECT 1 FROM workflow_events d
		   WHERE d.task_id = ? AND d.kind = ? AND (d.payload::jsonb ->> 'stage') = ?
		     AND d.created_at > COALESCE((
		       SELECT max(created_at) FROM workflow_events
		       WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'to') = ?
		     ), '-infinity'::timestamptz)
		     AND NOT EXISTS (
		       SELECT 1 FROM workflow_events r
		       WHERE r.task_id = d.task_id AND r.kind = ? AND (r.payload::jsonb ->> 'stage') = ?
		         AND r.created_at > d.created_at
		     )
		 )`),
		taskID, EventStageLaunchDeferred, string(stage),
		taskID, EventStageEntered, string(stage),
		EventAgentRunStarted, string(stage),
	).Scan(&pending), "task.log.deferred_launch_pending", "task", taskID, "stage", stage)
	return pending
}

// Runs of stage since the task last entered it, so a task that comes back to a stage gets its
// relaunches again. Tasks with no stage_entered for it (created there) count every run.
func (s *Store) CountRunsSinceStageEntry(ctx context.Context, taskID string, stage Stage) int {
	var count int
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM workflow_events
		 WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'stage') = ?
		   AND created_at > COALESCE((
		     SELECT max(created_at) FROM workflow_events
		     WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'to') = ?
		   ), '-infinity'::timestamptz)`),
		taskID, EventAgentRunStarted, string(stage),
		taskID, EventStageEntered, string(stage),
	).Scan(&count), "task.log.count_stage_runs", "task", taskID, "stage", stage)
	return count
}

// Whether the runner reported that instanceID's outputs did not all reach the daemon.
func (s *Store) OutputsUndelivered(ctx context.Context, taskID, instanceID string) bool {
	if instanceID == "" {
		return false
	}
	var found bool
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT EXISTS (SELECT 1 FROM workflow_events
		   WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'instance_id') = ?
		     AND (payload::jsonb ->> 'outputs') IN (?, ?))`),
		taskID, EventRunnerWarning, instanceID, OutputsPartial, OutputsFailed,
	).Scan(&found), "task.log.outputs_undelivered", "task", taskID)
	return found
}

func (s *Store) ListEvents(ctx context.Context, taskID string) ([]WorkflowEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, task_id, kind, ref_id, actor, summary, payload, round, created_at
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
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Kind, &e.RefID, &e.Actor, &e.Summary, &payload, &e.Round, &e.CreatedAt); err != nil {
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
