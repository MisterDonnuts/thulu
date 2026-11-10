package api

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

type ToolDef struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Type        string        `json:"type,omitempty"`
	Tools       []ExposedTool `json:"tools,omitempty"`
	// web/src/lib/mcp-entry.ts reads Source: store/disk/builtin carry scopes and default_scope;
	// runtime/registered carry at most a probed tool list with no scopes.
	Source string `json:"source,omitempty"`
	// Without DefaultScope the picker renders every tool of a bare entry as granted, including ones
	// the server refuses.
	DefaultScope string `json:"default_scope,omitempty"`
}

type ExposedTool struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	// Scope outside read/write (thulu-github "deploy", octopus-api "board") is never implied and
	// must be named. Empty: the picker treats the server as one grant.
	Scope string `json:"scope,omitempty" yaml:"scope"`
}

type toolYaml struct {
	Name         string        `yaml:"name"`
	Description  string        `yaml:"description"`
	Type         string        `yaml:"type"`
	DefaultScope string        `yaml:"default_scope"`
	Tools        []ExposedTool `yaml:"tools"`
}

func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	byName := map[string]ToolDef{}

	declared, err := s.catalogTools(r)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.tools.errors.read_tools"))
		return
	}
	// mergeToolDefs keeps the first entry per name: add declared, then runtime, then registered.
	mergeToolDefs(byName, declared)
	mergeToolDefs(byName, s.runtimeToolDefs(r))
	mergeToolDefs(byName, s.registeredToolDefs(r))

	out := make([]ToolDef, 0, len(byName))
	for _, def := range byName {
		out = append(out, def)
	}
	// Only admins trigger the probe: it dials every forwarding URL over SSE, and viewers must not
	// fan that out on every page load.
	if callerIsAdmin(r) {
		s.enrichExposedTools(r.Context(), out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) enrichExposedTools(ctx context.Context, defs []ToolDef) {
	if s.mcp.prober == nil || s.mcp.fwd == nil {
		return
	}
	urls := s.mcp.fwd.Snapshot()
	if len(urls) == 0 {
		return
	}
	var wg sync.WaitGroup
	for i := range defs {
		if len(defs[i].Tools) > 0 {
			continue
		}
		sseURL := urls[mcp.NameToEnvKey(defs[i].Name)]
		if sseURL == "" {
			continue
		}
		if cached, ok := s.mcp.toolsCache.get(sseURL); ok {
			defs[i].Tools = cached
			continue
		}
		wg.Add(1)
		go func(def *ToolDef, sseURL string) {
			defer wg.Done()
			tools, err := s.mcp.prober.Tools(ctx, sseURL)
			if err != nil {
				log.Printf("[api] %s", i18n.Log("api.tools.log.probe_failed", "name", def.Name, "url", sseURL, "err", err))
				return
			}
			exposed := make([]ExposedTool, 0, len(tools))
			for _, t := range tools {
				exposed = append(exposed, ExposedTool{Name: t.Name, Description: t.Description})
			}
			def.Tools = exposed
			s.mcp.toolsCache.put(sseURL, exposed)
		}(&defs[i], sseURL)
	}
	wg.Wait()
}

func mergeToolDefs(dst map[string]ToolDef, defs []ToolDef) {
	for _, d := range defs {
		if d.Name == "" {
			continue
		}
		if _, ok := dst[d.Name]; !ok {
			dst[d.Name] = d
		}
	}
}

// Store vs agents dir is all-or-nothing: a catalog half from rows, half from the dir the import
// replaced, is worse than either.
// Builtin descriptors fill in per name, else a bare octopus-api (read-only on the server) reads as
// every tool granted.
func (s *Server) catalogTools(r *http.Request) ([]ToolDef, error) {
	declared, err := s.declaredTools(r)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(declared))
	for _, d := range declared {
		have[d.Name] = true
	}
	for _, d := range scanBuiltinTools() {
		if !have[d.Name] {
			declared = append(declared, d)
		}
	}
	return declared, nil
}

func (s *Server) declaredTools(r *http.Request) ([]ToolDef, error) {
	if store := s.agentStore; store != nil {
		// MCP manuals are install-wide (GlobalTenant): the registry they describe is operator-only.
		defs, err := s.scanStoredTools(r, store)
		if err != nil {
			return nil, err
		}
		if len(defs) > 0 {
			return defs, nil
		}
	}
	return s.scanDiskTools()
}

func toolDefFromYAML(name string, body []byte, source string) (ToolDef, bool) {
	var y toolYaml
	if err := yaml.Unmarshal(body, &y); err != nil {
		return ToolDef{}, false
	}
	def := ToolDef{Name: name, Type: "mcp", Description: y.Description, Tools: y.Tools,
		DefaultScope: y.DefaultScope, Source: source}
	if y.Type != "" {
		def.Type = y.Type
	}
	return def, true
}

func (s *Server) scanStoredTools(r *http.Request, store *agentstore.Store) ([]ToolDef, error) {
	owners, err := store.ListOwners(r.Context(), agentstore.GlobalTenant, agentstore.KindTool)
	if err != nil {
		return nil, err
	}
	out := make([]ToolDef, 0, len(owners))
	for _, name := range owners {
		f, readErr := store.ReadFile(r.Context(), agentstore.GlobalTenant,
			agentstore.KindTool, name, "tool.yaml")
		if readErr != nil {
			continue
		}
		if def, ok := toolDefFromYAML(name, f.Body, "store"); ok {
			out = append(out, def)
		}
	}
	return out, nil
}

func (s *Server) scanDiskTools() ([]ToolDef, error) {
	toolsDir := filepath.Join(s.agentsDir, "tools")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ToolDef, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		yamlBytes, rerr := safepath.ReadFile(filepath.Join(toolsDir, name), "tool.yaml")
		if rerr != nil {
			continue
		}
		if def, ok := toolDefFromYAML(name, yamlBytes, "disk"); ok {
			out = append(out, def)
		}
	}
	return out, nil
}

func scanBuiltinTools() []ToolDef {
	native := builtin.NativeTools()
	entries, err := fs.ReadDir(native, ".")
	if err != nil {
		return nil
	}
	out := make([]ToolDef, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, readErr := fs.ReadFile(native, e.Name()+"/tool.yaml")
		if readErr != nil {
			continue
		}
		if def, ok := toolDefFromYAML(e.Name(), body, "builtin"); ok {
			out = append(out, def)
		}
	}
	return out
}

func liveToolDefs[T any](items []T, source string, meta func(*T) (name, desc string)) []ToolDef {
	out := make([]ToolDef, 0, len(items))
	for i := range items {
		name, desc := meta(&items[i])
		out = append(out, ToolDef{Name: name, Type: "mcp", Description: desc, Source: source})
	}
	return out
}

func (s *Server) runtimeToolDefs(r *http.Request) []ToolDef {
	if s.mcp.mgr == nil {
		return nil
	}
	svcs, err := s.mcp.mgr.List(r.Context())
	if err != nil {
		return nil
	}
	return liveToolDefs(svcs, "runtime", func(v *mcp.Service) (string, string) { return v.Name, v.Description })
}

func (s *Server) registeredToolDefs(r *http.Request) []ToolDef {
	if s.mcp.registry == nil {
		return nil
	}
	regs, err := s.mcp.registry.List(r.Context())
	if err != nil {
		return nil
	}
	return liveToolDefs(regs, "registered", func(v *mcp.Registration) (string, string) { return v.Name, v.Description })
}

const toolsProbeTTL = time.Minute

type toolsCache struct {
	mu      sync.Mutex
	entries map[string]toolsCacheEntry
}

type toolsCacheEntry struct {
	tools []ExposedTool
	at    time.Time
}

func (c *toolsCache) get(url string) ([]ExposedTool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[url]
	if !ok || time.Since(e.at) > toolsProbeTTL {
		return nil, false
	}
	return e.tools, true
}

func (c *toolsCache) put(url string, tools []ExposedTool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]toolsCacheEntry{}
	}
	c.entries[url] = toolsCacheEntry{tools: tools, at: time.Now()}
}
