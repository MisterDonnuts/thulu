package evidence

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	CaseDraft = "draft"
	CaseReady = "ready"
)

const (
	RunQueued   = "queued"
	RunRunning  = "running"
	RunPass     = "pass"
	RunFail     = "fail"
	RunBlocked  = "blocked"
	RunError    = "error"
	RunCanceled = "canceled"
)

const (
	BatchQueued   = "queued"
	BatchRunning  = "running"
	BatchDone     = "done"
	BatchCanceled = "canceled"
	BatchError    = "error"
)

const (
	TriggerManual    = "manual"
	TriggerSchedule  = "schedule"
	TriggerDeploy    = "deploy"
	TriggerAPI       = "api"
	TriggerRecording = "recording"
)

const (
	SourceAgent  = "agent"
	SourceTask   = "task"
	SourceUpload = "upload"
)

const (
	RecordingOpen   = "open"
	RecordingSaved  = "saved"
	RecordingClosed = "closed"
)

const (
	KindProject    = "project"
	KindSandbox    = "sandbox"
	KindStandalone = "standalone"
)

const (
	SandboxPending  = "pending"
	SandboxStarting = "starting"
	SandboxReady    = "ready"
	SandboxFailed   = "failed"
	SandboxReleased = "released"
)

const (
	PhaseRoom   = "room"
	PhaseSource = "source"
	PhaseTools  = "tools"
	PhaseSetup  = "setup"
	PhaseStart  = "start"
)

const SandboxName = "sandbox"

const (
	SandboxNoRoom         = "no_room"
	SandboxBranchNotFound = "branch_not_found" // {branch}
	SandboxSourceFailed   = "source_failed"
	SandboxLaunchFailed   = "launch_failed"
	SandboxToolsFailed    = "tools_failed"
	SandboxSetupFailed    = "setup_failed"
	SandboxStartFailed    = "start_failed" // {code}
	SandboxReadyTimeout   = "ready_timeout"
	SandboxNoPreviewPort  = "no_preview_port"
	SandboxParamsMissing  = "params_missing" // {names}
	SandboxUnavailable    = "unavailable"
	SandboxUnreachable    = "unreachable" // {error}
	SandboxNoInitScript   = "no_init_script"
	SandboxRestarted      = "restarted"
	SandboxError          = "error" // {error}
)

var sandboxReasonKeys = map[string]string{
	SandboxNoRoom:         "evidence.reasons.sandbox_no_room",
	SandboxBranchNotFound: "evidence.reasons.sandbox_branch_not_found",
	SandboxSourceFailed:   "evidence.reasons.sandbox_source_failed",
	SandboxLaunchFailed:   "evidence.reasons.sandbox_launch_failed",
	SandboxToolsFailed:    "evidence.reasons.sandbox_tools_failed",
	SandboxSetupFailed:    "evidence.reasons.sandbox_setup_failed",
	SandboxStartFailed:    "evidence.reasons.sandbox_start_failed",
	SandboxReadyTimeout:   "evidence.reasons.sandbox_ready_timeout",
	SandboxNoPreviewPort:  "evidence.reasons.sandbox_no_preview_port",
	SandboxParamsMissing:  "evidence.reasons.sandbox_params_missing",
	SandboxUnavailable:    "evidence.reasons.sandbox_unavailable",
	SandboxUnreachable:    "evidence.reasons.sandbox_unreachable",
	SandboxNoInitScript:   "evidence.reasons.sandbox_no_init_script",
	SandboxRestarted:      "evidence.reasons.daemon_restarted",
	SandboxError:          "evidence.reasons.sandbox_error",
}

type Reason struct {
	Code string
	Args map[string]string
}

func SandboxReason(code string, args ...any) Reason {
	if _, ok := sandboxReasonKeys[code]; !ok {
		code = SandboxUnavailable
	}
	r := Reason{Code: code}
	for i := 0; i+1 < len(args); i += 2 {
		if r.Args == nil {
			r.Args = map[string]string{}
		}
		r.Args[fmt.Sprint(args[i])] = fmt.Sprint(args[i+1])
	}
	return r
}

func (r Reason) Text(locale string) string {
	key, ok := sandboxReasonKeys[r.Code]
	if !ok {
		key = sandboxReasonKeys[SandboxUnavailable]
	}
	names := make([]string, 0, len(r.Args))
	for name := range r.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	args := make([]any, 0, 2*len(names))
	for _, name := range names {
		args = append(args, name, r.Args[name])
	}
	return i18n.T(locale, key, args...)
}

func (r Reason) String() string { return r.Text(i18n.SourceLocale) }

const (
	ArtifactVideo    = "video"
	ArtifactTake     = "take"
	ArtifactChapters = "chapters"
	ArtifactShot     = "shot"
	ArtifactPoster   = "poster"
)

// Must match the priorities task test cases use.
const (
	PriorityLow      = "low"
	PriorityMedium   = "medium"
	PriorityHigh     = "high"
	PriorityCritical = "critical"
)

const (
	maxTitle         = 200
	maxEnvName       = 60
	maxTags          = 12
	maxTag           = 32
	maxSecrets       = 20
	maxSecretValue   = 4096
	maxTakeBytes     = 1 << 20
	maxReportBytes   = 256 << 10
	RunHistory       = 20
	DefaultListLimit = 50
	MaxListLimit     = 500
)

var (
	secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,40}$`)
	hostRe       = regexp.MustCompile(`^[A-Za-z0-9.-]+(:\d{1,5})?$`)
)

type Case struct {
	ID            string              `json:"id"`
	WorkspaceID   string              `json:"workspace_id"`
	ProjectID     string              `json:"project_id"`
	Key           string              `json:"key"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	Steps         string              `json:"steps"`
	Expected      string              `json:"expected"`
	Priority      string              `json:"priority"`
	Tags          []string            `json:"tags"`
	Take          json.RawMessage     `json:"take,omitempty"`
	HasTake       bool                `json:"has_take"`
	TakeSource    string              `json:"take_source"`
	TakeUpdatedAt *time.Time          `json:"take_updated_at"`
	SourceTaskID  string              `json:"source_task_id"`
	SourceCaseKey string              `json:"source_case_key"`
	Status        string              `json:"status"`
	LastRunAt     *time.Time          `json:"last_run_at"`
	CreatedBy     string              `json:"created_by"`
	UpdatedBy     string              `json:"updated_by"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	Latest        map[string]RunBrief `json:"latest,omitempty"`
	Runs          []Run               `json:"runs,omitempty"`
}

type RunBrief struct {
	RunID      string     `json:"run_id"`
	Status     string     `json:"status"`
	FinishedAt *time.Time `json:"finished_at"`
	PosterID   string     `json:"poster_id"`
}

type CasePatch struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Steps       *string   `json:"steps,omitempty"`
	Expected    *string   `json:"expected,omitempty"`
	Priority    *string   `json:"priority,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	ProjectID   *string   `json:"project_id,omitempty"`
}

const ProjectNone = "none"

type CaseFilter struct {
	WorkspaceID       string
	Q                 string
	Statuses          []string
	ProjectID         string
	IncludeUnassigned bool
	Tag               string
	Tags              []string
	Priorities        []string
	EnvironmentID     string
}

const NotRunHere = "not_run"

// Secret values never leave the store except through Store.Secrets.
type Environment struct {
	ID                   string                `json:"id"`
	WorkspaceID          string                `json:"workspace_id"`
	Kind                 string                `json:"kind"`
	ProjectID            string                `json:"project_id"`
	ProjectName          string                `json:"project_name"`
	ProjectEnvironmentID string                `json:"project_environment_id"`
	Name                 string                `json:"name"`
	DisplayName          string                `json:"display_name"`
	BaseURL              string                `json:"base_url"`
	IsProduction         bool                  `json:"is_production"`
	AllowedHosts         []string              `json:"allowed_hosts"`
	SecretNames          []string              `json:"secret_names"`
	Schedule             string                `json:"schedule"`
	ScheduleTZ           string                `json:"schedule_tz"`
	OnDeploy             bool                  `json:"on_deploy"`
	LastScheduledAt      *time.Time            `json:"last_scheduled_at"`
	CreatedBy            string                `json:"created_by"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
	ScheduleProjectID    string                `json:"schedule_project_id"`
	ScheduleTag          string                `json:"schedule_tag"`
	Deploy               *DeployInfo           `json:"deploy"`
	Sandbox              *SandboxInfo          `json:"sandbox"`
	LastBatch            *Batch                `json:"last_batch,omitempty"`
	Can                  map[string]Permission `json:"can,omitempty"`
	RepoURL              string                `json:"-"`
	DefaultBranch        string                `json:"-"`
}

func (e *Environment) Label() string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.Name
}

type DeployInfo struct {
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	Repo        string `json:"repo"`
	Environment string `json:"environment"`
}

type SandboxInfo struct {
	EnvironmentID string `json:"environment_id"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	Port          int    `json:"port"`
	DefaultBranch string `json:"default_branch"`
	InitScriptURL string `json:"init_script_url"`
}

type EnvironmentPatch struct {
	Name              *string   `json:"name,omitempty"`
	DisplayName       *string   `json:"display_name,omitempty"`
	BaseURL           *string   `json:"base_url,omitempty"`
	IsProduction      *bool     `json:"is_production,omitempty"`
	AllowedHosts      *[]string `json:"allowed_hosts,omitempty"`
	Schedule          *string   `json:"schedule,omitempty"`
	ScheduleTZ        *string   `json:"schedule_tz,omitempty"`
	OnDeploy          *bool     `json:"on_deploy,omitempty"`
	ScheduleProjectID *string   `json:"schedule_project_id,omitempty"`
	ScheduleTag       *string   `json:"schedule_tag,omitempty"`
}

func (p EnvironmentPatch) fields() []string {
	var out []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"name", p.Name != nil}, {"display_name", p.DisplayName != nil}, {"base_url", p.BaseURL != nil},
		{"is_production", p.IsProduction != nil}, {"allowed_hosts", p.AllowedHosts != nil},
		{"schedule", p.Schedule != nil}, {"schedule_tz", p.ScheduleTZ != nil}, {"on_deploy", p.OnDeploy != nil},
		{"schedule_project_id", p.ScheduleProjectID != nil}, {"schedule_tag", p.ScheduleTag != nil},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

var editableFields = map[string]map[string]bool{
	KindProject: {"display_name": true, "base_url": true, "allowed_hosts": true, "schedule": true,
		"schedule_tz": true, "on_deploy": true},
	KindStandalone: {"name": true, "base_url": true, "allowed_hosts": true, "schedule": true,
		"schedule_tz": true, "is_production": true, "schedule_project_id": true, "schedule_tag": true},
	KindSandbox: {"allowed_hosts": true},
}

func CheckPatch(kind string, p EnvironmentPatch) error {
	for _, f := range p.fields() {
		if !editableFields[kind][f] {
			return apperr.Invalid("evidence.errors.field_not_editable", "field", f, "kind", kind)
		}
	}
	return nil
}

func CaseRunsOn(caseProjectID string, env Environment) bool {
	switch env.Kind {
	case KindProject:
		return caseProjectID == "" || caseProjectID == env.ProjectID
	case KindSandbox:
		return caseProjectID != "" && caseProjectID == env.ProjectID
	}
	return true
}

type Batch struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	EnvironmentID string     `json:"environment_id"`
	Trigger       string     `json:"trigger"`
	TriggerRef    string     `json:"trigger_ref"`
	Status        string     `json:"status"`
	Total         int        `json:"total"`
	Passed        int        `json:"passed"`
	Failed        int        `json:"failed"`
	Blocked       int        `json:"blocked"`
	Errored       int        `json:"errored"`
	Canceled      int        `json:"canceled"`
	Runner        string     `json:"runner"`
	Error         string     `json:"error"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	BaseURL       string     `json:"base_url"`
	SourceBranch  string     `json:"source_branch"`
	SourceCommit  string     `json:"source_commit"`
	SandboxStatus string     `json:"sandbox_status"`
	SandboxPhase  string     `json:"sandbox_phase"`
	SandboxError  string     `json:"sandbox_error"`
	SandboxReason string     `json:"sandbox_reason"`
	reasonArgs    map[string]string
	ErrorCause    string `json:"error_cause,omitempty"`
	Locale        string `json:"-"`
	SandboxLog    string `json:"sandbox_log,omitempty"`
	Runs          []Run  `json:"runs,omitempty"`
}

type Check struct {
	Label    string `json:"label"`
	Expected string `json:"expected"`
	Observed string `json:"observed"`
	Pass     bool   `json:"pass"`
	Measured bool   `json:"measured"`
}

type Failure struct {
	Step  int    `json:"step"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Error string `json:"error"`
}

type Run struct {
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	BatchID       string          `json:"batch_id"`
	CaseID        string          `json:"case_id"`
	CaseKey       string          `json:"case_key"`
	CaseTitle     string          `json:"case_title"`
	EnvironmentID string          `json:"environment_id"`
	Position      int             `json:"position"`
	Status        string          `json:"status"`
	Checks        []Check         `json:"checks"`
	Failures      []Failure       `json:"failures"`
	Error         string          `json:"error"`
	DurationMS    int64           `json:"duration_ms"`
	Take          json.RawMessage `json:"take,omitempty"`
	ReportMD      string          `json:"report_md,omitempty"`
	VideoID       string          `json:"video_id"`
	TakeID        string          `json:"take_id"`
	ChaptersID    string          `json:"chapters_id"`
	PosterID      string          `json:"poster_id"`
	ShotIDs       []string        `json:"shot_ids"`
	Trigger       string          `json:"trigger,omitempty"`
	TriggerRef    string          `json:"trigger_ref,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at"`
	FinishedAt    *time.Time      `json:"finished_at"`
	ErrorCause    string          `json:"error_cause,omitempty"`
	Artifacts     []Artifact      `json:"artifacts,omitempty"`
}

type RunResult struct {
	Status     string    `json:"status"`
	Checks     []Check   `json:"checks"`
	Failures   []Failure `json:"failures"`
	DurationMS int64     `json:"duration_ms"`
	ReportMD   string    `json:"report_md"`
	Error      string    `json:"error"`
	VideoID    string    `json:"video_id"`
	TakeID     string    `json:"take_id"`
	ChaptersID string    `json:"chapters_id"`
	PosterID   string    `json:"poster_id"`
	ShotIDs    []string  `json:"shot_ids"`
}

type RunFilter struct {
	WorkspaceID   string
	CaseID        string
	EnvironmentID string
	BatchID       string
	Statuses      []string
	Limit         int
}

type Artifact struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	RunID       string    `json:"run_id"`
	Kind        string    `json:"kind"`
	Filename    string    `json:"filename"`
	MimeType    string    `json:"mime_type"`
	Size        int64     `json:"size"`
	Path        string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
}

type Recording struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	CaseID        string     `json:"case_id"`
	EnvironmentID string     `json:"environment_id"`
	ChatID        string     `json:"chat_id"`
	Status        string     `json:"status"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	SavedAt       *time.Time `json:"saved_at"`
}

type Job struct {
	Run          JobRun            `json:"run"`
	Take         json.RawMessage   `json:"take"`
	BaseURL      string            `json:"base_url"`
	AllowedHosts []string          `json:"allowed_hosts"`
	Secrets      map[string]string `json:"secrets"`
	Sandbox      bool              `json:"sandbox"`
	Environment  JobEnvironment    `json:"environment"`
}

type JobEnvironment struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type JobRun struct {
	ID      string `json:"id"`
	CaseKey string `json:"case_key"`
	Title   string `json:"title"`
}

type Summary struct {
	Cases     int            `json:"cases"`
	Recorded  int            `json:"recorded"`
	Counts    map[string]int `json:"counts"`
	Running   int            `json:"running"`
	LastBatch *Batch         `json:"last_batch"`
}

func ValidPriority(p string) bool {
	switch p {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityCritical:
		return true
	}
	return false
}

func FinalRunStatus(s string) bool {
	switch s {
	case RunPass, RunFail, RunBlocked, RunError, RunCanceled:
		return true
	}
	return false
}

func ValidCaseStatus(s string) bool {
	switch s {
	case CaseDraft, CaseReady, RunPass, RunFail, RunBlocked, RunError, RunQueued, RunRunning:
		return true
	}
	return false
}

func ValidArtifactKind(k string) bool {
	switch k {
	case ArtifactVideo, ArtifactTake, ArtifactChapters, ArtifactShot, ArtifactPoster:
		return true
	}
	return false
}

func ValidSecretName(n string) bool { return secretNameRe.MatchString(n) }

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func normalizeTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", apperr.Invalid("evidence.errors.title_required")
	}
	if utf8.RuneCountInString(s) > maxTitle {
		return "", apperr.Invalid("evidence.errors.title_too_long", "max", maxTitle)
	}
	return s, nil
}

func normalizeTags(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if utf8.RuneCountInString(t) > maxTag {
			return nil, apperr.Invalid("evidence.errors.tag_too_long", "max", maxTag)
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, apperr.Invalid("evidence.errors.too_many_tags", "max", maxTags)
	}
	return out, nil
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", apperr.Invalid("evidence.errors.base_url_invalid")
	}
	if u.User != nil {
		return "", apperr.Invalid("evidence.errors.base_url_userinfo")
	}
	u.Fragment, u.RawQuery = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

func normalizeHosts(in []string) ([]string, error) {
	out := []string{}
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostRe.MatchString(h) {
			return nil, apperr.Invalid("evidence.errors.host_invalid", "host", h)
		}
		out = append(out, h)
	}
	return out, nil
}

// Only what the daemon checks; the runner validates the rest against the thulu-browser schema
// and reports an invalid take as an error run.
type takeShape struct {
	Version int               `json:"version"`
	Title   string            `json:"title"`
	BaseURL string            `json:"base_url"`
	Steps   []json.RawMessage `json:"steps"`
}

func ValidateTake(raw []byte) (json.RawMessage, error) {
	if len(raw) > maxTakeBytes {
		return nil, apperr.TooLarge("evidence.errors.take_too_large", "max", maxTakeBytes)
	}
	var t takeShape
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, apperr.Invalid("evidence.errors.take_not_json")
	}
	if t.Version != 1 {
		return nil, apperr.Invalid("evidence.errors.take_version")
	}
	if _, err := normalizeBaseURL(t.BaseURL); err != nil {
		return nil, apperr.Invalid("evidence.errors.take_base_url")
	}
	if len(t.Steps) == 0 {
		return nil, apperr.Invalid("evidence.errors.take_no_steps")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, apperr.Invalid("evidence.errors.take_not_json")
	}
	return json.RawMessage(buf.Bytes()), nil
}
