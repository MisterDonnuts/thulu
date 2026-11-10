# Agent data plane — Kubernetes manifests

Declarative substrate for the agent data plane: per-tenant isolation for
agent containers running untrusted, LLM-generated code.

The daemon schedules agent pods into these namespaces through its Kubernetes
runtime (`OCTOPUS_AGENT_RUNTIME=kubernetes`, see `docs/enterprise-setup.md`).

## What's here

| File | Purpose |
|------|---------|
| `runtimeclass-gvisor.yaml`, `runtimeclass-kata.yaml` | `RuntimeClass`es for gVisor (`runsc`) and Kata. Require containerd configured with the matching handler on the agent nodes. |
| `vap-runtimeclass.yaml` | `ValidatingAdmissionPolicy` for every pod in a data-plane namespace (label `thulu.dev/plane=data`, a `tenant-` prefix, or `octopus-sandboxes`): a sandboxed `runtimeClassName` and no privileged container, init container or ephemeral container. Also matches `pods/ephemeralcontainers`, so `kubectl debug` cannot add a privileged container to a running pod. |
| `tenant-namespace.template.yaml` | Per-tenant namespace: PSA `restricted`, deny-by-default `NetworkPolicy`, DNS + public HTTPS minus `INTERNAL_CIDRS`, the control-plane allow (agents → daemon/gateway/MCP, daemon → exec agent), `ResourceQuota` and `LimitRange`. |
| `egress-strict.template.yaml` | Replaces the public-HTTPS rule once the LLM gateway runs: DNS and the control plane only. |
| `kyverno-verify-images.template.yaml` | Kyverno policy: every `octopus*` image must carry a keyless cosign signature from the release workflow at a `v*` tag. Registry, signer identity and Rekor URL are variables. |
| `falco-rules.yaml` | Falco rules for runtime detection inside sandboxes. |
| `render.sh` | Renders the templates with defaults. Plain `envsubst` leaves unset variables empty and breaks the policies. |

### Rendering

```bash
TENANT_ID=ws-acme CONCURRENCY_LIMIT=5 \
  deploy/k8s-data-plane/render.sh tenant-namespace.template.yaml | kubectl apply -f -
```

| Variable | Default | Used by |
|----------|---------|---------|
| `TENANT_ID` | required | tenant, egress-strict |
| `CONCURRENCY_LIMIT` | `5` | tenant |
| `INTERNAL_CIDRS` | `10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16` | tenant. Add the cluster's pod and service CIDRs if they fall outside these. |
| `CONTROL_PLANE_LABEL` | `control` | tenant, egress-strict |
| `DAEMON_PORT` / `GATEWAY_PORT` / `MCP_PORT` / `EXEC_PORT` | `8585` / `8586` / `3000` / `8587` | tenant, egress-strict |
| `IMAGE_REGISTRY` | `ghcr.io/misterdonnuts` | kyverno |
| `SIGNING_SUBJECT` | `https://github.com/MisterDonnuts/thulu/.github/workflows/octopus-release.yml@refs/tags/v*` | kyverno |
| `REKOR_URL` | `https://rekor.sigstore.dev` | kyverno |

The control-plane rules select the namespace Thulu runs in by label, so label
it once:

```bash
kubectl label namespace octopus thulu.dev/plane=control
```

The Kyverno admission controller must reach the registry, `REKOR_URL` and the
Sigstore TUF root (`tuf-repo-cdn.sigstore.dev`). The policy fails closed: when
Rekor is unreachable, no Thulu pod is admitted. An air-gapped cluster needs its
own Sigstore deployment and `REKOR_URL` pointing at it.

## Isolation model (recap)

- **gVisor:** `runtimeClassName: gvisor` + per-tenant namespace + NetworkPolicy.
  Userspace-kernel isolation between tenants on the same node.
- **Kata (microVM):** the same, with a stronger runtime. Swap the RuntimeClass
  handler.
- **Dedicated nodes:** a node group per tenant (taint/toleration). The namespace
  template still applies for network isolation.

## Validation

`make k8s-lint` parses every manifest (and renders the tenant template with
sample values). Full behavioral validation (pods actually confined by gVisor,
NetworkPolicy enforced) requires a cluster with runsc + a CNI that implements
NetworkPolicy (e.g. Cilium/Calico) — run the conformance steps below on the
target cluster before onboarding tenants:

```bash
# 1. runsc active?
kubectl run gvisor-probe --rm -it --restart=Never \
  --overrides='{"spec":{"runtimeClassName":"gvisor"}}' \
  --image=alpine -- sh -c 'dmesg | head -1'   # expect gVisor's banner, not the host kernel

# 2. cross-tenant deny enforced?
# from a pod in tenant-a, curl a pod IP in tenant-b → must time out.

# 3. metadata endpoint blocked?
# from any agent pod: curl -m2 http://169.254.169.254 → must fail.
```
