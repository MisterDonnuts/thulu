package registry

import (
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Registry struct {
	mu     sync.RWMutex
	agents map[string]*agent.Agent
	store  *Store
}

func New() *Registry {
	return &Registry{agents: make(map[string]*agent.Agent)}
}

func (r *Registry) Upsert(a *agent.Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[a.ID] = a
	r.mirror(func(s *Store) error { return s.Upsert(a) }, "upsert", a.ID)
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
	r.mirror(func(s *Store) error { return s.Remove(id) }, "remove", id)
}

func (r *Registry) Get(idOrName string) (*agent.Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if idOrName == "" {
		return nil, apperr.NotFound("registry.errors.agent_not_found", "id", idOrName)
	}

	if a, ok := r.agents[idOrName]; ok {
		return a, nil
	}

	var match *agent.Agent
	for _, a := range r.agents {
		if a.Name == idOrName || strings.HasPrefix(a.ID, idOrName) {
			if match != nil {
				return nil, apperr.Conflict("registry.errors.ambiguous_identifier", "id", idOrName)
			}
			match = a
		}
	}
	if match == nil {
		return nil, apperr.NotFound("registry.errors.agent_not_found", "id", idOrName)
	}
	return match, nil
}

func (r *Registry) List() []*agent.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*agent.Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	return out
}
