package workshop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Any change to a catalog item's content must raise its revision (agent.json revision, skill.yaml
// version), or installed workspaces never see the update.
type Catalog struct {
	set *Set
}

type agentManifest struct {
	Revision     int               `json:"revision"`
	Tags         []string          `json:"tags"`
	Descriptions map[string]string `json:"descriptions"`
	Definition   json.RawMessage   `json:"definition"`
}

func LoadCatalog(fsys fs.FS) (*Catalog, error) {
	agents, err := loadEach(fsys, "agents", loadAgent)
	if err != nil {
		return nil, err
	}
	skills, err := loadEach(fsys, "skills", loadSkill)
	if err != nil {
		return nil, err
	}
	set := &Set{Agents: agents, Skills: skills}
	if err := set.checkUnique(); err != nil {
		return nil, err
	}
	return &Catalog{set: set}, nil
}

func loadEach[T any](fsys fs.FS, dir string, load func(fs.FS, string) (T, error)) ([]T, error) {
	names, err := subdirs(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(names))
	for _, name := range names {
		item, loadErr := load(fsys, name)
		if loadErr != nil {
			return nil, loadErr
		}
		out = append(out, item)
	}
	return out, nil
}

func loadSkill(fsys fs.FS, name string) (SkillItem, error) {
	files, err := readTree(fsys, path.Join("skills", name))
	if err != nil {
		return SkillItem{}, err
	}
	return NewSkillItem(name, 0, files)
}

func loadAgent(fsys fs.FS, name string) (AgentItem, error) {
	broken := apperr.Invalid("workshop.errors.catalog_item", "name", name)
	files, err := readTree(fsys, path.Join("agents", name))
	if err != nil {
		return AgentItem{}, err
	}
	rawManifest, ok := files["agent.json"]
	if !ok {
		return AgentItem{}, broken
	}
	delete(files, "agent.json")
	var m agentManifest
	if json.Unmarshal(rawManifest, &m) != nil {
		return AgentItem{}, broken
	}
	var def types.AgentType
	if json.Unmarshal(m.Definition, &def) != nil || def.Name != name {
		return AgentItem{}, broken
	}
	item, err := newAgentItem(def, m.Revision, m.Tags, files)
	item.Descriptions = m.Descriptions
	return item, err
}

func subdirs(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, apperr.Wrap(err, "workshop.errors.read_catalog", "dir", dir)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func readTree(fsys fs.FS, root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return apperr.Wrap(err, "workshop.errors.read_catalog", "dir", p)
		}
		rel := strings.TrimPrefix(p, root+"/")
		if err := agentstore.ValidatePath(rel); err != nil {
			return err
		}
		files[rel] = body
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, "workshop.errors.read_catalog", "dir", root)
	}
	return files, nil
}

func (c *Catalog) Agents() []AgentItem { return c.set.Agents }

func (c *Catalog) Skills() []SkillItem { return c.set.Skills }

func (c *Catalog) Agent(name string) (AgentItem, bool) { return c.set.Agent(name) }

func (c *Catalog) Skill(name string) (SkillItem, bool) { return c.set.Skill(name) }

type Ref struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

const (
	KindAgent = "agent"
	KindSkill = "skill"
)

func (c *Catalog) Select(refs []Ref) (*Set, error) {
	out := &Set{}
	pending := []string{}
	for _, r := range refs {
		switch r.Kind {
		case KindAgent:
			a, ok := c.set.Agent(r.Name)
			if !ok {
				return nil, NoSuchItem(r.Kind, r.Name)
			}
			if _, dup := out.Agent(a.Def.Name); !dup {
				out.Agents = append(out.Agents, a)
			}
			pending = append(pending, SkillRefs(a.Def)...)
		case KindSkill:
			if _, ok := c.set.Skill(r.Name); !ok {
				return nil, NoSuchItem(r.Kind, r.Name)
			}
			pending = append(pending, r.Name)
		default:
			return nil, apperr.Invalid("workshop.errors.bad_kind", "kind", r.Kind)
		}
	}
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if _, have := out.Skill(name); have {
			continue
		}
		sk, ok := c.set.Skill(name)
		if !ok {
			continue
		}
		out.Skills = append(out.Skills, sk)
		pending = append(pending, sk.Meta.Requires...)
	}
	return out, nil
}

func NoSuchItem(kind, name string) error {
	return apperr.NotFound(kindKey(kind, "workshop.errors.agent_no_such_item", "workshop.errors.skill_no_such_item"), "name", name)
}

func (c *Catalog) UsedBy(skill string) []string {
	var out []string
	for i := range c.set.Agents {
		a := &c.set.Agents[i]
		sel, err := c.Select([]Ref{{Kind: KindAgent, Name: a.Def.Name}})
		if err != nil {
			continue
		}
		if _, ok := sel.Skill(skill); ok {
			out = append(out, a.Def.Name)
		}
	}
	return out
}
