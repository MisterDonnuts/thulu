package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// CommentKind says what a comment is for, so the task view sorts conversation from the flow's own
// trail without reading the text. Rows written before the column existed read as CommentPlain.
type CommentKind string

const (
	// Free conversation between people and agents (the default).
	CommentPlain CommentKind = "comment"
	// An agent's task_ask question; the answer arrives as a reply.
	CommentQuestion CommentKind = "question"
	// What a stage's agent needs from a person when its verdict blocked the task. Opens a thread
	// the person's decision answers; it resolves itself when the task leaves the stage.
	CommentBlock CommentKind = "block"
	// A person's action with its reason (move, send back, relaunch, waive, give access, decide an
	// approval). Meta names the action.
	CommentDecision CommentKind = "decision"
	// A pending approval; its decision is the reply.
	CommentApproval CommentKind = "approval"
	// Thulu's note that agents stopped answering each other in a thread until a person replies.
	CommentPause CommentKind = "pause"
)

var commentKinds = map[CommentKind]bool{
	CommentPlain: true, CommentQuestion: true, CommentBlock: true, CommentDecision: true,
	CommentApproval: true, CommentPause: true,
}

func ValidCommentKind(k CommentKind) bool { return commentKinds[k] }

// Decision actions (CommentMeta.Action).
const (
	DecisionMove           = "move"
	DecisionSendBack       = "send_back"
	DecisionRelaunch       = "relaunch"
	DecisionWaive          = "waive"
	DecisionAccess         = "access"
	DecisionApprove        = "approve"
	DecisionRequestChanges = "request_changes"
	DecisionWaiveApproval  = "waive_approval"
)

// CommentMeta is the structured part of a typed comment. Every field is optional; which ones a
// kind fills is documented on the kind.
type CommentMeta struct {
	// decision: what the person did (Decision* constants).
	Action    string `json:"action,omitempty"`
	FromStage Stage  `json:"from_stage,omitempty"`
	ToStage   Stage  `json:"to_stage,omitempty"`
	// decision: the test cases a waive or an access grant covered.
	Cases []string `json:"cases,omitempty"`
	// decision: the names of the secrets an access grant stored (never their values).
	Secrets []string `json:"secrets,omitempty"`
	// block: the stage_verdict report that holds the agent's full report, and the run it closed.
	ReportID string `json:"report_id,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	// approval, and the decision that answers it.
	ApprovalID string `json:"approval_id,omitempty"`
	// pause: the replies agents gave each other before Thulu stopped them.
	Turns int `json:"turns,omitempty"`
}

func (m *CommentMeta) empty() bool {
	return m == nil || (m.Action == "" && m.FromStage == "" && m.ToStage == "" && len(m.Cases) == 0 &&
		len(m.Secrets) == 0 && m.ReportID == "" && m.RunID == "" && m.ApprovalID == "" && m.Turns == 0)
}

func encodeMeta(m *CommentMeta) string {
	if m.empty() {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeMeta(s string) *CommentMeta {
	if s == "" {
		return nil
	}
	var m CommentMeta
	if json.Unmarshal([]byte(s), &m) != nil || m.empty() {
		return nil
	}
	return &m
}

func commentKindOr(k CommentKind) CommentKind {
	if k == "" {
		return CommentPlain
	}
	return k
}

// Shares the task store's single sequence with Migrations.
var commentKindMigration = migrate.Migration{ID: 53, SQL: `ALTER TABLE task_comments
			ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'comment',
			ADD COLUMN IF NOT EXISTS meta TEXT NOT NULL DEFAULT ''`}

// NewComment is a typed comment the daemon posts. ThreadID, when set, makes it a reply in that
// thread (a reply to a reply joins the root); a thread that no longer exists makes it a root.
type NewComment struct {
	TaskID   string
	ThreadID string
	Source   Source
	Author   string
	Body     string
	Note     *Note
	Kind     CommentKind
	Meta     *CommentMeta
}

// PostComment stores a typed comment. A person's reply reopens a resolved thread, as AddReply does.
func (s *Store) PostComment(ctx context.Context, in NewComment) (*Comment, error) {
	if !ValidCommentKind(commentKindOr(in.Kind)) {
		return nil, apperr.Invalid("task.errors.comment_kind", "kind", in.Kind)
	}
	c := &Comment{TaskID: in.TaskID, Source: in.Source, Author: in.Author, Body: in.Body,
		Kind: commentKindOr(in.Kind), Meta: in.Meta}
	if in.Note != nil {
		c.Body, c.Key, c.BodyI18n = in.Note.Text(), in.Note.Key, in.Note.Texts()
	}
	if in.ThreadID != "" {
		root, err := s.ThreadRoot(ctx, in.TaskID, in.ThreadID)
		switch {
		case err == nil:
			c.ParentID = root
		case !errors.Is(err, ErrNoSuchThread):
			return nil, err
		}
	}
	out, err := s.insertComment(ctx, c)
	// A decision answers a stop or an approval; it never reopens one the action itself settled (a
	// move, a waive that cleared the blocked cases).
	if err == nil && c.ParentID != "" && in.Source == SourceUser && c.Kind != CommentDecision {
		s.reopenThread(ctx, in.TaskID, c.ParentID)
	}
	return out, err
}

func (s *Store) reopenThread(ctx context.Context, taskID, root string) {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_comments SET resolved_at = NULL, resolved_by = '' WHERE id = ? AND resolved_at IS NOT NULL`), root)
	logx.Warn(err, "task.log.reopen_thread", "thread", root, "task", taskID)
}

// OpenStopThread is the newest block thread of the task nobody resolved yet; "" when none.
func (s *Store) OpenStopThread(ctx context.Context, taskID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id FROM task_comments
		  WHERE task_id = ? AND kind = ? AND parent_id = '' AND resolved_at IS NULL
		  ORDER BY created_at DESC, id DESC LIMIT 1`), taskID, string(CommentBlock)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", apperr.Wrap(err, "task.errors.open_stop_thread", "task", taskID)
	}
	return id, nil
}

// OpenStopThreadOf is OpenStopThread over comments already read (oldest first).
func OpenStopThreadOf(comments []Comment) string {
	for i := len(comments) - 1; i >= 0; i-- {
		c := &comments[i]
		if c.Kind == CommentBlock && c.ParentID == "" && c.ResolvedAt == nil {
			return c.ID
		}
	}
	return ""
}

// ApprovalThreadOf is the thread an approval opened, over comments already read; "" when none.
func ApprovalThreadOf(comments []Comment, approvalID string) string {
	for i := range comments {
		c := &comments[i]
		if c.Kind == CommentApproval && c.ParentID == "" && c.Meta != nil && c.Meta.ApprovalID == approvalID {
			return c.ID
		}
	}
	return ""
}

// ResolveStopThreads resolves every open block thread of the task, as by.
// It returns how many it resolved.
func (s *Store) ResolveStopThreads(ctx context.Context, taskID, by string) int {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_comments SET resolved_at = ?, resolved_by = ?
		  WHERE task_id = ? AND kind = ? AND parent_id = '' AND resolved_at IS NULL`),
		time.Now().UTC(), by, taskID, string(CommentBlock))
	if err != nil {
		logx.Warn(err, "task.log.resolve_stop_threads", "task", taskID)
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.notifyTask(ctx, taskID)
	}
	return int(n)
}

// resolveStopsWhenCasesClear resolves the open stop threads of the task's current stage once a
// write left none of the task's test cases blocked. Only a stop raised in the stage the task is in
// counts: a stop of another stage is not about these cases.
func (s *Store) resolveStopsWhenCasesClear(ctx context.Context, taskID, by string) {
	var blocked int
	if err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM test_cases WHERE task_id = ? AND status = ?`), taskID, TestCaseBlocked).Scan(&blocked); err != nil {
		logx.Warn(err, "task.log.resolve_stop_threads", "task", taskID)
		return
	}
	if blocked > 0 {
		return
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_comments SET resolved_at = ?, resolved_by = ?
		  WHERE task_id = ? AND kind = ? AND parent_id = '' AND resolved_at IS NULL
		    AND stage = (SELECT stage FROM tasks WHERE id = ?)`),
		time.Now().UTC(), by, taskID, string(CommentBlock), taskID)
	if err != nil {
		logx.Warn(err, "task.log.resolve_stop_threads", "task", taskID)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.notifyTask(ctx, taskID)
	}
}

// What a person reads in a stop: the agent's ask, else the first sentence of its verdict body.
// At most maxStopAskRunes; a longer ask keeps its first sentence and cut reports it.
const maxStopAskRunes = 400

func stopAsk(ask, body string) (text string, cut bool) {
	ask = strings.Join(strings.Fields(ask), " ")
	if ask != "" {
		if utf8.RuneCountInString(ask) <= maxStopAskRunes {
			return ask, false
		}
		return cutRunes(firstSentence(ask), maxStopAskRunes), true
	}
	return cutRunes(firstSentence(firstBodyParagraph(body)), maxStopAskRunes), false
}

// firstBodyParagraph is the body's first paragraph of prose: headings are skipped (a title is not
// what the agent asks) and quote and list marks dropped.
func firstBodyParagraph(body string) string {
	var lines []string
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			if len(lines) > 0 {
				break
			}
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, ">*- "))
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " ")
}

// firstSentence cuts s after the first ".", "!" or "?" followed by a space (or the end).
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for i, r := range s {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		next := i + 1
		if next >= len(s) || s[next] == ' ' {
			return s[:next]
		}
	}
	return s
}

// leftStage reports whether a stage_entered payload moves the task to another column.
func leftStage(raw string) bool {
	var p map[string]any
	if json.Unmarshal([]byte(raw), &p) != nil {
		return false
	}
	from, to := payloadString(p, "from"), payloadString(p, "to")
	return to != "" && from != to
}
