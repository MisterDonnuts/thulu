package initwizard

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type MCPServer struct {
	// Must match the compose service and profile, the Kubernetes Service, MCP_<NAME>_URL and
	// thulu.mcp.default labels.
	Name string
	// Must match the server's section under `mcp:` in the chart's values.
	HelmKey string
	// A catalog key, not text: this table is built before the CLI picks its language.
	SummaryKey string
	DefaultOn  bool
}

// thulu-sandbox is absent on purpose: the daemon serves it at /sandbox/sse per task.
var NativeMCPServers = []MCPServer{
	{Name: "octopus-api", HelmKey: "octopusApi", DefaultOn: true,
		SummaryKey: "initwizard.mcp.octopus_api"},
	{Name: "thulu-github", HelmKey: "thuluGithub", DefaultOn: true,
		SummaryKey: "initwizard.mcp.thulu_github"},
	{Name: "thulu-browser", HelmKey: "thuluBrowser", DefaultOn: true,
		SummaryKey: "initwizard.mcp.thulu_browser"},
	{Name: "thulu-cloud", HelmKey: "thuluCloud", DefaultOn: false,
		SummaryKey: "initwizard.mcp.thulu_cloud"},
	{Name: "thulu-figma", HelmKey: "thuluFigma", DefaultOn: false,
		SummaryKey: "initwizard.mcp.thulu_figma"},
}

func (s MCPServer) Summary() string { return i18n.Text(s.SummaryKey) }

const mcpPort = "3000"

// An empty value would read back as "never answered" and restore the defaults on the next run.
const noMCPServers = "none"

const mcpServersKey = "OCTOPUS_MCP_SERVERS"

// Must match api.forwardCredentialNames.
const mcpForwardListEnv = "OCTOPUS_MCP_FORWARD_CREDENTIAL"

// Must match mcp.NameToEnvKey and the agent entrypoint.
func MCPEnvKey(name string) string {
	return "MCP_" + strings.ReplaceAll(strings.ToUpper(name), "-", "_") + "_URL"
}

func ComposeMCPURL(name string) string {
	return "http://" + name + ":" + mcpPort + "/sse"
}

func defaultMCPSelection() []string {
	out := []string{}
	for _, s := range NativeMCPServers {
		if s.DefaultOn {
			out = append(out, s.Name)
		}
	}
	return out
}

func parseMCPSelection(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == noMCPServers {
		return out
	}
	for _, name := range strings.Split(raw, ",") {
		if isNativeMCP(strings.TrimSpace(name)) {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

const VersionEnvKey = "OCTOPUS_VERSION"

func InstalledMCPServers(f *EnvFile) []string {
	return parseMCPSelection(f.Get(mcpServersKey))
}

func isNativeMCP(name string) bool {
	for _, s := range NativeMCPServers {
		if s.Name == name {
			return true
		}
	}
	return false
}

func containsName(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// An unchecked server's URL is removed only if it is the in-stack one; an operator's external
// server stays.
func (c *Config) applyMCPToEnv(f *EnvFile) {
	if c.MCPServers == nil {
		return
	}
	if len(c.MCPServers) == 0 {
		f.Set(mcpServersKey, noMCPServers)
	} else {
		f.Set(mcpServersKey, strings.Join(c.MCPServers, ","))
	}
	setOrUnset(f, "COMPOSE_PROFILES", strings.Join(c.MCPServers, ","))

	for _, s := range NativeMCPServers {
		key := MCPEnvKey(s.Name)
		if containsName(c.MCPServers, s.Name) {
			f.Set(key, ComposeMCPURL(s.Name))
		} else if f.Get(key) == ComposeMCPURL(s.Name) {
			f.Unset(key)
		}
	}
	setOrUnset(f, mcpForwardListEnv, strings.Join(mergeForwardList(f.Get(mcpForwardListEnv), c.MCPServers), ","))
}

func mergeForwardList(existing string, selected []string) []string {
	out := []string{}
	for _, name := range strings.Split(existing, ",") {
		name = strings.TrimSpace(name)
		if name == "" || isNativeMCP(name) || containsName(out, name) {
			continue
		}
		out = append(out, name)
	}
	for _, s := range NativeMCPServers {
		if containsName(selected, s.Name) {
			out = append(out, s.Name)
		}
	}
	return out
}

func (c *Config) mcpHelmValues() map[string]any {
	values := map[string]any{}
	for _, s := range NativeMCPServers {
		values[s.HelmKey] = map[string]any{"enabled": containsName(c.MCPServers, s.Name)}
	}
	if reg := imageRegistryOverride(); reg != "" {
		values["imageRegistry"] = reg
	}
	return values
}
