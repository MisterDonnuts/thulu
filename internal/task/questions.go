package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

const (
	QuestionKindQuestion = "question"
	QuestionKindBlocker  = "blocker"
)

const (
	QuestionOpen     = "open"
	QuestionAnswered = "answered"
	QuestionCanceled = "canceled"
)

const (
	QuestionDeliverySteered = "steered"
	QuestionDeliveryResumed = "resumed"
	QuestionDeliveryThread  = "thread"
)

// The octopus-api MCP checks only the shape (labels, defaults); these length limits are enforced
// nowhere else.
const (
	maxQuestionsPerAsk      = 4
	maxQuestionOptions      = 6
	maxQuestionHeaderRunes  = 12
	maxQuestionContextRunes = 4000
	maxQuestionTextRunes    = 1000
	maxOptionLabelRunes     = 80
	maxOptionDescRunes      = 300
	maxQuestionDefaultRunes = 1000
	maxAnswerTextRunes      = 4000

	MaxQuestionsPerRun      = 3
	MaxOpenQuestionsPerTask = 5
)

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type QuestionItem struct {
	Header      string           `json:"header"`
	Question    string           `json:"question"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select"`
	AllowOther  bool             `json:"allow_other"`
}

type QuestionPayload struct {
	Context         string         `json:"context"`
	Questions       []QuestionItem `json:"questions"`
	Default         string         `json:"default"`
	NeedsCredential bool           `json:"needs_credential,omitempty"`
}

type QuestionAnswer struct {
	QuestionIndex int      `json:"question_index"`
	Selected      []string `json:"selected"`
	Other         string   `json:"other"`
}

type QuestionAnswers struct {
	Answers []QuestionAnswer `json:"answers"`
	Note    string           `json:"note"`
}

type Question struct {
	ID         string           `json:"id"`
	TaskID     string           `json:"task_id"`
	CommentID  string           `json:"comment_id"`
	RunID      string           `json:"run_id"`
	Agent      string           `json:"agent"`
	Stage      Stage            `json:"stage"`
	SessionID  string           `json:"-"`
	Kind       string           `json:"kind"`
	Blocking   bool             `json:"blocking"`
	Payload    QuestionPayload  `json:"payload"`
	Status     string           `json:"status"`
	Answers    *QuestionAnswers `json:"answers"`
	AnsweredBy string           `json:"answered_by"`
	AnsweredAt *time.Time       `json:"answered_at"`
	Delivery   string           `json:"delivery"`
	CreatedAt  time.Time        `json:"created_at"`
}

type AskInput struct {
	Kind            string         `json:"kind"`
	Blocking        *bool          `json:"blocking"`
	Context         string         `json:"context"`
	Questions       []AskInputItem `json:"questions"`
	Default         string         `json:"default"`
	NeedsCredential bool           `json:"needs_credential"`
}

type AskInputItem struct {
	Header      string           `json:"header"`
	Question    string           `json:"question"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select"`
	AllowOther  *bool            `json:"allow_other"`
}

// Resolved by the daemon from the task and run key; never take it from the request body.
type AskTarget struct {
	RunID     string
	Agent     string
	Stage     Stage
	SessionID string
}

// Migration IDs 41–42 belong to questions; approvals use 43 onwards.
var questionMigrations = []migrate.Migration{
	{ID: 41, SQL: `CREATE TABLE IF NOT EXISTS task_questions (
			id          TEXT PRIMARY KEY,
			task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			comment_id  TEXT NOT NULL DEFAULT '',
			run_id      TEXT NOT NULL DEFAULT '',
			agent       TEXT NOT NULL DEFAULT '',
			stage       TEXT NOT NULL DEFAULT '',
			session_id  TEXT NOT NULL DEFAULT '',
			kind        TEXT NOT NULL,
			blocking    BOOLEAN NOT NULL DEFAULT TRUE,
			payload     JSONB NOT NULL,
			status      TEXT NOT NULL DEFAULT 'open',
			answers     JSONB,
			answered_by TEXT NOT NULL DEFAULT '',
			answered_at TIMESTAMPTZ,
			delivery    TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL
		)`},
	{ID: 42, SQL: `CREATE INDEX IF NOT EXISTS idx_task_questions_task ON task_questions(task_id, created_at)`},
}

func NormalizeAsk(in AskInput) (kind string, blocking bool, p QuestionPayload, err error) {
	kind = strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = QuestionKindQuestion
	}
	if kind != QuestionKindQuestion && kind != QuestionKindBlocker {
		return "", false, p, apperr.Invalid("task.questions.errors.bad_kind", "kind", kind)
	}
	blocking = in.Blocking == nil || *in.Blocking
	if kind == QuestionKindBlocker {
		blocking = true
	}
	p.Context = strings.TrimSpace(in.Context)
	p.Default = strings.TrimSpace(in.Default)
	p.NeedsCredential = in.NeedsCredential
	if err := checkLen("context", p.Context, maxQuestionContextRunes); err != nil {
		return "", false, p, err
	}
	if err := checkLen("default", p.Default, maxQuestionDefaultRunes); err != nil {
		return "", false, p, err
	}
	if !blocking && p.Default == "" {
		return "", false, p, apperr.Invalid("task.questions.errors.default_required")
	}
	if n := len(in.Questions); n == 0 || n > maxQuestionsPerAsk {
		return "", false, p, apperr.Invalid("task.questions.errors.question_count", "max", maxQuestionsPerAsk)
	}
	for i, q := range in.Questions {
		item, err := normalizeItem(i, q)
		if err != nil {
			return "", false, p, err
		}
		p.Questions = append(p.Questions, item)
	}
	return kind, blocking, p, nil
}

func normalizeItem(i int, q AskInputItem) (QuestionItem, error) {
	item := QuestionItem{
		Header:      strings.TrimSpace(q.Header),
		Question:    strings.TrimSpace(q.Question),
		MultiSelect: q.MultiSelect,
		AllowOther:  q.AllowOther == nil || *q.AllowOther,
		Options:     []QuestionOption{},
	}
	if item.Question == "" {
		return item, apperr.Invalid("task.questions.errors.question_required", "index", i)
	}
	if utf8.RuneCountInString(item.Header) > maxQuestionHeaderRunes {
		return item, apperr.Invalid("task.questions.errors.header_too_long", "index", i, "max", maxQuestionHeaderRunes)
	}
	if err := checkLen("question", item.Question, maxQuestionTextRunes); err != nil {
		return item, err
	}
	if len(q.Options) > maxQuestionOptions {
		return item, apperr.Invalid("task.questions.errors.too_many_options", "index", i, "max", maxQuestionOptions)
	}
	for _, o := range q.Options {
		opt := QuestionOption{Label: strings.TrimSpace(o.Label), Description: strings.TrimSpace(o.Description)}
		if opt.Label == "" {
			return item, apperr.Invalid("task.questions.errors.option_label_required", "index", i)
		}
		if err := checkLen("label", opt.Label, maxOptionLabelRunes); err != nil {
			return item, err
		}
		if err := checkLen("description", opt.Description, maxOptionDescRunes); err != nil {
			return item, err
		}
		if slices.ContainsFunc(item.Options, func(x QuestionOption) bool { return x.Label == opt.Label }) {
			return item, apperr.Invalid("task.questions.errors.option_label_duplicate", "index", i, "label", opt.Label)
		}
		item.Options = append(item.Options, opt)
	}
	if len(item.Options) == 0 {
		item.AllowOther = true
	}
	return item, nil
}

func checkLen(field, s string, limit int) error {
	if utf8.RuneCountInString(s) > limit {
		return apperr.Invalid("task.questions.errors.too_long", "field", field, "max", limit)
	}
	return nil
}

func normalizeAnswers(q *Question, in QuestionAnswers) (QuestionAnswers, error) {
	out := QuestionAnswers{Note: strings.TrimSpace(in.Note), Answers: []QuestionAnswer{}}
	if err := checkLen("note", out.Note, maxAnswerTextRunes); err != nil {
		return out, err
	}
	seen := map[int]bool{}
	for _, a := range in.Answers {
		if a.QuestionIndex < 0 || a.QuestionIndex >= len(q.Payload.Questions) {
			return out, apperr.Invalid("task.questions.errors.bad_index", "index", a.QuestionIndex)
		}
		if seen[a.QuestionIndex] {
			return out, apperr.Invalid("task.questions.errors.duplicate_index", "index", a.QuestionIndex)
		}
		seen[a.QuestionIndex] = true
		ans, err := normalizeAnswer(&q.Payload.Questions[a.QuestionIndex], a)
		if err != nil {
			return out, err
		}
		if len(ans.Selected) > 0 || ans.Other != "" {
			out.Answers = append(out.Answers, ans)
		}
	}
	slices.SortFunc(out.Answers, func(a, b QuestionAnswer) int { return a.QuestionIndex - b.QuestionIndex })
	if out.Note == "" && len(out.Answers) < len(q.Payload.Questions) {
		return out, apperr.Invalid("task.questions.errors.unanswered")
	}
	if q.Kind == QuestionKindBlocker && out.Note == "" &&
		!slices.ContainsFunc(out.Answers, func(a QuestionAnswer) bool { return a.Other != "" }) {
		return out, apperr.Invalid("task.questions.errors.blocker_needs_text")
	}
	return out, nil
}

func normalizeAnswer(item *QuestionItem, a QuestionAnswer) (QuestionAnswer, error) {
	ans := QuestionAnswer{QuestionIndex: a.QuestionIndex, Selected: []string{}, Other: strings.TrimSpace(a.Other)}
	for _, label := range a.Selected {
		label = strings.TrimSpace(label)
		if !slices.ContainsFunc(item.Options, func(o QuestionOption) bool { return o.Label == label }) {
			return ans, apperr.Invalid("task.questions.errors.unknown_option", "index", a.QuestionIndex, "label", label)
		}
		if !slices.Contains(ans.Selected, label) {
			ans.Selected = append(ans.Selected, label)
		}
	}
	if len(ans.Selected) > 1 && !item.MultiSelect {
		return ans, apperr.Invalid("task.questions.errors.single_select", "index", a.QuestionIndex)
	}
	if ans.Other != "" && !item.AllowOther {
		return ans, apperr.Invalid("task.questions.errors.other_not_allowed", "index", a.QuestionIndex)
	}
	return ans, checkLen("other", ans.Other, maxAnswerTextRunes)
}

func renderQuestionComment(kind, agent string, blocking bool, p QuestionPayload) string {
	var b strings.Builder
	switch {
	case kind == QuestionKindBlocker:
		b.WriteString(i18n.Text("task.questions.comment.heading_blocker", "agent", agent))
	case blocking:
		b.WriteString(i18n.Text("task.questions.comment.heading_blocking", "agent", agent))
	default:
		b.WriteString(i18n.Text("task.questions.comment.heading_non_blocking", "agent", agent))
	}
	b.WriteString("\n\n")
	if p.Context != "" {
		b.WriteString(p.Context)
		b.WriteString("\n\n")
	}
	for i, q := range p.Questions {
		fmt.Fprintf(&b, "%d. ", i+1)
		if q.Header != "" {
			b.WriteString("**" + q.Header + "** · ")
		}
		b.WriteString(q.Question)
		b.WriteString("\n")
		for _, o := range q.Options {
			if o.Description != "" {
				b.WriteString(i18n.Text("task.questions.comment.option_described", "label", o.Label, "description", o.Description))
			} else {
				b.WriteString(i18n.Text("task.questions.comment.option", "label", o.Label))
			}
			b.WriteString("\n")
		}
		switch {
		case len(q.Options) == 0:
			b.WriteString(i18n.Text("task.questions.comment.free_answer"))
			b.WriteString("\n")
		case q.MultiSelect && q.AllowOther:
			b.WriteString(i18n.Text("task.questions.comment.multi_and_other"))
			b.WriteString("\n")
		case q.MultiSelect:
			b.WriteString(i18n.Text("task.questions.comment.multi"))
			b.WriteString("\n")
		case q.AllowOther:
			b.WriteString(i18n.Text("task.questions.comment.other"))
			b.WriteString("\n")
		}
	}
	if p.Default != "" {
		b.WriteString("\n")
		b.WriteString(i18n.Text("task.questions.comment.default", "default", p.Default))
	}
	return strings.TrimRight(b.String(), "\n")
}

func answerLine(a QuestionAnswer) string {
	picked := strings.Join(a.Selected, ", ")
	switch {
	case picked != "" && a.Other != "":
		return i18n.Text("task.questions.reply.picked_and_other", "picked", picked, "other", a.Other)
	case picked != "":
		return picked
	}
	return a.Other
}

func itemLabel(p QuestionPayload, i int) string {
	if i < len(p.Questions) && p.Questions[i].Header != "" {
		return p.Questions[i].Header
	}
	return fmt.Sprintf("%d", i+1)
}

func answerArg(a QuestionAnswer) any {
	picked := strings.Join(a.Selected, ", ")
	if picked != "" && a.Other != "" {
		return apperr.New("task.questions.reply.picked_and_other", "picked", picked, "other", a.Other)
	}
	return answerLine(a)
}

func renderAnswerReply(q *Question, ans QuestionAnswers) Note {
	n := NoteOf("task.questions.reply.heading")
	for _, a := range ans.Answers {
		n = n.Add("task.questions.reply.line", "label", itemLabel(q.Payload, a.QuestionIndex), "answer", answerArg(a))
	}
	if ans.Note != "" {
		n = n.Add("task.questions.reply.note", "note", ans.Note)
	}
	return n
}

func renderAnswersForAgent(qs []Question) string {
	var b strings.Builder
	for i := range qs {
		q := &qs[i]
		if q.Answers == nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(i18n.Text("task.questions.prompts.answered_by", "author", orSomeone(q.AnsweredBy)))
		answered := map[int]QuestionAnswer{}
		for _, a := range q.Answers.Answers {
			answered[a.QuestionIndex] = a
		}
		for idx, item := range q.Payload.Questions {
			b.WriteString("\n\n")
			b.WriteString(i18n.Text("task.questions.prompts.question", "label", itemLabel(q.Payload, idx), "question", item.Question))
			if a, ok := answered[idx]; ok {
				b.WriteString("\n")
				b.WriteString(i18n.Text("task.questions.prompts.answer", "answer", answerLine(a)))
			}
		}
		if q.Answers.Note != "" {
			b.WriteString("\n\n")
			b.WriteString(i18n.Text("task.questions.prompts.note", "note", q.Answers.Note))
		}
	}
	return b.String()
}

func orSomeone(s string) string {
	if anonymous(s) {
		return i18n.Text("task.prompts.someone")
	}
	return s
}

func anonymous(name string) bool {
	return name == "" || name == auth.DevModeAuthorName
}

func personArg(name string) any {
	if anonymous(name) {
		return apperr.New("task.prompts.someone")
	}
	return name
}

//i18n:ignore SQL column list
const questionCols = `id, task_id, comment_id, run_id, agent, stage, session_id, kind, blocking,
	payload::text, status, answers::text, answered_by, answered_at, delivery, created_at`

func scanQuestion(r rowScanner) (*Question, error) {
	var (
		q        Question
		payload  string
		answers  sql.NullString
		answered sql.NullTime
	)
	if err := r.Scan(&q.ID, &q.TaskID, &q.CommentID, &q.RunID, &q.Agent, &q.Stage, &q.SessionID,
		&q.Kind, &q.Blocking, &payload, &q.Status, &answers, &q.AnsweredBy, &answered, &q.Delivery, &q.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(payload), &q.Payload); err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.decode", "id", q.ID)
	}
	if answers.Valid && answers.String != "" {
		var a QuestionAnswers
		if err := json.Unmarshal([]byte(answers.String), &a); err != nil {
			return nil, apperr.Wrap(err, "task.questions.errors.decode", "id", q.ID)
		}
		q.Answers = &a
	}
	if answered.Valid {
		at := answered.Time.UTC()
		q.AnsweredAt = &at
	}
	q.CreatedAt = q.CreatedAt.UTC()
	return &q, nil
}

func (s *Store) queryQuestions(ctx context.Context, where string, args ...any) ([]Question, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT `+questionCols+` FROM task_questions `+where), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.list")
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, apperr.Wrap(err, "task.questions.errors.scan")
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

func (s *Store) ListQuestions(ctx context.Context, taskID string) ([]Question, error) {
	return s.queryQuestions(ctx, `WHERE task_id = ? ORDER BY created_at ASC, id ASC`, taskID)
}

// Scoped by taskID: another task's question must read as not found.
func (s *Store) GetQuestion(ctx context.Context, taskID, id string) (*Question, error) {
	q, err := scanQuestion(s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+questionCols+` FROM task_questions WHERE id = ? AND task_id = ?`), id, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Wrap(ErrNotFound, "task.questions.errors.not_found", "id", id)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.scan")
	}
	return q, nil
}

func (s *Store) runQuestions(ctx context.Context, taskID, runID string) ([]Question, error) {
	if runID == "" {
		return nil, nil
	}
	return s.queryQuestions(ctx, `WHERE task_id = ? AND run_id = ? ORDER BY created_at ASC, id ASC`, taskID, runID)
}

func (s *Store) decorateQuestionCounts(ctx context.Context, out []Task) {
	if len(out) == 0 {
		return
	}
	ids := taskIDsOf(out)
	q := `SELECT task_id, COUNT(*) FROM task_questions
	       WHERE status = ? AND task_id IN (` + placeholderList(len(ids)) + `) GROUP BY task_id`
	rows, err := s.db.QueryContext(ctx, s.rb(q), append([]any{QuestionOpen}, ids...)...)
	if err != nil {
		logx.Warn(err, "task.questions.log.decorate_counts")
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			logx.Warn(err, "task.questions.log.decorate_counts")
			return
		}
		counts[id] = n
	}
	logx.Warn(rows.Err(), "task.questions.log.decorate_counts")
	for i := range out {
		out[i].OpenQuestions = counts[out[i].ID]
	}
}

func (s *Store) OpenQuestionsOnThread(ctx context.Context, taskID, rootID string) ([]Question, error) {
	return s.queryQuestions(ctx, `WHERE task_id = ? AND comment_id = ? AND status = ? ORDER BY created_at ASC`,
		taskID, rootID, QuestionOpen)
}

// The task row is locked for the count so two racing asks cannot both slip under a limit.
func (s *Store) AskQuestion(ctx context.Context, t *Task, target AskTarget, in AskInput) (*Question, error) {
	kind, blocking, payload, err := NormalizeAsk(in)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.encode")
	}
	now := time.Now().UTC()
	q := &Question{
		ID: mustGenerateID(), TaskID: t.ID, RunID: target.RunID, Agent: target.Agent, Stage: target.Stage,
		SessionID: target.SessionID, Kind: kind, Blocking: blocking, Payload: payload,
		Status: QuestionOpen, CreatedAt: now,
	}
	comment := &Comment{
		ID: mustGenerateID(), TaskID: t.ID, Source: SourceAgent, Author: target.Agent,
		Body: renderQuestionComment(kind, target.Agent, blocking, payload), Stage: target.Stage, CreatedAt: now,
	}
	q.CommentID = comment.ID

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.insert")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.getRawFrom(ctx, tx, t.ID, true); err != nil {
		return nil, err
	}
	var perRun, open int
	if err := tx.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FILTER (WHERE run_id = ?), count(*) FILTER (WHERE status = ?)
		   FROM task_questions WHERE task_id = ?`), target.RunID, QuestionOpen, t.ID).Scan(&perRun, &open); err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.count")
	}
	if perRun >= MaxQuestionsPerRun {
		return nil, apperr.RateLimited("task.questions.errors.run_limit", "max", MaxQuestionsPerRun)
	}
	if open >= MaxOpenQuestionsPerTask {
		return nil, apperr.RateLimited("task.questions.errors.open_limit", "max", MaxOpenQuestionsPerTask)
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO task_comments (id, task_id, parent_id, source, author, body, stage, created_at) VALUES (?,?,?,?,?,?,?,?)`),
		comment.ID, comment.TaskID, "", comment.Source, comment.Author, comment.Body, comment.Stage, comment.CreatedAt); err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.insert")
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO task_questions (id, task_id, comment_id, run_id, agent, stage, session_id, kind, blocking,
		    payload, status, delivery, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		q.ID, q.TaskID, q.CommentID, q.RunID, q.Agent, q.Stage, q.SessionID, q.Kind, q.Blocking,
		string(raw), q.Status, "", q.CreatedAt); err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.insert")
	}
	if err := tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.insert")
	}
	s.touchTask(ctx, t.ID, now)
	s.notifyTask(ctx, t.ID)
	logx.Warn(s.RecordEvent(ctx, t.ID, EventQuestionAsked, q.ID, "agent:"+q.Agent,
		i18n.Text("task.questions.events.asked", "kind", q.Kind, "count", len(payload.Questions)),
		questionEventPayload(q)), "task.questions.log.record_event", "task", t.ID)
	return q, nil
}

func questionEventPayload(q *Question) map[string]any {
	return map[string]any{
		"question_id": q.ID,
		"kind":        q.Kind,
		"blocking":    q.Blocking,
		"comment_id":  q.CommentID,
		"run_id":      q.RunID,
		"agent":       q.Agent,
		"stage":       string(q.Stage),
		"delivery":    q.Delivery,
	}
}

func (s *Store) closeQuestion(ctx context.Context, q *Question, status string, ans *QuestionAnswers, by string) (bool, error) {
	var raw any
	if ans != nil {
		b, err := json.Marshal(ans)
		if err != nil {
			return false, apperr.Wrap(err, "task.questions.errors.encode")
		}
		raw = string(b)
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_questions SET status = ?, answers = ?, answered_by = ?, answered_at = ?
		  WHERE id = ? AND status = ?`), status, raw, by, now, q.ID, QuestionOpen)
	if err != nil {
		return false, apperr.Wrap(err, "task.questions.errors.update")
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	q.Status, q.Answers, q.AnsweredBy, q.AnsweredAt = status, ans, by, &now
	return true, nil
}

// Claims only unstamped rows; false if another path got any first, so two answers or an answer
// racing the run's end never start two runs.
func (s *Store) claimDelivery(ctx context.Context, ids []string, delivery string) bool {
	if len(ids) == 0 {
		return false
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_questions SET delivery = ? WHERE delivery = '' AND id IN (`+placeholderList(len(ids))+`)`), deliveryArgs(delivery, ids)...)
	if err != nil {
		logx.Warn(err, "task.questions.log.set_delivery", "delivery", delivery)
		return false
	}
	n, err := res.RowsAffected()
	if err != nil || int(n) != len(ids) {
		if n > 0 {
			s.releaseDelivery(ctx, ids, delivery)
		}
		return false
	}
	return true
}

func (s *Store) releaseDelivery(ctx context.Context, ids []string, delivery string) {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_questions SET delivery = '' WHERE delivery = ? AND id IN (`+placeholderList(len(ids))+`)`),
		deliveryArgs(delivery, ids)...)
	logx.Warn(err, "task.questions.log.set_delivery", "delivery", "")
}

func deliveryArgs(delivery string, ids []string) []any {
	args := make([]any, 0, 1+len(ids))
	args = append(args, delivery)
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

func (s *Store) setRunQuestionSession(ctx context.Context, taskID, runID, sid string) {
	if sid == "" || runID == "" {
		return
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_questions SET session_id = ? WHERE task_id = ? AND run_id = ? AND delivery = ''`),
		sid, taskID, runID)
	logx.Warn(err, "task.questions.log.set_session", "task", taskID)
}

// Questions are not a column of the task row, so nothing else publishes their changes.
func (s *Store) notifyTask(ctx context.Context, taskID string) {
	if s.changes == nil {
		return
	}
	t, err := s.getRaw(ctx, taskID)
	if err != nil {
		return
	}
	s.notify(ChangeUpdated, t)
}

type PendingQuestion struct {
	Question
	TaskTitle string `json:"task_title"`
	BoardID   string `json:"board_id"`
}

func (s *Store) ListPendingQuestions(ctx context.Context, workspaceID string, unrestricted bool) ([]PendingQuestion, error) {
	q := `SELECT q.id, q.task_id, q.comment_id, q.run_id, q.agent, q.stage, q.session_id, q.kind, q.blocking,
	             q.payload::text, q.status, q.answers::text, q.answered_by, q.answered_at, q.delivery, q.created_at,
	             t.title, t.board_id
	        FROM task_questions q JOIN tasks t ON t.id = q.task_id
	       WHERE q.status = ? AND t.archived_at IS NULL`
	args := []any{QuestionOpen}
	if !unrestricted {
		q += ` AND t.workspace_id = ?`
		args = append(args, workspaceID)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(q+` ORDER BY q.created_at ASC, q.id ASC LIMIT `+strconv.Itoa(InboxLimit)), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.list")
	}
	defer rows.Close()
	out := []PendingQuestion{}
	for rows.Next() {
		var p PendingQuestion
		var payload string
		var answers sql.NullString
		var answered sql.NullTime
		if err := rows.Scan(&p.ID, &p.TaskID, &p.CommentID, &p.RunID, &p.Agent, &p.Stage, &p.SessionID,
			&p.Kind, &p.Blocking, &payload, &p.Status, &answers, &p.AnsweredBy, &answered, &p.Delivery, &p.CreatedAt,
			&p.TaskTitle, &p.BoardID); err != nil {
			return nil, apperr.Wrap(err, "task.questions.errors.scan")
		}
		if err := json.Unmarshal([]byte(payload), &p.Payload); err != nil {
			return nil, apperr.Wrap(err, "task.questions.errors.decode", "id", p.ID)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) tasksAwaitingInput(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id FROM tasks WHERE status = ?`), string(StatusAwaitingInput))
	if err != nil {
		return nil, apperr.Wrap(err, "task.questions.errors.list")
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, apperr.Wrap(err, "task.questions.errors.scan")
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *Dispatcher) AnswerQuestion(ctx context.Context, t *Task, questionID string, in QuestionAnswers, author string) (*Question, error) {
	q, err := d.store.GetQuestion(ctx, t.ID, questionID)
	if err != nil {
		return nil, err
	}
	if q.Status != QuestionOpen {
		return nil, apperr.Conflict("task.questions.errors.not_open", "status", q.Status)
	}
	ans, err := normalizeAnswers(q, in)
	if err != nil {
		return nil, err
	}
	if err := d.closeAnswered(ctx, q, ans, author); err != nil {
		return nil, err
	}
	_, replyErr := d.store.AddNote(ctx, t.ID, q.CommentID, SourceUser, author, renderAnswerReply(q, ans))
	logx.Warn(replyErr, "task.questions.log.post_reply", "question", q.ID)
	d.deliverAnswer(ctx, t.ID, q)
	return d.store.GetQuestion(ctx, t.ID, q.ID)
}

func (d *Dispatcher) AnswerFromThreadReply(ctx context.Context, t *Task, c *Comment) ([]Question, error) {
	if c == nil || c.ParentID == "" || c.Source != SourceUser || strings.TrimSpace(c.Body) == "" {
		return nil, nil
	}
	open, err := d.store.OpenQuestionsOnThread(ctx, t.ID, c.ParentID)
	if err != nil || len(open) == 0 {
		return nil, err
	}
	var answered []Question
	for i := range open {
		q := &open[i]
		ans := QuestionAnswers{Answers: []QuestionAnswer{}}
		for idx := range q.Payload.Questions {
			ans.Answers = append(ans.Answers, QuestionAnswer{QuestionIndex: idx, Selected: []string{}, Other: strings.TrimSpace(c.Body)})
		}
		if err := d.closeAnswered(ctx, q, ans, c.Author); err != nil {
			continue
		}
		d.deliverAnswer(ctx, t.ID, q)
		if fresh, err := d.store.GetQuestion(ctx, t.ID, q.ID); err == nil {
			answered = append(answered, *fresh)
		}
	}
	return answered, nil
}

func (d *Dispatcher) closeAnswered(ctx context.Context, q *Question, ans QuestionAnswers, author string) error {
	ok, err := d.store.closeQuestion(ctx, q, QuestionAnswered, &ans, author)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Conflict("task.questions.errors.not_open", "status", QuestionAnswered)
	}
	logx.Warn(d.store.RecordEvent(ctx, q.TaskID, EventQuestionAnswered, q.ID, author,
		i18n.Text("task.questions.events.answered", "kind", q.Kind), questionEventPayload(q)),
		"task.questions.log.record_event", "task", q.TaskID)
	d.store.notifyTask(ctx, q.TaskID)
	return nil
}

// Serializes per task the decisions that end a wait (answer, run end, startup reconcile);
// interleaved, they could park a task just resumed.
var questionLocks = struct {
	sync.Mutex
	byTask map[string]*sync.Mutex
}{byTask: map[string]*sync.Mutex{}}

func lockQuestions(taskID string) func() {
	questionLocks.Lock()
	mu, ok := questionLocks.byTask[taskID]
	if !ok {
		mu = &sync.Mutex{}
		questionLocks.byTask[taskID] = mu
	}
	questionLocks.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (d *Dispatcher) deliverAnswer(ctx context.Context, taskID string, q *Question) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.questions.log.reload_task", "task", taskID)
		return
	}
	if !q.Blocking {
		if !d.store.claimDelivery(ctx, []string{q.ID}, QuestionDeliverySteered) {
			return
		}
		if !d.CanSteer(t) || !d.steerAnswers(ctx, t, []Question{*q}) {
			d.store.releaseDelivery(ctx, []string{q.ID}, QuestionDeliverySteered)
			d.store.claimDelivery(ctx, []string{q.ID}, QuestionDeliveryThread)
		}
		return
	}
	defer lockQuestions(taskID)()
	runQs, err := d.store.runQuestions(ctx, taskID, q.RunID)
	if err != nil {
		logx.Warn(err, "task.questions.log.list_run", "task", taskID)
		return
	}
	waiting := undeliveredBlocking(runQs)
	if len(waiting) == 0 || hasOpenBlocking(runQs) {
		return
	}
	// While the task still runs this run, holdForQuestions (same lock) delivers at its end;
	// resuming now would use the pre-run session and race the old run's end for the task's status.
	if t.CurrentRunID == q.RunID && t.Status == StatusRunning {
		ids := questionIDs(waiting)
		if d.launcher.ContainerAlive(q.RunID) && d.CanSteer(t) &&
			d.store.claimDelivery(ctx, ids, QuestionDeliverySteered) && !d.steerAnswers(ctx, t, waiting) {
			d.store.releaseDelivery(ctx, ids, QuestionDeliverySteered)
		}
		return
	}
	d.resumeWithAnswers(ctx, t, waiting)
}

func (d *Dispatcher) steerAnswers(ctx context.Context, t *Task, qs []Question) bool {
	text := renderAnswersForAgent(qs)
	author := qs[len(qs)-1].AnsweredBy
	msg, err := d.store.AddMessage(ctx, &Message{TaskID: t.ID, Role: RoleUser, Author: author, Body: text})
	if err != nil {
		logx.Warn(err, "task.questions.log.record_message", "task", t.ID)
		return false
	}
	if err := d.steerRun(ctx, t.CurrentRunID, steerPayload{ID: msg.ID, Kind: "answer", Text: text, Author: author}); err != nil {
		logx.Warn(err, "task.questions.log.steer", "task", t.ID, "run", t.CurrentRunID)
		logx.Warn(d.store.UpdateMessageDelivery(ctx, msg.ID, string(DeliveryThread), t.AssignedAgent(t.Stage), "", ""),
			"task.log.stamp_delivery", "message", msg.ID)
		return false
	}
	logx.Warn(d.store.UpdateMessageDelivery(ctx, msg.ID, string(DeliverySteered), t.AssignedAgent(t.Stage), t.CurrentRunID, ""),
		"task.log.stamp_delivery", "message", msg.ID)
	return true
}

// Callers hold the task's question lock. Answers from a stage the task left stay in the thread:
// resuming would hand them to another stage's agent.
func (d *Dispatcher) resumeWithAnswers(ctx context.Context, t *Task, qs []Question) {
	ids := questionIDs(qs)
	if qs[0].Stage != t.Stage {
		d.store.claimDelivery(ctx, ids, QuestionDeliveryThread)
		return
	}
	if !d.store.claimDelivery(ctx, ids, QuestionDeliveryResumed) {
		return
	}
	rt := *t
	if q := qs[0]; q.SessionID != "" {
		rt.SessionID, rt.SessionAgent, rt.SessionStage = q.SessionID, q.Agent, string(q.Stage)
	}
	release := func() { d.store.releaseDelivery(context.WithoutCancel(ctx), ids, QuestionDeliveryResumed) }
	if err := d.resumeStage(ctx, &rt, renderAnswersForAgent(qs), LaunchAnswerResume, release); err != nil {
		release()
		d.resumeFailed(ctx, t, NoteOf("task.questions.comment.resume_failed", "err", err), err)
	}
}

func (d *Dispatcher) CancelQuestion(ctx context.Context, t *Task, questionID, by string) (*Question, error) {
	q, err := d.store.GetQuestion(ctx, t.ID, questionID)
	if err != nil {
		return nil, err
	}
	if q.Status != QuestionOpen {
		return nil, apperr.Conflict("task.questions.errors.not_open", "status", q.Status)
	}
	if err := d.cancelOne(ctx, q, by, NoteOf("task.questions.reply.canceled", "author", personArg(by))); err != nil {
		return nil, err
	}
	d.unparkIfNothingOpen(ctx, t.ID)
	return q, nil
}

// reasonKey must be a task.questions.reply catalog key without variables.
func (d *Dispatcher) CancelOpenQuestions(ctx context.Context, taskID, by, reasonKey string) []Question {
	open, err := d.store.queryQuestions(ctx, `WHERE task_id = ? AND status = ?`, taskID, QuestionOpen)
	if err != nil {
		logx.Warn(err, "task.questions.log.list_open", "task", taskID)
		return nil
	}
	var closed []Question
	for i := range open {
		if d.cancelOne(ctx, &open[i], by, NoteOf(reasonKey)) == nil {
			closed = append(closed, open[i])
		}
	}
	return closed
}

func (d *Dispatcher) cancelOne(ctx context.Context, q *Question, by string, reply Note) error {
	ok, err := d.store.closeQuestion(ctx, q, QuestionCanceled, nil, by)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Conflict("task.questions.errors.not_open", "status", QuestionCanceled)
	}
	_, replyErr := d.store.AddNote(ctx, q.TaskID, q.CommentID, SourceSystem, "octopus", reply)
	logx.Warn(replyErr, "task.questions.log.post_reply", "question", q.ID)
	logx.Warn(d.store.RecordEvent(ctx, q.TaskID, EventQuestionCanceled, q.ID, by,
		i18n.Text("task.questions.events.canceled", "kind", q.Kind), questionEventPayload(q)),
		"task.questions.log.record_event", "task", q.TaskID)
	d.store.notifyTask(ctx, q.TaskID)
	return nil
}

func (d *Dispatcher) unparkIfNothingOpen(ctx context.Context, taskID string) {
	defer lockQuestions(taskID)()
	d.settleParked(ctx, taskID)
}

// Callers hold the task's question lock.
func (d *Dispatcher) settleParked(ctx context.Context, taskID string) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil || t.Status != StatusAwaitingInput {
		return
	}
	if t.CurrentRunID != "" && d.launcher.ContainerAlive(t.CurrentRunID) {
		return
	}
	if hasOpenBlocking(t.Questions) {
		return
	}
	if waiting := undeliveredBlocking(t.Questions); len(waiting) > 0 {
		d.resumeWithAnswers(ctx, t, waiting)
		return
	}
	// Only out of awaiting_input: a resume launched meanwhile set the task running, and idling it
	// would let a second run start on the same worktree.
	d.store.claimAwaitingInput(ctx, taskID)
}

// true stops the run's end (no audit, no advance). If a resume already took the answers, that run
// owns the stage and this end must yield.
func (d *Dispatcher) holdForQuestions(ctx context.Context, taskID, runID string) bool {
	defer lockQuestions(taskID)()
	qs, err := d.store.runQuestions(ctx, taskID, runID)
	if err != nil {
		logx.Warn(err, "task.questions.log.list_run", "task", taskID)
		return false
	}
	if len(qs) == 0 {
		return false
	}
	t, err := d.store.Get(ctx, taskID)
	if err != nil {
		return false
	}
	sameSession := t.SessionID != "" && t.SessionAgent == qs[0].Agent && t.SessionStage == string(qs[0].Stage)
	if hasOpenBlocking(qs) {
		if sameSession {
			d.store.setRunQuestionSession(ctx, taskID, runID, t.SessionID)
		}
		d.setStatus(ctx, taskID, StatusAwaitingInput)
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventRunAwaitingInput, runID, "agent:"+qs[0].Agent,
			i18n.Text("task.questions.events.awaiting_input", "count", countOpenBlocking(qs)),
			map[string]any{"run_id": runID, "stage": string(qs[0].Stage), "agent": qs[0].Agent}),
			"task.questions.log.record_event", "task", taskID)
		return true
	}
	if slices.ContainsFunc(qs, func(q Question) bool { return q.Blocking && q.Delivery == QuestionDeliveryResumed }) {
		return true
	}
	waiting := undeliveredBlocking(qs)
	if len(waiting) == 0 {
		return false
	}
	if sameSession {
		for i := range waiting {
			waiting[i].SessionID = t.SessionID
		}
	}
	d.setStatus(ctx, taskID, StatusAwaitingInput)
	d.resumeWithAnswers(ctx, t, waiting)
	return true
}

// Call at startup, after Reconcile.
func (d *Dispatcher) ReconcileQuestions(ctx context.Context) {
	ids, err := d.store.tasksAwaitingInput(ctx)
	if err != nil {
		logx.Warn(err, "task.questions.log.reconcile_list")
		return
	}
	for _, id := range ids {
		unlock := lockQuestions(id)
		d.store.releaseUnlaunchedResume(ctx, id)
		d.settleParked(ctx, id)
		unlock()
	}
}

// Startup only: assumes no launch can be in flight.
func (s *Store) releaseUnlaunchedResume(ctx context.Context, taskID string) {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE task_questions q SET delivery = ''
		  FROM tasks t
		 WHERE t.id = q.task_id AND q.task_id = ? AND t.status = ? AND q.run_id = t.current_run_id
		   AND q.delivery = ? AND q.status = ?`),
		taskID, string(StatusAwaitingInput), QuestionDeliveryResumed, QuestionAnswered)
	logx.Warn(err, "task.questions.log.set_delivery", "delivery", "")
}

func (s *Store) claimAwaitingInput(ctx context.Context, taskID string) {
	res, err := s.db.ExecContext(ctx, s.rb(`UPDATE tasks SET status = ? WHERE id = ? AND status = ?`),
		string(StatusIdle), taskID, string(StatusAwaitingInput))
	if err != nil {
		logx.Warn(err, "task.questions.log.unpark", "task", taskID)
		return
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.notifyTask(ctx, taskID)
	}
}

// Called by Store.Update on every stage change. Non-blocking questions stay here; a manual move
// cancels them in the API.
func (s *Store) cancelQuestionsLeftBehind(ctx context.Context, t *Task) {
	open, err := s.queryQuestions(ctx, `WHERE task_id = ? AND status = ? AND blocking = TRUE AND stage <> ?`,
		t.ID, QuestionOpen, string(t.Stage))
	if err != nil {
		logx.Warn(err, "task.questions.log.list_open", "task", t.ID)
		return
	}
	for i := range open {
		q := &open[i]
		ok, err := s.closeQuestion(ctx, q, QuestionCanceled, nil, "octopus")
		if err != nil || !ok {
			logx.Warn(err, "task.questions.log.cancel", "question", q.ID)
			continue
		}
		_, replyErr := s.AddNote(ctx, q.TaskID, q.CommentID, SourceSystem, "octopus",
			NoteOf("task.questions.reply.canceled_moved"))
		logx.Warn(replyErr, "task.questions.log.post_reply", "question", q.ID)
		logx.Warn(s.RecordEvent(ctx, q.TaskID, EventQuestionCanceled, q.ID, "octopus",
			i18n.Text("task.questions.events.canceled", "kind", q.Kind), questionEventPayload(q)),
			"task.questions.log.record_event", "task", q.TaskID)
		if s.onQuestionCanceled != nil {
			s.onQuestionCanceled(ctx, *q)
		}
	}
	s.claimAwaitingInput(ctx, t.ID)
}

// The API turns this into the question.canceled webhook; set at boot.
func (s *Store) WithQuestionCancelHook(fn func(ctx context.Context, q Question)) *Store {
	s.onQuestionCanceled = fn
	return s
}

func hasOpenBlocking(qs []Question) bool { return countOpenBlocking(qs) > 0 }

func countOpenBlocking(qs []Question) int {
	n := 0
	for i := range qs {
		if qs[i].Blocking && qs[i].Status == QuestionOpen {
			n++
		}
	}
	return n
}

func undeliveredBlocking(qs []Question) []Question {
	var out []Question
	for i := range qs {
		if qs[i].Blocking && qs[i].Status == QuestionAnswered && qs[i].Delivery == "" {
			out = append(out, qs[i])
		}
	}
	return out
}

func questionIDs(qs []Question) []string {
	ids := make([]string, 0, len(qs))
	for i := range qs {
		ids = append(ids, qs[i].ID)
	}
	return ids
}
