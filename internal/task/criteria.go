package task

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Acceptance criteria live in task_criteria, one row per AC-n. The brief keeps an
// acceptance-criteria section generated from the rows, so agents read them where they always did;
// an agent that still writes that section as markdown fills the rows through the same parser.

// CriteriaSection is the brief section the rows are written to and parsed from.
const CriteriaSection = "acceptance-criteria"

// The heading is the section's address in the body, like the canonical sections.
const criteriaSectionTitle = "Acceptance criteria" //i18n:ignore section heading, the section's address in the body

const (
	maxCriteria       = 100
	maxCriterionRunes = 2000
)

var criterionKeyRe = regexp.MustCompile(`^AC-\d+$`)

// ValidCriterionKey reports whether k has the AC-n shape.
func ValidCriterionKey(k string) bool { return criterionKeyRe.MatchString(k) }

type Criterion struct {
	TaskID     string     `json:"task_id"`
	Key        string     `json:"key"`
	Text       string     `json:"text"`
	Position   int        `json:"position"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// CriterionInput is one criterion as a writer gives it.
type CriterionInput struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// CriteriaChange is what a write did to the list, by key.
type CriteriaChange struct {
	Changed []string
	Added   []string
	Removed []string
}

func (c CriteriaChange) empty() bool { return len(c.Changed)+len(c.Added)+len(c.Removed) == 0 }

// Keys is every key the write touched, sorted by number.
func (c CriteriaChange) Keys() []string {
	out := slices.Concat(c.Changed, c.Added, c.Removed)
	sortCriterionKeys(out)
	return out
}

// Shares the task store's single sequence with Migrations.
var criteriaMigrations = []migrate.Migration{
	{ID: 54, SQL: `CREATE TABLE IF NOT EXISTS task_criteria (
			task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			key         TEXT NOT NULL,
			text        TEXT NOT NULL,
			position    INTEGER NOT NULL DEFAULT 0,
			approved_by TEXT NOT NULL DEFAULT '',
			approved_at TIMESTAMPTZ,
			created_at  TIMESTAMPTZ NOT NULL,
			updated_at  TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (task_id, key)
		)`},
}

const criterionCols = `task_id, key, text, position, approved_by, approved_at, created_at, updated_at`

func scanCriterion(r rowScanner) (Criterion, error) {
	var c Criterion
	var at sql.NullTime
	if err := r.Scan(&c.TaskID, &c.Key, &c.Text, &c.Position, &c.ApprovedBy, &at, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	if at.Valid {
		t := at.Time.UTC()
		c.ApprovedAt = &t
	}
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

func (s *Store) ListCriteria(ctx context.Context, taskID string) ([]Criterion, error) {
	return s.listCriteria(ctx, s.db, taskID)
}

func (s *Store) listCriteria(ctx context.Context, q rowsQueryer, taskID string) ([]Criterion, error) {
	rows, err := q.QueryContext(ctx, s.rb(
		`SELECT `+criterionCols+` FROM task_criteria WHERE task_id = ? ORDER BY position ASC, key ASC`), taskID)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_criteria", "task", taskID)
	}
	defer rows.Close()
	out := []Criterion{}
	for rows.Next() {
		c, scanErr := scanCriterion(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "task.errors.list_criteria", "task", taskID)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// criteriaKeys is the set of keys the task's list has; empty when it has none.
func (s *Store) criteriaKeys(ctx context.Context, q rowsQueryer, taskID string) ([]string, error) {
	list, err := s.listCriteria(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(list))
	for i := range list {
		keys[i] = list[i].Key
	}
	return keys, nil
}

// checkCriteria trims every criterion and refuses a list a writer must fix.
func checkCriteria(in []CriterionInput) ([]CriterionInput, error) {
	if len(in) > maxCriteria {
		return nil, apperr.Wrap(ErrInvalid, "task.errors.too_many_criteria", "max", maxCriteria)
	}
	out := make([]CriterionInput, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, c := range in {
		c.Key, c.Text = strings.TrimSpace(c.Key), strings.TrimSpace(c.Text)
		switch {
		case !ValidCriterionKey(c.Key):
			return nil, apperr.Wrap(ErrInvalid, "task.errors.criterion_key_shape", "key", c.Key)
		case seen[c.Key]:
			return nil, apperr.Wrap(ErrInvalid, "task.errors.criterion_key_taken", "key", c.Key)
		case c.Text == "":
			return nil, apperr.Wrap(ErrInvalid, "task.errors.criterion_text_required", "key", c.Key)
		case utf8.RuneCountInString(c.Text) > maxCriterionRunes:
			return nil, apperr.Wrap(ErrInvalid, "task.errors.criterion_text_too_long", "key", c.Key, "max", maxCriterionRunes)
		}
		seen[c.Key] = true
		out = append(out, c)
	}
	return out, nil
}

// SetCriteria replaces the task's list and rewrites the acceptance-criteria section of the brief
// from it, in one transaction. A criterion whose text did not change keeps its approval.
func (s *Store) SetCriteria(ctx context.Context, taskID, actor string, in []CriterionInput) ([]Criterion, error) {
	list, err := checkCriteria(in)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.criteria_write", "task", taskID)
	}
	defer func() { _ = tx.Rollback() }()
	t, err := s.getRawTx(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	change, err := s.replaceCriteria(ctx, tx, taskID, list, now, false)
	if err != nil {
		return nil, err
	}
	body, err := withCriteriaSection(t.Body, list)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, s.rb(`UPDATE tasks SET body = ?, updated_at = ? WHERE id = ?`), body, now, taskID); err != nil {
		return nil, apperr.Wrap(err, "task.errors.criteria_write", "task", taskID)
	}
	if err = tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "task.errors.criteria_write", "task", taskID)
	}
	t.Body, t.UpdatedAt = body, now
	s.notify(ChangeUpdated, t)
	s.afterCriteriaChange(ctx, taskID, actor, change)
	return s.ListCriteria(ctx, taskID)
}

// replaceCriteria writes list as the task's rows. Rows keep their created_at; the approval stays
// only on a criterion whose text is the same. fromBody says list was parsed back from the brief:
// the section cannot carry a blank line or a leading mark, so a stored text that reads the same
// once written to the section and parsed back is the same criterion, and keeps its exact text.
func (s *Store) replaceCriteria(ctx context.Context, tx *sql.Tx, taskID string, list []CriterionInput, now time.Time,
	fromBody bool,
) (CriteriaChange, error) {
	old, err := s.listCriteria(ctx, tx, taskID)
	if err != nil {
		return CriteriaChange{}, err
	}
	byKey := make(map[string]Criterion, len(old))
	for i := range old {
		byKey[old[i].Key] = old[i]
	}
	var change CriteriaChange
	kept := make(map[string]bool, len(list))
	if _, err = tx.ExecContext(ctx, s.rb(`DELETE FROM task_criteria WHERE task_id = ?`), taskID); err != nil {
		return change, apperr.Wrap(err, "task.errors.criteria_write", "task", taskID)
	}
	for i, in := range list {
		row := Criterion{TaskID: taskID, Key: in.Key, Text: in.Text, Position: i + 1, CreatedAt: now, UpdatedAt: now}
		if prev, ok := byKey[in.Key]; ok {
			row.CreatedAt = prev.CreatedAt
			if fromBody && prev.Text != in.Text && sameInSection(prev.Key, prev.Text, in.Text) {
				row.Text = prev.Text
			}
			if prev.Text == row.Text {
				row.ApprovedBy, row.ApprovedAt, row.UpdatedAt = prev.ApprovedBy, prev.ApprovedAt, prev.UpdatedAt
			} else {
				change.Changed = append(change.Changed, in.Key)
			}
		} else {
			change.Added = append(change.Added, in.Key)
		}
		kept[in.Key] = true
		if err := s.insertCriterion(ctx, tx, row, ""); err != nil {
			return change, err
		}
	}
	for i := range old {
		if !kept[old[i].Key] {
			change.Removed = append(change.Removed, old[i].Key)
		}
	}
	return change, nil
}

func (s *Store) insertCriterion(ctx context.Context, ex execer, c Criterion, suffix string) error {
	var at any
	if c.ApprovedAt != nil {
		at = *c.ApprovedAt
	}
	if _, err := ex.ExecContext(ctx, s.rb(`INSERT INTO task_criteria (`+criterionCols+`) VALUES (?,?,?,?,?,?,?,?)`+suffix),
		c.TaskID, c.Key, c.Text, c.Position, c.ApprovedBy, at, c.CreatedAt, c.UpdatedAt); err != nil {
		return apperr.Wrap(err, "task.errors.criteria_write", "task", c.TaskID)
	}
	return nil
}

// syncCriteriaFromBody keeps the rows in step with an acceptance-criteria section someone wrote as
// markdown (an agent from before task_criteria, a person editing the description). Only a section
// whose text changed counts; one that lost every AC-n line but still has prose leaves the rows as
// they are, and a section removed or emptied clears them.
func (s *Store) syncCriteriaFromBody(ctx context.Context, tx *sql.Tx, taskID, oldBody, newBody string, now time.Time) (CriteriaChange, error) {
	before, hadBefore := criteriaContent(oldBody)
	after, hasAfter := criteriaContent(newBody)
	if hadBefore == hasAfter && strings.TrimSpace(before) == strings.TrimSpace(after) {
		return CriteriaChange{}, nil
	}
	list := ParseCriteria(after)
	if len(list) == 0 && strings.TrimSpace(after) != "" {
		return CriteriaChange{}, nil
	}
	return s.replaceCriteria(ctx, tx, taskID, list, now, true)
}

// sameInSection reports whether two texts of key read the same once written to the section and
// parsed back.
func sameInSection(key, a, b string) bool {
	return sectionText(key, a) == sectionText(key, b)
}

func sectionText(key, text string) string {
	got := ParseCriteria(FormatCriteria([]CriterionInput{{Key: key, Text: text}}))
	if len(got) == 0 {
		return ""
	}
	return got[0].Text
}

func criteriaContent(body string) (string, bool) {
	_, secs := ParseSections(body)
	for _, sec := range secs {
		if sec.Slug == CriteriaSection {
			return sec.Content, true
		}
	}
	return "", false
}

// withCriteriaSection rewrites the acceptance-criteria section of body from list; an empty list
// removes it.
func withCriteriaSection(body string, list []CriterionInput) (string, error) {
	if len(list) == 0 {
		out, _ := RemoveSection(body, CriteriaSection)
		return out, nil
	}
	title := ""
	if _, ok := criteriaContent(body); !ok {
		title = criteriaSectionTitle
	}
	return SetSection(body, CriteriaSection, title, FormatCriteria(list))
}

// FormatCriteria writes one criterion per line, "AC-n  text"; the lines after a criterion's first
// are indented under its text, as spec-writing shows them.
func FormatCriteria(list []CriterionInput) string {
	var b strings.Builder
	for i, c := range list {
		if i > 0 {
			b.WriteByte('\n')
		}
		pad := strings.Repeat(" ", len(c.Key)+2)
		for j, line := range strings.Split(c.Text, "\n") {
			if j == 0 {
				b.WriteString(c.Key + "  " + strings.TrimSpace(line))
				continue
			}
			if line = strings.TrimSpace(line); line != "" {
				b.WriteString("\n" + pad + line)
			}
		}
	}
	return b.String()
}

var (
	criterionLineRe = regexp.MustCompile(
		`^\s*(?:[-*+]\s+|\d+[.)]\s+)?(?:\[[ xX]\]\s+)?(?:\*\*|__)?(AC-\d+)(?:\*\*|__|\b)\s*[:.)\-–—]?\s*(?:\*\*|__)?\s*(.*)$`)
	criterionCellRe = regexp.MustCompile(`^(?:\*\*|__)?(AC-\d+)(?:\*\*|__)?$`)
	tableRuleRe     = regexp.MustCompile(`^\|?[\s:|-]+\|?$`)
)

// ParseCriteria reads the AC-n criteria of a markdown section: a line that starts with AC-n (after
// an optional bullet, number, checkbox or bold) opens one, the lines under it until a blank line
// continue it, and a table row with an AC-n cell is one. Fence lines are dropped, their content
// is read. A repeated key keeps its first text.
func ParseCriteria(content string) []CriterionInput {
	var p criteriaParser
	for _, line := range strings.Split(content, "\n") {
		p.line(line)
	}
	p.flush()
	return p.out
}

type criteriaParser struct {
	out  []CriterionInput
	seen map[string]bool
	cur  *CriterionInput
}

func (p *criteriaParser) flush() {
	if p.cur == nil {
		return
	}
	c := p.cur
	p.cur = nil
	c.Text = strings.TrimSpace(c.Text)
	if c.Text == "" || p.seen[c.Key] {
		return
	}
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	p.seen[c.Key] = true
	p.out = append(p.out, *c)
}

func (p *criteriaParser) line(line string) {
	trimmed := strings.TrimSpace(line)
	switch {
	case fenceRe.MatchString(line), trimmed == "", strings.HasPrefix(trimmed, "#"):
		p.flush()
	case strings.HasPrefix(trimmed, "|"):
		p.flush()
		if c, ok := criterionFromRow(trimmed); ok {
			p.cur = &c
			p.flush()
		}
	default:
		if m := criterionLineRe.FindStringSubmatch(line); m != nil {
			p.flush()
			p.cur = &CriterionInput{Key: m[1], Text: strings.TrimSpace(m[2])}
			return
		}
		if p.cur == nil {
			return
		}
		if p.cur.Text != "" {
			p.cur.Text += "\n"
		}
		p.cur.Text += trimmed
	}
}

func criterionFromRow(row string) (CriterionInput, bool) {
	if tableRuleRe.MatchString(row) {
		return CriterionInput{}, false
	}
	cells := strings.Split(strings.Trim(row, "|"), "|")
	key := ""
	var text []string
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if m := criterionCellRe.FindStringSubmatch(cell); m != nil && key == "" {
			key = m[1]
			continue
		}
		if cell != "" {
			text = append(text, cell)
		}
	}
	if key == "" {
		return CriterionInput{}, false
	}
	return CriterionInput{Key: key, Text: strings.Join(text, " ")}, true
}

// ApproveCriteria records by's approval on the named criteria, every one when keys is empty.
func (s *Store) ApproveCriteria(ctx context.Context, taskID, by string, keys []string) ([]Criterion, error) {
	list, err := s.ListCriteria(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apperr.Wrap(ErrInvalid, "task.errors.criteria_none")
	}
	have := make([]string, len(list))
	for i := range list {
		have[i] = list[i].Key
	}
	keys = dedupeStrings(keys)
	if len(keys) == 0 {
		keys = have
	}
	if unknown := unknownKeys(keys, have); len(unknown) > 0 {
		return nil, apperr.Wrap(ErrInvalid, "task.errors.criteria_unknown",
			"keys", strings.Join(unknown, ", "), "valid", strings.Join(have, ", "))
	}
	now := time.Now().UTC()
	args := []any{by, now, taskID}
	for _, k := range keys {
		args = append(args, k)
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE task_criteria SET approved_by = ?, approved_at = ?
		WHERE task_id = ? AND key IN (`+placeholderList(len(keys))+`)`), args...); err != nil {
		return nil, apperr.Wrap(err, "task.errors.criteria_write", "task", taskID)
	}
	s.notifyTask(ctx, taskID)
	return s.ListCriteria(ctx, taskID)
}

// ImportCriteria replaces the task's rows with the ones an export bundle carries, approvals
// included; the rows Create derived from the imported body give way. No rows leaves them as they are.
func (s *Store) ImportCriteria(ctx context.Context, taskID string, list []Criterion) error {
	if len(list) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "task.errors.import_criteria", "task", taskID)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, s.rb(`DELETE FROM task_criteria WHERE task_id = ?`), taskID); err != nil {
		return apperr.Wrap(err, "task.errors.import_criteria", "task", taskID)
	}
	now := time.Now().UTC()
	for i := range list {
		c := list[i]
		if !ValidCriterionKey(c.Key) {
			return apperr.Invalid("task.errors.import_criterion", "key", c.Key)
		}
		c.TaskID = taskID
		c.CreatedAt, c.UpdatedAt = nonZeroTime(c.CreatedAt, now), nonZeroTime(c.UpdatedAt, now)
		err = s.insertCriterion(ctx, tx, c, ` ON CONFLICT (task_id, key) DO NOTHING`)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return apperr.Wrap(err, "task.errors.import_criteria", "task", taskID)
	}
	return nil
}

func unknownKeys(keys, have []string) []string {
	var out []string
	for _, k := range keys {
		if !slices.Contains(have, k) {
			out = append(out, k)
		}
	}
	return out
}

// afterCriteriaChange tells Activity when a criterion moved under tests that were already locked:
// the cases that cover it may no longer prove it.
func (s *Store) afterCriteriaChange(ctx context.Context, taskID, actor string, change CriteriaChange) {
	if change.empty() || !s.testsLocked(ctx, taskID) {
		return
	}
	keys := change.Keys()
	cases, err := s.ListTestCases(ctx, taskID)
	logx.Warn(err, "task.log.criteria_changed", "task", taskID)
	var covering []string
	for i := range cases {
		if slices.ContainsFunc(cases[i].Covers, func(k string) bool { return slices.Contains(keys, k) }) {
			covering = append(covering, cases[i].Key)
		}
	}
	if actor == "" {
		actor = "octopus"
	}
	logx.Warn(s.RecordEvent(ctx, taskID, EventCriteriaChanged, "", actor,
		i18n.Text("task.criteria.events.changed", "keys", strings.Join(keys, ", ")),
		map[string]any{"keys": keys, "changed": nonNilStrings(change.Changed), "added": nonNilStrings(change.Added),
			"removed": nonNilStrings(change.Removed), "cases": nonNilStrings(covering)}),
		"task.log.criteria_changed", "task", taskID)
}

func (s *Store) testsLocked(ctx context.Context, taskID string) bool {
	var locked bool
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT EXISTS (SELECT 1 FROM workflow_events WHERE task_id = ? AND kind = ?)`), taskID, EventTestsLocked).Scan(&locked)
	logx.Warn(err, "task.log.criteria_changed", "task", taskID)
	return locked
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// sortCriterionKeys orders AC-n keys by n.
func sortCriterionKeys(keys []string) {
	num := func(k string) int {
		n := 0
		for _, r := range strings.TrimPrefix(k, "AC-") {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	sort.SliceStable(keys, func(i, j int) bool { return num(keys[i]) < num(keys[j]) })
}
