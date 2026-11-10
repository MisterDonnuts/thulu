package types

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

type Stage string

const (
	StageBacklog    Stage = "backlog"
	StageRefinement Stage = "refinement"
	StageReview     Stage = "review"
	StageInProgress Stage = "in_progress"
	StageAIReview   Stage = "ai_review"
	StageTesting    Stage = "testing"
	StageHandoff    Stage = "handoff"
	StageRelease    Stage = "release"
	StageDone       Stage = "done"

	StageMention Stage = "mention"

	StageChat Stage = "chat"
)

// No hyphens: stage ids are embedded in hyphen-delimited instance IDs
// ("<agent>-task-<id>-<stage>-<ts>"), filenames, Docker labels and Prometheus labels.
var stageIDRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// Shape only: label parsers keep well-formed ids of stages not yet in the registry, so per-stage
// config survives stage recreation.
func ValidStageID(s string) bool { return stageIDRe.MatchString(s) }

// Flow behavior must come from these flags, never from a stage's name, so user-defined stages
// behave like the built-ins.
type StageInfo struct {
	ID             Stage  `json:"id"`
	Label          string `json:"label"`
	Color          string `json:"color"`
	Position       int    `json:"position"`
	AutoAdvance    bool   `json:"auto_advance"`
	BlockedByFlags bool   `json:"blocked_by_flags"`
	Initial        bool   `json:"initial"`
	Terminal       bool   `json:"terminal"`
	IngestFlags    bool   `json:"ingest_flags"`
	PushBranch     bool   `json:"push_branch"`
	OpenPR         bool   `json:"open_pr"`
	WipGroup       string `json:"wip_group"`
	Virtual        bool   `json:"virtual"`

	RequiredApprovals  []ApprovalRequirement `json:"required_approvals,omitempty"`
	OnChangesRequested string                `json:"on_changes_requested,omitempty"`
	ChangesKickBackTo  Stage                 `json:"changes_kick_back_to,omitempty"`
}

const (
	ChangesResume   = "resume"
	ChangesKickBack = "kick_back"
	ChangesStay     = "stay"
)

type ApprovalRequirement struct {
	Key           string   `json:"key"`
	Label         string   `json:"label"`
	TaskTypes     []string `json:"task_types,omitempty"`
	ResetOnChange bool     `json:"reset_on_change"`
}

func (r *ApprovalRequirement) UnmarshalJSON(b []byte) error {
	type plain ApprovalRequirement
	v := plain{ResetOnChange: true}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*r = ApprovalRequirement(v)
	return nil
}

const (
	WipGroupWork   = "work"
	WipGroupReview = "review"
)

var VirtualStages = []Stage{StageMention, StageChat}

type ParamDef struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Default string   `json:"default,omitempty"`
	Options []string `json:"options,omitempty"`
}

type AuditorSpec struct {
	Stages       []string          `json:"stages,omitempty"`
	TurnsByStage map[string]string `json:"turns_by_stage,omitempty"`
	ModelByStage map[string]string `json:"model_by_stage,omitempty"`
	// Empty for a stage: the auditor runs on the agent's engine.
	EngineByStage map[string]string   `json:"engine_by_stage,omitempty"`
	SkillsByStage map[string][]string `json:"skills_by_stage,omitempty"`
}

func (a *AuditorSpec) RunsForStage(stage string) bool {
	if a == nil {
		return false
	}
	for _, s := range a.Stages {
		if s == stage {
			return true
		}
	}
	return false
}

func (a *AuditorSpec) TurnsForStage(stage string) string {
	if a == nil {
		return ""
	}
	return a.TurnsByStage[stage]
}

func (a *AuditorSpec) ModelForStage(stage string) string {
	if a == nil {
		return ""
	}
	return a.ModelByStage[stage]
}

func (a *AuditorSpec) EngineForStage(stage string) string {
	if a == nil {
		return ""
	}
	return a.EngineByStage[stage]
}

func (a *AuditorSpec) SkillsForStage(stage string) string {
	if a == nil {
		return ""
	}
	return strings.Join(a.SkillsByStage[stage], ",")
}

type AgentType struct {
	Name             string              `json:"name"`
	Dir              string              `json:"dir"`
	Type             string              `json:"type"`
	Description      string              `json:"description,omitempty"`
	ValidStages      []Stage             `json:"valid_stages,omitempty"`
	Engine           string              `json:"engine,omitempty"`
	SupportedEngines []string            `json:"supported_engines,omitempty"`
	Model            string              `json:"model,omitempty"`
	Effort           string              `json:"effort,omitempty"`
	SkillsMode       string              `json:"skills_mode,omitempty"`
	Image            string              `json:"image,omitempty"`
	WorksWithoutRepo bool                `json:"works_without_repo,omitempty"`
	Params           []ParamDef          `json:"params,omitempty"`
	TurnsByStage     map[string]string   `json:"turns_by_stage,omitempty"`
	SkillsByStage    map[string][]string `json:"skills_by_stage,omitempty"`
	MCPByStage       map[string][]string `json:"mcp_by_stage,omitempty"`
	Auditor          *AuditorSpec        `json:"auditor,omitempty"`
	// Set on the copy an auditor on another engine runs as: its key is that engine's default, never
	// the profile the agent is bound to. Never stored.
	IgnoreLLMBinding bool `json:"-"`
	// omitzero, not omitempty: an agent without a policy must serialize as before, since the
	// Workshop hashes definitions and a new key reads as an edit.
	TokenOptimizer TokenOptimizer `json:"token_optimizer,omitzero"`

	SystemSkillsOff map[string][]string `json:"system_skills_off,omitempty"`

	ActiveWhen []string `json:"-"`

	SystemSkills []SystemSkillRef `json:"-"`
}

type SystemSkillRef struct {
	Name string
	MCP  string
	When string
}

const defaultStageTurns = "20"

func (at *AgentType) SupportsStage(s Stage) bool {
	for _, v := range at.ValidStages {
		if v == s {
			return true
		}
	}
	return false
}

func (at *AgentType) SupportsChat() bool {
	return at.SupportsStage(StageChat)
}

func (at *AgentType) AnswersMentions() bool {
	return len(at.ValidStages) == 0 || at.SupportsStage(StageMention) || at.SupportsChat()
}

func (at *AgentType) AnswersMentionsByChat() bool {
	return at.SupportsChat() && !at.SupportsStage(StageMention)
}

func (at *AgentType) TurnsForStage(stage string) string {
	if v, ok := at.TurnsByStage[stage]; ok && v != "" {
		return v
	}
	return defaultStageTurns
}

func (at *AgentType) DefaultSkills(stage string) string {
	return strings.Join(at.StageSkills(stage), ",")
}

// The one place the system-skill opt-out is applied: $SKILLS, chat containers and the run bundle
// (and its ETag) all read this.
// The bundle serves a same-named grant from the system copy, so turning a system skill off drops
// the grant too.
func (at *AgentType) StageSkills(stage string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	system := make(map[string]SystemSkillRef, len(at.SystemSkills))
	for _, sk := range at.SystemSkills {
		system[sk.Name] = sk
	}
	for _, name := range at.SkillsByStage[stage] {
		if sk, ok := system[strings.TrimSpace(name)]; ok && (at.SystemSkillOff(stage, name) || !at.whenHolds(sk.When)) {
			continue
		}
		add(name)
	}
	for _, sk := range at.SystemSkills {
		if MCPWired(at.MCPByStage[stage], sk.MCP) && at.whenHolds(sk.When) && !at.SystemSkillOff(stage, sk.Name) {
			add(sk.Name)
		}
	}
	return out
}

func (at *AgentType) whenHolds(when string) bool {
	when = strings.TrimSpace(when)
	if when == "" {
		return true
	}
	for _, c := range at.ActiveWhen {
		if c == when {
			return true
		}
	}
	return false
}

func (at *AgentType) SystemSkillOff(stage, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, off := range at.SystemSkillsOff[stage] {
		if strings.TrimSpace(off) == name {
			return true
		}
	}
	return false
}

func MCPWired(entries []string, server string) bool {
	if server == "" {
		return true
	}
	for _, entry := range entries {
		if MCPServerName(entry) == server {
			return true
		}
	}
	return false
}

func MCPServerName(entry string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(entry), "@")
	return name
}

func (at *AgentType) DefaultMCP(stage string) string {
	if at.MCPByStage == nil {
		return ""
	}
	return strings.Join(at.MCPByStage[stage], ",")
}

type composeFileDiscover struct {
	Services map[string]composeServiceDiscover `yaml:"services"`
}

type composeServiceDiscover struct {
	Labels      map[string]string `yaml:"labels"`
	Environment []string          `yaml:"environment"`
}

func parseTurnsLabel(label string) map[string]string {
	if label == "" {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(label, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		chunks := strings.SplitN(part, ":", 2)
		if len(chunks) != 2 {
			continue
		}
		stage := strings.TrimSpace(chunks[0])
		turns := strings.TrimSpace(chunks[1])
		if stage == "" || turns == "" || !ValidStageID(stage) {
			continue
		}
		out[stage] = turns
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseAuditorLabels(labels map[string]string, validStages []Stage) *AuditorSpec {
	stages := auditorStages(labels, validStages)
	if len(stages) == 0 {
		return nil
	}
	spec := &AuditorSpec{Stages: stages}
	seedLegacyAuditor(spec, labels)
	applyAuditorStageOverrides(spec, labels)
	return spec
}

func auditorStages(labels map[string]string, validStages []Stage) []string {
	if stagesLabel := strings.TrimSpace(labels[LabelAuditorStages]); stagesLabel != "" {
		var stages []string
		for _, part := range strings.Split(stagesLabel, ",") {
			s := strings.TrimSpace(part)
			if ValidStageID(s) {
				stages = append(stages, s)
			}
		}
		return stages
	}
	if strings.TrimSpace(labels[LabelAuditor]) == "true" {
		stages := make([]string, 0, len(validStages))
		for _, s := range validStages {
			stages = append(stages, string(s))
		}
		return stages
	}
	return nil
}

func seedLegacyAuditor(spec *AuditorSpec, labels map[string]string) {
	if legacyTurns := strings.TrimSpace(labels[LabelAuditorTurnsLegacy]); legacyTurns != "" {
		spec.TurnsByStage = map[string]string{}
		for _, s := range spec.Stages {
			spec.TurnsByStage[s] = legacyTurns
		}
	}
	if legacyModel := strings.TrimSpace(labels[LabelAuditorModelLegacy]); legacyModel != "" {
		spec.ModelByStage = map[string]string{}
		for _, s := range spec.Stages {
			spec.ModelByStage[s] = legacyModel
		}
	}
	if legacySkills := strings.TrimSpace(labels[LabelAuditorSkillsLegacy]); legacySkills != "" {
		spec.SkillsByStage = map[string][]string{}
		for _, s := range spec.Stages {
			spec.SkillsByStage[s] = parseSkillsList(legacySkills)
		}
	}
}

func applyAuditorStageOverrides(spec *AuditorSpec, labels map[string]string) {
	spec.TurnsByStage = mergeStageStrings(spec.TurnsByStage, labels, LabelAuditorTurnsPrefix)
	spec.ModelByStage = mergeStageStrings(spec.ModelByStage, labels, LabelAuditorModelPrefix)
	spec.EngineByStage = mergeStageStrings(spec.EngineByStage, labels, LabelAuditorEnginePrefix)
	spec.SkillsByStage = mergeStageSkills(spec.SkillsByStage, labels, LabelAuditorSkillsPrefix)
}

func mergeStageStrings(dst, labels map[string]string, prefix string) map[string]string {
	for k, v := range labels {
		v = strings.TrimSpace(v)
		stage := auditorStageKey(k, prefix)
		if v == "" || stage == "" {
			continue
		}
		if dst == nil {
			dst = map[string]string{}
		}
		dst[stage] = v
	}
	return dst
}

func mergeStageSkills(dst map[string][]string, labels map[string]string, prefix string) map[string][]string {
	for k, v := range labels {
		stage := auditorStageKey(k, prefix)
		skills := parseSkillsList(strings.TrimSpace(v))
		if stage == "" || len(skills) == 0 {
			continue
		}
		if dst == nil {
			dst = map[string][]string{}
		}
		dst[stage] = skills
	}
	return dst
}

func auditorStageKey(key, prefix string) string {
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	stage := strings.TrimPrefix(key, prefix)
	if !ValidStageID(stage) {
		return ""
	}
	return stage
}

func parseSkillsList(label string) []string {
	if label == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(label, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

var paramMeta = map[string]ParamDef{
	"MAX_TURNS": {
		Name: "MAX_TURNS", Type: "text",
		Default: "20",
	},
	"TARGET_URL": {
		Name: "TARGET_URL", Type: "text",
	},
	"AGENT_NAME": {
		Name: "AGENT_NAME", Type: "text",
	},
	"MAX_PAGES": {
		Name: "MAX_PAGES", Type: "text",
		Default: "6",
	},
	"PROMPT": {
		Name: "PROMPT", Type: "textarea",
	},
}

const (
	LabelTurns                 = "thulu.turns"
	LabelStages                = "thulu.stages"
	LabelDescription           = "thulu.description"
	LabelEngine                = "thulu.engine"
	LabelEnginesSupported      = "thulu.engines.supported"
	LabelModel                 = "thulu.model"
	LabelEffort                = "thulu.effort"
	LabelSkillsDefaultPrefix   = "thulu.skills.default."
	LabelSkillsMode            = "thulu.skills.mode"
	LabelMCPDefaultPrefix      = "thulu.mcp.default."
	LabelSystemSkillsOffPrefix = "thulu.skills.system_off."
	LabelAuditor               = "thulu.auditor"
	LabelAuditorStages         = "thulu.auditor.stages"
	LabelAuditorTurnsPrefix    = "thulu.auditor.turns."
	LabelAuditorModelPrefix    = "thulu.auditor.model."
	LabelAuditorEnginePrefix   = "thulu.auditor.engine."
	LabelAuditorSkillsPrefix   = "thulu.auditor.skills."
	LabelAuditorTurnsLegacy    = "thulu.auditor.turns"
	LabelAuditorModelLegacy    = "thulu.auditor.model"
	LabelAuditorSkillsLegacy   = "thulu.auditor.skills"
	LabelWorksWithoutRepo      = "thulu.works_without_repo"
	LabelTokenOptimizer        = "thulu.token_optimizer" //nolint:gosec // G101: label name
	LabelTokenOptimizerEngine  = LabelTokenOptimizer + ".engine"
	LabelTokenOptimizerModel   = LabelTokenOptimizer + ".model"
)

func parseEnginesSupported(label, fallback string) []string {
	label = strings.TrimSpace(label)
	if label == "" {
		if fallback == "" {
			return nil
		}
		return []string{fallback}
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, part := range strings.Split(label, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 && fallback != "" {
		return []string{fallback}
	}
	return out
}

func (at *AgentType) SupportsEngine(name string) bool {
	if name == "" {
		return false
	}
	for _, e := range at.SupportedEngines {
		if e == name {
			return true
		}
	}
	if len(at.SupportedEngines) == 0 && at.Engine != "" && at.Engine == name {
		return true
	}
	return false
}

func parseStagesLabel(label string) []Stage {
	if label == "" {
		return nil
	}
	var out []Stage
	for _, part := range strings.Split(label, ",") {
		s := strings.TrimSpace(part)
		if ValidStageID(s) {
			out = append(out, Stage(s))
		}
	}
	return out
}

// A missing agents/ dir is a normal install (agents live in Postgres); erroring made GET /types 500
// and the board think the daemon was down.
func Discover(baseDir string) ([]AgentType, error) {
	agentsDir := filepath.Join(baseDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []AgentType{}, nil
	}
	if err != nil {
		return nil, err
	}

	var result []AgentType
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if at, ok := discoverAgent(agentsDir, e.Name()); ok {
			result = append(result, at)
		}
	}

	return result, nil
}

func discoverAgent(agentsDir, name string) (AgentType, bool) {
	data, err := safepath.ReadFile(filepath.Join(agentsDir, name), "docker-compose.yml")
	if err != nil {
		return AgentType{}, false
	}
	var cf composeFileDiscover
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return AgentType{}, false
	}
	for _, svc := range cf.Services {
		if svc.Labels["thulu.agent"] == "true" {
			return agentTypeFromService(agentsDir, name, svc), true
		}
	}
	return AgentType{}, false
}

func agentTypeFromService(agentsDir, name string, svc composeServiceDiscover) AgentType {
	at := AgentType{
		Name:             name,
		Dir:              filepath.Join(agentsDir, name),
		Type:             svc.Labels["thulu.type"],
		Description:      svc.Labels[LabelDescription],
		ValidStages:      parseStagesLabel(svc.Labels[LabelStages]),
		Engine:           strings.TrimSpace(svc.Labels[LabelEngine]),
		SupportedEngines: parseEnginesSupported(svc.Labels[LabelEnginesSupported], strings.TrimSpace(svc.Labels[LabelEngine])),
		Model:            svc.Labels[LabelModel],
		Effort:           svc.Labels[LabelEffort],
		SkillsMode:       normalizeSkillsMode(svc.Labels[LabelSkillsMode]),
		WorksWithoutRepo: svc.Labels[LabelWorksWithoutRepo] == "true",
		TurnsByStage:     parseTurnsLabel(svc.Labels[LabelTurns]),
	}
	parseDefaultSkillsAndMCP(svc.Labels, &at)
	at.Auditor = parseAuditorLabels(svc.Labels, at.ValidStages)
	at.TokenOptimizer = parseTokenOptimizerLabels(svc.Labels)
	at.Params = parseServiceParams(svc.Environment, at.Engine)
	return at
}

const (
	SkillsModeAuto   = "auto"
	SkillsModeInline = "inline"
	SkillsModeNative = "native"
)

func normalizeSkillsMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case SkillsModeInline:
		return SkillsModeInline
	case SkillsModeNative:
		return SkillsModeNative
	default:
		return SkillsModeAuto
	}
}

func parseDefaultSkillsAndMCP(labels map[string]string, at *AgentType) {
	for labelKey, labelVal := range labels {
		switch {
		case strings.HasPrefix(labelKey, LabelSkillsDefaultPrefix):
			at.SkillsByStage = putStageList(at.SkillsByStage, strings.TrimPrefix(labelKey, LabelSkillsDefaultPrefix), labelVal)
		case strings.HasPrefix(labelKey, LabelMCPDefaultPrefix):
			at.MCPByStage = putStageList(at.MCPByStage, strings.TrimPrefix(labelKey, LabelMCPDefaultPrefix), labelVal)
		case strings.HasPrefix(labelKey, LabelSystemSkillsOffPrefix):
			at.SystemSkillsOff = putStageList(at.SystemSkillsOff, strings.TrimPrefix(labelKey, LabelSystemSkillsOffPrefix), labelVal)
		}
	}
}

func putStageList(m map[string][]string, stage, val string) map[string][]string {
	items := parseSkillsList(val)
	if stage == "" || len(items) == 0 {
		return m
	}
	if m == nil {
		m = make(map[string][]string)
	}
	m[stage] = items
	return m
}

func parseServiceParams(environment []string, engineName string) []ParamDef {
	var params []ParamDef
	for _, env := range environment {
		key := strings.SplitN(env, "=", 2)[0]
		if key == "HOME" || key == "INSTANCE_ID" {
			continue
		}
		if key == "MODEL" {
			params = append(params, modelParam(engineName))
			continue
		}
		if meta, ok := paramMeta[key]; ok {
			params = append(params, meta)
		} else {
			params = append(params, ParamDef{Name: key, Type: "text"})
		}
	}
	return params
}

func modelParam(engineName string) ParamDef {
	eng, err := engine.Get(engineName)
	if err != nil {
		return ParamDef{Name: "MODEL", Type: "text"}
	}
	caps := eng.Capabilities()
	return ParamDef{
		Name: "MODEL", Type: "select",
		Default: caps.DefaultModel,
		Options: append([]string(nil), caps.Models...),
	}
}

type LaunchParams struct {
	Env               map[string]string `json:"env"`
	NoBuild           bool              `json:"no_build,omitempty"`
	WorkspacePaths    map[string]string `json:"workspace_paths,omitempty"`
	StagePaths        map[string]string `json:"stage_paths,omitempty"` //nolint:lll // sanitized project name → sidecar host path; mounted at /workspace/projects/<name>/.thulu
	WorkspaceReadOnly bool              `json:"workspace_read_only,omitempty"`

	// Required when the daemon runs inside a container: its paths do not exist on the Docker host,
	// so bind mounts would resolve to nothing.
	UseCopyMode bool `json:"use_copy_mode,omitempty"`

	DataVolume string `json:"data_volume,omitempty"`

	OutputContainerPath string `json:"output_container_path,omitempty"`

	LabelOverrides map[string]string `json:"label_overrides,omitempty"`

	ExtraMounts []ExtraMount `json:"extra_mounts,omitempty"`

	NamedVolumes []ExtraMount `json:"named_volumes,omitempty"`
	// EngineCacheScope mounts that scope's engine volume (internal/enginecache) and points
	// OCTOPUS_ENGINE_DIR at it, so engines installed on first use survive the container. Empty: the
	// container installs into its own home.
	EngineCacheScope string `json:"engine_cache_scope,omitempty"`

	Resources ResourceCaps `json:"resources,omitempty"`
}

type ResourceCaps struct {
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	PidsLimit   int64 `json:"pids_limit,omitempty"`
	NanoCPUs    int64 `json:"nano_cpus,omitempty"`
}

type ExtraMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}
