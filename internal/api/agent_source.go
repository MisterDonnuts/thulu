package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// An empty ws (unstamped task, dev mode) resolves through catalogTenant the same way an unrestricted
// caller's writes do, so the agents a dev-mode dashboard edits are the ones its tasks run.
func (s *Server) agents(ws string) types.AgentSource {
	if s.agentSource != nil {
		return s.agentSource
	}
	if s.agentStore == nil {
		return s.agentCatalog
	}
	tenant := s.catalogTenant(context.Background(), ws)
	if wc := s.cachedCatalog(tenant); wc != nil {
		return wc
	}
	fresh := &workspaceCatalog{
		db:     &dbAgentSource{store: s.agentStore, tenant: tenant},
		system: s.systemSkills,
	}
	if src, loaded := s.catalogs.LoadOrStore(tenant, fresh); loaded {
		if wc, ok := src.(*workspaceCatalog); ok {
			return wc
		}
	}
	return fresh
}

func (s *Server) cachedCatalog(tenant string) *workspaceCatalog {
	src, ok := s.catalogs.Load(tenant)
	if !ok {
		return nil
	}
	if wc, ok := src.(*workspaceCatalog); ok {
		return wc
	}
	return nil
}

func (s *Server) catalogTenant(ctx context.Context, ws string) string {
	if ws != "" {
		return ws
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.unrestrictedWorkspace(ctx)
}

// Resolves at call time for components wired before SetAgentStore. A source captured by value stays
// on the directory catalog after definitions move to the database.
type serverAgents struct{ s *Server }

func (a serverAgents) For(ws string) types.AgentSource { return a.s.agents(ws) }

func (s *Server) SetAgentStore(store *agentstore.Store) {
	if store == nil {
		return
	}
	s.agentStore = store
	s.systemSkills = &systemSkillCache{store: store}
	s.catalogs.Clear()
}

func (s *Server) AgentStore() *agentstore.Store { return s.agentStore }

func (s *Server) workspaceStore(w http.ResponseWriter, r *http.Request) (*agentstore.Store, string, bool) {
	if s.agentStore == nil {
		return nil, "", true
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return nil, "", false
	}
	if err := s.agentStore.EnsureSeeded(r.Context(), ws); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return nil, "", false
	}
	return s.agentStore, ws, true
}

func (s *Server) invalidateCatalog(tenant string) {
	if s.agentStore == nil {
		s.agentCatalog.Invalidate()
		return
	}
	if wc := s.cachedCatalog(tenant); wc != nil {
		wc.Invalidate()
	}
}

// Seeds on first read, so a workspace that never opened the dashboard still gets its starter pack
// when a task first asks for an agent.
type workspaceCatalog struct {
	db     *dbAgentSource
	system *systemSkillCache

	seedMu sync.Mutex
	seeded bool
}

func (c *workspaceCatalog) All() ([]types.AgentType, error) {
	if err := c.ensureSeeded(); err != nil {
		return nil, err
	}
	all, err := c.db.All()
	if err != nil {
		return nil, err
	}
	refs := c.system.refs()
	for i := range all {
		all[i].SystemSkills = refs
	}
	return all, nil
}

func (c *workspaceCatalog) Invalidate() {
	c.db.Invalidate()
	c.system.invalidate()
}

func (c *workspaceCatalog) ensureSeeded() error {
	c.seedMu.Lock()
	defer c.seedMu.Unlock()
	if c.seeded {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.db.store.EnsureSeeded(ctx, c.db.tenant); err != nil {
		return err
	}
	c.seeded = true
	return nil
}

type systemSkillCache struct {
	store *agentstore.Store

	mu      sync.Mutex
	cached  []types.SystemSkillRef
	checked time.Time
}

func (c *systemSkillCache) refs() []types.SystemSkillRef {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checked.IsZero() && time.Since(c.checked) < agentCacheTTL {
		return c.cached
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := c.store.ListSystemSkills(ctx)
	if err != nil {
		// Serve the stale list on error: a run without its system skills is worse than one with a
		// list a few seconds old.
		log.Printf("[agents] %s", i18n.Log("api.agents.log.system_skills_unreadable", "err", err))
		return c.cached
	}
	refs := make([]types.SystemSkillRef, 0, len(list))
	for i := range list {
		refs = append(refs, types.SystemSkillRef{Name: list[i].Name, MCP: list[i].MCP, When: list[i].When})
	}
	c.cached, c.checked = refs, time.Now()
	return refs
}

func (c *systemSkillCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.checked = time.Time{}
	c.mu.Unlock()
}

func statusForStoreErr(err error) int {
	switch {
	case errors.Is(err, agentstore.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, agentstore.ErrExists):
		return http.StatusConflict
	case errors.Is(err, agentstore.ErrSecretInBody):
		// 422, not 400: the UI uses it to show the error next to the body editor.
		return http.StatusUnprocessableEntity
	case errors.Is(err, agentstore.ErrInvalidMode),
		errors.Is(err, agentstore.ErrInvalidName),
		errors.Is(err, agentstore.ErrInvalidPath),
		errors.Is(err, agentstore.ErrInvalidKind),
		errors.Is(err, agentstore.ErrReservedOwner),
		errors.Is(err, agentstore.ErrBodyTooLarge):
		return http.StatusBadRequest
	}
	if k := apperr.KindOf(err); k != apperr.KindInternal {
		return k.HTTPStatus()
	}
	return http.StatusInternalServerError
}

type newAgentShape struct {
	Description string   `json:"description"`
	ValidStages []string `json:"valid_stages"`
	Skills      []string `json:"skills"`
}

func (n newAgentShape) apply(at *types.AgentType) {
	if d := strings.TrimSpace(n.Description); d != "" {
		at.Description = d
	}
	if n.ValidStages != nil {
		at.ValidStages = at.ValidStages[:0]
		for _, id := range n.ValidStages {
			if types.ValidStageID(id) {
				at.ValidStages = append(at.ValidStages, types.Stage(id))
			}
		}
	}
	if len(n.Skills) == 0 || len(at.ValidStages) == 0 {
		return
	}
	if at.SkillsByStage == nil {
		at.SkillsByStage = map[string][]string{}
	}
	for _, st := range at.ValidStages {
		list := at.SkillsByStage[string(st)]
		for _, sk := range n.Skills {
			if sk != "" && !slices.Contains(list, sk) {
				list = append(list, sk)
			}
		}
		at.SkillsByStage[string(st)] = list
	}
}

func (s *Server) createStoredAgent(ctx context.Context, store *agentstore.Store,
	tenant, name, template, actor string, shape newAgentShape,
) (agentstore.Agent, error) {
	if template != "" {
		return store.CloneAgentWith(ctx, tenant, template, name, actor, shape.apply)
	}
	// No valid stages unless the dialog picked some: an agent dispatched before anyone chose its
	// stages would run with an empty identity.
	at := types.AgentType{
		Name:       name,
		Type:       name,
		SkillsMode: types.SkillsModeAuto,
	}
	shape.apply(&at)
	return store.CreateAgent(ctx, tenant, agentstore.Agent{AgentType: at}, actor)
}

func actorID(r *http.Request) string {
	if id := actorIDFromRequest(r); id != "" {
		return id
	}
	return "api"
}

// Invalidate only reaches the process that served the write; agentCacheTTL bounds how stale another
// replica can be.
type dbAgentSource struct {
	store  *agentstore.Store
	tenant string

	mu      sync.Mutex
	cached  []types.AgentType
	stamp   string
	loaded  bool
	checked time.Time
}

const agentCacheTTL = 5 * time.Second

func (d *dbAgentSource) All() ([]types.AgentType, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if d.loaded && time.Since(d.checked) < agentCacheTTL {
		return cloneTypes(d.cached), nil
	}
	stamp, err := d.store.Fingerprint(ctx, d.tenant)
	if err != nil {
		return nil, err
	}
	d.checked = time.Now()
	if d.loaded && stamp == d.stamp {
		return cloneTypes(d.cached), nil
	}

	stored, err := d.store.ListAgents(ctx, d.tenant)
	if err != nil {
		return nil, err
	}
	out := make([]types.AgentType, 0, len(stored))
	for i := range stored {
		out = append(out, stored[i].AgentType)
	}
	d.cached, d.stamp, d.loaded = out, stamp, true
	return cloneTypes(out), nil
}

func (d *dbAgentSource) Invalidate() {
	d.mu.Lock()
	d.loaded = false
	d.mu.Unlock()
}

// Callers mutate their copy, so every slice, map or pointer field added to AgentType must be cloned
// here too.
func cloneTypes(in []types.AgentType) []types.AgentType {
	out := make([]types.AgentType, 0, len(in))
	for i := range in {
		at := in[i]
		at.ValidStages = append([]types.Stage(nil), in[i].ValidStages...)
		at.SupportedEngines = append([]string(nil), in[i].SupportedEngines...)
		at.Params = append([]types.ParamDef(nil), in[i].Params...)
		at.TurnsByStage = cloneMapString(in[i].TurnsByStage)
		at.SkillsByStage = cloneMapSlice(in[i].SkillsByStage)
		at.MCPByStage = cloneMapSlice(in[i].MCPByStage)
		at.SystemSkillsOff = cloneMapSlice(in[i].SystemSkillsOff)
		at.SystemSkills = append([]types.SystemSkillRef(nil), in[i].SystemSkills...)
		at.ActiveWhen = append([]string(nil), in[i].ActiveWhen...)
		if in[i].Auditor != nil {
			spec := *in[i].Auditor
			spec.Stages = append([]string(nil), in[i].Auditor.Stages...)
			spec.TurnsByStage = cloneMapString(in[i].Auditor.TurnsByStage)
			spec.ModelByStage = cloneMapString(in[i].Auditor.ModelByStage)
			spec.EngineByStage = cloneMapString(in[i].Auditor.EngineByStage)
			spec.SkillsByStage = cloneMapSlice(in[i].Auditor.SkillsByStage)
			at.Auditor = &spec
		}
		out = append(out, at)
	}
	return out
}

func cloneMapString(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneMapSlice(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func (s *Server) systemSkillsChanged() { s.systemSkills.invalidate() }
