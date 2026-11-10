package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type AgentSettings struct {
	Name                 string              `json:"name"`
	Description          string              `json:"description"`
	ValidStages          []string            `json:"valid_stages"`
	Model                string              `json:"model,omitempty"`
	Effort               string              `json:"effort,omitempty"`
	Engine               string              `json:"engine,omitempty"`
	SkillsMode           string              `json:"skills_mode,omitempty"`
	WorksWithoutRepo     bool                `json:"works_without_repo"`
	TurnsByStage         map[string]string   `json:"turns_by_stage"`
	SkillsByStage        map[string][]string `json:"skills_by_stage"`
	MCPByStage           map[string][]string `json:"mcp_by_stage"`
	AuditorStages        []string            `json:"auditor_stages"`
	AuditorTurnsByStage  map[string]string   `json:"auditor_turns_by_stage"`
	AuditorModelByStage  map[string]string   `json:"auditor_model_by_stage"`
	AuditorEngineByStage map[string]string   `json:"auditor_engine_by_stage"`
	AuditorSkillsByStage map[string][]string `json:"auditor_skills_by_stage"`
	// On PUT, nil keeps the stored value so clients predating this field don't re-enable every
	// system skill on save; {} clears it.
	SystemSkillsOff     map[string][]string            `json:"system_skills_off"`
	TokenOptimizer      *AgentTokenOptimizer           `json:"token_optimizer"`
	SystemSkills        []SystemSkillInfo              `json:"system_skills,omitempty"`
	SystemSkillsByStage map[string][]SystemSkillStatus `json:"system_skills_by_stage,omitempty"`
	UnknownStages       []string                       `json:"unknown_stages,omitempty"`
}

type AgentTokenOptimizer struct {
	Level  string `json:"level"`
	Engine string `json:"engine"`
	Model  string `json:"model"`
}

func agentTokenOptimizerOf(t types.TokenOptimizer) *AgentTokenOptimizer {
	out := &AgentTokenOptimizer{Level: t.Level, Engine: t.Engine, Model: t.Model}
	if out.Level == "" {
		out.Level = types.TokenOptimizerOff
	}
	return out
}

func tokenOptimizerFromSettings(ctx context.Context, in *AgentTokenOptimizer) (types.TokenOptimizer, error) {
	out := types.TokenOptimizer{Level: in.Level, Engine: in.Engine, Model: in.Model}.Normalize()
	known := func(name string) bool {
		_, ok := engineByName(ctx, name)
		return ok
	}
	if err := out.Validate(known); err != nil {
		return types.TokenOptimizer{}, err
	}
	return out, nil
}

type SystemSkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MCP         string `json:"mcp"`
	Enforcement string `json:"enforcement,omitempty"`
	When        string `json:"when,omitempty"`
}

type SystemSkillStatus struct {
	SystemSkillInfo
	Wired   bool `json:"wired"`
	Enabled bool `json:"enabled"`
}

func (s *Server) handleGetAgentSettings(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	settings, err := s.agentSettings(tenant, r.PathValue("name"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	s.attachSystemSkills(r, store, &settings)
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) attachSystemSkills(r *http.Request, store *agentstore.Store, settings *AgentSettings) {
	if store == nil {
		return
	}
	list, err := store.ListSystemSkills(r.Context())
	if err != nil {
		return
	}
	infos := make([]SystemSkillInfo, 0, len(list))
	for i := range list {
		info := SystemSkillInfo{Name: list[i].Name, MCP: list[i].MCP, When: list[i].When}
		if def, found, defErr := storedSkillDef(r, store, agentstore.SystemTenant, list[i].Name); defErr == nil && found {
			info.Description, info.Enforcement = def.Description, def.Enforcement
		}
		infos = append(infos, info)
	}
	settings.SystemSkills = infos
	settings.SystemSkillsByStage = systemSkillsByStage(infos, *settings)
}

// Must make the same decision types.StageSkills makes at launch.
func systemSkillsByStage(infos []SystemSkillInfo, settings AgentSettings) map[string][]SystemSkillStatus {
	at := types.AgentType{MCPByStage: settings.MCPByStage, SystemSkillsOff: settings.SystemSkillsOff}
	out := make(map[string][]SystemSkillStatus, len(settings.ValidStages))
	for _, stage := range settings.ValidStages {
		rows := make([]SystemSkillStatus, 0, len(infos))
		for _, info := range infos {
			rows = append(rows, SystemSkillStatus{
				SystemSkillInfo: info,
				Wired:           types.MCPWired(settings.MCPByStage[stage], info.MCP),
				Enabled:         !at.SystemSkillOff(stage, info.Name),
			})
		}
		out[stage] = rows
	}
	return out
}

func normalizeSystemSkillsOff(in map[string][]string) map[string][]string {
	var out map[string][]string
	for stage, names := range in {
		if !types.ValidStageID(stage) {
			continue
		}
		seen := map[string]bool{}
		clean := make([]string, 0, len(names))
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			clean = append(clean, name)
		}
		if len(clean) == 0 {
			continue
		}
		sort.Strings(clean)
		if out == nil {
			out = map[string][]string{}
		}
		out[stage] = clean
	}
	return out
}

func (s *Server) agentSettings(tenant, name string) (AgentSettings, error) {
	discovered, _ := s.agents(tenant).All() //nolint:errcheck // best-effort listing
	var at *types.AgentType
	for i := range discovered {
		if discovered[i].Name == name {
			at = &discovered[i]
			break
		}
	}
	if at == nil {
		return AgentSettings{}, apperr.NotFound("api.agents.errors.not_in_catalog")
	}
	settings := settingsOf(at)
	settings.UnknownStages = s.collectUnknownStages(settings)
	return settings, nil
}

func settingsOf(at *types.AgentType) AgentSettings {
	settings := AgentSettings{
		Name:                 at.Name,
		Description:          at.Description,
		Model:                at.Model,
		Effort:               at.Effort,
		Engine:               at.Engine,
		SkillsMode:           at.SkillsMode,
		WorksWithoutRepo:     at.WorksWithoutRepo,
		TurnsByStage:         map[string]string{},
		SkillsByStage:        map[string][]string{},
		MCPByStage:           map[string][]string{},
		AuditorStages:        []string{},
		AuditorTurnsByStage:  map[string]string{},
		AuditorModelByStage:  map[string]string{},
		AuditorEngineByStage: map[string]string{},
		AuditorSkillsByStage: map[string][]string{},
		SystemSkillsOff:      map[string][]string{},
		TokenOptimizer:       agentTokenOptimizerOf(at.TokenOptimizer),
	}
	for k, v := range at.SystemSkillsOff {
		settings.SystemSkillsOff[k] = v
	}
	for _, st := range at.ValidStages {
		settings.ValidStages = append(settings.ValidStages, string(st))
	}
	for k, v := range at.TurnsByStage {
		settings.TurnsByStage[k] = v
	}
	for k, v := range at.SkillsByStage {
		settings.SkillsByStage[k] = v
	}
	for k, v := range at.MCPByStage {
		settings.MCPByStage[k] = v
	}
	if at.Auditor != nil {
		settings.AuditorStages = append(settings.AuditorStages, at.Auditor.Stages...)
		for k, v := range at.Auditor.TurnsByStage {
			settings.AuditorTurnsByStage[k] = v
		}
		for k, v := range at.Auditor.ModelByStage {
			settings.AuditorModelByStage[k] = v
		}
		for k, v := range at.Auditor.EngineByStage {
			settings.AuditorEngineByStage[k] = v
		}
		for k, v := range at.Auditor.SkillsByStage {
			settings.AuditorSkillsByStage[k] = v
		}
	}
	return settings
}

func settingsSnapshot(at *types.AgentType) []byte {
	out, err := json.MarshalIndent(settingsOf(at), "", "  ")
	if err != nil {
		return nil
	}
	return out
}

func (s *Server) collectUnknownStages(as AgentSettings) []string {
	seen := map[string]bool{}
	note := func(id string) {
		if id != "" && !seen[id] && !s.catalogHas(types.Stage(id)) {
			seen[id] = true
		}
	}
	for _, id := range as.ValidStages {
		note(id)
	}
	for _, id := range as.AuditorStages {
		note(id)
	}
	for id := range as.TurnsByStage {
		note(id)
	}
	for id := range as.SkillsByStage {
		note(id)
	}
	for id := range as.MCPByStage {
		note(id)
	}
	for id := range as.AuditorTurnsByStage {
		note(id)
	}
	for id := range as.AuditorModelByStage {
		note(id)
	}
	for id := range as.AuditorEngineByStage {
		note(id)
	}
	for id := range as.AuditorSkillsByStage {
		note(id)
	}
	for id := range as.SystemSkillsOff {
		note(id)
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handlePutAgentSettings(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	var settings AgentSettings
	if !decodeBody(w, r, &settings) {
		return
	}
	if settings.TokenOptimizer != nil {
		policy, err := tokenOptimizerFromSettings(r.Context(), settings.TokenOptimizer)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		settings.TokenOptimizer = agentTokenOptimizerOf(policy)
	}
	if err := normalizeAuditorEngines(&settings); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	// Write rows when a store exists: the agents dir is an emptyDir reseeded from a ConfigMap, so
	// compose edits are lost on restart.
	if store != nil {
		if err := s.putAgentSettingsRow(r.Context(), store, tenant, name, settings, actorID(r)); err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		s.invalidateCatalog(tenant)
		s.auditAgentDef(r, tenant, name, "api.agents.audit.settings_updated")
		writeJSON(w, http.StatusOK, s.savedAgentSettings(r, store, tenant, name, settings))
		return
	}

	dir, err := s.agentDir(name)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	if status, err := writeAgentSettings(dir, name, settings); err != nil {
		writeError(w, r, status, err)
		return
	}
	s.invalidateCatalog("")
	writeJSON(w, http.StatusOK, s.savedAgentSettings(r, nil, "", name, settings))
}

func (s *Server) savedAgentSettings(r *http.Request, store *agentstore.Store, tenant, name string, sent AgentSettings) AgentSettings {
	out, err := s.agentSettings(tenant, name)
	if err != nil {
		out = sent
	}
	s.attachSystemSkills(r, store, &out)
	return out
}

// Start from the existing row: fields the form lacks (image, template flag, creation metadata) must
// not be cleared.
func (s *Server) putAgentSettingsRow(ctx context.Context, store *agentstore.Store,
	tenant, name string, in AgentSettings, actor string,
) error {
	existing, err := store.GetAgent(ctx, tenant, name)
	if err != nil {
		return err
	}
	before := settingsSnapshot(&existing.AgentType)
	beforeDef, err := json.Marshal(existing.AgentType)
	if err != nil {
		return err
	}
	at := &existing.AgentType
	at.Name = name
	at.Description = in.Description
	at.Model = in.Model
	at.Effort = in.Effort
	at.WorksWithoutRepo = in.WorksWithoutRepo
	at.TurnsByStage = in.TurnsByStage
	at.SkillsByStage = in.SkillsByStage
	at.MCPByStage = in.MCPByStage
	if in.SystemSkillsOff != nil {
		at.SystemSkillsOff = normalizeSystemSkillsOff(in.SystemSkillsOff)
	}
	if in.TokenOptimizer != nil {
		at.TokenOptimizer = types.TokenOptimizer{
			Level: in.TokenOptimizer.Level, Engine: in.TokenOptimizer.Engine, Model: in.TokenOptimizer.Model,
		}.Normalize()
	}
	at.ValidStages = at.ValidStages[:0]
	for _, id := range in.ValidStages {
		if types.ValidStageID(id) {
			at.ValidStages = append(at.ValidStages, types.Stage(id))
		}
	}
	if in.Engine != "" {
		at.Engine = in.Engine
	}
	// Store "auto" as auto, not the resolved mode, so the agent follows its engine's default if the
	// engine changes.
	at.SkillsMode = normalizeSkillsModeInput(in.SkillsMode)
	at.Auditor = auditorFromSettings(in)

	afterDef, err := json.Marshal(existing.AgentType)
	if err != nil {
		return err
	}
	// Row, revision and definition snapshot commit in one transaction: a history with holes reads
	// as complete.
	return store.InTx(ctx, func(tx *agentstore.Tx) error {
		if !bytes.Equal(beforeDef, afterDef) {
			if err := tx.SnapshotAgent(tenant, name, agentstore.DefReasonSettings, actor); err != nil {
				return err
			}
		}
		if _, err := tx.PutAgent(tenant, existing, actor); err != nil {
			return err
		}
		return tx.RecordSettings(tenant, name, before, settingsSnapshot(at), actor)
	})
}

// Return nil, not an empty spec: a non-nil AuditorSpec with no stages reads as configured
// everywhere else.
func auditorFromSettings(in AgentSettings) *types.AuditorSpec {
	if len(in.AuditorStages) == 0 {
		return nil
	}
	return &types.AuditorSpec{
		Stages:        in.AuditorStages,
		TurnsByStage:  in.AuditorTurnsByStage,
		ModelByStage:  in.AuditorModelByStage,
		EngineByStage: in.AuditorEngineByStage,
		SkillsByStage: in.AuditorSkillsByStage,
	}
}

// An empty engine means "the agent's", so it is not stored. A name the daemon does not know would
// only fail when the run ends and the audit starts, so it is refused here.
func normalizeAuditorEngines(in *AgentSettings) error {
	for stage, name := range in.AuditorEngineByStage {
		name = strings.TrimSpace(name)
		if name == "" {
			delete(in.AuditorEngineByStage, stage)
			continue
		}
		if _, err := engine.Get(name); err != nil {
			return apperr.Invalid("api.llm.errors.unknown_engine", "engine", name)
		}
		in.AuditorEngineByStage[stage] = name
	}
	return nil
}

func normalizeSkillsModeInput(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case types.SkillsModeInline:
		return types.SkillsModeInline
	case types.SkillsModeNative:
		return types.SkillsModeNative
	default:
		return types.SkillsModeAuto
	}
}

func writeAgentSettings(dir, name string, settings AgentSettings) (int, error) {
	composePath := filepath.Join(dir, "docker-compose.yml")
	// Read through os.Root (safepath) so symlinks cannot resolve outside the agent dir.
	data, err := safepath.ReadFile(dir, "docker-compose.yml")
	if err != nil {
		return http.StatusNotFound, apperr.NotFound("api.agents.errors.compose_not_found")
	}

	var doc map[string]interface{}
	if unmarshalErr := yaml.Unmarshal(data, &doc); unmarshalErr != nil {
		return http.StatusInternalServerError, apperr.Wrap(unmarshalErr, "api.agents.errors.compose_parse")
	}

	services, ok := doc["services"].(map[string]interface{})
	if !ok {
		return http.StatusInternalServerError, apperr.Internal("api.agents.errors.compose_no_services")
	}

	svcName := pickAgentService(services)
	svc, _ := services[svcName].(map[string]interface{}) //nolint:errcheck // type assertion; nil check below handles failure
	if svc == nil {
		return http.StatusInternalServerError, apperr.Internal("api.agents.errors.agent_service_not_found")
	}

	preserved := preservedLabels(svc)
	newLabels := buildAgentLabels(name, settings, preserved)

	labelsOut := map[string]interface{}{}
	for k, v := range newLabels {
		labelsOut[k] = v
	}
	svc["labels"] = labelsOut
	services[svcName] = svc
	doc["services"] = services

	out, err := yaml.Marshal(doc)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if err := os.WriteFile(composePath, out, 0o644); err != nil {
		return http.StatusInternalServerError, err
	}
	return http.StatusOK, nil
}

var managedAgentLabels = map[string]bool{
	types.LabelDescription:         true,
	types.LabelStages:              true,
	types.LabelModel:               true,
	types.LabelEffort:              true,
	types.LabelEngine:              true,
	types.LabelSkillsMode:          true,
	types.LabelWorksWithoutRepo:    true,
	types.LabelTurns:               true,
	types.LabelAuditor:             true,
	types.LabelAuditorStages:       true,
	types.LabelAuditorTurnsLegacy:  true,
	types.LabelAuditorModelLegacy:  true,
	types.LabelAuditorSkillsLegacy: true,
	"thulu.agent":                  true,
	"thulu.type":                   true,
	"thulu.phases":                 true,
}

func preservedLabels(svc map[string]interface{}) map[string]string {
	preserved := map[string]string{}
	existing, ok := svc["labels"].(map[string]interface{})
	if !ok {
		return preserved
	}
	for k, v := range existing {
		if managedAgentLabels[k] ||
			strings.HasPrefix(k, types.LabelSkillsDefaultPrefix) ||
			strings.HasPrefix(k, types.LabelMCPDefaultPrefix) ||
			strings.HasPrefix(k, types.LabelAuditorTurnsPrefix) ||
			strings.HasPrefix(k, types.LabelAuditorModelPrefix) ||
			strings.HasPrefix(k, types.LabelAuditorEnginePrefix) ||
			strings.HasPrefix(k, types.LabelAuditorSkillsPrefix) {
			continue
		}
		preserved[k] = fmt.Sprintf("%v", v)
	}
	return preserved
}

func buildAgentLabels(name string, settings AgentSettings, preserved map[string]string) map[string]string {
	newLabels := map[string]string{
		"thulu.agent":     "true",
		"thulu.type":      name,
		types.LabelStages: csvOf(settings.ValidStages),
	}
	if turns := turnsByStageToLabel(settings.TurnsByStage); turns != "" {
		newLabels[types.LabelTurns] = turns
	}
	if settings.Description != "" {
		newLabels[types.LabelDescription] = settings.Description
	}
	if settings.Model != "" {
		newLabels[types.LabelModel] = settings.Model
	}
	if settings.Effort != "" {
		newLabels[types.LabelEffort] = settings.Effort
	}
	if settings.Engine != "" {
		newLabels[types.LabelEngine] = settings.Engine
	}
	if settings.WorksWithoutRepo {
		newLabels[types.LabelWorksWithoutRepo] = "true"
	}
	if m := settings.SkillsMode; m == types.SkillsModeInline || m == types.SkillsModeNative {
		newLabels[types.LabelSkillsMode] = m
	}
	applyAuditorSettingsLabels(newLabels, settings)
	for stage, skills := range settings.SkillsByStage {
		if len(skills) > 0 {
			newLabels[types.LabelSkillsDefaultPrefix+stage] = csvOf(skills)
		}
	}
	for stage, mcpTools := range settings.MCPByStage {
		if len(mcpTools) > 0 {
			newLabels[types.LabelMCPDefaultPrefix+stage] = csvOf(mcpTools)
		}
	}
	applySystemSkillsOffLabels(newLabels, preserved, settings.SystemSkillsOff)
	applyTokenOptimizerLabels(newLabels, preserved, settings.TokenOptimizer)
	for k, v := range preserved {
		newLabels[k] = v
	}
	return newLabels
}

// With off nil, stored labels survive only because preservedLabels does not strip
// LabelSystemSkillsOffPrefix.
func applySystemSkillsOffLabels(newLabels, preserved map[string]string, off map[string][]string) {
	if off == nil {
		return
	}
	for k := range preserved {
		if strings.HasPrefix(k, types.LabelSystemSkillsOffPrefix) {
			delete(preserved, k)
		}
	}
	for stage, names := range normalizeSystemSkillsOff(off) {
		newLabels[types.LabelSystemSkillsOffPrefix+stage] = csvOf(names)
	}
}

func applyTokenOptimizerLabels(newLabels, preserved map[string]string, t *AgentTokenOptimizer) {
	if t == nil {
		return
	}
	for k := range preserved {
		if types.IsTokenOptimizerLabel(k) {
			delete(preserved, k)
		}
	}
	policy := types.TokenOptimizer{Level: t.Level, Engine: t.Engine, Model: t.Model}
	for k, v := range types.TokenOptimizerLabels(policy) {
		newLabels[k] = v
	}
}

// Never write the flat auditor labels; hand-edited composes still use them via
// parseAuditorLabels.
func applyAuditorSettingsLabels(newLabels map[string]string, settings AgentSettings) {
	activeStageSet := map[string]bool{}
	for _, s := range settings.ValidStages {
		activeStageSet[s] = true
	}
	auditorSet := map[string]bool{}
	var auditorStages []string
	for _, s := range settings.AuditorStages {
		s = strings.TrimSpace(s)
		if s != "" && activeStageSet[s] {
			auditorStages = append(auditorStages, s)
			auditorSet[s] = true
		}
	}
	if len(auditorStages) == 0 {
		return
	}
	newLabels[types.LabelAuditorStages] = csvOf(auditorStages)
	for prefix, byStage := range map[string]map[string]string{
		types.LabelAuditorTurnsPrefix:  settings.AuditorTurnsByStage,
		types.LabelAuditorModelPrefix:  settings.AuditorModelByStage,
		types.LabelAuditorEnginePrefix: settings.AuditorEngineByStage,
	} {
		for stage, v := range byStage {
			if v = strings.TrimSpace(v); v != "" && auditorSet[stage] {
				newLabels[prefix+stage] = v
			}
		}
	}
	for stage, skills := range settings.AuditorSkillsByStage {
		if auditorSet[stage] && len(skills) > 0 {
			newLabels[types.LabelAuditorSkillsPrefix+stage] = csvOf(skills)
		}
	}
}

func pickAgentService(services map[string]interface{}) string {
	for name, raw := range services {
		svc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		labels, ok := svc["labels"].(map[string]interface{})
		if !ok {
			continue
		}
		if v, ok := labels["thulu.agent"]; ok && fmt.Sprintf("%v", v) == "true" {
			return name
		}
	}
	if _, ok := services["agent"]; ok {
		return "agent"
	}
	for name := range services {
		return name
	}
	return ""
}

func csvOf(items []string) string {
	out := []string{}
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func turnsByStageToLabel(turns map[string]string) string {
	if len(turns) == 0 {
		return ""
	}
	keys := make([]string, 0, len(turns))
	for k := range turns {
		if strings.TrimSpace(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(turns[k])
		if v == "" {
			continue
		}
		parts = append(parts, k+":"+v)
	}
	return strings.Join(parts, ",")
}
