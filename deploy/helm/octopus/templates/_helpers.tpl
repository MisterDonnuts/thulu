{{/* Common naming + label helpers. */}}

{{- define "octopus.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "octopus.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "octopus.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "octopus.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "octopus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "octopus.selectorLabels" -}}
app.kubernetes.io/name: {{ include "octopus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* octopus.selectorLabels as key=value pairs (OCTOPUS_CONTROL_PLANE_POD_SELECTOR). */}}
{{- define "octopus.selectorLabelsList" -}}
{{- $pairs := list -}}
{{- range $k, $v := include "octopus.selectorLabels" . | fromYaml -}}
{{- $pairs = append $pairs (printf "%s=%s" $k $v) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end -}}

{{- define "octopus.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "octopus.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding env credentials (created or referenced). */}}
{{- define "octopus.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- include "octopus.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "octopus.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- if .Values.image.digest -}}
{{- printf "%s:%s@%s" .Values.image.repository $tag .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Sibling release image (<repository>-<suffix>:<tag>), e.g. octopus-agent. */}}
{{- define "octopus.siblingImage" -}}
{{- $tag := default .root.Chart.AppVersion .root.Values.image.tag -}}
{{- printf "%s-%s:%s" .root.Values.image.repository .suffix $tag -}}
{{- end -}}

{{/* The daemon ServiceAccount's username as the API server sees it. */}}
{{- define "octopus.serviceAccountUser" -}}
{{- printf "system:serviceaccount:%s:%s" .Release.Namespace (include "octopus.serviceAccountName" .) -}}
{{- end -}}

{{/* Name of the Secret the chart creates for an inline database.url. */}}
{{- define "octopus.databaseSecretName" -}}
{{- printf "%s-database" (include "octopus.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* ClusterRole the daemon binds to itself in each tenant namespace (rbac.yaml). */}}
{{- define "octopus.tenantSecretsRole" -}}
{{- printf "%s-tenant-secrets" (include "octopus.fullname" .) -}}
{{- end -}}

{{/*
In-cluster URL of the daemon's API: https when tls.enabled, so agents and MCP
servers dial the scheme the pod serves.
*/}}
{{- define "octopus.daemonURL" -}}
{{- printf "%s://%s.%s.svc:%v" (ternary "https" "http" .Values.tls.enabled) (include "octopus.fullname" .) .Release.Namespace .Values.service.port -}}
{{- end -}}

{{/*
GitHub Enterprise Server API base: github.apiUrl, else <github.url>/api/v3,
else empty (github.com). Mirrors internal/githubcfg.
*/}}
{{- define "octopus.githubAPIURL" -}}
{{- if .Values.github.apiUrl -}}
{{- trimSuffix "/" .Values.github.apiUrl -}}
{{- else if and .Values.github.url (ne (trimSuffix "/" .Values.github.url | lower) "https://github.com") -}}
{{- printf "%s/api/v3" (trimSuffix "/" .Values.github.url) -}}
{{- end -}}
{{- end -}}

{{/* Agent image build (agentRuntime.imageBuild): on only in kubernetes mode. */}}
{{- define "octopus.imageBuild.enabled" -}}
{{- if and (eq .Values.agentRuntime.mode "kubernetes") .Values.agentRuntime.imageBuild.enabled -}}true{{- end -}}
{{- end -}}

{{- define "octopus.imageBuild.namespace" -}}
{{- .Values.agentRuntime.imageBuild.namespace | default (printf "%s-builder" .Release.Name) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* No registry configured: the chart runs one in the builder namespace. */}}
{{- define "octopus.imageBuild.bundled" -}}
{{- if not .Values.agentRuntime.imageBuild.registry -}}true{{- end -}}
{{- end -}}

{{- define "octopus.imageBuild.pushRegistry" -}}
{{- if include "octopus.imageBuild.bundled" . -}}
{{- printf "registry.%s.svc.cluster.local:5000" (include "octopus.imageBuild.namespace" .) -}}
{{- else -}}
{{- .Values.agentRuntime.imageBuild.registry | trimSuffix "/" -}}
{{- end -}}
{{- end -}}

{{- define "octopus.imageBuild.pullRegistry" -}}
{{- if include "octopus.imageBuild.bundled" . -}}
{{- printf "localhost:%d" (int .Values.agentRuntime.imageBuild.localRegistry.hostPort) -}}
{{- else -}}
{{- include "octopus.imageBuild.pushRegistry" . -}}
{{- end -}}
{{- end -}}

{{/* The dockerconfigjson Secret in the builder namespace: the chart's for the bundled registry. */}}
{{- define "octopus.imageBuild.authSecret" -}}
{{- if include "octopus.imageBuild.bundled" . -}}registry-auth{{- else -}}{{ .Values.agentRuntime.imageBuild.pushSecret }}{{- end -}}
{{- end -}}
