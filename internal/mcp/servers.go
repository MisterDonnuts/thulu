package mcp

import (
	"net/url"
	"strings"
)

// The engine config writers in images/snippets (mcp-json.js, mcp-codex-args.js, the OpenCode
// writer) map this JSON; change them together.
const ServersEnv = "OCTOPUS_MCP_SERVERS"

type ServerEntry struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Transport string `json:"transport"`
	RunKey    bool   `json:"run_key"`
}

const ProxyPathPrefix = "/mcp-proxy/"

func ProxyURL(apiBase, name string) string {
	return strings.TrimRight(apiBase, "/") + ProxyPathPrefix + name
}

// Suffix rules must match images/snippets/mcp-config.sh.
type ToolEntry struct {
	Server string
	Scope  string
	Tools  []string
}

func ParseToolEntry(raw string) ToolEntry {
	name, suffix, _ := strings.Cut(strings.TrimSpace(raw), "@")
	e := ToolEntry{Server: strings.TrimSpace(name)}
	suffix = strings.TrimSpace(suffix)
	switch suffix {
	case "":
	case "read", "write":
		e.Scope = suffix
	default:
		for _, t := range strings.Split(suffix, "+") {
			if t = strings.TrimSpace(t); t != "" {
				e.Tools = append(e.Tools, t)
			}
		}
	}
	return e
}

// First entry wins for a repeated server, as the generated config (one JSON key per name) always
// did.
func ParseToolList(csv string) []ToolEntry {
	var out []ToolEntry
	seen := map[string]bool{}
	for _, raw := range strings.Split(csv, ",") {
		e := ParseToolEntry(raw)
		if e.Server == "" || seen[e.Server] {
			continue
		}
		seen[e.Server] = true
		out = append(out, e)
	}
	return out
}

func WithoutServer(csv, server string) string {
	var keep []string
	for _, raw := range strings.Split(csv, ",") {
		if e := ParseToolEntry(raw); e.Server != "" && e.Server != server {
			keep = append(keep, strings.TrimSpace(raw))
		}
	}
	return strings.Join(keep, ",")
}

func FindToolEntry(csv, server string) (ToolEntry, bool) {
	for _, e := range ParseToolList(csv) {
		if e.Server == server {
			return e, true
		}
	}
	return ToolEntry{}, false
}

// A scope, or a list containing a scope keyword, can only be expanded by the server, so the server
// enforces it from the forwarded query.
func (e ToolEntry) Restricted() bool {
	if e.Scope != "" || len(e.Tools) == 0 {
		return false
	}
	for _, t := range e.Tools {
		if t == "read" || t == "write" {
			return false
		}
	}
	return true
}

func (e ToolEntry) Allows(tool string) bool {
	if !e.Restricted() {
		return true
	}
	for _, t := range e.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// Servers parse this query (task, locale, scope or comma-separated tools); keep it in step with
// them.
func RunQuery(taskID, locale string, e ToolEntry) url.Values {
	q := url.Values{}
	if taskID != "" {
		q.Set("task", taskID)
	}
	if locale != "" {
		q.Set("locale", locale)
	}
	switch {
	case e.Scope != "":
		q.Set("scope", e.Scope)
	case len(e.Tools) > 0:
		q.Set("tools", strings.Join(e.Tools, ","))
	}
	return q
}

func WithQuery(raw string, q url.Values) string {
	if len(q) == 0 {
		return raw
	}
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return raw + sep + q.Encode()
}
