package task

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Attention says what a task needs from a person, worked out by the daemon so the task view opens
// on one card instead of a banner, a chip and two buttons that each tell part of it. nil when
// nothing needs a person (a running task, a task waiting on nobody).
type Attention struct {
	Kind     string `json:"kind"`
	Headline string `json:"headline"`
	Stage    Stage  `json:"stage"`
	RunID    string `json:"run_id"`
	// The stage's verdict did not reach the daemon: the items come from the run's test cases.
	VerdictLost bool            `json:"verdict_lost"`
	Items       []AttentionItem `json:"items"`
	Options     []string        `json:"options"`
	ApprovalID  string          `json:"approval_id,omitempty"`
	QuestionID  string          `json:"question_id,omitempty"`
	// The comment thread a person answers in: the open stop (kind block) of a blocked task, the
	// approval's thread, or the question's comment. "" when there is none (older tasks).
	ThreadID string `json:"thread_id,omitempty"`
}

type AttentionItem struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	Ask   string `json:"ask"`
	Why   string `json:"why"`
}

// An approval the task meets later: required in a stage ahead, or approved on an older head of the
// branch (stale) and so asked again after the next run.
type UpcomingApproval struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Label      string `json:"label"`
	Stage      Stage  `json:"stage"`
	StageLabel string `json:"stage_label"`
	Stale      bool   `json:"stale"`
}

const (
	AttentionDecision = "decision"
	AttentionApproval = "approval"
	AttentionQuestion = "question"
	AttentionFailure  = "failure"
)

const (
	OptionTestByHand     = "test_by_hand"
	OptionWaiveCases     = "waive_cases"
	OptionSendBack       = "send_back"
	OptionAnswerQuestion = "answer_question"
	OptionDecideApproval = "decide_approval"
	OptionRerun          = "rerun"
)

// What the task's visit to its current stage left: when it entered, the verdict that stopped it
// there (the agent's own, or the auditor's when the auditor blocked) and the latest run that
// finished there.
type stageVisit struct {
	entered  time.Time
	verdict  *stageVerdict
	runID    string
	instance string
	lost     bool
	// The auditor stopped the task (blocked, failed or left no verdict): the agent's verdict is not
	// what stopped it. When it blocked, verdict is its feedback and auditorNote the comment it left
	// for a person (auditor-comment.md) since the task entered the stage.
	byAuditor   bool
	auditorNote string
}

func (d *Dispatcher) stageContext(ctx context.Context, t *Task) stageVisit {
	v := stageVisit{entered: d.store.lastStageEntry(ctx, t.ID, t.Stage)}
	reports, err := d.store.ListReports(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "task.log.attention_reports", "task", t.ID)
	}
	v.runID, v.instance = d.store.lastStageRun(ctx, t.ID, t.Stage, v.entered)
	cause := ""
	if t.Block != nil {
		cause = t.Block.Cause
	}
	switch cause {
	case BlockAuditor:
		// The agent's verdict (often continue, with a summary of its run) is not what stopped the
		// task: the card speaks with the auditor's ask.
		v.byAuditor = true
		v.verdict = latestVerdict(reports, ReportKindAuditorFeedback, t.Stage, v.entered)
		v.auditorNote = auditorNote(t.Comments, v.entered)
	case BlockAuditorFailed, BlockAuditorNoVerdict:
		// No verdict stopped it: the auditor did not finish.
		v.byAuditor = true
	default:
		v.verdict = latestVerdict(reports, ReportKindStageVerdict, t.Stage, v.entered)
		v.lost = cause == BlockVerdictUndelivered ||
			(v.verdict == nil && d.store.OutputsUndelivered(ctx, t.ID, v.instance))
	}
	return v
}

// latestVerdict is the newest readable verdict of kind left in stage since the task entered it.
func latestVerdict(reports []Report, kind string, stage Stage, since time.Time) *stageVerdict {
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		if r.Kind != kind || r.Stage != stage || r.CreatedAt.Before(since) {
			continue
		}
		if sv := parseVerdict(r.Body); sv.Verdict != "" {
			return &sv
		}
	}
	return nil
}

// auditorNote is the newest top-level comment the auditor posted since the task entered the stage.
func auditorNote(comments []Comment, since time.Time) string {
	for i := len(comments) - 1; i >= 0; i-- {
		c := &comments[i]
		if c.Source == SourceAuditor && c.ParentID == "" && !c.CreatedAt.Before(since) {
			if body := strings.TrimSpace(c.Body); body != "" {
				return body
			}
		}
	}
	return ""
}

// lastStageEntry is when the task last entered stage; zero when it never did (created there).
func (s *Store) lastStageEntry(ctx context.Context, taskID string, stage Stage) time.Time {
	var at sql.NullTime
	logx.Warn(s.db.QueryRowContext(ctx, s.rb(
		`SELECT max(created_at) FROM workflow_events
		 WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'to') = ?`),
		taskID, EventStageEntered, string(stage)).Scan(&at), "task.log.attention_events", "task", taskID)
	if !at.Valid {
		return time.Time{}
	}
	return at.Time
}

// lastStageRun names the latest run of stage that finished since the task entered it.
func (s *Store) lastStageRun(ctx context.Context, taskID string, stage Stage, since time.Time) (runID, instance string) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT payload FROM workflow_events
		 WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'stage') = ? AND created_at >= ?
		 ORDER BY created_at DESC LIMIT 1`),
		taskID, EventAgentRunFinished, string(stage), since).Scan(&raw)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logx.Warn(err, "task.log.attention_events", "task", taskID)
		}
		return "", ""
	}
	var p struct {
		ContainerID string `json:"container_id"`
		InstanceID  string `json:"instance_id"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		logx.Warn(err, "task.log.attention_events", "task", taskID)
	}
	return p.ContainerID, p.InstanceID
}

// needsPersonCases lists the cases the current stage left to a person: the ones its verdict names
// in needs_person, else (no verdict, or an agent's block that names none) every case with a blocked
// or failed run since the task entered the stage. A waiver or a pass recorded afterwards does not take
// a case off this list; it settles it.
func (d *Dispatcher) needsPersonCases(t *Task, v stageVisit) []string {
	var refs []string
	if v.verdict != nil {
		for _, n := range v.verdict.NeedsPerson {
			if c := findCase(t.TestCases, n.Ref); c != nil && !slices.Contains(refs, c.Key) {
				refs = append(refs, c.Key)
			}
		}
		if len(refs) > 0 || len(v.verdict.NeedsPerson) > 0 || v.verdict.Verdict != verdictBlock {
			return refs
		}
	}
	// The cases the agent's runs left stuck are not what an auditor stopped the task for; waiving
	// them must not walk the task past the auditor.
	if v.byAuditor {
		return refs
	}
	for i := range t.TestCases {
		c := &t.TestCases[i]
		if slices.ContainsFunc(c.Runs, func(r TestCaseRun) bool {
			return !r.RanAt.Before(v.entered) && (r.Verdict == TestCaseBlocked || r.Verdict == TestCaseFail)
		}) {
			refs = append(refs, c.Key)
		}
	}
	return refs
}

func findCase(cases []TestCase, ref string) *TestCase {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	for i := range cases {
		if strings.EqualFold(cases[i].Key, ref) || cases[i].ID == ref {
			return &cases[i]
		}
	}
	return nil
}

func caseSettled(c *TestCase) bool { return c.Status == TestCasePass || c.Status == TestCaseWaived }

// Attention works out the card and the later approvals for GET /tasks/{id}. t must come from the
// store's Get (approvals, questions, test cases and block filled in).
func (d *Dispatcher) Attention(ctx context.Context, t *Task) (*Attention, []UpcomingApproval) {
	if t == nil {
		return nil, nil
	}
	upcoming := d.upcomingApprovals(ctx, t)
	stages := d.stagesFor(t)
	info, _ := stages.Get(t.Stage)
	if info.Terminal || t.Status == StatusRunning || t.Status == StatusQueued {
		return nil, upcoming
	}
	// The stop of the stage the task is in is the newest thing that needs a person: an open
	// question, from this stage or one before, never covers it.
	if t.Status == StatusBlocked {
		return d.blockedAttention(ctx, t), upcoming
	}
	q := currentQuestion(t)
	if a := d.approvalAttention(t, info); a != nil && !questionOutranks(q, t, a.ApprovalID) {
		return a, upcoming
	}
	if q != nil {
		return questionAttention(t, q), upcoming
	}
	return d.failureAttention(ctx, t), upcoming
}

// currentQuestion is the open question of the stage the task is in, a blocking one first. A
// question another stage asked is left out: its run is over, a non-blocking one went on with its
// default and a blocking one is no longer waited on. A question with no stage (older rows) counts
// as the current stage's.
func currentQuestion(t *Task) *Question {
	var soft *Question
	for i := range t.Questions {
		q := &t.Questions[i]
		if q.Status != QuestionOpen || (q.Stage != "" && q.Stage != t.Stage) {
			continue
		}
		if q.Blocking {
			return q
		}
		if soft == nil {
			soft = q
		}
	}
	return soft
}

// A pending approval of the stage outranks an older question and any non-blocking one; only a
// blocking question asked after it, whose run waits on the answer, comes first.
func questionOutranks(q *Question, t *Task, approvalID string) bool {
	if q == nil || !q.Blocking {
		return false
	}
	for i := range t.Approvals {
		if t.Approvals[i].ID == approvalID {
			return q.CreatedAt.After(t.Approvals[i].CreatedAt)
		}
	}
	return false
}

func questionAttention(t *Task, q *Question) *Attention {
	a := &Attention{Kind: AttentionQuestion, Stage: t.Stage, RunID: q.RunID, QuestionID: q.ID,
		ThreadID: q.CommentID, Options: []string{OptionAnswerQuestion}, Items: []AttentionItem{}}
	first := ""
	for _, item := range q.Payload.Questions {
		a.Items = append(a.Items, AttentionItem{Ref: item.Header, Title: item.Header, Ask: item.Question,
			Why: NeedsPersonWhyDecision})
		if first == "" {
			first = item.Question
		}
	}
	a.Headline = NoteOf("task.attention.question", "agent", q.Agent,
		"question", cutRunes(first, maxVerdictHeadline)).Text()
	return a
}

// NeedsPersonWhyDecision is the why of a question or an approval: a person decides.
const NeedsPersonWhyDecision = "decision"

func (d *Dispatcher) blockedAttention(ctx context.Context, t *Task) *Attention {
	v := d.stageContext(ctx, t)
	a := &Attention{Stage: t.Stage, RunID: v.runID, VerdictLost: v.lost, Items: []AttentionItem{},
		ThreadID: OpenStopThreadOf(t.Comments)}
	stage := d.stageNote(t, t.Stage)
	a.Items = d.needsPersonItems(t, v)
	agentHeadline := ""
	if v.verdict != nil {
		agentHeadline = cutRunes(string(v.verdict.Headline), maxVerdictHeadline)
	}
	if v.byAuditor {
		ask := auditorAsk(v)
		agentHeadline = cmp.Or(agentHeadline, cutRunes(firstBodyLine(ask), maxVerdictHeadline))
		if len(a.Items) == 0 && ask != "" {
			a.Items = []AttentionItem{{Ask: cutRunes(ask, maxAuditorAskRunes), Why: NeedsPersonOther}}
		}
	}
	switch {
	case len(a.Items) > 0:
		decisionAttention(t, a, stage, agentHeadline)
	case v.lost:
		a.Kind, a.Options = AttentionFailure, []string{OptionRerun, OptionSendBack}
		a.Headline = NoteOf("task.attention.verdict_lost", "stage", stage).Text()
	default:
		a.Kind, a.Options = blockKind(t.Block), []string{OptionSendBack, OptionRerun}
		a.Headline = agentHeadline
		if a.Headline == "" && v.verdict != nil {
			a.Headline = v.verdict.headline()
		}
		if a.Headline == "" {
			a.Headline = NoteOf(blockHeadlineKey(t.Block), "stage", stage).Text()
		}
	}
	return a
}

// decisionAttention fills a stop that names items for a person: its options, and a headline that is
// the agent's (or the auditor's), else a count of the cases or items waiting.
func decisionAttention(t *Task, a *Attention, stage any, agentHeadline string) {
	cases := slices.ContainsFunc(a.Items, func(it AttentionItem) bool { return findCase(t.TestCases, it.Ref) != nil })
	a.Kind = AttentionDecision
	if cases {
		a.Options = append(a.Options, OptionTestByHand, OptionWaiveCases)
	}
	a.Options = append(a.Options, OptionSendBack, OptionRerun)
	a.Headline = agentHeadline
	switch {
	case a.Headline != "":
	case cases:
		passed := 0
		for i := range t.TestCases {
			if t.TestCases[i].Status == TestCasePass {
				passed++
			}
		}
		a.Headline = NoteOf("task.attention.cases_need_person", "stage", stage,
			"passed", passed, "total", len(t.TestCases), "count", len(a.Items)).Text()
	default:
		a.Headline = NoteOf("task.attention.items_need_person", "stage", stage, "count", len(a.Items)).Text()
	}
}

// What the auditor asks of a person: the ask of its verdict, else the comment it left for a person,
// else the first line of its feedback.
func auditorAsk(v stageVisit) string {
	if v.verdict != nil {
		if ask := strings.TrimSpace(string(v.verdict.Ask)); ask != "" {
			return ask
		}
	}
	if v.auditorNote != "" {
		return v.auditorNote
	}
	if v.verdict != nil {
		return firstBodyLine(v.verdict.Body)
	}
	return ""
}

const maxAuditorAskRunes = 400

// A stop an agent or the board chose waits for a person's decision; one where something did not
// work (no verdict, no auditor) is a failure.
func blockKind(b *BlockInfo) string {
	if b == nil {
		return AttentionDecision
	}
	switch b.Cause {
	case BlockVerdictMissing, BlockVerdictUndelivered, BlockAuditorFailed, BlockAuditorNoVerdict:
		return AttentionFailure
	}
	return AttentionDecision
}

func blockHeadlineKey(b *BlockInfo) string {
	if b == nil {
		return "task.attention.blocked"
	}
	switch b.Cause {
	case BlockVerdictMissing:
		return "task.attention.verdict_missing"
	case BlockVerdictUndelivered:
		return "task.attention.verdict_lost"
	case BlockKickBackLimit, BlockKickBackRepeated:
		return "task.attention.kick_back_limit"
	case BlockAuditorFailed, BlockAuditorNoVerdict:
		return "task.attention.auditor_failed"
	}
	return "task.attention.blocked"
}

// The verdict's needs_person items still open, else the cases with a blocked or failed run since
// the stage began that nobody has passed or waived since.
func (d *Dispatcher) needsPersonItems(t *Task, v stageVisit) []AttentionItem {
	if v.verdict != nil && len(v.verdict.NeedsPerson) > 0 {
		return verdictItems(t, v.verdict.NeedsPerson)
	}
	items := []AttentionItem{}
	for _, ref := range d.needsPersonCases(t, v) {
		c := findCase(t.TestCases, ref)
		if c == nil || caseSettled(c) {
			continue
		}
		items = append(items, AttentionItem{Ref: c.Key, Title: c.Title, Why: NeedsPersonOther, Ask: stuckRunText(c)})
	}
	return items
}

// The verdict's needs_person entries, minus the cases settled since; an entry that names no case of
// the task stays as the agent wrote it.
func verdictItems(t *Task, needs []NeedsPerson) []AttentionItem {
	items := []AttentionItem{}
	for _, n := range needs {
		it := AttentionItem{Ref: n.Ref, Ask: n.Ask, Why: n.Why}
		if c := findCase(t.TestCases, n.Ref); c != nil {
			if caseSettled(c) {
				continue
			}
			it.Ref, it.Title = c.Key, c.Title
		}
		items = append(items, it)
	}
	return items
}

// What the newest blocked or failed run of a case saw.
func stuckRunText(c *TestCase) string {
	for i := range c.Runs {
		if r := &c.Runs[i]; r.Verdict == TestCaseBlocked || r.Verdict == TestCaseFail {
			return cutRunes(cmp.Or(r.Observed, r.Note), 200)
		}
	}
	return ""
}

// The pending approvals that hold the stage the task is in: the stage's own, and approvals of no
// stage (an agent's flags) only where the stage is gated by flags. Anything else pending no longer
// holds the flow: it belongs to a stage the task left, or to none that waits on it.
func approvalsHoldingStage(t *Task, info types.StageInfo) []*Approval {
	var pending []*Approval
	for i := range t.Approvals {
		a := &t.Approvals[i]
		if a.Status.Settled() {
			continue
		}
		if a.Stage == t.Stage || (a.Stage == "" && info.BlockedByFlags) {
			pending = append(pending, a)
		}
	}
	return pending
}

// Waiting is what a person can unblock now, the block the task view pins as "Waiting for you":
// the approvals that hold the stage the task is in and the open questions that stage asked (or a
// question with no stage, older rows). An approval or question an earlier stage left open is out:
// the flow went on without it.
type Waiting struct {
	Approvals []string `json:"approvals"`
	Questions []string `json:"questions"`
}

// WaitingFor works out Waiting for GET /tasks/{id}; t must come from the store's Get. A task in a
// terminal stage waits on nobody.
func (d *Dispatcher) WaitingFor(t *Task) Waiting {
	out := Waiting{Approvals: []string{}, Questions: []string{}}
	if t == nil {
		return out
	}
	info, _ := d.stagesFor(t).Get(t.Stage)
	if info.Terminal {
		return out
	}
	for _, a := range approvalsHoldingStage(t, info) {
		out.Approvals = append(out.Approvals, a.ID)
	}
	for i := range t.Questions {
		q := &t.Questions[i]
		if q.Status == QuestionOpen && (q.Stage == "" || q.Stage == t.Stage) {
			out.Questions = append(out.Questions, q.ID)
		}
	}
	return out
}

func (d *Dispatcher) approvalAttention(t *Task, info types.StageInfo) *Attention {
	pending := approvalsHoldingStage(t, info)
	if len(pending) == 0 {
		return nil
	}
	out := &Attention{Kind: AttentionApproval, Stage: t.Stage, ApprovalID: pending[0].ID,
		ThreadID: ApprovalThreadOf(t.Comments, pending[0].ID),
		Options:  []string{OptionDecideApproval}, Items: []AttentionItem{}}
	labels := make([]string, 0, len(pending))
	for _, a := range pending {
		out.Items = append(out.Items, AttentionItem{Ref: cmp.Or(a.Key, a.ID), Title: a.Label, Ask: a.Reason,
			Why: NeedsPersonWhyDecision})
		labels = append(labels, a.Label)
	}
	out.Headline = NoteOf("task.attention.approval", "stage", d.stageNote(t, t.Stage),
		"approvals", strings.Join(labels, ", ")).Text()
	return out
}

// A run that ended without finishing the stage's work.
func (d *Dispatcher) failureAttention(ctx context.Context, t *Task) *Attention {
	key := ""
	switch t.Status {
	case StatusFailed, StatusResumeFailed:
		key = "task.attention.failed"
	case StatusOutOfTurns:
		key = "task.attention.out_of_turns"
	case StatusNeedsMoreTurns:
		key = "task.attention.needs_more_turns"
	case StatusNotStarted:
		key = "task.attention.not_started"
	default:
		return nil
	}
	runID, _ := d.store.lastStageRun(ctx, t.ID, t.Stage, d.store.lastStageEntry(ctx, t.ID, t.Stage))
	return &Attention{Kind: AttentionFailure, Stage: t.Stage, RunID: runID, Items: []AttentionItem{},
		Options:  []string{OptionRerun, OptionSendBack},
		Headline: NoteOf(key, "stage", d.stageNote(t, t.Stage)).Text()}
}

// The task's own approvals that lie ahead or went stale (approved on another head), and which
// stage requirements already opened one.
func (d *Dispatcher) openedUpcoming(ctx context.Context, t *Task, ahead func(Stage) bool,
	label func(Stage) string) ([]UpcomingApproval, map[string]bool) {
	head, headRead := "", false
	out := []UpcomingApproval{}
	opened := map[string]bool{}
	for i := range t.Approvals {
		a := &t.Approvals[i]
		if a.Source == ApprovalFromStage {
			opened[string(a.Stage)+"\x00"+a.Key] = true
		}
		stale := false
		if a.Status == ApprovalApproved && a.ApprovedAtRef != "" {
			if !headRead {
				head, headRead = d.currentRef(ctx, t), true
			}
			stale = head != "" && head != a.ApprovedAtRef
		}
		if !stale && (a.Status.Settled() || !ahead(a.Stage)) {
			continue
		}
		out = append(out, UpcomingApproval{ID: a.ID, Key: a.Key, Label: a.Label, Stage: a.Stage,
			StageLabel: label(a.Stage), Stale: stale})
	}
	return out, opened
}

// upcomingApprovals lists what the task meets later: approvals of stages ahead (opened or still only
// required by the board) and approvals given on an older head of the branch.
func (d *Dispatcher) upcomingApprovals(ctx context.Context, t *Task) []UpcomingApproval {
	stages := d.stagesFor(t)
	cur, curOK := stages.Get(t.Stage)
	label := func(s Stage) string { return d.stageName(t, s) }
	ahead := func(s Stage) bool {
		info, ok := stages.Get(s)
		return ok && curOK && !info.Virtual && info.Position > cur.Position
	}
	out, opened := d.openedUpcoming(ctx, t, ahead, label)
	list := stages.List()
	for i := range list {
		st := &list[i]
		if !ahead(st.ID) {
			continue
		}
		for _, req := range st.RequiredApprovals {
			if opened[string(st.ID)+"\x00"+req.Key] || !requirementApplies(req, t.TypeID) {
				continue
			}
			out = append(out, UpcomingApproval{Key: req.Key, Label: req.Label, Stage: st.ID, StageLabel: label(st.ID)})
		}
	}
	slices.SortStableFunc(out, func(a, b UpcomingApproval) int {
		ia, _ := stages.Get(a.Stage)
		ib, _ := stages.Get(b.Stage)
		return cmp.Compare(ia.Position, ib.Position)
	})
	return out
}
