package workshop

import (
	"context"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type StoreState struct {
	Store  *agentstore.Store
	Tenant string
}

var _ State = StoreState{}

func (s StoreState) Agent(ctx context.Context, name string) (agentstore.Agent, bool, error) {
	a, err := s.Store.GetAgent(ctx, s.Tenant, name)
	if errors.Is(err, agentstore.ErrNotFound) {
		return agentstore.Agent{}, false, nil
	}
	if err != nil {
		return agentstore.Agent{}, false, err
	}
	return a, true, nil
}

func (s StoreState) Files(ctx context.Context, kind, name string) (map[string][]byte, error) {
	files, err := s.Store.ReadOwner(ctx, s.Tenant, kind, name)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(files))
	for i := range files {
		out[files[i].Path] = files[i].Body
	}
	return out, nil
}

func (s StoreState) Origin(ctx context.Context, kind, name string) (agentstore.Origin, bool, error) {
	o, err := s.Store.GetOrigin(ctx, s.Tenant, kind, name)
	if errors.Is(err, agentstore.ErrNotFound) {
		return agentstore.Origin{}, false, nil
	}
	if err != nil {
		return agentstore.Origin{}, false, err
	}
	return o, true, nil
}

func (s StoreState) ChangedSince(ctx context.Context, kind, name string, since time.Time) (bool, error) {
	return s.Store.ChangedSince(ctx, s.Tenant, kind, name, since)
}

func (s StoreState) IsSystemSkill(ctx context.Context, name string) (bool, error) {
	_, err := s.Store.GetSystemSkill(ctx, name)
	if errors.Is(err, agentstore.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func ExportAgent(ctx context.Context, st State, name string) (*Set, error) {
	return Export(ctx, st, []Ref{{Kind: KindAgent, Name: name}})
}

func ExportSkill(ctx context.Context, st State, name string) (*Set, error) {
	return Export(ctx, st, []Ref{{Kind: KindSkill, Name: name}})
}

func Export(ctx context.Context, st State, refs []Ref) (*Set, error) {
	set := &Set{}
	var skills []string
	for _, r := range refs {
		switch r.Kind {
		case KindAgent:
			uses, err := exportAgent(ctx, st, set, r.Name)
			if err != nil {
				return nil, err
			}
			skills = append(skills, uses...)
		case KindSkill:
			if err := skillExists(ctx, st, r.Name); err != nil {
				return nil, err
			}
			skills = append(skills, r.Name)
		default:
			return nil, apperr.Invalid("workshop.errors.bad_kind", "kind", r.Kind)
		}
	}
	if err := addSkills(ctx, st, set, skills); err != nil {
		return nil, err
	}
	if len(set.Agents) > MaxAgents || len(set.Skills) > MaxSkills {
		return nil, apperr.TooLarge("workshop.errors.too_many_items", "agents", MaxAgents, "skills", MaxSkills)
	}
	if files, bytes := set.size(); files > MaxFiles || bytes > MaxBytes {
		return nil, apperr.TooLarge("workshop.errors.bundle_too_large")
	}
	return set, nil
}

func exportAgent(ctx context.Context, st State, set *Set, name string) ([]string, error) {
	if _, dup := set.Agent(name); dup {
		return nil, nil
	}
	ag, ok, err := st.Agent(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	files, err := st.Files(ctx, agentstore.KindAgent, name)
	if err != nil {
		return nil, err
	}
	item, err := newAgentItem(ag.AgentType, 0, nil, files)
	if err != nil {
		return nil, err
	}
	set.Agents = append(set.Agents, item)
	return SkillRefs(ag.AgentType), nil
}

func skillExists(ctx context.Context, st State, name string) error {
	have, err := st.Files(ctx, agentstore.KindSkill, name)
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return agentstore.ErrNotFound
	}
	return nil
}

// Must count like budget.decode so an export never exceeds what Decode accepts.
func (s *Set) size() (files, bytes int) {
	for i := range s.Agents {
		for _, body := range s.Agents[i].Files {
			files++
			bytes += len(body)
		}
	}
	for i := range s.Skills {
		for _, body := range s.Skills[i].Files {
			files++
			bytes += len(body)
		}
	}
	return files, bytes
}

func addSkills(ctx context.Context, st State, set *Set, names []string) error {
	pending := append([]string(nil), names...)
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if _, have := set.Skill(name); have || name == baseSkill {
			continue
		}
		if sys, err := st.IsSystemSkill(ctx, name); err != nil || sys {
			if err != nil {
				return err
			}
			continue
		}
		files, err := st.Files(ctx, agentstore.KindSkill, name)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			continue
		}
		item, err := NewSkillItem(name, 0, files)
		if err != nil {
			return err
		}
		set.Skills = append(set.Skills, item)
		pending = append(pending, item.Meta.Requires...)
	}
	return nil
}
