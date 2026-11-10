package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/patch"
)

const (
	TestCasePending = "pending"
	TestCasePass    = "pass"
	TestCaseFail    = "fail"
	TestCaseBlocked = "blocked"
	TestCaseSkipped = "skipped"
)

const (
	TestCasePriorityLow      = "low"
	TestCasePriorityMedium   = "medium"
	TestCasePriorityHigh     = "high"
	TestCasePriorityCritical = "critical"
)

const (
	DefaultTestCaseListLimit = 200
	MaxTestCaseListLimit     = 1000
)

const maxTestCaseTitle = 200

var TestCaseStatuses = []string{TestCasePending, TestCasePass, TestCaseFail, TestCaseBlocked, TestCaseSkipped}

var (
	testCaseKeyRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+[a-z]?$`)
	autoCaseKeyRe  = regexp.MustCompile(`^TC-(\d+)[a-z]?$`)
	bodyItemKeyRe  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*-\d+[a-z]?)\s*[:.\-–—]\s*(\S.*)$`)
	likeEscapement = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
)

func ValidTestCaseStatus(s string) bool {
	switch s {
	case TestCasePending, TestCasePass, TestCaseFail, TestCaseBlocked, TestCaseSkipped:
		return true
	}
	return false
}

func ValidTestCaseVerdict(v string) bool {
	return v == TestCasePass || v == TestCaseFail || v == TestCaseBlocked
}

func ValidTestCasePriority(p string) bool {
	switch p {
	case TestCasePriorityLow, TestCasePriorityMedium, TestCasePriorityHigh, TestCasePriorityCritical:
		return true
	}
	return false
}

func ValidTestCaseKey(k string) bool { return testCaseKeyRe.MatchString(k) }

type TestCase struct {
	ID          string        `json:"id"`
	TaskID      string        `json:"task_id"`
	Key         string        `json:"key"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Steps       string        `json:"steps"`
	Expected    string        `json:"expected"`
	Priority    string        `json:"priority"`
	Status      string        `json:"status"`
	Covered     bool          `json:"covered"`
	Position    int           `json:"position"`
	CreatedBy   string        `json:"created_by"`
	UpdatedBy   string        `json:"updated_by"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Runs        []TestCaseRun `json:"runs"`
}

type TestCaseRun struct {
	ID                string `json:"id"`
	TestCaseID        string `json:"test_case_id"`
	TaskID            string `json:"task_id"`
	Verdict           string `json:"verdict"`
	Observed          string `json:"observed"`
	Note              string `json:"note"`
	VideoAttachmentID string `json:"video_attachment_id"`
	TakeAttachmentID  string `json:"take_attachment_id"`
	// CommentID holds the evidence of runs recorded before reports existed; old rows still use it.
	ReportID             string    `json:"report_id"`
	CommentID            string    `json:"comment_id"`
	CaptureAttachmentIDs []string  `json:"capture_attachment_ids"`
	Author               string    `json:"author"`
	RanAt                time.Time `json:"ran_at"`
}

type TestCasePatch struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Steps       *string `json:"steps,omitempty"`
	Expected    *string `json:"expected,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	Status      *string `json:"status,omitempty"`
	Covered     *bool   `json:"covered,omitempty"`
	Position    *int    `json:"position,omitempty"`
}

type TestCaseFilter struct {
	WorkspaceID  string
	Unrestricted bool
	Statuses     []string
	BoardID      string
	ProjectID    string
	Q            string
	Uncovered    bool
	Limit        int
}

type TestCaseRow struct {
	TestCase
	// Shadows TestCase.Runs so a row serializes without the run history.
	Runs      []TestCaseRun `json:"runs,omitempty"`
	TaskTitle string        `json:"task_title"`
	TaskStage string        `json:"task_stage"`
	BoardID   string        `json:"board_id"`
	LastRun   *TestCaseRun  `json:"last_run"`
	RunsCount int           `json:"runs_count"`
}

const (
	testCaseCols = `c.id, c.task_id, c.key, c.title, c.description, c.steps, c.expected,
		c.priority, c.status, c.covered, c.position, c.created_by, c.updated_by, c.created_at, c.updated_at`
	//i18n:ignore SQL column list
	testCaseRunCols = `r.id, r.test_case_id, r.task_id, r.verdict, r.observed, r.note,
		r.video_attachment_id, r.take_attachment_id, r.report_id, r.comment_id, r.capture_attachment_ids::text,
		r.author, r.ran_at`
	testCaseOrder  = ` ORDER BY c.position ASC, c.created_at ASC, c.id ASC`
	insertTestCase = `INSERT INTO test_cases (id, task_id, key, title, description, steps, expected,
		priority, status, covered, position, created_by, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	insertTestCaseRun = `INSERT INTO test_case_runs (id, test_case_id, task_id, verdict, observed, note,
		video_attachment_id, take_attachment_id, report_id, comment_id, capture_attachment_ids, author, ran_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,CAST(? AS jsonb),?,?)`
)

func scanTestCase(r rowScanner, extra ...any) (TestCase, error) {
	var c TestCase
	dest := append([]any{&c.ID, &c.TaskID, &c.Key, &c.Title, &c.Description, &c.Steps, &c.Expected,
		&c.Priority, &c.Status, &c.Covered, &c.Position, &c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt},
		extra...)
	if err := r.Scan(dest...); err != nil {
		return c, err
	}
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

func scanTestCaseRun(r rowScanner) (TestCaseRun, error) {
	var run TestCaseRun
	var captures string
	if err := r.Scan(&run.ID, &run.TestCaseID, &run.TaskID, &run.Verdict, &run.Observed, &run.Note,
		&run.VideoAttachmentID, &run.TakeAttachmentID, &run.ReportID, &run.CommentID, &captures,
		&run.Author, &run.RanAt); err != nil {
		return run, err
	}
	run.RanAt = run.RanAt.UTC()
	if err := json.Unmarshal([]byte(captures), &run.CaptureAttachmentIDs); err != nil {
		return run, apperr.Wrap(err, "task.errors.decode_captures", "run", run.ID)
	}
	if run.CaptureAttachmentIDs == nil {
		run.CaptureAttachmentIDs = []string{}
	}
	return run, nil
}

func (s *Store) ListTestCases(ctx context.Context, taskID string) ([]TestCase, error) {
	cases, err := s.listCases(ctx, s.db, taskID)
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return cases, nil
	}
	runs, err := s.collectRuns(ctx, `WHERE r.task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	for i := range cases {
		cases[i].Runs = runs[cases[i].ID]
		if cases[i].Runs == nil {
			cases[i].Runs = []TestCaseRun{}
		}
	}
	return cases, nil
}

func (s *Store) GetTestCase(ctx context.Context, taskID, ref string) (*TestCase, error) {
	c, err := s.findCase(ctx, s.db, taskID, ref)
	if err != nil {
		return nil, err
	}
	runs, err := s.collectRuns(ctx, `WHERE r.test_case_id = ?`, c.ID)
	if err != nil {
		return nil, err
	}
	c.Runs = runs[c.ID]
	if c.Runs == nil {
		c.Runs = []TestCaseRun{}
	}
	return c, nil
}

func (s *Store) listCases(ctx context.Context, q rowsQueryer, taskID string) ([]TestCase, error) {
	rows, err := q.QueryContext(ctx, s.rb(
		`SELECT `+testCaseCols+` FROM test_cases c WHERE c.task_id = ?`+testCaseOrder), taskID)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_test_cases", "task", taskID)
	}
	defer rows.Close()
	out := []TestCase{}
	for rows.Next() {
		c, scanErr := scanTestCase(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "task.errors.scan_test_case")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) findCase(ctx context.Context, q rowQueryer, taskID, ref string) (*TestCase, error) {
	c, err := scanTestCase(q.QueryRowContext(ctx, s.rb(
		`SELECT `+testCaseCols+` FROM test_cases c WHERE c.task_id = ? AND (c.id = ? OR c.key = ?)`),
		taskID, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Wrap(ErrNotFound, "task.errors.test_case_ref", "ref", ref)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.read_test_case", "ref", ref)
	}
	return &c, nil
}

// where is concatenated into the SQL: pass only constant strings over alias r.
func (s *Store) collectRuns(ctx context.Context, where string, args ...any) (map[string][]TestCaseRun, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+testCaseRunCols+` FROM test_case_runs r `+where+` ORDER BY r.ran_at DESC, r.id DESC`), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_test_case_runs")
	}
	defer rows.Close()
	out := map[string][]TestCaseRun{}
	for rows.Next() {
		run, scanErr := scanTestCaseRun(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "task.errors.scan_test_case_run")
		}
		out[run.TestCaseID] = append(out[run.TestCaseID], run)
	}
	return out, rows.Err()
}

// getRawTx locks the task row, so key and position allocation cannot interleave with another writer
// of the same task.
func (s *Store) testCaseTx(ctx context.Context, taskID string, fn func(tx *sql.Tx, now time.Time) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "task.errors.test_cases_begin", "task", taskID)
	}
	defer func() { _ = tx.Rollback() }()

	t, err := s.getRawTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if fnErr := fn(tx, now); fnErr != nil {
		return fnErr
	}
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE tasks SET updated_at = ? WHERE id = ?`), now, taskID); err != nil {
		return apperr.Wrap(err, "task.errors.test_cases_bump", "task", taskID)
	}
	if err = tx.Commit(); err != nil {
		return apperr.Wrap(err, "task.errors.test_cases_commit", "task", taskID)
	}
	t.UpdatedAt = now
	s.notify(ChangeUpdated, t)
	return nil
}

func (s *Store) AddTestCases(ctx context.Context, taskID, author string, in []TestCase) ([]TestCase, error) {
	if len(in) == 0 {
		return nil, apperr.Wrap(ErrInvalid, "task.errors.no_test_cases_given")
	}
	var out []TestCase
	err := s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		existing, err := s.listCases(ctx, tx, taskID)
		if err != nil {
			return err
		}
		out, err = s.insertNewCases(ctx, tx, taskID, author, existing, in, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) insertNewCases(ctx context.Context, tx *sql.Tx, taskID, author string, existing, in []TestCase, now time.Time) ([]TestCase, error) {
	cases := make([]TestCase, len(in))
	for i := range in {
		c, err := newTestCase(in[i])
		if err != nil {
			return nil, err
		}
		cases[i] = c
	}
	var seq int
	if err := tx.QueryRowContext(ctx, s.rb(`SELECT test_case_seq FROM tasks WHERE id = ?`), taskID).Scan(&seq); err != nil {
		return nil, apperr.Wrap(err, "task.errors.test_cases_seq", "task", taskID)
	}
	seq, err := allocateKeys(existing, cases, seq)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE tasks SET test_case_seq = ? WHERE id = ?`), seq, taskID); err != nil {
		return nil, apperr.Wrap(err, "task.errors.test_cases_seq", "task", taskID)
	}
	pos := 0
	for i := range existing {
		pos = max(pos, existing[i].Position)
	}
	for i := range cases {
		c := &cases[i]
		pos++
		c.ID, c.TaskID, c.Position = mustGenerateID(), taskID, pos
		c.CreatedBy, c.UpdatedBy, c.CreatedAt, c.UpdatedAt = author, author, now, now
		c.Runs = []TestCaseRun{}
		if err := s.insertCase(ctx, tx, c, ""); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

func newTestCase(in TestCase) (TestCase, error) {
	c := TestCase{
		Key: strings.TrimSpace(in.Key), Title: strings.TrimSpace(in.Title),
		Description: in.Description, Steps: in.Steps, Expected: in.Expected,
		Priority: in.Priority, Status: in.Status, Covered: in.Covered,
	}
	if c.Priority == "" {
		c.Priority = TestCasePriorityMedium
	}
	if c.Status == "" {
		c.Status = TestCasePending
	}
	if c.Key != "" && !ValidTestCaseKey(c.Key) {
		return c, apperr.Wrap(ErrInvalid, "task.errors.test_case_key_shape", "key", c.Key)
	}
	if err := validateTestCase(&c); err != nil {
		return c, err
	}
	return c, nil
}

func validateTestCase(c *TestCase) error {
	switch {
	case c.Title == "":
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_title_required")
	case utf8.RuneCountInString(c.Title) > maxTestCaseTitle:
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_title_too_long", "max", maxTestCaseTitle)
	case !ValidTestCasePriority(c.Priority):
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_unknown_priority", "priority", c.Priority)
	case !ValidTestCaseStatus(c.Status):
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_unknown_status", "status", c.Status)
	}
	return nil
}

// seq remembers keys of deleted cases, so a new case never takes over a deleted one's key (and the
// links and comments that name it).
func allocateKeys(existing, cases []TestCase, seq int) (int, error) {
	taken := make(map[string]bool, len(existing)+len(cases))
	next := max(seq, 0)
	reserve := func(key string) {
		taken[key] = true
		if m := autoCaseKeyRe.FindStringSubmatch(key); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				next = max(next, n)
			}
		}
	}
	for i := range existing {
		reserve(existing[i].Key)
	}
	for i := range cases {
		if k := cases[i].Key; k != "" {
			if taken[k] {
				return seq, apperr.Wrap(ErrConflict, "task.errors.test_case_key_taken", "key", k)
			}
			reserve(k)
		}
	}
	for i := range cases {
		c := &cases[i]
		for c.Key == "" {
			next++
			if k := "TC-" + strconv.Itoa(next); !taken[k] {
				c.Key = k
				taken[k] = true
			}
		}
	}
	return next, nil
}

func (s *Store) insertCase(ctx context.Context, ex execer, c *TestCase, suffix string) error {
	_, err := ex.ExecContext(ctx, s.rb(insertTestCase+suffix),
		c.ID, c.TaskID, c.Key, c.Title, c.Description, c.Steps, c.Expected,
		c.Priority, c.Status, c.Covered, c.Position, c.CreatedBy, c.UpdatedBy, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return apperr.Wrap(err, "task.errors.insert_test_case", "key", c.Key)
	}
	return nil
}

func (s *Store) UpdateTestCase(ctx context.Context, taskID, ref, author string, p TestCasePatch) (*TestCase, error) {
	var id string
	err := s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		c, err := s.findCase(ctx, tx, taskID, ref)
		if err != nil {
			return err
		}
		id = c.ID
		if p.Title != nil {
			c.Title = strings.TrimSpace(*p.Title)
		}
		patch.Set(&c.Description, p.Description)
		patch.Set(&c.Steps, p.Steps)
		patch.Set(&c.Expected, p.Expected)
		patch.Set(&c.Priority, p.Priority)
		patch.Set(&c.Status, p.Status)
		patch.Set(&c.Covered, p.Covered)
		if vErr := validateTestCase(c); vErr != nil {
			return vErr
		}
		if p.Position != nil {
			if c.Position, err = s.moveCase(ctx, tx, taskID, c.ID, *p.Position); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, s.rb(
			`UPDATE test_cases SET title=?, description=?, steps=?, expected=?, priority=?, status=?,
			        covered=?, position=?, updated_by=?, updated_at=? WHERE id=?`),
			c.Title, c.Description, c.Steps, c.Expected, c.Priority, c.Status,
			c.Covered, c.Position, author, now, c.ID)
		if err != nil {
			return apperr.Wrap(err, "task.errors.update_test_case", "key", c.Key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetTestCase(ctx, taskID, id)
}

func (s *Store) moveCase(ctx context.Context, tx *sql.Tx, taskID, caseID string, to int) (int, error) {
	cases, err := s.listCases(ctx, tx, taskID)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(cases))
	for i := range cases {
		if cases[i].ID != caseID {
			ids = append(ids, cases[i].ID)
		}
	}
	at := min(max(to, 1), len(ids)+1) - 1
	ids = slices.Insert(ids, at, caseID)
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, s.rb(`UPDATE test_cases SET position = ? WHERE id = ?`), i+1, id); err != nil {
			return 0, apperr.Wrap(err, "task.errors.reorder_test_case", "id", id)
		}
	}
	return at + 1, nil
}

func (s *Store) DeleteTestCase(ctx context.Context, taskID, ref string) error {
	return s.testCaseTx(ctx, taskID, func(tx *sql.Tx, _ time.Time) error {
		res, err := tx.ExecContext(ctx, s.rb(
			`DELETE FROM test_cases WHERE task_id = ? AND (id = ? OR key = ?)`), taskID, ref, ref)
		if err != nil {
			return apperr.Wrap(err, "task.errors.delete_test_case", "ref", ref)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.Wrap(ErrNotFound, "task.errors.test_case_ref", "ref", ref)
		}
		return nil
	})
}

func (s *Store) RecordTestCaseRun(ctx context.Context, taskID, ref string, run TestCaseRun) (*TestCaseRun, *TestCase, error) {
	if !ValidTestCaseVerdict(run.Verdict) {
		return nil, nil, apperr.Wrap(ErrInvalid, "task.errors.test_case_bad_verdict", "verdict", run.Verdict)
	}
	run.CaptureAttachmentIDs = dedupeStrings(run.CaptureAttachmentIDs)
	if err := s.checkRunEvidence(ctx, taskID, &run); err != nil {
		return nil, nil, err
	}
	captures, err := json.Marshal(run.CaptureAttachmentIDs)
	if err != nil {
		return nil, nil, apperr.Wrap(err, "task.errors.encode_captures")
	}
	var caseID string
	err = s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		c, findErr := s.findCase(ctx, tx, taskID, ref)
		if findErr != nil {
			return findErr
		}
		caseID = c.ID
		run.ID, run.TestCaseID, run.TaskID = mustGenerateID(), c.ID, taskID
		if run.RanAt.IsZero() {
			run.RanAt = now
		}
		if _, insErr := tx.ExecContext(ctx, s.rb(insertTestCaseRun),
			run.ID, run.TestCaseID, run.TaskID, run.Verdict, run.Observed, run.Note,
			run.VideoAttachmentID, run.TakeAttachmentID, run.ReportID, run.CommentID, string(captures),
			run.Author, run.RanAt); insErr != nil {
			return apperr.Wrap(insErr, "task.errors.insert_test_case_run", "key", c.Key)
		}
		if _, updErr := tx.ExecContext(ctx, s.rb(
			`UPDATE test_cases SET status = ?, covered = TRUE, updated_by = ?, updated_at = ? WHERE id = ?`),
			run.Verdict, run.Author, now, c.ID); updErr != nil {
			return apperr.Wrap(updErr, "task.errors.mark_test_case", "key", c.Key)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	c, err := s.GetTestCase(ctx, taskID, caseID)
	if err != nil {
		return nil, nil, err
	}
	return &run, c, nil
}

// Every evidence id must belong to taskID so a run cannot point at another tenant's file, report or
// comment.
// A foreign id gets ErrNotFound, the same answer as a missing one.
func (s *Store) checkRunEvidence(ctx context.Context, taskID string, run *TestCaseRun) error {
	ids := append([]string{run.VideoAttachmentID, run.TakeAttachmentID}, run.CaptureAttachmentIDs...)
	for _, id := range ids {
		if id == "" {
			continue
		}
		a, err := s.GetAttachment(ctx, id)
		if err != nil {
			return err
		}
		if a.TaskID != taskID {
			return apperr.Wrap(ErrNotFound, "task.errors.attachment_ref", "id", id)
		}
	}
	if err := belongsTo(ctx, taskID, run.ReportID, s.ReportBelongsTo, "task.errors.report_ref", "id", run.ReportID); err != nil {
		return err
	}
	return belongsTo(ctx, taskID, run.CommentID, s.CommentBelongsTo, "task.errors.comment_ref", "id", run.CommentID)
}

func belongsTo(ctx context.Context, taskID, id string, check func(context.Context, string, string) (bool, error), key string, args ...any) error {
	if id == "" {
		return nil
	}
	ok, err := check(ctx, taskID, id)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Wrap(ErrNotFound, key, args...)
	}
	return nil
}

func (s *Store) ImportTestCasesFromBody(ctx context.Context, taskID, author, body string) ([]TestCase, error) {
	items := bodyTestCases(body)
	created := []TestCase{}
	err := s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		existing, err := s.listCases(ctx, tx, taskID)
		if err != nil {
			return err
		}
		titles := make(map[string]bool, len(existing)+len(items))
		keys := make(map[string]bool, len(existing)+len(items))
		for i := range existing {
			titles[strings.ToLower(existing[i].Title)] = true
			keys[existing[i].Key] = true
		}
		var fresh []TestCase
		for i := range items {
			t, k := strings.ToLower(items[i].Title), items[i].Key
			if titles[t] || (k != "" && keys[k]) {
				continue
			}
			titles[t], keys[k] = true, true
			fresh = append(fresh, items[i])
		}
		if len(fresh) == 0 {
			return nil
		}
		created, err = s.insertNewCases(ctx, tx, taskID, author, existing, fresh, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Titles over maxTestCaseTitle are cut, not refused: the import is one-shot and the full line stays
// in the body.
func bodyTestCases(body string) []TestCase {
	_, sections := ParseSections(body)
	var out []TestCase
	for _, sec := range sections {
		if sec.Slug != "test-cases" {
			continue
		}
		for _, item := range sec.Items {
			key, title := splitBodyItemKey(strings.TrimSpace(item.Text))
			if title == "" {
				continue
			}
			if r := []rune(title); len(r) > maxTestCaseTitle {
				title = strings.TrimSpace(string(r[:maxTestCaseTitle]))
			}
			c := TestCase{Key: key, Title: title}
			if item.Checked {
				c.Status, c.Covered = TestCasePass, true
			}
			out = append(out, c)
		}
	}
	return out
}

func splitBodyItemKey(text string) (key, title string) {
	if m := bodyItemKeyRe.FindStringSubmatch(text); m != nil {
		return m[1], strings.TrimSpace(m[2])
	}
	return "", text
}

func (s *Store) CopyTestCases(ctx context.Context, fromTaskID, toTaskID, author string) error {
	cases, err := s.listCases(ctx, s.db, fromTaskID)
	if err != nil || len(cases) == 0 {
		return err
	}
	return s.testCaseTx(ctx, toTaskID, func(tx *sql.Tx, now time.Time) error {
		return s.copyTestCasesForClone(ctx, tx, toTaskID, author, cases, now)
	})
}

func (s *Store) copyTestCasesForClone(ctx context.Context, tx *sql.Tx, dstID, author string, cases []TestCase, now time.Time) error {
	for i := range cases {
		c := cases[i]
		c.ID, c.TaskID, c.Status, c.Covered = mustGenerateID(), dstID, TestCasePending, false
		if author != "" {
			c.CreatedBy = author
		}
		c.UpdatedBy, c.CreatedAt, c.UpdatedAt = c.CreatedBy, now, now
		if err := s.insertCase(ctx, tx, &c, ""); err != nil {
			return err
		}
	}
	return nil
}

// kept is false when the task already has a different case under the same key; the caller must then
// skip c's runs, which point at c's id.
func (s *Store) ImportTestCase(ctx context.Context, c TestCase) (kept bool, err error) {
	if c.ID == "" || c.TaskID == "" {
		return false, apperr.Invalid("task.errors.import_test_case_ids")
	}
	if err := s.insertCase(ctx, s.db, &c, ` ON CONFLICT DO NOTHING`); err != nil {
		return false, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM test_cases WHERE id = ?`), c.ID).Scan(&n); err != nil {
		return false, apperr.Wrap(err, "task.errors.import_test_case", "id", c.ID)
	}
	return n == 1, nil
}

// Evidence ids are not checked: the bundle carries the attachments and comments with their original
// ids.
func (s *Store) ImportTestCaseRun(ctx context.Context, r TestCaseRun) error {
	if r.ID == "" || r.TestCaseID == "" || r.TaskID == "" {
		return apperr.Invalid("task.errors.import_test_case_run_ids")
	}
	if r.CaptureAttachmentIDs == nil {
		r.CaptureAttachmentIDs = []string{}
	}
	captures, err := json.Marshal(r.CaptureAttachmentIDs)
	if err != nil {
		return apperr.Wrap(err, "task.errors.import_test_case_run", "id", r.ID)
	}
	if _, err = s.db.ExecContext(ctx, s.rb(insertTestCaseRun+` ON CONFLICT (id) DO NOTHING`),
		r.ID, r.TestCaseID, r.TaskID, r.Verdict, r.Observed, r.Note,
		r.VideoAttachmentID, r.TakeAttachmentID, r.ReportID, r.CommentID, string(captures),
		r.Author, r.RanAt); err != nil {
		return apperr.Wrap(err, "task.errors.import_test_case_run", "id", r.ID)
	}
	return nil
}

// Counts ignore the status filter: the QA view's status chips pick it and keep showing each one's
// total.
// Scoped callers see only tasks stamped with their workspace; the unstamped-row exemption
// does not apply here.
func (s *Store) ListWorkspaceTestCases(ctx context.Context, f TestCaseFilter) ([]TestCaseRow, TestCaseCounts, error) {
	where, args, err := f.where()
	if err != nil {
		return nil, TestCaseCounts{}, err
	}
	const from = ` FROM test_cases c JOIN tasks t ON t.id = c.task_id`
	anyStatus := f
	anyStatus.Statuses = nil
	countWhere, countArgs, err := anyStatus.where()
	if err != nil {
		return nil, TestCaseCounts{}, err
	}
	counts, err := s.countTestCases(ctx, from+countWhere, countArgs)
	if err != nil {
		return nil, TestCaseCounts{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultTestCaseListLimit
	}
	limit = min(limit, MaxTestCaseListLimit)
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+testCaseCols+`, t.title, t.stage, t.board_id,
		        (SELECT count(*) FROM test_case_runs r WHERE r.test_case_id = c.id)`+
			from+where+
			` ORDER BY t.updated_at DESC, t.id DESC, c.position ASC, c.id ASC LIMIT ?`),
		append(args, limit)...)
	if err != nil {
		return nil, TestCaseCounts{}, apperr.Wrap(err, "task.errors.list_workspace_test_cases")
	}
	defer rows.Close()
	out := []TestCaseRow{}
	for rows.Next() {
		var row TestCaseRow
		if row.TestCase, err = scanTestCase(rows, &row.TaskTitle, &row.TaskStage, &row.BoardID, &row.RunsCount); err != nil {
			return nil, TestCaseCounts{}, apperr.Wrap(err, "task.errors.scan_workspace_test_case")
		}
		row.TestCase.Runs = nil
		out = append(out, row)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, TestCaseCounts{}, rowsErr
	}
	if decErr := s.decorateLastRuns(ctx, out); decErr != nil {
		return nil, TestCaseCounts{}, decErr
	}
	return out, counts, nil
}

func (f TestCaseFilter) where() (string, []any, error) {
	var conds []string
	var args []any
	if !f.Unrestricted {
		conds, args = append(conds, `t.workspace_id = ?`), append(args, f.WorkspaceID)
	}
	if len(f.Statuses) > 0 {
		for _, st := range f.Statuses {
			if !ValidTestCaseStatus(st) {
				return "", nil, apperr.Wrap(ErrInvalid, "task.errors.test_case_unknown_status", "status", st)
			}
			args = append(args, st)
		}
		//i18n:ignore SQL fragment
		conds = append(conds, `c.status IN (`+placeholderList(len(f.Statuses))+`)`)
	}
	if f.BoardID != "" {
		conds, args = append(conds, `t.board_id = ?`), append(args, f.BoardID)
	}
	if f.ProjectID != "" {
		conds = append(conds, `EXISTS (SELECT 1 FROM task_projects tp WHERE tp.task_id = t.id AND tp.project_id = ?)`)
		args = append(args, f.ProjectID)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		pat := "%" + likeEscapement.Replace(q) + "%"
		conds = append(conds, `(c.key ILIKE ? OR c.title ILIKE ? OR t.title ILIKE ?)`)
		args = append(args, pat, pat, pat)
	}
	if f.Uncovered {
		conds = append(conds, `c.covered = FALSE`)
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return ` WHERE ` + strings.Join(conds, ` AND `), args, nil
}

type TestCaseCounts struct {
	Status  map[string]int
	Covered map[string]int
}

func (s *Store) countTestCases(ctx context.Context, fromWhere string, args []any) (TestCaseCounts, error) {
	counts := TestCaseCounts{Status: map[string]int{}, Covered: map[string]int{}}
	for _, st := range TestCaseStatuses {
		counts.Status[st], counts.Covered[st] = 0, 0
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT c.status, count(*), count(*) FILTER (WHERE c.covered)`+fromWhere+` GROUP BY c.status`), args...)
	if err != nil {
		return TestCaseCounts{}, apperr.Wrap(err, "task.errors.count_workspace_test_cases")
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n, covered int
		if err := rows.Scan(&st, &n, &covered); err != nil {
			return TestCaseCounts{}, err
		}
		counts.Status[st], counts.Covered[st] = n, covered
	}
	return counts, rows.Err()
}

func (s *Store) decorateLastRuns(ctx context.Context, out []TestCaseRow) error {
	if len(out) == 0 {
		return nil
	}
	args := make([]any, len(out))
	for i := range out {
		args[i] = out[i].ID
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT DISTINCT ON (r.test_case_id) `+testCaseRunCols+` FROM test_case_runs r
		 WHERE r.test_case_id IN (`+placeholderList(len(args))+`)`+
			` ORDER BY r.test_case_id, r.ran_at DESC, r.id DESC`), args...)
	if err != nil {
		return apperr.Wrap(err, "task.errors.last_test_case_runs")
	}
	defer rows.Close()
	last := make(map[string]*TestCaseRun, len(out))
	for rows.Next() {
		run, scanErr := scanTestCaseRun(rows)
		if scanErr != nil {
			return apperr.Wrap(scanErr, "task.errors.scan_last_test_case_run")
		}
		last[run.TestCaseID] = &run
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out {
		out[i].LastRun = last[out[i].ID]
	}
	return nil
}
