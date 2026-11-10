# Thulu Helm chart (self-hosted)

Deploys the single-node Thulu daemon (Community or Enterprise edition) into a
Kubernetes cluster. `values-saas.example.yaml` runs the same chart in SaaS mode.

> **License needed for the default runtime.** `agentRuntime.mode` defaults to
> `kubernetes`, which runs each agent as a pod and is an Enterprise feature:
> without a license that includes `kubernetes` (`secrets.licenseKey`), the
> daemon does not start. Without a license, set `agentRuntime.mode=docker`.
> Docker mode needs `dockerSocket.enabled=true` (a hostPath mount of the node's
> Docker socket, which is root on that node: pin the pod to a dedicated node)
> or a `DOCKER_HOST` in `extraEnv` that points at an isolated Docker endpoint.

## Quick start

```bash
# 1. Generate a JWT secret (auth fails closed without it)
JWT=$(openssl rand -hex 32)

# 2. Install. The daemon keeps its state in Postgres; the chart refuses to
#    render without a database. Without an Enterprise license, add
#    --set agentRuntime.mode=docker and a Docker endpoint (see "Docker socket"
#    below): the default kubernetes runtime needs a license, and the daemon
#    does not start without one.
helm install octopus deploy/helm/octopus \
  --namespace octopus --create-namespace \
  --set config.domain=thulu.acme.example \
  --set secrets.jwtSecret="$JWT" \
  --set database.url='postgres://octopus:PASS@db.acme.internal:5432/octopus?sslmode=verify-full' \
  --set ingress.hosts[0].host=thulu.acme.example \
  --set ingress.tls[0].hosts[0]=thulu.acme.example

# 3. Let tenant namespaces reach the daemon (see deploy/k8s-data-plane).
kubectl label namespace octopus thulu.dev/plane=control

# Or with a values file (recommended):
helm install octopus deploy/helm/octopus -n octopus --create-namespace \
  -f my-values.yaml          # see values-production.example.yaml (or values-saas.example.yaml)
```

## Enterprise Edition

Set `secrets.licenseKey` to an Enterprise license to turn on audit
export, the Kubernetes agent runtime and Stripe billing.
Without it the daemon refuses `agentRuntime.mode=kubernetes`, the default, and
stops; for the AGPL Community Edition set `agentRuntime.mode=docker`.
See [docs/ee-licensing.md](../../../docs/ee-licensing.md).

## Security notes (read before production)

Hardened defaults: uid/gid 10001 (non-root, numeric so the kubelet can verify
it), `readOnlyRootFilesystem`, all capabilities dropped, `RuntimeDefault`
seccomp, a single replica, and fail-closed checks for the JWT secret and the
database. Additional controls:

- **Agent runtime.** The default `agentRuntime.mode=kubernetes` launches each
  agent, sandbox and chat as a pod in a `tenant-<workspace>` namespace. The
  daemon's ClusterRole can create pods cluster-wide (RBAC cannot scope to
  namespaces that do not exist yet), so the chart also installs a
  `ValidatingAdmissionPolicy` (`agentRuntime.admissionPolicy`, Kubernetes
  >= 1.30) that confines every write of the daemon's ServiceAccount to
  `tenant-*` namespaces labeled `thulu.dev/plane=data` plus
  `agentRuntime.sandboxNamespace`, and rejects any pod it creates that uses a
  RuntimeClass outside `allowedRuntimeClasses`, host namespaces, hostPath,
  Secret volumes, Secret env other than the pod's own `<pod>-env` Secret,
  privileged containers or a ServiceAccount token. The daemon holds no
  cluster-wide Secret access: it binds the `<release>-octopus-tenant-secrets`
  ClusterRole to its own ServiceAccount with one RoleBinding
  (`octopus-daemon-tenant`) per tenant namespace, and the policy rejects any
  other RoleBinding it tries to write. Without the policy, treat the daemon's
  credentials as cluster-admin-equivalent.
- **Agent image build.** `agentRuntime.imageBuild` (on by default in
  kubernetes mode) creates `<release>-builder`, a PSA-privileged namespace
  where the daemon runs a rootless BuildKit Job that installs Claude Code into
  an agent image once per version, plus a registry for it and a node-local
  read-only mirror on `127.0.0.1:30500`. The daemon may write there only
  Jobs of that one shape (a second policy, `<fullname>-daemon-builds`) and
  their build-context ConfigMaps. `agentRuntime.imageBuild.enabled=false`
  removes all of it. Details: "Kubernetes: the agent image the install
  builds" in `docs/enterprise-setup.md`.
- **Docker socket (opt-in).** `agentRuntime.mode=docker` needs a Docker
  endpoint. `dockerSocket.enabled=true` hostPath-mounts the node's
  `/var/run/docker.sock`, which grants root on that node: pin the pod to a
  dedicated node (`nodeSelector` + taints) and add the socket's group to
  `podSecurityContext.supplementalGroups`. A Docker/sysbox endpoint you
  control, set through `extraEnv.DOCKER_HOST`, isolates better. The chart
  refuses docker mode with neither. For a mounted socket set both
  `agentRuntime.mode=docker` and `dockerSocket.enabled=true`.
- **Secrets.** Prefer `secrets.existingSecret` and `database.existingSecret`
  (sealed-secrets / Vault / External Secrets / SOPS) over inlining
  credentials. `database.url` is stored in a chart-owned Secret and reaches
  the pod through `secretKeyRef`, never as a literal in the pod spec.
- **Images.** Release images are signed with cosign (keyless, from the
  release workflow) and carry an SPDX SBOM attestation. Pin `image.digest`,
  and enforce signatures at admission with
  `deploy/k8s-data-plane/kyverno-verify-images.template.yaml`.
- **NetworkPolicy.** `networkPolicy.enabled=true` limits ingress to the
  ingress-controller namespace, the release namespace, tenant namespaces (in
  kubernetes mode) and `extraIngressFrom`; egress to DNS, HTTPS, the release
  namespace, Postgres, the API server and the tenant exec agents. Narrow
  `networkPolicy.postgres.cidrs` and `networkPolicy.apiServer.cidrs` to your
  addresses. In kubernetes mode the daemon also writes a NetworkPolicy into
  each tenant namespace: ingress only from the daemon pod on the exec-agent
  port, egress to DNS, the daemon, the gateway, the MCP servers and public
  443 minus private ranges and every node's own address.
- **MCP NetworkPolicy.** `mcp.networkPolicy.enabled=true` limits the MCP
  servers to ingress on 3000 from the daemon and (kubernetes mode) tenant
  agent pods, and egress to DNS, the daemon API and `publicPorts` (443) on
  any address outside `privateCIDRs`. The servers act for agents of every
  tenant and thulu-browser opens any page an agent names, so turn this on
  for a shared install. Each node's own IPv4 address is excluded too
  (`excludeNodeAddresses`, read from the cluster at install/upgrade; rerun
  `helm upgrade` when a node's address changes; when the installing identity
  cannot list nodes, set it to false and list them in `privateCIDRs` as /32). A
  private GitHub Enterprise Server for thulu-github goes in
  `mcp.networkPolicy.extraEgress`.
- **/metrics.** The series cover every workspace, so without a token
  `/metrics` needs an admin session in the operator workspace
  (`OCTOPUS_OPERATOR_WORKSPACE`). Set `secrets.metricsToken` and have
  Prometheus send it as `Authorization: Bearer <token>`.
- **Client IPs.** `config.trustedProxies` is empty by default, so the daemon
  ignores `X-Forwarded-For` and the audit log shows the ingress controller's
  address. Set it to the controller pods' IPs or pod CIDR. Do not use
  `private`: in a cluster that covers every pod, agent pods included, and any
  of them could forge the client address.
- **Stored credentials.** Set `secrets.secretboxKey` (or
  `OCTOPUS_SECRETBOX_KEY` in `secrets.existingSecret`). Without it, Git
  tokens and LLM keys are encrypted with a key derived from the JWT secret,
  so rotating that secret would lose them. The daemon re-encrypts existing
  values under the new key on its next start; keep the key afterwards.
- **TLS at the ingress.** No cert-manager annotation by default. Set
  `ingress.certManager.clusterIssuer` (public ACME or an internal CA
  ClusterIssuer) or `ingress.certManager.issuer`, or provide the TLS Secret
  yourself.
- **Agent-only paths at the edge.** `ingress.blockedPaths.enabled=true`
  adds a second Ingress that refuses the paths only agents and MCP servers
  use (`/llm-gateway`, `/internal/`, `/git/`, `/sandbox/sse`,
  `/sandbox/messages`, `/run/bundle`) plus `/metrics`, `/openapi.json`, GitHub's public
  callbacks and `/ingest/` unless their `expose*` flag is set. With Traefik
  it renders a Middleware that answers 403; for another controller set
  `traefik.middleware=false` and deny through `blockedPaths.annotations`.
  Agents reach the daemon on the Service, so they are not affected.
  `values-saas.example.yaml` turns it on.
- **TLS to the pod.** `tls.enabled=true` with `tls.secretName` (a
  `kubernetes.io/tls` Secret) makes the daemon serve HTTPS itself, so the hop
  from the ingress and every agent call is encrypted. The certificate must
  name `<release>-octopus.<namespace>.svc`, its CA goes in `caBundle`, and
  the ingress must talk HTTPS to the backend (ingress-nginx:
  `nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"` in
  `ingress.annotations`). Probes, `OCTOPUS_AGENT_BASE_URL` and the MCP
  servers switch to https on their own. The LLM gateway port stays plain
  HTTP inside the cluster.
- **Internal CA.** `caBundle.configMap` names a ConfigMap with a PEM bundle
  (`kubectl create configmap corp-ca --from-file=ca.crt=root.pem`). The
  daemon trusts it (`SSL_CERT_DIR`), copies it into tenant namespaces for
  agents and sandboxes (`OCTOPUS_EXTRA_CA_BUNDLE`, `GIT_SSL_CAINFO`,
  `NODE_EXTRA_CA_CERTS`), and the MCP servers get it as
  `NODE_EXTRA_CA_CERTS`. Use it for a GHES, a registry or a TLS-inspecting
  proxy signed by your PKI, and for `tls.enabled`.
- **GitHub Enterprise Server.** Set `github.url` (and `github.apiUrl` if the
  API is not at `<url>/api/v3`). Login, App tokens, git and agents follow
  it, and thulu-github gets `GITHUB_API_URL`.

## Key values

| Key | Default | Notes |
|-----|---------|-------|
| `config.domain` | `thulu.example.com` | Public domain (cookies/redirects) |
| `secrets.jwtSecret` | `""` | **Required** unless `secrets.existingSecret` |
| `secrets.existingSecret` | `""` | Reference an externally-managed Secret |
| `secrets.licenseKey` | `""` | EE license key |
| `secrets.secretboxKey` | `""` | Encrypts stored credentials; set once and keep |
| `secrets.metricsToken` | `""` | Bearer token for `/metrics` |
| `config.trustedProxies` | `""` | Ingress controller IPs/CIDRs allowed to set `X-Forwarded-For` |
| `config.sso.allowedDomains` / `allowedGroups` | `""` | Sign-in allowlists (`OCTOPUS_SSO_ALLOWED_DOMAINS` / `_GROUPS`) |
| `config.sso.allowPersonalWorkspaces` | `""` | `true`/`false`; empty = off with OIDC, on otherwise |
| `config.sso.defaultWorkspace` / `defaultRole` | `""` | Where new SSO users land, and as what (default viewer) |
| `config.auditSink` | `""` | `stdout`, `stderr` or `file:/path`, besides Postgres |
| `config.apiKeyMaxTTL` | `""` | Operator key lifetime cap; empty = 90d, `0` = none |
| `github.url` / `apiUrl` | `""` | GitHub Enterprise Server |
| `tls.enabled` / `secretName` | `false` / `""` | Daemon serves HTTPS with a `kubernetes.io/tls` Secret |
| `database.url` | `""` | Postgres DSN, stored in `<release>-octopus-database` |
| `database.existingSecret` / `existingSecretKey` | `""` / `OCTOPUS_DATABASE_URL` | DSN from your own Secret |
| `image.digest` | `""` | Pull by `sha256:` digest instead of tag |
| `agentRuntime.mode` | `kubernetes` | `kubernetes` or `docker` |
| `agentRuntime.engineImages.<engine>` | `""` | Agent image of each engine (`claude-code`, `codex`, `opencode`). Default `<image.repository>-agent-<engine>` at the chart's tag |
| `agentRuntime.agentImage` | `""` | One agent image for every engine; beats `engineImages`. Unset by default |
| `agentRuntime.sandboxImage` | `""` | Default `<image.repository>-sandbox` at the chart's tag |
| `agentRuntime.sandboxNamespace` | `octopus-sandboxes` | Namespace for sandboxes outside any workspace |
| `agentRuntime.admissionPolicy.enabled` | `true` | Fence the daemon's ServiceAccount (K8s >= 1.30) |
| `agentRuntime.admissionPolicy.allowedRuntimeClasses` | `[kata-clh, gvisor]` | Empty list skips the RuntimeClass check (dev clusters only) |
| `dockerSocket.enabled` | `false` | hostPath-mount the node Docker socket (docker mode) |
| `caBundle.configMap` / `key` | `""` / `ca.crt` | ConfigMap with an extra CA (PEM) for the daemon, agents and sandboxes |
| `persistence.size` | `10Gi` | Worktrees, logs and attachments at `/data` |
| `ingress.enabled` | `true` | |
| `ingress.certManager.clusterIssuer` / `issuer` | `""` | cert-manager issuer; set at most one |
| `ingress.blockedPaths.enabled` | `false` | Refuse agent-only paths at the edge (403) |
| `ingress.blockedPaths.exposeMetrics` / `exposeOpenAPI` / `exposeGithub` / `exposeIngest` | `false` | Keep routing that path from outside |
| `podSecurityContext.runAsUser` / `runAsGroup` / `fsGroup` | `10001` | Matches the image's numeric USER |
| `networkPolicy.enabled` | `false` | Opt-in ingress/egress restriction |
| `networkPolicy.extraIngressFrom` / `extraEgress` | `[]` | Extra peers / egress rules |
| `networkPolicy.postgres` | `{port: 5432, cidrs: []}` | Empty `cidrs` = any address on that port |
| `networkPolicy.apiServer` | `{ports: [443, 6443], cidrs: []}` | Kubernetes mode only |
| `mcp.networkPolicy.enabled` | `false` | Opt-in ingress/egress restriction for the MCP servers |
| `mcp.networkPolicy.allowTenantIngress` | `true` | Tenant agent pods may call the shared servers (kubernetes mode) |
| `mcp.networkPolicy.publicPorts` / `privateCIDRs` | `[443]` / RFC 1918, CGNAT, link-local | Public egress, and the ranges it never reaches |
| `mcp.networkPolicy.excludeNodeAddresses` | `true` | Except every node's InternalIP/ExternalIP (IPv4, Helm lookup at install/upgrade) |
| `mcp.networkPolicy.dnsNamespace` | `kube-system` | Empty = DNS to any address |
| `mcp.networkPolicy.extraIngressFrom` / `extraEgress` | `[]` | Extra peers / egress rules |
| `resources` | 250m/512Mi → 2/2Gi | Daemon pod (agents run as separate pods/containers) |

`replicaCount` must stay 1: the agent registry is in memory and the
dispatcher loops assume a single writer.

## Upgrade / backup

```bash
helm upgrade octopus deploy/helm/octopus -n octopus -f my-values.yaml
```

Schema migrations run at startup. Back up Postgres and the `/data` PVC before
upgrading. See `docs/upgrade-guide.md`.
