package infra

import (
	"slices"
	"strings"

	"github.com/docker/docker/api/types"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

type Kind string

const (
	KindDaemon       Kind = "daemon"
	KindPostgres     Kind = "postgres"
	KindWeb          Kind = "web"
	KindAgent        Kind = "agent"
	KindSandbox      Kind = "sandbox"
	KindChat         Kind = "chat"
	KindMCP          Kind = "mcp"
	KindBrowser      Kind = "browser"
	KindPlatform     Kind = "platform"
	KindSystem       Kind = "system"
	KindOtherInstall Kind = "other_install"
	KindOther        Kind = "other"
)

var kindOrder = []Kind{
	KindAgent, KindSandbox, KindChat, KindBrowser, KindMCP,
	KindDaemon, KindPostgres, KindWeb, KindPlatform, KindSystem, KindOtherInstall, KindOther,
}

const (
	labelComposeService = "com.docker.compose.service"
	labelComposeProject = "com.docker.compose.project"
	// Must match the labels internal/mcp/k8s.go puts on the Helm chart's MCP Deployments.
	labelPartOf  = "app.kubernetes.io/part-of"
	labelAppName = "app.kubernetes.io/name"
	partOfMCP    = "octopus-mcp"
)

// Service names from the dev (api, web) and production (octopus, caddy) compose files; update when
// either renames one.
var serviceKinds = map[string]Kind{
	"octopus": KindDaemon, "api": KindDaemon, "daemon": KindDaemon,
	"pg": KindPostgres, "postgres": KindPostgres, "db": KindPostgres,
	"caddy": KindWeb, "web": KindWeb,
}

func classifyDocker(ctr *types.Container, composeProjects []string) Kind {
	l := ctr.Labels
	if dockerx.ForeignInstall(l) {
		return KindOtherInstall
	}
	switch dockergc.ClassifyContainer(ctr, composeProjects) {
	case dockergc.ContainerBrowser:
		return KindBrowser
	case dockergc.ContainerAgent:
		return KindAgent
	case dockergc.ContainerSandbox:
		return KindSandbox
	case dockergc.ContainerChat:
		return KindChat
	case dockergc.ContainerMCP:
		return KindMCP
	case dockergc.ContainerBuild, dockergc.ContainerLogin:
		return KindPlatform
	case dockergc.ContainerStack:
		svc := l[labelComposeService]
		if k, ok := serviceKinds[svc]; ok {
			return k
		}
		if slices.Contains(dockergc.NativeMCPServers, svc) {
			return KindMCP
		}
		return KindPlatform
	}
	return KindOther
}

func dockerAttribution(c *Container, l map[string]string) {
	c.WorkspaceID = l[agent.LabelWorkspaceID]
	c.TaskID = firstNonEmpty(l[agent.LabelTask], l[sandbox.SandboxTaskLabel])
	c.Agent = l[agent.LabelType]
	c.Stage = l[agent.LabelStage]
	c.Service = firstNonEmpty(l[labelComposeService], l["octopus.mcp.service"])
	if c.Kind == KindAgent {
		// Containers started before the instance label was restored carry the instance only in
		// their name.
		c.Instance = firstNonEmpty(l[agent.LabelInstance], c.Name)
	}
}

func classifyPod(ns string, labels map[string]string, name, controlNS string, tenantNS bool) Kind {
	switch {
	case labels[kubeapi.LabelSandbox] != "":
		return KindSandbox
	case labels[kubeapi.LabelManagedBy] == "octopus" && labels[kubeapi.LabelStage] == "chat":
		return KindChat
	case labels[kubeapi.LabelManagedBy] == "octopus":
		return KindAgent
	case labels[labelPartOf] == partOfMCP:
		return KindMCP
	case ns == "kube-system":
		return KindSystem
	}
	if ns != controlNS && !tenantNS {
		return KindOther
	}
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "postgres"), strings.Contains(lower, "-pg-"), strings.HasSuffix(lower, "-pg"):
		return KindPostgres
	case strings.Contains(lower, "caddy"), strings.Contains(lower, "-web-"):
		return KindWeb
	case labels[labelAppName] == "octopus" && !strings.Contains(lower, "gateway"):
		return KindDaemon
	}
	return KindPlatform
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
