package automation

import (
	"context"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Payload field names reach recipes as trigger.payload and are part of the contract; publishers
// must emit exactly these.
const (
	EventPRReviewComment = "github.pull_request_review_comment"
	EventPRReview        = "github.pull_request_review"
	EventIssueComment    = "github.issue_comment"
	EventPullRequest     = "github.pull_request"
	EventPush            = "github.push"
	EventCheckSuite      = "github.check_suite"

	EventTaskCreated      = "task.created"
	EventTaskStageChanged = "task.stage_changed"
	EventTaskFailed       = "task.failed"
)

var knownEvents = map[string]string{
	EventPRReviewComment:  "automation.events.pr_review_comment",
	EventPRReview:         "automation.events.pr_review",
	EventIssueComment:     "automation.events.issue_comment",
	EventPullRequest:      "automation.events.pull_request",
	EventPush:             "automation.events.push",
	EventCheckSuite:       "automation.events.check_suite",
	EventTaskCreated:      "automation.events.task_created",
	EventTaskStageChanged: "automation.events.task_stage_changed",
	EventTaskFailed:       "automation.events.task_failed",
}

// Must match the keys built by api.automationPayload. task_id, task_title and task_stage arrive
// only when a task owns the PR.
var githubPayloadFields = []string{
	"event", "action", "repo", "sender", "sender_is_bot",
	"pr_url", "pr_number", "pr_title", "pr_draft", "branch",
	"comment", "comment_url", "comment_by", "review_state", "review_body",
	"issue_number", "issue_title", "ref",
	"task_id", "task_title", "task_stage",
}

// Must match the keys built by api.publishTaskAutomationEvent.
var taskPayloadFields = []string{
	"task_id", "title", "stage", "board_id", "status", "tags",
	"source", "parent_id", PayloadAutomationID, PayloadChainDepth,
}

// Must match the keys added by Debounced.TriggerPayload.
var debouncePayloadFields = []string{"events", "event_count"}

var payloadFields = map[string][]string{
	EventPRReviewComment:  githubPayloadFields,
	EventPRReview:         githubPayloadFields,
	EventIssueComment:     githubPayloadFields,
	EventPullRequest:      githubPayloadFields,
	EventPush:             githubPayloadFields,
	EventCheckSuite:       githubPayloadFields,
	EventTaskCreated:      taskPayloadFields,
	EventTaskStageChanged: append([]string{"from", "to"}, taskPayloadFields...),
	EventTaskFailed:       taskPayloadFields,
}

func CheckFilterFields(t *Trigger) error {
	if t.Event == "" || t.Filter == "" {
		return nil
	}
	fields, ok := payloadFields[t.Event]
	if !ok {
		return nil
	}
	expr, err := Compile(t.Filter)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, f := range append(append([]string{}, fields...), debouncePayloadFields...) {
		known[f] = true
	}
	for _, p := range expr.Paths() {
		if len(p) < 3 || p[0] != "trigger" || p[1] != "payload" || known[p[2]] {
			continue
		}
		list := append([]string{}, fields...)
		sort.Strings(list)
		return apperr.Invalid("automation.errors.filter_unknown_field",
			"field", p[2], "event", t.Event, "known", strings.Join(list, ", "))
	}
	return nil
}

func KnownEvent(name string) bool { _, ok := knownEvents[name]; return ok }

func KnownEvents() []string {
	out := make([]string, 0, len(knownEvents))
	for k := range knownEvents {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func EventDescriptionsFor(ctx context.Context) map[string]string {
	out := make(map[string]string, len(knownEvents))
	for k, key := range knownEvents {
		out[k] = i18n.TCtx(ctx, key)
	}
	return out
}

type Event struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Kind        string         `json:"kind"`
	Payload     map[string]any `json:"payload"`
	DedupeKey   string         `json:"dedupe_key,omitempty"`
}

const (
	PayloadAutomationID = "automation_id"
	PayloadChainDepth   = "automation_depth"
)

const MaxChainDepth = 3

// A payload decoded from JSON carries the depth as float64, one built in Go as int.
func ChainDepthOf(payload map[string]any) int {
	switch v := payload[PayloadChainDepth].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}
