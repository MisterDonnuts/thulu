package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Catalog struct {
	dir string

	mu     sync.Mutex
	cached []AgentType
	stamp  string
	loaded bool
	err    error
	nLoads int
}

func NewCatalog(baseDir string) *Catalog { return &Catalog{dir: baseDir} }

func (c *Catalog) Dir() string { return c.dir }

// Returns a copy: callers mutate what they get, and that must not reach the cache.
func (c *Catalog) All() ([]AgentType, error) {
	if c == nil {
		return nil, apperr.Unavailable("types.errors.catalog_not_configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stamp := c.fingerprint()
	if !c.loaded || stamp != c.stamp {
		c.cached, c.err = Discover(c.dir)
		c.stamp, c.loaded = stamp, true
		c.nLoads++
	}
	if c.err != nil {
		return nil, c.err
	}
	return cloneAgentTypes(c.cached), nil
}

func (c *Catalog) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}

func (c *Catalog) reloads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nLoads
}

func (c *Catalog) fingerprint() string {
	entries, err := os.ReadDir(filepath.Join(c.dir, "agents"))
	if err != nil {
		return "unreadable:" + err.Error()
	}
	var b []byte
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, statErr := os.Stat(filepath.Join(c.dir, "agents", e.Name(), "docker-compose.yml"))
		if statErr != nil {
			b = append(b, e.Name()...)
			b = append(b, '!')
			continue
		}
		b = append(b, fmt.Sprintf("%s:%d:%d;", e.Name(), st.Size(), st.ModTime().UnixNano())...)
	}
	return string(b)
}

// A new slice or map field on AgentType or its Auditor must be copied here, or callers mutate the
// cache.
func cloneAgentTypes(in []AgentType) []AgentType {
	out := make([]AgentType, len(in))
	copy(out, in)
	for i := range out {
		out[i].ValidStages = append([]Stage(nil), in[i].ValidStages...)
		out[i].SupportedEngines = append([]string(nil), in[i].SupportedEngines...)
		out[i].Params = append([]ParamDef(nil), in[i].Params...)
		out[i].TurnsByStage = cloneStringMap(in[i].TurnsByStage)
		out[i].SkillsByStage = cloneStringSliceMap(in[i].SkillsByStage)
		out[i].MCPByStage = cloneStringSliceMap(in[i].MCPByStage)
		out[i].SystemSkillsOff = cloneStringSliceMap(in[i].SystemSkillsOff)
		if in[i].Auditor != nil {
			spec := *in[i].Auditor
			spec.Stages = append([]string(nil), in[i].Auditor.Stages...)
			spec.TurnsByStage = cloneStringMap(in[i].Auditor.TurnsByStage)
			spec.ModelByStage = cloneStringMap(in[i].Auditor.ModelByStage)
			spec.EngineByStage = cloneStringMap(in[i].Auditor.EngineByStage)
			spec.SkillsByStage = cloneStringSliceMap(in[i].Auditor.SkillsByStage)
			out[i].Auditor = &spec
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}
