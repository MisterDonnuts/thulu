{{/*
MCP servers shipped with Thulu. `key` is the section under .Values.mcp,
`name` is the server's identity everywhere else: the image
(<registry>/octopus-mcp-<name>), the MCP_<NAME>_URL the daemon forwards to
agents, and the name agents enable per stage (thulu.mcp.default.<stage>).
thulu-sandbox is not listed: the daemon serves it itself at /sandbox/sse.
*/}}
{{- define "octopus.mcpCatalog" -}}
servers:
  - key: octopusApi
    name: octopus-api
  - key: thuluGithub
    name: thulu-github
  - key: thuluBrowser
    name: thulu-browser
  - key: thuluCloud
    name: thulu-cloud
  - key: thuluFigma
    name: thulu-figma
{{- end -}}

{{/* Resource name of one MCP server: <release fullname>-<server name>. */}}
{{- define "octopus.mcpFullname" -}}
{{- printf "%s-%s" (include "octopus.fullname" .root) .name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Labels for one MCP server. The daemon's MCP page (internal/mcp/k8s.go) lists
pods by part-of=octopus-mcp and names each server by app.kubernetes.io/name,
so name is the bare server name. It is deliberately NOT octopus.selectorLabels:
the daemon's Deployment selects on name=octopus + instance, and a pod carrying
both would be counted as one of its replicas.
*/}}
{{- define "octopus.mcpSelectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end -}}

{{- define "octopus.mcpLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "octopus.mcpSelectorLabels" . }}
app.kubernetes.io/component: mcp
app.kubernetes.io/part-of: octopus-mcp
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end -}}

{{/* Image reference of one MCP server. */}}
{{- define "octopus.mcpImage" -}}
{{- $cfg := index .root.Values.mcp .key -}}
{{- $repo := default (printf "%s/octopus-mcp-%s" (trimSuffix "/" .root.Values.mcp.imageRegistry) .name) $cfg.image.repository -}}
{{- $tag := $cfg.image.tag | default .root.Values.mcp.imageTag | default .root.Chart.AppVersion -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{/*
Where a run's thulu-browser runs (OCTOPUS_BROWSER_MODE, internal/browserrun):
the daemon's own default, per-run when OCTOPUS_SAAS_MODE is on and shared
otherwise, read from the same extraEnv the daemon gets.
*/}}
{{- define "octopus.browserMode" -}}
{{- $mode := lower (trim (toString (.Values.extraEnv.OCTOPUS_BROWSER_MODE | default ""))) -}}
{{- $saas := lower (trim (toString (.Values.extraEnv.OCTOPUS_SAAS_MODE | default ""))) -}}
{{- if $mode -}}
{{- $mode -}}
{{- else if or (eq $saas "1") (eq $saas "true") -}}
per-run
{{- else -}}
shared
{{- end -}}
{{- end -}}

{{/*
Daemon env for the enabled MCP servers: MCP_<NAME>_URL, which the daemon
forwards to every agent pod, and OCTOPUS_MCP_FORWARD_CREDENTIAL, which hands
these first-party servers the run's own API key. A key already set through
extraEnv wins, so an operator can point one server elsewhere without a
duplicate env entry.
*/}}
{{- define "octopus.mcpEnv" -}}
{{- $forward := list -}}
{{- range $s := (include "octopus.mcpCatalog" . | fromYaml).servers }}
{{- $cfg := index $.Values.mcp $s.key }}
{{- if $cfg.enabled }}
{{- $forward = append $forward $s.name }}
{{- $envKey := printf "MCP_%s_URL" ($s.name | upper | replace "-" "_") }}
{{- if not (hasKey $.Values.extraEnv $envKey) }}
- name: {{ $envKey }}
  value: {{ printf "http://%s.%s.svc:3000/sse" (include "octopus.mcpFullname" (dict "root" $ "name" $s.name)) $.Release.Namespace | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- if and (eq (include "octopus.browserMode" .) "per-run") .Values.mcp.thuluBrowser.enabled (not (hasKey .Values.extraEnv "OCTOPUS_BROWSER_IMAGE")) }}
{{- /* The per-run sidecar runs the same image as the shared server, so a
mirror set in mcp.imageRegistry reaches it too. */}}
- name: OCTOPUS_BROWSER_IMAGE
  value: {{ include "octopus.mcpImage" (dict "root" $ "name" "thulu-browser" "key" "thuluBrowser") | quote }}
{{- end }}
{{- if and $forward (not (hasKey .Values.extraEnv "OCTOPUS_MCP_FORWARD_CREDENTIAL")) }}
- name: OCTOPUS_MCP_FORWARD_CREDENTIAL
  value: {{ join "," $forward | quote }}
{{- end }}
{{- end -}}
