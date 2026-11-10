package api

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

type SkillDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Requires    []string `json:"requires,omitempty"`
	HasScripts  bool     `json:"has_scripts,omitempty"`
	System      bool     `json:"system,omitempty"`
	MCP         string   `json:"mcp,omitempty"`
	Version     int      `json:"version,omitempty"`
	Enforcement string   `json:"enforcement,omitempty"`
	Origin      string   `json:"origin,omitempty"`
}

type skillYaml struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Type        string   `yaml:"type"`
	Requires    []string `yaml:"requires"`
	Enforcement string   `yaml:"enforcement"`
}

// Reserved for system skills so a release can add one without colliding with a workspace's skill.
const systemSkillPrefix = "octopus-"

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store != nil {
		defs, err := s.storedSkills(r, store, tenant)
		if err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		writeJSON(w, http.StatusOK, defs)
		return
	}
	writeDiskSkills(w, r, filepath.Join(s.agentsDir, "skills"))
}

// _base is loaded for every agent, so it is not a pickable skill.
func writeDiskSkills(w http.ResponseWriter, r *http.Request, skillsDir string) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, []SkillDef{})
			return
		}
		writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.skills.errors.read_dir"))
		return
	}
	out := make([]SkillDef, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "_base" || strings.HasPrefix(name, ".") {
			continue
		}
		if def, ok := skillDefFromEntry(skillsDir, name); ok {
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// A workspace skill sharing a system skill's name is hidden: runs read the system copy
// (catalogContent).
func (s *Server) storedSkills(r *http.Request, store *agentstore.Store, tenant string) ([]SkillDef, error) {
	ctx := r.Context()
	system, err := store.ListSystemSkills(ctx)
	if err != nil {
		return nil, err
	}
	taken := make(map[string]bool, len(system))
	for i := range system {
		taken[system[i].Name] = true
	}
	origins, err := store.Origins(ctx, tenant, agentstore.KindSkill)
	if err != nil {
		return nil, err
	}
	owners, err := store.ListOwners(ctx, tenant, agentstore.KindSkill)
	if err != nil {
		return nil, err
	}
	out := make([]SkillDef, 0, len(owners)+len(system))
	for _, name := range owners {
		if name == "_base" || strings.HasPrefix(name, ".") || taken[name] {
			continue
		}
		def, ok, err := storedSkillDef(r, store, tenant, name)
		if err != nil {
			return nil, err
		}
		if ok {
			def.Origin = origins[name].Origin
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for i := range system {
		def, ok, err := storedSkillDef(r, store, agentstore.SystemTenant, system[i].Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		def.System, def.MCP, def.Version = true, system[i].MCP, system[i].Version
		out = append(out, def)
	}
	return out, nil
}

// Must classify the same way as skillDefFromEntry.
func storedSkillDef(r *http.Request, store *agentstore.Store, tenant, name string) (SkillDef, bool, error) {
	files, err := store.ListOwner(r.Context(), tenant, agentstore.KindSkill, name)
	if err != nil {
		return SkillDef{}, false, err
	}
	def := SkillDef{Name: name}
	var hasPrompt bool
	for i := range files {
		switch {
		case files[i].Path == agentstore.SkillPromptFile:
			hasPrompt = true
		case files[i].Path == agentstore.SkillMetaFile:
			f, err := store.ReadFile(r.Context(), tenant, agentstore.KindSkill, name, agentstore.SkillMetaFile)
			if err != nil {
				continue
			}
			var y skillYaml
			if err := yaml.Unmarshal(f.Body, &y); err == nil {
				def.Description = y.Description
				def.Type = y.Type
				def.Requires = y.Requires
				def.Enforcement = y.Enforcement
			}
		case strings.HasPrefix(files[i].Path, "scripts/"):
			def.HasScripts = true
		}
	}
	if !hasPrompt {
		return SkillDef{}, false, nil
	}
	if def.Type == "" {
		if def.HasScripts {
			def.Type = "tool"
		} else {
			def.Type = "knowledge"
		}
	}
	return def, true, nil
}

func skillDefFromEntry(skillsDir, name string) (SkillDef, bool) {
	dir := filepath.Join(skillsDir, name)
	if _, err := os.Stat(filepath.Join(dir, "prompt.md")); err != nil {
		return SkillDef{}, false
	}
	def := SkillDef{Name: name}
	if yamlBytes, err := safepath.ReadFile(dir, "skill.yaml"); err == nil {
		var y skillYaml
		if err := yaml.Unmarshal(yamlBytes, &y); err == nil {
			def.Description = y.Description
			def.Type = y.Type
			def.Requires = y.Requires
			def.Enforcement = y.Enforcement
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "scripts")); err == nil && info.IsDir() {
		def.HasScripts = true
		if def.Type == "" {
			def.Type = "tool"
		}
	} else if def.Type == "" {
		def.Type = "knowledge"
	}
	return def, true
}

func (s *Server) skillStore(w http.ResponseWriter, r *http.Request) (*agentstore.Store, string, bool) {
	return s.revisionStore(w, r)
}

func (s *Server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if !s.workspaceSkillName(w, r, store, name) {
		return
	}
	ctx := r.Context()
	if existing, err := store.ListOwner(ctx, tenant, agentstore.KindSkill, name); err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	} else if len(existing) > 0 {
		writeErrKey(w, r, http.StatusConflict, "api.skills.errors.skill_exists", "name", name)
		return
	}
	meta, err := yaml.Marshal(skillYaml{Name: name, Description: strings.TrimSpace(body.Description), Type: "knowledge"})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	actor := actorID(r)
	for _, f := range []struct {
		path string
		body []byte
	}{
		{agentstore.SkillMetaFile, meta},
		{agentstore.SkillPromptFile, []byte("# " + name + "\n")},
	} {
		written, perr := store.PutFile(ctx, tenant, agentstore.KindSkill, name, f.path, f.body, actor)
		if perr != nil {
			writeError(w, r, statusForStoreErr(perr), perr)
			return
		}
		s.auditContentChange(r, tenant, agentstore.KindSkill, name, f.path, agentstore.ActionUpdated, written.SHA)
	}
	def, _, err := storedSkillDef(r, store, tenant, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, def)
}

func (s *Server) workspaceSkillName(w http.ResponseWriter, r *http.Request, store *agentstore.Store, name string) bool {
	if err := agentstore.ValidateName(name); err != nil || !safeComponent(name) {
		writeErrKey(w, r, http.StatusBadRequest, "api.skills.errors.invalid_name")
		return false
	}
	_, err := store.GetSystemSkill(r.Context(), name)
	if err == nil || strings.HasPrefix(name, systemSkillPrefix) {
		writeError(w, r, http.StatusConflict, agentstore.ErrSystemSkillName)
		return false
	}
	if !errors.Is(err, agentstore.ErrNotFound) {
		writeError(w, r, statusForStoreErr(err), err)
		return false
	}
	return true
}

const (
	skillEnforcementOnDemand = "on_demand"
	skillEnforcementAlways   = "always"
)

// Engines truncate the description engines read before opening a skill (Claude Code at 1024
// characters).
const skillDescriptionMax = 1024

func (s *Server) handlePatchSkill(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !s.workspaceSkillName(w, r, store, name) {
		return
	}
	var body struct {
		Description *string `json:"description"`
		Enforcement *string `json:"enforcement"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	set := map[string]string{}
	if body.Description != nil {
		d := strings.Join(strings.Fields(*body.Description), " ")
		if len([]rune(d)) > skillDescriptionMax {
			writeErrKey(w, r, http.StatusBadRequest, "api.skills.errors.description_too_long", "max", skillDescriptionMax)
			return
		}
		set["description"] = d
	}
	if body.Enforcement != nil {
		switch e := strings.TrimSpace(*body.Enforcement); e {
		case "", skillEnforcementOnDemand, skillEnforcementAlways:
			set["enforcement"] = e
		default:
			writeErrKey(w, r, http.StatusBadRequest, "api.skills.errors.invalid_enforcement")
			return
		}
	}
	ctx := r.Context()
	files, err := store.ListOwner(ctx, tenant, agentstore.KindSkill, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	if len(files) == 0 {
		writeError(w, r, http.StatusNotFound, agentstore.ErrNotFound)
		return
	}
	current, err := readSkillYAML(ctx, store, tenant, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	meta, err := setSkillYAML(current, name, set)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "agentstore.errors.skill_yaml"))
		return
	}
	f, err := store.PutFile(ctx, tenant, agentstore.KindSkill, name, agentstore.SkillMetaFile, meta, actorID(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.auditContentChange(r, tenant, agentstore.KindSkill, name, agentstore.SkillMetaFile, agentstore.ActionUpdated, f.SHA)
	def, _, err := storedSkillDef(r, store, tenant, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

func setSkillYAML(current []byte, name string, set map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
		set = maps.Clone(set)
		if _, ok := set["name"]; !ok {
			set["name"] = name
		}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, apperr.New("api.skills.errors.yaml_not_mapping")
	}
	keys := slices.Sorted(maps.Keys(set))
	if i := slices.Index(keys, "name"); i > 0 {
		keys = append([]string{"name"}, slices.Delete(keys, i, i+1)...)
	}
	for _, key := range keys {
		value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: set[key]}
		found := false
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == key {
				value.HeadComment, value.LineComment = root.Content[i+1].HeadComment, root.Content[i+1].LineComment
				root.Content[i+1] = value
				found = true
				break
			}
		}
		if !found {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
		}
	}
	// yaml.Marshal indents by 4 and would re-indent the user's nested lists; skill.yaml files use
	// 2.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) handleListSkillFiles(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	s.writeOwnerFiles(w, r, store, tenant, r.PathValue("name"))
}

func (s *Server) handleReadSkillFile(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	f, err := store.ReadFile(r.Context(), tenant, agentstore.KindSkill, r.PathValue("name"), storedPath(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeText(w, f.Body)
}

func (s *Server) handleWriteSkillFile(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !s.workspaceSkillName(w, r, store, name) {
		return
	}
	body, ok := readContentBody(w, r)
	if !ok {
		return
	}
	path := storedPath(r)
	if path == agentstore.SkillMetaFile {
		var y skillYaml
		if err := yaml.Unmarshal(body, &y); err != nil {
			writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "agentstore.errors.skill_yaml"))
			return
		}
	}
	f, err := store.PutFile(r.Context(), tenant, agentstore.KindSkill, name, path, body, actorID(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.auditContentChange(r, tenant, agentstore.KindSkill, name, path, agentstore.ActionUpdated, f.SHA)
	writeJSON(w, http.StatusOK, map[string]any{"size": f.Size, "sha256": f.SHA})
}

func (s *Server) handleDeleteSkillFile(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	name, path := r.PathValue("name"), storedPath(r)
	if err := store.DeleteFile(r.Context(), tenant, agentstore.KindSkill, name, path, actorID(r)); err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.auditContentChange(r, tenant, agentstore.KindSkill, name, path, agentstore.ActionDeleted, "")
	writeStatus(w, "deleted")
}

// Without ?revoke=1 agents keep the grant, and it ships again if a skill with that name is
// recreated.
func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	ctx, name, actor := r.Context(), r.PathValue("name"), actorID(r)
	files, err := store.ListOwner(ctx, tenant, agentstore.KindSkill, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	if len(files) == 0 {
		writeError(w, r, http.StatusNotFound, agentstore.ErrNotFound)
		return
	}
	var revoked []agentstore.Agent
	if r.URL.Query().Get("revoke") == "1" {
		if revoked, err = agentsRevoking(ctx, store, tenant, name); err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
	}
	err = store.InTx(ctx, func(tx *agentstore.Tx) error {
		return deleteSkillTx(tx, tenant, name, actor, files, revoked)
	})
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	for i := range files {
		s.auditContentChange(r, tenant, agentstore.KindSkill, name, files[i].Path, agentstore.ActionDeleted, "")
	}
	if len(revoked) > 0 {
		s.invalidateCatalog(tenant)
		for i := range revoked {
			s.auditAgentDef(r, tenant, revoked[i].Name, "api.agents.audit.settings_updated")
		}
	}
	if err := store.ForgetOrigin(ctx, tenant, agentstore.KindSkill, name); err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "revoked": len(revoked)})
}

func readSkillYAML(ctx context.Context, store *agentstore.Store, tenant, name string) ([]byte, error) {
	f, err := store.ReadFile(ctx, tenant, agentstore.KindSkill, name, agentstore.SkillMetaFile)
	if errors.Is(err, agentstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return f.Body, nil
}

func agentsRevoking(ctx context.Context, store *agentstore.Store, tenant, name string) ([]agentstore.Agent, error) {
	agents, err := store.ListAgents(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var revoked []agentstore.Agent
	for i := range agents {
		if revokeSkill(&agents[i].AgentType, name) {
			revoked = append(revoked, agents[i])
		}
	}
	return revoked, nil
}

func deleteSkillTx(tx *agentstore.Tx, tenant, name, actor string, files []agentstore.File, revoked []agentstore.Agent) error {
	for i := range files {
		if err := tx.DeleteFile(tenant, agentstore.KindSkill, name, files[i].Path, actor); err != nil {
			return err
		}
	}
	for i := range revoked {
		if _, err := tx.PutAgent(tenant, revoked[i], actor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleListDeletedSkills(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	owners, err := store.ListDeletedOwners(r.Context(), tenant, agentstore.KindSkill)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	out := make([]agentstore.DeletedOwner, 0, len(owners))
	for _, o := range owners {
		if o.Name == "_base" || strings.HasPrefix(o.Name, ".") || strings.HasPrefix(o.Name, systemSkillPrefix) {
			continue
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRestoreSkill(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !s.workspaceSkillName(w, r, store, name) {
		return
	}
	ctx := r.Context()
	if existing, err := store.ListOwner(ctx, tenant, agentstore.KindSkill, name); err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	} else if len(existing) > 0 {
		writeErrKey(w, r, http.StatusConflict, "api.skills.errors.skill_exists", "name", name)
		return
	}
	restored, err := store.RestoreDeleted(ctx, tenant, agentstore.KindSkill, name, actorID(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	for i := range restored {
		s.auditContentChange(r, tenant, agentstore.KindSkill, name, restored[i].Path, agentstore.ActionRestored, restored[i].SHA)
	}
	def, _, err := storedSkillDef(r, store, tenant, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

func (s *Server) handleSkillRevisions(w http.ResponseWriter, r *http.Request) {
	s.listRevisions(w, r, agentstore.KindSkill)
}

func (s *Server) handleSkillRevisionBody(w http.ResponseWriter, r *http.Request) {
	s.revisionBody(w, r, agentstore.KindSkill)
}

func (s *Server) handleRestoreSkillRevision(w http.ResponseWriter, r *http.Request) {
	s.restoreRevision(w, r, agentstore.KindSkill)
}

func (s *Server) writeOwnerFiles(w http.ResponseWriter, r *http.Request, store *agentstore.Store, tenant, name string) {
	files, err := store.ListOwner(r.Context(), tenant, agentstore.KindSkill, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	out := make([]agentFileEntry, 0, len(files))
	for i := range files {
		out = append(out, agentFileEntry{Path: files[i].Path, Kind: "file", Size: int64(files[i].Size)})
	}
	if r.URL.Query().Get("deleted") == "1" {
		deleted, err := store.ListDeleted(r.Context(), tenant, agentstore.KindSkill, name)
		if err != nil {
			writeError(w, r, statusForStoreErr(err), err)
			return
		}
		for i := range deleted {
			out = append(out, agentFileEntry{Path: deleted[i].Path, Kind: "file", Size: int64(deleted[i].Size), Deleted: true})
		}
	}
	if len(out) == 0 {
		writeError(w, r, http.StatusNotFound, agentstore.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListSystemSkills(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	skills, err := store.ListSystemSkills(r.Context())
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(skills))
}

func (s *Server) handleListSystemSkillFiles(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	s.writeOwnerFiles(w, r, store, agentstore.SystemTenant, r.PathValue("name"))
}

func (s *Server) handleReadSystemSkillFile(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.skillStore(w, r)
	if !ok {
		return
	}
	f, err := store.ReadFile(r.Context(), agentstore.SystemTenant, agentstore.KindSkill, r.PathValue("name"), storedPath(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeText(w, f.Body)
}

// Operator-only: a system skill change reaches every workspace's next run.
func (s *Server) systemSkillWriter(w http.ResponseWriter, r *http.Request) (*agentstore.Store, bool) {
	if !s.requireOperator(w, r, "api.scope.operator_surfaces.system_skills", "api.scope.operator_verbs.change") {
		return nil, false
	}
	store, _, ok := s.skillStore(w, r)
	return store, ok
}

// Hotfix path: keeps the stored version, so the next release that bumps the version replaces it.
func (s *Server) handlePutSystemSkill(w http.ResponseWriter, r *http.Request) {
	store, ok := s.systemSkillWriter(w, r)
	if !ok {
		return
	}
	var body struct {
		Files map[string]string `json:"files"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	files := make(map[string][]byte, len(body.Files))
	for p, b := range body.Files {
		files[strings.TrimPrefix(p, "/")] = []byte(b)
	}
	name := r.PathValue("name")
	sk, err := store.PutSystemSkill(r.Context(), name, files, actorID(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.systemSkillsChanged()
	s.auditContentChange(r, agentstore.SystemTenant, agentstore.KindSkill, name, "*", agentstore.ActionUpdated, "")
	writeJSON(w, http.StatusOK, sk)
}

func (s *Server) handlePutSystemSkillFile(w http.ResponseWriter, r *http.Request) {
	store, ok := s.systemSkillWriter(w, r)
	if !ok {
		return
	}
	body, ok := readContentBody(w, r)
	if !ok {
		return
	}
	name, path := r.PathValue("name"), storedPath(r)
	f, err := store.PutSystemSkillFile(r.Context(), name, path, body, actorID(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.systemSkillsChanged()
	s.auditContentChange(r, agentstore.SystemTenant, agentstore.KindSkill, name, path, agentstore.ActionUpdated, f.SHA)
	writeJSON(w, http.StatusOK, map[string]any{"size": f.Size, "sha256": f.SHA})
}

// A system skill the binary carries comes back at the next boot.
func (s *Server) handleDeleteSystemSkill(w http.ResponseWriter, r *http.Request) {
	store, ok := s.systemSkillWriter(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := store.DeleteSystemSkill(r.Context(), name, actorID(r)); err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.systemSkillsChanged()
	s.auditContentChange(r, agentstore.SystemTenant, agentstore.KindSkill, name, "*", agentstore.ActionDeleted, "")
	writeStatus(w, "deleted")
}
