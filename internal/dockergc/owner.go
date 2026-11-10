package dockergc

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/enginecache"
	"github.com/MisterDonnuts/thulu/internal/i18n"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
	"github.com/MisterDonnuts/thulu/internal/sandbox/sandboxid"
)

const (
	// Set by internal/mcp on the MCP containers it creates outside compose.
	labelMCP = "octopus.mcp"
	// Must equal browserrun.LabelBrowser (a test holds them together).
	LabelBrowser = "thulu.browser"

	labelSandboxCache = sandboxid.CacheVolumeLabel

	labelPersonalLogin = personallogin.VolumeLabel
	labelEngineCache   = enginecache.VolumeLabel

	labelComposeProject    = "com.docker.compose.project"
	labelComposeWorkingDir = "com.docker.compose.project.working_dir"
	labelComposeVolume     = "com.docker.compose.volume"

	// Set by deploy/Dockerfile; it survives on a build that lost its tag, which is how a dangling
	// daemon build is told apart.
	labelImageSource = "org.opencontainers.image.source"
	sourceRepo       = "github.com/misterdonnuts/thulu"
	// Stamped by deploy/Dockerfile from VERSION.
	labelImageVersion = "org.opencontainers.image.version"
)

// Where the release workflow pushes every image.
const DefaultRegistry = "ghcr.io/misterdonnuts"

type ContainerKind string

const (
	ContainerAgent   ContainerKind = "agent"
	ContainerSandbox ContainerKind = "sandbox"
	ContainerChat    ContainerKind = "chat"
	ContainerBrowser ContainerKind = "browser"
	ContainerLogin   ContainerKind = "login"
	//i18n:ignore enum value, not a message
	ContainerBuild ContainerKind = "build leftover"
	ContainerMCP   ContainerKind = "mcp"
	ContainerStack ContainerKind = "stack"
)

var containerKindLabels = map[ContainerKind]string{
	ContainerAgent:   "dockergc.container_kinds.agent",
	ContainerSandbox: "dockergc.container_kinds.sandbox",
	ContainerChat:    "dockergc.container_kinds.chat",
	ContainerBrowser: "dockergc.container_kinds.browser",
	ContainerLogin:   "dockergc.container_kinds.login",
	ContainerBuild:   "dockergc.container_kinds.build",
	ContainerMCP:     "dockergc.container_kinds.mcp",
	ContainerStack:   "dockergc.container_kinds.stack",
}

func (k ContainerKind) Label() string {
	if key, ok := containerKindLabels[k]; ok {
		return i18n.Text(key)
	}
	return string(k)
}

type ImageKind string

const (
	ImageDaemon    ImageKind = "daemon"
	ImageComponent ImageKind = "component"
	//i18n:ignore enum value, not a message
	ImageBuild ImageKind = "dangling build"
	//i18n:ignore enum value, not a message
	ImageBase ImageKind = "build base"
)

const daemonFamily = "daemon"

// Local names are what the Makefile and tools/docker-compose.yml tag on a dev machine.
var (
	publishedComponents = func() map[string]bool {
		m := map[string]bool{
			agentimage.Combined: true, "octopus-agent-base": true,
			"octopus-sandbox": true, "octopus-runtime": true,
		}
		for _, e := range agentimage.Engines {
			m[agentimage.Name(e)] = true
		}
		return m
	}()
	localComponents = map[string]bool{
		"octopus-docker-agent": true, "octopus-docker": true, "octopus-runtime": true,
		"thulu-browser": true, "thulu-github": true, "thulu-cloud": true, "thulu-figma": true,
		"octopus-api": true,
	}
)

const mcpImagePrefix = "octopus-mcp-"

// Mirrors initwizard.NativeMCPServers (a test holds them together) so the daemon does not import
// the wizard.
var NativeMCPServers = []string{"octopus-api", "thulu-github", "thulu-browser", "thulu-cloud", "thulu-figma"}

// Data volumes are removed only by `uninstall -purge-data`; only list in cacheVolumes what the next
// `up` rebuilds.
var (
	dataVolumes = map[string]bool{
		"octopus-data": true, "octopus-pg": true, "caddy-data": true, "caddy-config": true,
		"octopus-dev-data": true, "octopus-dev-pg": true,
	}
	cacheVolumes = map[string]bool{
		"go-mod-cache": true, "go-build-cache": true, "web-node-modules": true,
	}
)

type classifier struct {
	registries map[string]bool
	projects   map[string]bool
	// Compose names an image built for a service with no `image:` <project>-<service>.
	composeBuilt map[string]bool
}

func newClassifier(opts *Options) *classifier {
	c := &classifier{
		registries:   map[string]bool{DefaultRegistry: true},
		projects:     map[string]bool{},
		composeBuilt: map[string]bool{},
	}
	for _, r := range opts.Registries {
		if r = strings.TrimRight(strings.TrimSpace(r), "/"); r != "" {
			c.registries[r] = true
		}
	}
	for _, p := range opts.ComposeProjects {
		if p = strings.TrimSpace(p); p != "" {
			c.projects[p] = true
			c.composeBuilt[p+"-api"] = true
			c.composeBuilt[p+"-octopus"] = true
		}
	}
	return c
}

func (c *classifier) repo(repo string) (ImageKind, string) {
	repo = trimDockerHub(repo)
	prefix, name := "", repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		prefix, name = repo[:i], repo[i+1:]
	}
	if prefix != "" && !c.registries[prefix] {
		return "", ""
	}
	switch {
	case name == "octopus", prefix == "" && (name == "octopus-daemon" || c.composeBuilt[name]):
		return ImageDaemon, daemonFamily
	case strings.HasPrefix(name, mcpImagePrefix):
		return ImageComponent, strings.TrimPrefix(name, mcpImagePrefix)
	case publishedComponents[name], localComponents[name]:
		return ImageComponent, name
	}
	return "", ""
}

func (c *classifier) image(img *image.Summary) (kind ImageKind, family string, refs []string) {
	for _, ref := range tagRefs(img) {
		k, f := c.repo(splitRef(ref).repo)
		if k == "" {
			continue
		}
		if kind == "" || k == ImageDaemon {
			kind, family = k, f
		}
		refs = append(refs, ref)
	}
	if kind != "" {
		return kind, family, refs
	}
	if hasOctopusSource(img.Labels) {
		if len(tagRefs(img)) == 0 {
			return ImageBuild, daemonFamily, nil
		}
		return ImageDaemon, daemonFamily, tagRefs(img)
	}
	return "", "", nil
}

func hasOctopusSource(labels map[string]string) bool {
	src := strings.ToLower(strings.TrimSpace(labels[labelImageSource]))
	src = strings.TrimSuffix(strings.TrimSuffix(src, "/"), ".git")
	src = strings.TrimPrefix(strings.TrimPrefix(src, "https://"), "http://")
	return src == sourceRepo
}

func (c *classifier) container(ctr *types.Container, ownedImage func(id string) bool) ContainerKind {
	l := ctr.Labels
	switch {
	// Another install on the same engine made it: never reap or count it.
	case dockerx.ForeignInstall(l):
		return ""
	case l[agent.LabelManaged] == "true":
		return ContainerAgent
	case l[sandboxid.ContainerLabel] == "true":
		return ContainerSandbox
	case l[agent.LabelChat] == "true":
		return ContainerChat
	case l[LabelBrowser] == "true":
		return ContainerBrowser
	case l[personallogin.ContainerLabel] != "":
		return ContainerLogin
	case l[labelMCP] == "true":
		return ContainerMCP
	case l[labelComposeProject] != "" && c.projects[l[labelComposeProject]]:
		return ContainerStack
	case l[labelComposeProject] == "" && !isRunning(ctr.State) && ownedImage(ctr.ImageID):
		return ContainerBuild
	}
	return ""
}

func ClassifyContainer(ctr *types.Container, composeProjects []string) ContainerKind {
	return newClassifier(&Options{ComposeProjects: composeProjects}).container(ctr, func(string) bool { return false })
}

// A network the operator created by hand under the agent network's name has no managed label and is
// left alone.
func (c *classifier) network(name string, labels map[string]string, agentNetwork string) bool {
	if name == agentNetwork && labels[agentnet.ManagedLabel] != "" {
		return true
	}
	return labels[labelComposeProject] != "" && c.projects[labels[labelComposeProject]]
}

func isRunning(state string) bool {
	switch state {
	case "running", "restarting", "paused":
		return true
	}
	return false
}

type ref struct {
	repo, tag, digest string
}

func splitRef(s string) ref {
	var r ref
	if i := strings.Index(s, "@"); i >= 0 {
		r.digest = s[i+1:]
		s = s[:i]
	}
	slash := strings.LastIndex(s, "/")
	if c := strings.LastIndex(s, ":"); c > slash {
		r.tag = s[c+1:]
		s = s[:c]
	}
	r.repo = s
	return r
}

func normalizeRef(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "sha256:") {
		return s
	}
	r := splitRef(s)
	out := trimDockerHub(r.repo)
	if r.tag == "" && r.digest == "" {
		r.tag = "latest"
	}
	if r.tag != "" {
		out += ":" + r.tag
	}
	if r.digest != "" {
		out += "@" + r.digest
	}
	return out
}

func trimDockerHub(repo string) string {
	for _, p := range []string{"docker.io/library/", "index.docker.io/library/", "docker.io/", "index.docker.io/"} {
		if strings.HasPrefix(repo, p) {
			return strings.TrimPrefix(repo, p)
		}
	}
	return repo
}

// The classic image store lists "<none>:<none>" for an untagged image.
func tagRefs(img *image.Summary) []string {
	out := make([]string, 0, len(img.RepoTags))
	for _, t := range img.RepoTags {
		if t != "" && t != "<none>:<none>" && t != "<none>@<none>" {
			out = append(out, t)
		}
	}
	return out
}
