package dockergc

import (
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/sandbox/sandboxid"
)

// The daemon sees all of these: the compose stacks hand deploy/.env to the daemon container whole
// (env_file).
const (
	EnvRollbackImage    = "OCTOPUS_ROLLBACK_IMAGE"
	EnvRollbackUntil    = "OCTOPUS_ROLLBACK_UNTIL"
	EnvRollbackDays     = "OCTOPUS_ROLLBACK_IMAGE_DAYS"
	DefaultRollbackDays = 7
	// Written by `thulu apply` in deploy/.env. Supersedes EnvRollbackImage, still honored for
	// installs an older CLI updated.
	EnvKeptImages = "OCTOPUS_KEPT_IMAGES"

	envDaemonImage  = "OCTOPUS_IMAGE"
	envVersion      = "OCTOPUS_VERSION"
	envSandboxImage = "OCTOPUS_SANDBOX_IMAGE"
	// Same value as browserrun.EnvImage.
	envBrowserImage = "OCTOPUS_BROWSER_IMAGE"
)

// What deploy/docker-compose.yml runs when OCTOPUS_IMAGE is unset.
//
//i18n:ignore image reference, not a message
const composeDefaultDaemon = "octopus:latest"

// Every native MCP server is kept, enabled or not, so no list of enabled servers has to be
// authoritative.
func KeepFromEnv(getenv func(string) string) []string {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	registry := strings.TrimRight(get(imageref.RegistryEnv), "/")
	published := registry
	if published == "" {
		published = DefaultRegistry
	}
	tag := imageTag(get(envVersion))

	var keep []string
	if img := get(envDaemonImage); img != "" {
		keep = append(keep, img)
	} else {
		keep = append(keep, composeDefaultDaemon)
		if get(envVersion) != "" {
			keep = append(keep, published+"/octopus:"+tag)
		}
	}
	images := append(agentimage.FromEnv(getenv).All(), get(envSandboxImage), get(envBrowserImage), sandboxid.DefaultRuntimeImage)
	for _, img := range images {
		if img == "" {
			continue
		}
		// Launchers resolve a registry-less name against the mirror; either spelling may be on
		// disk.
		keep = append(keep, img)
		if resolved := imageref.WithRegistry(registry, img); resolved != img {
			keep = append(keep, resolved)
		}
	}
	for _, name := range NativeMCPServers {
		keep = append(keep, published+"/"+MCPImage(name)+":"+tag)
		if img := get(MCPImageEnv(name)); img != "" {
			keep = append(keep, img)
		}
	}
	keep = append(keep, SplitList(get(EnvKeptImages))...)
	return keep
}

// The repository a native MCP server is published as: the server name without its thulu- prefix
// (thulu-github ships as octopus-mcp-github, octopus-api as octopus-mcp-octopus-api).
func MCPImage(server string) string {
	return mcpImagePrefix + strings.TrimPrefix(server, "thulu-")
}

// The compose files read this key (OCTOPUS_MCP_GITHUB_IMAGE) and fall back to
// <registry>/<MCPImage>:<version> when unset.
func MCPImageEnv(server string) string {
	return strings.ToUpper(strings.ReplaceAll(MCPImage(server), "-", "_")) + "_IMAGE"
}

func SplitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
}

func RegistriesFromEnv(getenv func(string) string) []string {
	if r := strings.TrimRight(strings.TrimSpace(getenv(imageref.RegistryEnv)), "/"); r != "" {
		return []string{r}
	}
	return nil
}

func RollbackFromEnv(getenv func(string) string) (images []string, until time.Time) {
	img := strings.TrimSpace(getenv(EnvRollbackImage))
	if img == "" {
		return nil, time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(getenv(EnvRollbackUntil)))
	if err != nil {
		return nil, time.Time{}
	}
	return []string{img}, t
}

func RollbackDays(getenv func(string) string) int {
	v := strings.TrimSpace(getenv(EnvRollbackDays))
	if v == "" {
		return DefaultRollbackDays
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return DefaultRollbackDays
	}
	return n
}

// Must match initwizard.ImageTag and the release workflow's tags.
func imageTag(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "dev" {
		return "latest"
	}
	return version
}
