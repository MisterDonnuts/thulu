package api

import (
	"context"
	"log"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Log, never return the error: a rejected webhook delivery makes GitHub disable the hook.
func (s *Server) publishAutomationEvent(ctx context.Context, kind, workspaceID, dedupeKey string, payload map[string]any) {
	pub := s.AutomationPublisher()
	if pub == nil {
		return
	}
	if err := pub.Publish(ctx, automation.Event{
		WorkspaceID: workspaceID,
		Kind:        kind,
		DedupeKey:   dedupeKey,
		Payload:     payload,
	}); err != nil {
		log.Printf("[automation] %s", i18n.Log("api.automations.log.publish_failed", "kind", kind, "err", err))
	}
}

type githubEventPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Draft   bool   `json:"draft"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Issue struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		PR      *struct {
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	Review struct {
		State string `json:"state"`
		Body  string `json:"body"`
		User  struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
	} `json:"review"`
	Sender struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"sender"`
	Ref string `json:"ref"`
}

// Attribute by installation only: a repository can be linked from several workspaces and a PR URL
// can be written on any task, so either would leak one tenant's events to another.
func (s *Server) githubEventWorkspace(ctx context.Context, ev githubEventPayload) string {
	if ev.Installation.ID == 0 || s.gh.store == nil {
		return ""
	}
	inst, err := s.gh.store.GetInstallation(ctx, ev.Installation.ID)
	if err != nil {
		return ""
	}
	if inst.WorkspaceID == "" && !githubapp.InstallerProofRequired() {
		// Self-hosted, unclaimed install: with one workspace nobody else can be reached, so events
		// keep flowing; with several, or in SaaS mode, they wait for a claim.
		return s.soleWorkspaceID(ctx)
	}
	return inst.WorkspaceID
}

// Payload keys are read by stored recipes (trigger.payload.action == "created"); renaming one
// breaks them.
func (s *Server) automationPayload(ctx context.Context, event, workspaceID string, ev githubEventPayload) (payload map[string]any, dedupeKey string) {
	prURL := ev.PullRequest.HTMLURL
	if prURL == "" && ev.Issue.PR != nil {
		prURL = ev.Issue.PR.HTMLURL
	}
	commenter := ev.Comment.User
	if commenter.Login == "" {
		commenter = ev.Review.User
	}
	if commenter.Login == "" {
		commenter = ev.Sender
	}

	payload = map[string]any{
		"event":  event,
		"action": ev.Action,
		"repo":   ev.Repository.FullName,
		"sender": ev.Sender.Login,
		// A recipe that wakes an agent on every comment would otherwise answer itself forever.
		//i18n:ignore GitHub's account type value, not a message
		"sender_is_bot": strings.EqualFold(ev.Sender.Type, "Bot") || strings.HasSuffix(ev.Sender.Login, "[bot]"),
		"pr_url":        prURL,
		"pr_number":     ev.PullRequest.Number,
		"pr_title":      ev.PullRequest.Title,
		"pr_draft":      ev.PullRequest.Draft,
		"branch":        ev.PullRequest.Head.Ref,
		"comment":       ev.Comment.Body,
		"comment_url":   ev.Comment.HTMLURL,
		"comment_by":    commenter.Login,
		"review_state":  ev.Review.State,
		"review_body":   ev.Review.Body,
		"issue_number":  ev.Issue.Number,
		"issue_title":   ev.Issue.Title,
		"ref":           ev.Ref,
	}
	if prURL != "" {
		if t := s.taskForPR(ctx, workspaceID, prURL); t != nil {
			payload["task_id"] = t.ID
			payload["task_title"] = t.Title
			payload["task_stage"] = string(t.Stage)
		}
		dedupeKey = prURL
	}
	if dedupeKey == "" {
		dedupeKey = ev.Repository.FullName + ":" + event
	}
	return payload, dedupeKey
}

// Only workspaceID's tasks: pr_urls is written by agents, so a URL on another tenant's card proves
// nothing.
func (s *Server) taskForPR(ctx context.Context, workspaceID, prURL string) *task.Task {
	if workspaceID == "" {
		return nil
	}
	all, err := s.taskStore.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil
	}
	for i := range all {
		for _, url := range all[i].PRURLs {
			if url == prURL {
				return &all[i]
			}
		}
	}
	return nil
}

func githubEventKind(event string) string {
	switch event {
	case "pull_request_review_comment":
		return automation.EventPRReviewComment
	case "pull_request_review":
		return automation.EventPRReview
	case "issue_comment":
		return automation.EventIssueComment
	case "pull_request":
		return automation.EventPullRequest
	case "push":
		return automation.EventPush
	case "check_suite":
		return automation.EventCheckSuite
	}
	return ""
}

func (s *Server) publishTaskAutomationEvent(ctx context.Context, kind string, t *task.Task, extra map[string]any) {
	if t == nil {
		return
	}
	payload := map[string]any{
		"task_id":  t.ID,
		"title":    t.Title,
		"stage":    string(t.Stage),
		"board_id": t.BoardID,
		"status":   string(t.Status),
		"tags":     toAnyList(t.Tags),
	}
	for k, v := range extra {
		payload[k] = v
	}
	s.publishAutomationEvent(ctx, kind, t.WorkspaceID, t.ID+":"+kind, payload)
}

func toAnyList(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
