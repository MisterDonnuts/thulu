# Deploying the MCP tool servers on Kubernetes

> This is the **in-cluster runbook**. For how MCPs get wired to agents
> regardless of where the server runs, and for Docker and externally hosted
> servers, see [`mcp-integration.md`](mcp-integration.md). For the whole
> install, see [`enterprise-setup.md`](enterprise-setup.md).

The Helm chart deploys Thulu's own MCP servers: `octopus-api`,
`thulu-github`, `thulu-browser`, `thulu-cloud` and `thulu-figma` (the last two
off by default). Each one is a Deployment plus a ClusterIP Service in the
release namespace, switched on by
`mcp.<server>.enabled` in the values. `thulu init` writes those flags from the
checklist it shows. There is no separate manifest to apply.

`thulu-sandbox` is not one of them: the daemon serves it itself at
`<AGENT_BASE_URL>/sandbox/sse` and the dispatcher injects that per-task URL.

---

## What the chart creates

For each enabled server (`templates/mcp.yaml`):

| Object | Name | Notes |
|---|---|---|
| Deployment | `<release fullname>-<server>` | 1 replica, image `<mcp.imageRegistry>/octopus-mcp-<server>:<tag>` |
| Service | same name, port 3000 | ClusterIP; agents reach it by FQDN |

Pods carry `app.kubernetes.io/name: <server>` and
`app.kubernetes.io/part-of: octopus-mcp`. The daemon's `/mcps` page lists and
restarts them by those two labels.

Every pod runs as uid/gid 1000 with `runAsNonRoot`, a read-only root
filesystem, all capabilities dropped, no privilege escalation, the
`RuntimeDefault` seccomp profile and `automountServiceAccountToken: false`. The
writable paths are emptyDirs: `/tmp` for all of them, plus `/dev/shm` (1Gi,
memory) and `/agents-shared` (1Gi) for `thulu-browser`.

The daemon Deployment gets, per enabled server:

```
MCP_<NAME>_URL=http://<fullname>-<server>.<namespace>.svc:3000/sse
OCTOPUS_MCP_FORWARD_CREDENTIAL=octopus-api,thulu-github,thulu-browser[,thulu-cloud][,thulu-figma]
```

The daemon forwards every `MCP_<NAME>_URL` into each agent pod, and
`OCTOPUS_MCP_FORWARD_CREDENTIAL` tells it which servers receive the run's own
API key on the handshake. None of the servers holds a credential of its own.
The servers call back into the daemon at
`http://<fullname>.<namespace>.svc:<service.port>`.

A key you set in the top-level `extraEnv` wins over the generated one, so one
server can point somewhere else without a duplicate env entry.

---

## Values

```yaml
mcp:
  imageRegistry: ghcr.io/misterdonnuts   # <registry>/octopus-mcp-<server>
  imageTag: ""                           # empty = the chart's appVersion
  pullPolicy: IfNotPresent
  podSecurityContext: {...}              # uid 1000, runAsNonRoot, seccomp
  securityContext: {...}                 # read-only root, drop ALL
  nodeSelector: {}
  tolerations: []
  octopusApi:   { enabled: true,  image: {repository: "", tag: ""}, resources: {...}, extraEnv: {} }
  thuluGithub:  { enabled: true,  ... }
  thuluBrowser: { enabled: true,  ... }
  thuluCloud:   { enabled: false, ... }
```

`thuluCloud` is off by default: it is the one server that talks to
your cloud account, and it does nothing until an environment has a cloud
credential. `extraEnv` per server takes plain variables, for example
`GITHUB_API_URL` for GitHub Enterprise on `thuluGithub`.

---

## Mirrored registry or a node without one

For an air-gapped cluster, push the `octopus-mcp-*` images to your mirror
and set `mcp.imageRegistry` to it. `thulu init` does this for you when
`OCTOPUS_IMAGE_REGISTRY` is set.

On a single k3s node with no registry, build the images on the node and pin
them into containerd:

```bash
# on the node, from the repo root
for t in thulu-browser thulu-github octopus-api thulu-cloud thulu-figma; do
  img=octopus-mcp-$t:local
  docker build -t "$img" "tools/$t"
  docker save "$img" -o "/tmp/$t.tar"
  sudo k3s ctr -n k8s.io images import "/tmp/$t.tar" && rm "/tmp/$t.tar"
  sudo k3s ctr -n k8s.io images label "docker.io/library/$img" io.cri-containerd.pinned=pinned
done
```

then point the chart at them:

```yaml
mcp:
  imageRegistry: docker.io/library
  imageTag: local
  pullPolicy: IfNotPresent
```

Re-importing an image drops its pin; label it again after every import, or the
kubelet's GC evicts it and the pod falls into `ErrImagePull`.

---

## Verify

```bash
kubectl get pods,svc -n <namespace> -l app.kubernetes.io/part-of=octopus-mcp
kubectl exec -n <namespace> deploy/<fullname> -c octopus -- \
  curl -s http://<fullname>-thulu-github.<namespace>.svc:3000/health
# {"ok":true,...}
```

Then enable the server for a stage in the agent's settings, run a task and look
at the agent's `session.log` init event:

```json
"mcp_servers":[{"name":"thulu-github","status":"connected"}]
```

`status:"failed"` means the agent pod cannot reach the Service. Check the FQDN
and any NetworkPolicy between the agents' namespace and the release namespace
on port 3000, and between the MCP pods and the daemon on its service port.

---

## Known limitations

- **One replica each.** SSE sessions are sticky; add session affinity before
  adding replicas.
- **Browser scratch is per pod.** Screenshots reach the task when the agent
  passes `task_id` (uploaded through the daemon API). The "persist to
  the run dir" mode writes into the pod's `/agents-shared` emptyDir, which the
  agent never sees.
