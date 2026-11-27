package task

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// A waiver is a Testing decision: a person skips cases that need them (a real account, a device, a
// data write) and says why. Each case gets a test_case_runs row with verdict waived, the person as
// author and the reason as note, so the tally, the History grid and the readiness check all read
// it as a known gap.

func checkWaiveReason(reason *string) error {
	*reason = strings.TrimSpace(*reason)
	switch {
	case *reason == "":
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_waive_reason")
	case utf8.RuneCountInString(*reason) > maxWaiveReasonRunes:
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_waive_reason_too_long", "max", maxWaiveReasonRunes)
	}
	return nil
}

// WaiveTestCases records a waiver run on each case in one transaction: an unknown ref waives none.
func (s *Store) WaiveTestCases(ctx context.Context, taskID string, refs []string, author, reason, headRef string) ([]TestCase, error) {
	if err := checkWaiveReason(&reason); err != nil {
		return nil, err
	}
	refs = dedupeStrings(refs)
	if len(refs) == 0 {
		return nil, apperr.Wrap(ErrInvalid, "task.errors.test_case_waive_none")
	}
	var waived []TestCase
	unblocked := false
	round := s.TaskRound(ctx, taskID)
	err := s.testCaseTx(ctx, taskID, func(tx *sql.Tx, now time.Time) error {
		for _, ref := range refs {
			c, err := s.findCase(ctx, tx, taskID, strings.TrimSpace(ref))
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.rb(insertTestCaseRun),
				mustGenerateID(), c.ID, taskID, TestCaseWaived, "", reason, "", "", "", "", "[]",
				author, headRef, "[]", "", "", false, round, "", now); err != nil {
				return apperr.Wrap(err, "task.errors.insert_test_case_run", "key", c.Key)
			}
			if _, err := tx.ExecContext(ctx, s.rb(
				`UPDATE test_cases SET status = ?, updated_by = ?, updated_at = ? WHERE id = ?`),
				TestCaseWaived, author, now, c.ID); err != nil {
				return apperr.Wrap(err, "task.errors.mark_test_case", "key", c.Key)
			}
			unblocked = unblocked || c.Status == TestCaseBlocked
			c.Status = TestCaseWaived
			waived = append(waived, *c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if unblocked {
		s.resolveStopsWhenCasesClear(ctx, taskID, author)
	}
	return waived, nil
}

// WaiveResult is what POST /tasks/{id}/test-cases/waive answers.
type WaiveResult struct {
	Waived   int   `json:"waived"`
	Advanced bool  `json:"advanced"`
	Stage    Stage `json:"stage"`
	// The keys of the cases waived.
	Cases []string `json:"cases"`
}

// WaiveTestCases records the waiver and, when the task is blocked in a stage whose needs-person
// cases are now all passed or waived, unblocks it and moves it on as a continue verdict would.
func (d *Dispatcher) WaiveTestCases(ctx context.Context, t *Task, refs []string, author, reason string) (WaiveResult, error) {
	waived, err := d.store.WaiveTestCases(ctx, t.ID, refs, author, reason, d.currentRef(ctx, t))
	if err != nil {
		return WaiveResult{}, err
	}
	for i := range waived {
		c := &waived[i]
		logx.Warn(d.store.RecordEvent(ctx, t.ID, EventTestCaseRun, c.ID, author,
			fmt.Sprintf("%s · %s → %s", c.Key, c.Title, TestCaseWaived), //i18n:ignore timeline arrow
			map[string]string{"verdict": TestCaseWaived, "reason": strings.TrimSpace(reason)}),
			"task.log.record_test_case_waived", "task", t.ID)
	}
	res := WaiveResult{Waived: len(waived), Stage: t.Stage, Cases: make([]string, 0, len(waived))}
	for i := range waived {
		res.Cases = append(res.Cases, waived[i].Key)
	}
	fresh, err := d.store.Get(ctx, t.ID)
	if err != nil {
		return res, nil
	}
	if fresh.Status != StatusBlocked || !d.needsPersonSettled(ctx, fresh) {
		res.Stage = fresh.Stage
		return res, nil
	}
	// The waiver arrives on an HTTP request whose context ends with the answer; the next stage's
	// agent launches in the background and must outlive it.
	d.autoAdvanceStage(context.WithoutCancel(ctx), fresh, fresh.ID, fresh.Stage, 0, "")
	if after, err := d.store.GetRow(ctx, t.ID); err == nil {
		res.Stage = after.Stage
		res.Advanced = after.Stage != fresh.Stage
	}
	return res, nil
}

// Whether every case the current stage left to a person is now passed or waived. A stage that named
// no case (or a verdict about something else) never counts as settled by a waiver.
func (d *Dispatcher) needsPersonSettled(ctx context.Context, t *Task) bool {
	refs := d.needsPersonCases(t, d.stageContext(ctx, t))
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		i := slices.IndexFunc(t.TestCases, func(c TestCase) bool { return c.Key == ref || c.ID == ref })
		if i < 0 {
			return false
		}
		if st := t.TestCases[i].Status; st != TestCasePass && st != TestCaseWaived {
			return false
		}
	}
	return true
}
