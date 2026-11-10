package credentials

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The gate runs the same Authorize as tool calls so the two cannot disagree. Only expired/invalid
// block;
// disabling a credential is the operator's way to run without it, so it must never block.

type Fault struct {
	CredentialID string   `json:"credential_id"`
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	State        State    `json:"state"`
	Reason       Reason   `json:"reason"`
	Detail       string   `json:"detail"`
	Tools        []string `json:"tools"`
}

func (f Fault) String() string {
	return fmt.Sprintf("%s (%s) — %s. Affects: %s",
		f.Name, f.Provider, f.Detail, strings.Join(f.Tools, ", "))
}

type DispatchRequest struct {
	// Must be built exactly as the runtime builds it, or the gate and enforcement disagree.
	Reach Reach
	Stage string
	// Same format as thulu.mcp.default.<stage>; nil means the stage is not narrowed. Honoring
	// suffixes keeps the gate from blocking on tools the agent never gets.
	MCPEntries []string
}

func (a *Authorizer) CheckDispatch(ctx context.Context, req DispatchRequest) ([]Fault, error) {
	if req.Reach.WorkspaceID == "" {
		return nil, nil
	}
	avail, err := a.ListAvailable(ctx, ListRequest{
		Reach: req.Reach,
		Stage: req.Stage,
	})
	if err != nil {
		return nil, err
	}
	inStage := a.stageFilter(req.MCPEntries)

	byCredential := map[string]*Fault{}
	for i := range avail {
		t := &avail[i]
		if !blocksDispatch(t.Decision.Reason) || !inStage(t.Server, t.Tool) {
			continue
		}
		f, ok := byCredential[t.Decision.CredentialID]
		if !ok {
			f = &Fault{
				CredentialID: t.Decision.CredentialID,
				Provider:     t.Decision.Provider,
				Reason:       t.Decision.Reason,
				Detail:       t.Decision.Detail,
			}
			byCredential[t.Decision.CredentialID] = f
		}
		f.Tools = append(f.Tools, t.Server+"/"+t.Tool)
	}
	if len(byCredential) == 0 {
		return nil, nil
	}

	reachable, err := a.store.Reachable(ctx, req.Reach)
	if err != nil {
		return nil, err
	}
	for _, c := range reachable {
		if f, ok := byCredential[c.ID]; ok {
			f.Name, f.State = c.Name, c.State
		}
	}

	out := make([]Fault, 0, len(byCredential))
	for _, f := range byCredential {
		sort.Strings(f.Tools)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func blocksDispatch(r Reason) bool {
	return r == ReasonExpired || r == ReasonInvalid
}

// A suffix is a tool list only if every segment is a cataloged tool of that server; otherwise it is
// a scope (e.g. `write`) and the whole server stays in play.
// When in doubt, include: dropping a tool would bring back the mid-run failure.
func (a *Authorizer) stageFilter(entries []string) func(server, tool string) bool {
	if entries == nil {
		return func(string, string) bool { return true }
	}
	wholeServer := map[string]bool{}
	perTool := map[string]bool{}
	for _, raw := range entries {
		server, tools := a.parseStageEntry(raw)
		switch {
		case server == "":
			continue
		case len(tools) == 0:
			wholeServer[server] = true
		default:
			for _, name := range tools {
				perTool[PolicyKey(server, name)] = true
			}
		}
	}
	return func(server, tool string) bool {
		return wholeServer[server] || perTool[PolicyKey(server, tool)]
	}
}

func (a *Authorizer) parseStageEntry(raw string) (server string, tools []string) {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return "", nil
	}
	server, suffix, hasSuffix := strings.Cut(entry, "@")
	server = strings.TrimSpace(server)
	if server == "" || !hasSuffix {
		return server, nil
	}
	for _, name := range strings.Split(suffix, "+") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := a.catalog.Lookup(server, name); !ok {
			return server, nil
		}
		tools = append(tools, name)
	}
	return server, tools
}
