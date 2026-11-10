package automation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(apperr.Wrap(err, "automation.errors.crypto_rand"))
	}
	return hex.EncodeToString(b)
}

const (
	TriggerSchedule = "schedule"
	TriggerEvent    = "event"
	TriggerMetric   = "metric"
	TriggerManual   = "manual"
	TriggerTest     = "test"
)

const (
	StepMCP       = "mcp"
	StepHTTP      = "http"
	StepScript    = "script"
	StepQuery     = "query"
	StepCondition = "condition"
	StepFanout    = "fanout"
	StepTask      = "task"
	StepMention   = "mention"
)

const (
	RunRunning  = "running"
	RunSuccess  = "success"
	RunFailed   = "failed"
	RunSkipped  = "skipped"
	RunTimeout  = "timeout"
	RunCanceled = "canceled"
)

const (
	StepOK      = "ok"
	StepSkipped = "skipped"
	StepFailed  = "failed"
)

const (
	ConcurrencyForbid  = "forbid"
	ConcurrencyAllow   = "allow"
	ConcurrencyReplace = "replace"
)

const (
	DuplicateComment = "comment"
	DuplicateSkip    = "skip"
	DuplicateCreate  = "create"
)

type Spec struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Trigger     Trigger        `yaml:"trigger" json:"trigger"`
	Policy      Policy         `yaml:"policy,omitempty" json:"policy"`
	Vars        map[string]any `yaml:"vars,omitempty" json:"vars,omitempty"`
	Steps       []Step         `yaml:"steps" json:"steps"`
}

type Trigger struct {
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	TZ       string `yaml:"tz,omitempty" json:"tz,omitempty"`

	Event  string `yaml:"event,omitempty" json:"event,omitempty"`
	Filter string `yaml:"filter,omitempty" json:"filter,omitempty"`

	Metric *MetricTrigger `yaml:"metric,omitempty" json:"metric,omitempty"`

	Manual bool `yaml:"manual,omitempty" json:"manual,omitempty"`
}

type MetricTrigger struct {
	Environment string   `yaml:"environment" json:"environment"`
	Metric      string   `yaml:"metric" json:"metric"`
	Window      Duration `yaml:"window" json:"window"`
	Agg         string   `yaml:"agg,omitempty" json:"agg,omitempty"`
	Op          string   `yaml:"op" json:"op"`
	Threshold   float64  `yaml:"threshold" json:"threshold"`
	Every       Duration `yaml:"every,omitempty" json:"every,omitempty"`
}

func (t Trigger) Kind() string {
	switch {
	case t.Schedule != "":
		return TriggerSchedule
	case t.Event != "":
		return TriggerEvent
	case t.Metric != nil:
		return TriggerMetric
	default:
		return TriggerManual
	}
}

type Policy struct {
	Concurrency        string   `yaml:"concurrency,omitempty" json:"concurrency"`
	StartingDeadline   Duration `yaml:"starting_deadline,omitempty" json:"starting_deadline"`
	Debounce           Duration `yaml:"debounce,omitempty" json:"debounce"`
	Timeout            Duration `yaml:"timeout,omitempty" json:"timeout"`
	MaxAgentRunsPerDay int      `yaml:"max_agent_runs_per_day,omitempty" json:"max_agent_runs_per_day"`
	HistoryLimit       int      `yaml:"history_limit,omitempty" json:"history_limit"`
}

type Step struct {
	ID              string `yaml:"id" json:"id"`
	When            string `yaml:"when,omitempty" json:"when,omitempty"`
	ContinueOnError bool   `yaml:"continue_on_error,omitempty" json:"continue_on_error,omitempty"`

	Condition string       `yaml:"condition,omitempty" json:"condition,omitempty"`
	MCP       *MCPStep     `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	HTTP      *HTTPStep    `yaml:"http,omitempty" json:"http,omitempty"`
	Script    *ScriptStep  `yaml:"script,omitempty" json:"script,omitempty"`
	Query     *QueryStep   `yaml:"query,omitempty" json:"query,omitempty"`
	Task      *TaskStep    `yaml:"task,omitempty" json:"task,omitempty"`
	Mention   *MentionStep `yaml:"mention,omitempty" json:"mention,omitempty"`
	Fanout    *FanoutStep  `yaml:"fanout,omitempty" json:"fanout,omitempty"`
	Steps     []Step       `yaml:"steps,omitempty" json:"steps,omitempty"`
}

func (s Step) Kind() string {
	switch {
	case s.Condition != "":
		return StepCondition
	case s.MCP != nil:
		return StepMCP
	case s.HTTP != nil:
		return StepHTTP
	case s.Script != nil:
		return StepScript
	case s.Query != nil:
		return StepQuery
	case s.Task != nil:
		return StepTask
	case s.Mention != nil:
		return StepMention
	case s.Fanout != nil:
		return StepFanout
	default:
		return ""
	}
}

type MCPStep struct {
	Server string         `yaml:"server" json:"server"`
	Tool   string         `yaml:"tool" json:"tool"`
	Args   map[string]any `yaml:"args,omitempty" json:"args,omitempty"`
}

type HTTPStep struct {
	Method     string            `yaml:"method,omitempty" json:"method,omitempty"`
	URL        string            `yaml:"url" json:"url"`
	Headers    map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body       string            `yaml:"body,omitempty" json:"body,omitempty"`
	Credential string            `yaml:"credential,omitempty" json:"credential,omitempty"`
	Timeout    Duration          `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type ScriptStep struct {
	Run     string            `yaml:"run" json:"run"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Timeout Duration          `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type QueryStep struct {
	Source      string   `yaml:"source" json:"source"`
	Board       string   `yaml:"board,omitempty" json:"board,omitempty"`
	Stage       string   `yaml:"stage,omitempty" json:"stage,omitempty"`
	Tag         string   `yaml:"tag,omitempty" json:"tag,omitempty"`
	ProjectID   string   `yaml:"project_id,omitempty" json:"project_id,omitempty"`
	WithPR      bool     `yaml:"with_pr,omitempty" json:"with_pr,omitempty"`
	Open        bool     `yaml:"open,omitempty" json:"open,omitempty"`
	StaleFor    Duration `yaml:"stale_for,omitempty" json:"stale_for,omitempty"`
	Limit       int      `yaml:"limit,omitempty" json:"limit,omitempty"`
	Environment string   `yaml:"environment,omitempty" json:"environment,omitempty"`
	Metric      string   `yaml:"metric,omitempty" json:"metric,omitempty"`
	Window      Duration `yaml:"window,omitempty" json:"window,omitempty"`
	Agg         string   `yaml:"agg,omitempty" json:"agg,omitempty"`
}

type TaskStep struct {
	Title            string            `yaml:"title" json:"title"`
	Body             string            `yaml:"body,omitempty" json:"body,omitempty"`
	Board            string            `yaml:"board,omitempty" json:"board,omitempty"`
	Type             string            `yaml:"type,omitempty" json:"type,omitempty"`
	Stage            string            `yaml:"stage,omitempty" json:"stage,omitempty"`
	ProjectIDs       []string          `yaml:"project_ids,omitempty" json:"project_ids,omitempty"`
	StageAssignments map[string]string `yaml:"stage_assignments,omitempty" json:"stage_assignments,omitempty"`
	Tags             []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Priority         string            `yaml:"priority,omitempty" json:"priority,omitempty"`
	Fingerprint      string            `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	OnDuplicate      string            `yaml:"on_duplicate,omitempty" json:"on_duplicate,omitempty"`
}

type MentionStep struct {
	TaskID string `yaml:"task_id" json:"task_id"`
	Agent  string `yaml:"agent,omitempty" json:"agent,omitempty"`
	Body   string `yaml:"body" json:"body"`
}

type FanoutStep struct {
	Over     string `yaml:"over" json:"over"`
	MaxItems int    `yaml:"max_items" json:"max_items"`
	As       string `yaml:"as,omitempty" json:"as,omitempty"`
}

const MaxFanout = 50

const MaxScriptTimeout = 10 * time.Minute

type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }
func (d Duration) String() string {
	if d == 0 {
		return ""
	}
	return time.Duration(d).String()
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		// Returned as a yaml.TypeError so yamlError's yamlWrongType pattern matches it and reports
		// "takes a duration".
		return &yaml.TypeError{Errors: []string{
			//i18n:ignore a yaml.v3 message, read back by yamlError, never shown
			fmt.Sprintf("line %d: cannot unmarshal %s into automation.Duration", value.Line, value.ShortTag()),
		}}
	}
	var s string
	if err := value.Decode(&s); err != nil {
		return invalidWrap(err, "automation.errors.duration_not_string")
	}
	return d.parse(s)
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*d = 0
		return nil
	}
	return d.parse(s)
}

func (d Duration) MarshalJSON() ([]byte, error) { return []byte(`"` + d.String() + `"`), nil }

func (d *Duration) parse(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return apperr.Invalid("automation.errors.bad_duration", "value", s)
	}
	*d = Duration(v)
	return nil
}

type Automation struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Enabled     bool       `json:"enabled"`
	Spec        string     `json:"spec"`
	TriggerKind string     `json:"trigger_kind"`
	Event       string     `json:"trigger_event,omitempty"`
	Cron        string     `json:"cron,omitempty"`
	TZ          string     `json:"tz,omitempty"`
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	LastStatus  string     `json:"last_status,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`

	Parsed *Spec `json:"parsed,omitempty"`

	// Transient, set by ClaimDue. The catch-up gate measures lateness against DueAt; using now
	// would make a firing the daemon slept through look punctual.
	DueAt time.Time `json:"-"`
}

type Run struct {
	ID           string     `json:"id"`
	AutomationID string     `json:"automation_id"`
	WorkspaceID  string     `json:"workspace_id"`
	Status       string     `json:"status"`
	Trigger      string     `json:"trigger"`
	Reason       string     `json:"reason,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Error        string     `json:"error,omitempty"`
	AgentRuns    int        `json:"agent_runs"`
	ItemsEmitted int        `json:"items_emitted"`
	Payload      string     `json:"payload,omitempty"`
	DedupeKey    string     `json:"-"`
	Steps        []RunStep  `json:"steps,omitempty"`
	ReasonMsg    Msg        `json:"-"`
	ErrorMsg     Msg        `json:"-"`
}

type RunStep struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Seq        int        `json:"seq"`
	StepID     string     `json:"step_id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	OutputMsg  Msg        `json:"-"`
	ErrorMsg   Msg        `json:"-"`
}

const MaxStepOutput = 32 * 1024

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

func invalidWrap(err error, key string, args ...any) error {
	return apperr.WrapKind(apperr.KindInvalid, err, key, args...)
}

func ParseSpec(src string) (*Spec, error) {
	var spec Spec
	dec := yaml.NewDecoder(strings.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return nil, yamlError(err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

// These match yaml.v3's error text verbatim; recheck them when upgrading yaml.v3.
var (
	//i18n:ignore a yaml.v3 message pattern, not a message
	yamlUnknownKey = regexp.MustCompile(`^line (\d+): field (\S+) not found in type \S+$`)
	//i18n:ignore a yaml.v3 message pattern, not a message
	yamlWrongType = regexp.MustCompile("^line (\\d+): cannot unmarshal !!\\w+(?: `(.*)`)? into (\\S+)$")
	yamlSyntax    = regexp.MustCompile(`^yaml: (?:line (\d+): )?(.*)$`)
)

func yamlError(err error) error {
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		first := te.Errors[0]
		if m := yamlUnknownKey.FindStringSubmatch(first); m != nil {
			return apperr.Invalid("automation.errors.yaml_unknown_key", "line", m[1], "key", m[2])
		}
		if m := yamlWrongType.FindStringSubmatch(first); m != nil {
			if m[2] == "" {
				return apperr.Invalid("automation.errors.yaml_wrong_shape", "line", m[1], "want", yamlKind(m[3]))
			}
			return apperr.Invalid("automation.errors.yaml_wrong_type", "line", m[1], "value", m[2], "want", yamlKind(m[3]))
		}
	}
	if m := yamlSyntax.FindStringSubmatch(err.Error()); len(m) > 1 && m[1] != "" {
		return apperr.Invalid("automation.errors.yaml_syntax_line", "line", m[1], "detail", m[2])
	}
	return invalidWrap(err, "automation.errors.parse_spec")
}

func yamlKind(goType string) apperr.List {
	key := "automation.errors.yaml_kind_text"
	switch {
	case strings.HasPrefix(goType, "[]"):
		key = "automation.errors.yaml_kind_list"
	case strings.HasPrefix(goType, "map["), strings.HasPrefix(goType, "automation."):
		key = "automation.errors.yaml_kind_mapping"
		if goType == "automation.Duration" {
			key = "automation.errors.yaml_kind_duration"
		}
	case goType == "bool":
		key = "automation.errors.yaml_kind_bool"
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "float"):
		key = "automation.errors.yaml_kind_number"
	}
	return apperr.List{Errs: []error{apperr.New(key)}}
}

func (s *Spec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return apperr.Invalid("automation.errors.name_required")
	}
	if err := s.Trigger.validate(); err != nil {
		return err
	}
	if err := s.Policy.validate(); err != nil {
		return err
	}
	if len(s.Steps) == 0 {
		return apperr.Invalid("automation.errors.steps_required")
	}
	seen := map[string]bool{}
	return validateSteps(s.Steps, seen, false)
}

func (t *Trigger) validate() error {
	shapes := 0
	if t.Schedule != "" {
		shapes++
	}
	if t.Event != "" {
		shapes++
	}
	if t.Metric != nil {
		shapes++
	}
	if t.Manual {
		shapes++
	}
	if shapes == 0 {
		return apperr.Invalid("automation.errors.trigger_missing")
	}
	if shapes > 1 {
		return apperr.Invalid("automation.errors.trigger_ambiguous")
	}
	if t.Schedule != "" {
		if _, err := ParseCron(t.Schedule, t.TZ); err != nil {
			return err
		}
	}
	if t.Filter != "" {
		if _, err := Compile(t.Filter); err != nil {
			return invalidWrap(err, "automation.errors.trigger_filter")
		}
	}
	if t.Event != "" && !KnownEvent(t.Event) {
		return apperr.Invalid("automation.errors.unknown_event", "event", t.Event, "known", strings.Join(KnownEvents(), ", "))
	}
	if t.Metric != nil {
		return t.Metric.validate()
	}
	return nil
}

func (m *MetricTrigger) validate() error {
	if m.Environment == "" || m.Metric == "" {
		return apperr.Invalid("automation.errors.metric_trigger_target_required")
	}
	switch m.Op {
	case ">", ">=", "<", "<=":
	default:
		return apperr.Invalid("automation.errors.metric_trigger_op")
	}
	switch m.Agg {
	case "", "avg", "max", "min", "sum", "last":
	default:
		return apperr.Invalid("automation.errors.metric_trigger_agg")
	}
	if m.Window.Duration() <= 0 {
		m.Window = Duration(15 * time.Minute)
	}
	if m.Every.Duration() <= 0 {
		m.Every = Duration(5 * time.Minute)
	}
	if m.Every.Duration() < time.Minute {
		return apperr.Invalid("automation.errors.metric_trigger_every")
	}
	return nil
}

func (p *Policy) validate() error {
	switch p.Concurrency {
	case "":
		p.Concurrency = ConcurrencyForbid
	case ConcurrencyForbid, ConcurrencyAllow, ConcurrencyReplace:
	default:
		return apperr.Invalid("automation.errors.concurrency_invalid")
	}
	if p.Timeout.Duration() <= 0 {
		p.Timeout = Duration(10 * time.Minute)
	}
	if p.Timeout.Duration() > time.Hour {
		return apperr.Invalid("automation.errors.timeout_too_long")
	}
	if p.HistoryLimit <= 0 {
		p.HistoryLimit = 200
	}
	if p.MaxAgentRunsPerDay < 0 {
		return apperr.Invalid("automation.errors.budget_negative")
	}
	return nil
}

func validateSteps(steps []Step, seen map[string]bool, nested bool) error {
	for i := range steps {
		st := &steps[i]
		if !idPattern.MatchString(st.ID) {
			return apperr.Invalid("automation.errors.step_id_invalid", "step", st.ID)
		}
		if seen[st.ID] {
			return apperr.Invalid("automation.errors.step_id_duplicate", "step", st.ID)
		}
		seen[st.ID] = true

		kind := st.Kind()
		if kind == "" {
			return apperr.Invalid("automation.errors.step_no_action", "step", st.ID)
		}
		if err := singleAction(st); err != nil {
			return err
		}
		if st.When != "" {
			if _, err := Compile(st.When); err != nil {
				return invalidWrap(err, "automation.errors.step_when", "step", st.ID)
			}
		}
		if err := validateStepBody(st, kind, nested, seen); err != nil {
			return err
		}
	}
	return nil
}

func singleAction(st *Step) error {
	n := 0
	for _, set := range []bool{
		st.Condition != "", st.MCP != nil, st.HTTP != nil, st.Script != nil,
		st.Query != nil, st.Task != nil, st.Mention != nil, st.Fanout != nil,
	} {
		if set {
			n++
		}
	}
	if n > 1 {
		return apperr.Invalid("automation.errors.step_multiple_actions", "step", st.ID, "count", n)
	}
	return nil
}

func validateStepBody(st *Step, kind string, nested bool, seen map[string]bool) error {
	var err error
	switch kind {
	case StepCondition:
		err = validateConditionStep(st)
	case StepMCP:
		err = validateMCPStep(st)
	case StepHTTP:
		err = validateHTTPStep(st)
	case StepScript:
		err = validateScriptStep(st)
	case StepQuery:
		err = validateQueryStep(st)
	case StepTask:
		err = validateTaskStep(st)
	case StepMention:
		err = validateMentionStep(st)
	case StepFanout:
		return validateFanoutStep(st, nested, seen)
	}
	if err != nil {
		return err
	}
	if len(st.Steps) > 0 {
		return apperr.Invalid("automation.errors.step_children_not_fanout", "step", st.ID)
	}
	return nil
}

func validateConditionStep(st *Step) error {
	if _, err := Compile(st.Condition); err != nil {
		return invalidWrap(err, "automation.errors.step_condition", "step", st.ID)
	}
	return nil
}

func validateMCPStep(st *Step) error {
	if st.MCP.Server == "" || st.MCP.Tool == "" {
		return apperr.Invalid("automation.errors.step_mcp_target_required", "step", st.ID)
	}
	return validateArgTemplates(st.ID, st.MCP.Args)
}

func validateHTTPStep(st *Step) error {
	if st.HTTP.URL == "" {
		return apperr.Invalid("automation.errors.step_http_url_required", "step", st.ID)
	}
	if err := ValidateTemplate(st.HTTP.URL); err != nil {
		return invalidWrap(err, "automation.errors.step_http_url", "step", st.ID)
	}
	if err := ValidateTemplate(st.HTTP.Body); err != nil {
		return invalidWrap(err, "automation.errors.step_http_body", "step", st.ID)
	}
	return nil
}

func validateScriptStep(st *Step) error {
	if strings.TrimSpace(st.Script.Run) == "" {
		return apperr.Invalid("automation.errors.step_script_required", "step", st.ID)
	}
	if err := ValidateTemplate(st.Script.Run); err != nil {
		return invalidWrap(err, "automation.errors.step_script", "step", st.ID)
	}
	if st.Script.Timeout.Duration() > MaxScriptTimeout {
		return apperr.Invalid("automation.errors.step_script_timeout",
			"step", st.ID, "max", MaxScriptTimeout)
	}
	return nil
}

func validateQueryStep(st *Step) error {
	switch st.Query.Source {
	case "tasks", "metric":
		return nil
	default:
		return apperr.Invalid("automation.errors.step_query_source", "step", st.ID)
	}
}

func validateTaskStep(st *Step) error {
	if strings.TrimSpace(st.Task.Title) == "" {
		return apperr.Invalid("automation.errors.step_task_title_required", "step", st.ID)
	}
	switch st.Task.OnDuplicate {
	case "":
		st.Task.OnDuplicate = DuplicateComment
	case DuplicateComment, DuplicateSkip, DuplicateCreate:
	default:
		return apperr.Invalid("automation.errors.step_on_duplicate", "step", st.ID)
	}
	return validateTemplates(st.ID, st.Task.Title, st.Task.Body, st.Task.Fingerprint)
}

func validateMentionStep(st *Step) error {
	if st.Mention.TaskID == "" || strings.TrimSpace(st.Mention.Body) == "" {
		return apperr.Invalid("automation.errors.step_mention_required", "step", st.ID)
	}
	return validateTemplates(st.ID, st.Mention.TaskID, st.Mention.Body)
}

func validateFanoutStep(st *Step, nested bool, seen map[string]bool) error {
	if nested {
		return apperr.Invalid("automation.errors.step_fanout_nested", "step", st.ID)
	}
	if st.Fanout.Over == "" {
		return apperr.Invalid("automation.errors.step_fanout_over_required", "step", st.ID)
	}
	if _, err := Compile(st.Fanout.Over); err != nil {
		return invalidWrap(err, "automation.errors.step_fanout_over", "step", st.ID)
	}
	switch {
	case st.Fanout.MaxItems <= 0:
		return apperr.Invalid("automation.errors.step_fanout_max_items_required", "step", st.ID)
	case st.Fanout.MaxItems > MaxFanout:
		return apperr.Invalid("automation.errors.step_fanout_max_items_cap", "step", st.ID, "max", MaxFanout)
	case len(st.Steps) == 0:
		return apperr.Invalid("automation.errors.step_fanout_no_steps", "step", st.ID)
	}
	return validateSteps(st.Steps, seen, true)
}

func validateTemplates(stepID string, tpls ...string) error {
	for _, tpl := range tpls {
		if err := ValidateTemplate(tpl); err != nil {
			return invalidWrap(err, "automation.errors.step_template", "step", stepID)
		}
	}
	return nil
}

func validateArgTemplates(stepID string, args map[string]any) error {
	for _, v := range args {
		if err := walkArgTemplates(stepID, v); err != nil {
			return err
		}
	}
	return nil
}

func walkArgTemplates(stepID string, v any) error {
	switch t := v.(type) {
	case string:
		if err := ValidateTemplate(t); err != nil {
			return invalidWrap(err, "automation.errors.step_args", "step", stepID)
		}
	case map[string]any:
		for _, item := range t {
			if err := walkArgTemplates(stepID, item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range t {
			if err := walkArgTemplates(stepID, item); err != nil {
				return err
			}
		}
	}
	return nil
}
