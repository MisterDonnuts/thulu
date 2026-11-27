package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Structured fields of test cases and their runs (task view v2): which criteria a case covers, its
// preconditions, what a blocked run needs from a person, and the round each run belongs to.

// Shares the task store's single sequence with Migrations.
var testCaseFieldMigrations = []migrate.Migration{
	{ID: 55, SQL: `ALTER TABLE test_cases
			ADD COLUMN IF NOT EXISTS covers TEXT NOT NULL DEFAULT '[]',
			ADD COLUMN IF NOT EXISTS preconditions TEXT NOT NULL DEFAULT ''`},
	{ID: 56, SQL: `ALTER TABLE test_case_runs
			ADD COLUMN IF NOT EXISTS needs TEXT NOT NULL DEFAULT '[]',
			ADD COLUMN IF NOT EXISTS tried TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS tried_result TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS writes_data BOOLEAN NOT NULL DEFAULT FALSE,
			ADD COLUMN IF NOT EXISTS round INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS vtt_attachment_id TEXT NOT NULL DEFAULT ''`},
}

// taskViewMigrations are the task view v2 ones (54-58) and the task skips (59), after
// commentKindMigration.
func taskViewMigrations() []migrate.Migration {
	out := append([]migrate.Migration(nil), criteriaMigrations...)
	out = append(out, testCaseFieldMigrations...)
	out = append(out, roundMigrations...)
	return append(out, stageSkipsMigration)
}

var (
	descCoversRe        = regexp.MustCompile(`(?i)\b(?:covers?|cubre)\s*:\s*([^\n]*)`)
	descPreconditionsRe = regexp.MustCompile(`(?is)\b(?:pre-?conditions?|precondiciones?)\s*:\s*(.+?)(?:\n\s*\n|$)`)
	criterionRefRe      = regexp.MustCompile(`\bAC-\d+\b`)
	sentenceEndRe       = regexp.MustCompile(`\.(?:\s|$)`)
)

// deriveCaseFields fills covers and preconditions from the description of a case written before
// those fields existed ("Covers: AC-2. Preconditions: …"). A field the writer set wins.
func deriveCaseFields(c *TestCase) {
	if len(c.Covers) == 0 {
		c.Covers = coversFromDescription(c.Description)
	}
	if c.Preconditions == "" {
		c.Preconditions = preconditionsFromDescription(c.Description)
	}
}

func coversFromDescription(desc string) []string {
	m := descCoversRe.FindStringSubmatch(desc)
	if m == nil {
		return []string{}
	}
	segment := m[1]
	if loc := sentenceEndRe.FindStringIndex(segment); loc != nil {
		segment = segment[:loc[0]]
	}
	return normalizeCovers(criterionRefRe.FindAllString(segment, -1))
}

func preconditionsFromDescription(desc string) string {
	m := descPreconditionsRe.FindStringSubmatch(desc)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// normalizeCovers trims and dedupes the keys, keeping their order; never nil.
func normalizeCovers(in []string) []string {
	out := make([]string, 0, len(in))
	for _, k := range in {
		if k = strings.TrimSpace(k); k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func coversJSON(covers []string) string {
	b, err := json.Marshal(normalizeCovers(covers))
	if err != nil {
		return "[]"
	}
	return string(b)
}

// checkCovers refuses a key that is not AC-n and, once the task has criteria, one it does not have,
// naming the valid ones.
func checkCovers(covers, criteria []string) error {
	for _, k := range covers {
		if !ValidCriterionKey(k) {
			return apperr.Wrap(ErrInvalid, "task.errors.test_case_covers_shape", "key", k)
		}
	}
	if len(criteria) == 0 {
		return nil
	}
	if unknown := unknownKeys(covers, criteria); len(unknown) > 0 {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_covers_unknown",
			"keys", strings.Join(unknown, ", "), "valid", strings.Join(criteria, ", "))
	}
	return nil
}

// prepareCases normalizes the cases to add and checks their covers against the task's criteria.
func prepareCases(in []TestCase, criteria []string) ([]TestCase, error) {
	cases := make([]TestCase, len(in))
	for i := range in {
		c, err := newTestCase(in[i])
		if err != nil {
			return nil, err
		}
		err = checkCovers(c.Covers, criteria)
		if err != nil {
			return nil, err
		}
		cases[i] = c
	}
	return cases, nil
}

// patchCaseFields applies a patch's covers and preconditions to c; covers are checked against the
// task's criteria.
func (s *Store) patchCaseFields(ctx context.Context, tx *sql.Tx, taskID string, c *TestCase, p TestCasePatch) error {
	if p.Preconditions != nil {
		c.Preconditions = strings.TrimSpace(*p.Preconditions)
	}
	if p.Covers == nil {
		return nil
	}
	criteria, err := s.criteriaKeys(ctx, tx, taskID)
	if err != nil {
		return err
	}
	covers := normalizeCovers(*p.Covers)
	err = checkCovers(covers, criteria)
	if err != nil {
		return err
	}
	c.Covers = covers
	return nil
}

// checkStatusPatch refuses the statuses only their own action sets: waived (a waiver with its
// reason) and queued (giving access). Keeping a case's current status is fine.
func checkStatusPatch(current string, next *string) error {
	switch {
	case next == nil || *next == current:
		return nil
	case *next == TestCaseWaived:
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_waive_by_status")
	case *next == TestCaseQueued:
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_queue_by_status")
	}
	return nil
}

// subtitlesFor finds the task's .vtt named after the video ("login.mp4" → "login.vtt", or
// "login.chapters.vtt"), the newest when there are several; "" when there is none.
func (s *Store) subtitlesFor(ctx context.Context, taskID, videoID string) string {
	if videoID == "" {
		return ""
	}
	video, err := s.GetAttachment(ctx, videoID)
	if err != nil {
		return ""
	}
	name := strings.ToLower(video.Filename)
	base := strings.TrimSuffix(name, path.Ext(name))
	atts, err := s.ListAttachments(ctx, taskID)
	if err != nil {
		return ""
	}
	exact, prefixed := "", ""
	var exactAt, prefixedAt time.Time
	for i := range atts {
		a := &atts[i]
		f := strings.ToLower(a.Filename)
		if !strings.HasSuffix(f, ".vtt") {
			continue
		}
		switch {
		case f == base+".vtt" && !a.CreatedAt.Before(exactAt):
			exact, exactAt = a.ID, a.CreatedAt
		case strings.HasPrefix(f, base+".") && !a.CreatedAt.Before(prefixedAt):
			prefixed, prefixedAt = a.ID, a.CreatedAt
		}
	}
	if exact != "" {
		return exact
	}
	return prefixed
}

// QueueTestCases marks the cases queued for the relaunch a person's access starts, in one
// transaction: an unknown ref queues none. It returns the cases and the status each had, so a
// relaunch that does not start can put them back.
func (s *Store) QueueTestCases(ctx context.Context, taskID string, refs []string, author string) ([]TestCase, map[string]string, error) {
	refs = dedupeStrings(refs)
	if len(refs) == 0 {
		return nil, nil, apperr.Wrap(ErrInvalid, "task.errors.test_case_queue_none")
	}
	var queued []TestCase
	prev := map[string]string{}
	err := s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		for _, ref := range refs {
			c, err := s.findCase(ctx, tx, taskID, strings.TrimSpace(ref))
			if err != nil {
				return err
			}
			if _, done := prev[c.ID]; done {
				continue
			}
			prev[c.ID] = c.Status
			if _, err := tx.ExecContext(ctx, s.rb(
				`UPDATE test_cases SET status = ?, updated_by = ?, updated_at = ? WHERE id = ?`),
				TestCaseQueued, author, now, c.ID); err != nil {
				return apperr.Wrap(err, "task.errors.mark_test_case", "key", c.Key)
			}
			c.Status = TestCaseQueued
			queued = append(queued, *c)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return queued, prev, nil
}

// RestoreTestCaseStatuses puts back the statuses QueueTestCases returned, on cases still queued.
func (s *Store) RestoreTestCaseStatuses(ctx context.Context, taskID string, prev map[string]string, author string) error {
	return s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		for id, status := range prev {
			if _, err := tx.ExecContext(ctx, s.rb(
				`UPDATE test_cases SET status = ?, updated_by = ?, updated_at = ? WHERE id = ? AND task_id = ? AND status = ?`),
				status, author, now, id, taskID, TestCaseQueued); err != nil {
				return apperr.Wrap(err, "task.errors.mark_test_case", "key", id)
			}
		}
		return nil
	})
}

// ReleaseQueuedTestCases takes every case still queued out of the queue, back to the verdict of
// its newest run (pending when it never ran): the run the access relaunched ended, never started or
// the task left the stage, and a case that stayed queued was not run. It returns how many it released.
func (s *Store) ReleaseQueuedTestCases(ctx context.Context, taskID, by string) int {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE test_cases SET status = COALESCE((SELECT r.verdict FROM test_case_runs r
		   WHERE r.test_case_id = test_cases.id ORDER BY r.ran_at DESC, r.id DESC LIMIT 1), ?),
		   updated_by = ?, updated_at = ?
		 WHERE task_id = ? AND status = ?`),
		TestCasePending, by, time.Now().UTC(), taskID, TestCaseQueued)
	if err != nil {
		logx.Warn(err, "task.log.release_queued_cases", "task", taskID)
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.notifyTask(ctx, taskID)
	}
	return int(n)
}

// SettleStops resolves the current stage's stops once no case is blocked any more.
func (s *Store) SettleStops(ctx context.Context, taskID, by string) {
	s.resolveStopsWhenCasesClear(ctx, taskID, by)
}
