package types

type AgentSource interface {
	// The returned slice is the caller's to mutate; implementations must not hand out cached state
	// that leaks into the next caller.
	All() ([]AgentType, error)

	Invalidate()
}

var _ AgentSource = (*Catalog)(nil)

type WorkspaceAgents interface {
	For(workspaceID string) AgentSource
}

func FindAgent(src AgentSource, name string) *AgentType {
	if src == nil {
		return nil
	}
	all, err := src.All()
	if err != nil {
		return nil
	}
	for i := range all {
		if all[i].Name == name || all[i].Type == name {
			cp := all[i]
			return &cp
		}
	}
	return nil
}

func AllWorkspaces(src AgentSource) WorkspaceAgents { return sameSource{src} }

type sameSource struct{ src AgentSource }

func (s sameSource) For(string) AgentSource { return s.src }
