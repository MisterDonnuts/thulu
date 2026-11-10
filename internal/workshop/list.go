package workshop

import (
	"context"
)

const (
	StatusAvailable = "available"
	StatusInstalled = "installed"
	StatusUpdate    = "update"
	StatusChanged   = "changed"
	StatusReserved  = "reserved"
)

type Entry struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags"`
	Revision    int      `json:"revision"`
	Status      string   `json:"status"`
	Installed   int      `json:"installed_revision,omitempty"`
	Edited      bool     `json:"edited,omitempty"`
	Disabled    bool     `json:"disabled,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Requires    []string `json:"requires,omitempty"`
	UsedBy      []string `json:"used_by,omitempty"`
	MCP         []string `json:"mcp,omitempty"`
	Stages      []string `json:"stages,omitempty"`
	Image       string   `json:"image,omitempty"`
	Engine      string   `json:"engine,omitempty"`
	Model       string   `json:"model,omitempty"`
}

type Detail struct {
	Entry
	Files []File `json:"files"`
}

func List(ctx context.Context, st State, c *Catalog, locale string) ([]Entry, error) {
	out := []Entry{}
	agents := c.Agents()
	for i := range agents {
		a := &agents[i]
		e, err := agentEntry(ctx, st, a, locale)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	skills := c.Skills()
	for i := range skills {
		sk := &skills[i]
		e, err := skillEntry(ctx, st, c, sk, locale)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func Describe(ctx context.Context, st State, c *Catalog, kind, name, locale string) (Detail, bool, error) {
	switch kind {
	case KindAgent:
		a, ok := c.Agent(name)
		if !ok {
			return Detail{}, false, nil
		}
		e, err := agentEntry(ctx, st, &a, locale)
		return Detail{Entry: e, Files: encodeFiles(a.Files)}, true, err
	case KindSkill:
		sk, ok := c.Skill(name)
		if !ok {
			return Detail{}, false, nil
		}
		e, err := skillEntry(ctx, st, c, &sk, locale)
		return Detail{Entry: e, Files: encodeFiles(sk.Files)}, true, err
	}
	return Detail{}, false, nil
}

func agentEntry(ctx context.Context, st State, a *AgentItem, locale string) (Entry, error) {
	it, err := classifyAgent(ctx, st, a, SourceWorkshop)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Kind: KindAgent, Name: a.Def.Name, Description: localized(a.Def.Description, a.Descriptions, locale),
		Tags: nonNil(a.Tags), Revision: a.Revision,
		Status: statusOf(it.Action), Installed: it.Installed, Edited: it.Edited, Disabled: it.Disabled,
		Skills: SkillRefs(a.Def), MCP: MCPRefs(a.Def), Stages: StageRefs(a.Def),
		Image: a.Def.Image, Engine: a.Def.Engine, Model: a.Def.Model,
	}, nil
}

func skillEntry(ctx context.Context, st State, c *Catalog, sk *SkillItem, locale string) (Entry, error) {
	it, err := classifySkill(ctx, st, sk, SourceWorkshop, keptSkill{})
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Kind: KindSkill, Name: sk.Name, Description: localized(sk.Meta.Description, sk.Meta.Descriptions, locale),
		Tags: nonNil(sk.Meta.Tags), Revision: sk.Revision,
		Status: statusOf(it.Action), Installed: it.Installed, Edited: it.Edited,
		Requires: sk.Meta.Requires, UsedBy: c.UsedBy(sk.Name), MCP: nonEmpty(sk.Meta.MCP),
	}, nil
}

func localized(source string, byLocale map[string]string, locale string) string {
	if d := byLocale[locale]; d != "" {
		return d
	}
	return source
}

func statusOf(a Action) string {
	switch a {
	case ActionInstall:
		return StatusAvailable
	case ActionSame:
		return StatusInstalled
	case ActionUpdate:
		return StatusUpdate
	case ActionReserved, ActionRejected:
		return StatusReserved
	case ActionConflict:
		return StatusChanged
	}
	return StatusChanged
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
