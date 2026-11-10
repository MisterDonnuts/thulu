#!/usr/bin/env bash
set -euo pipefail

CLUSTER="${CLUSTER:-octopus-dev}"
DATA_PLANE_DIR="deploy/k8s-data-plane"

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: '$1' not found on PATH" >&2; exit 1; }; }

cmd_up() {
  need k3d
  need kubectl

  if k3d cluster list 2>/dev/null | grep -q "^${CLUSTER}\b"; then
    echo "==> cluster ${CLUSTER} already exists"
  else
    echo "==> creating k3d cluster ${CLUSTER} (flannel + built-in netpol disabled for Calico)"
    # k3s's default CNI does not enforce NetworkPolicy, which tenant isolation depends on; Calico
    # must own the CNI.
    k3d cluster create "${CLUSTER}" \
      --k3s-arg '--flannel-backend=none@server:*' \
      --k3s-arg '--disable-network-policy@server:*' \
      --wait
  fi

  echo "==> installing Calico"
  kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.28.0/manifests/calico.yaml
  echo "==> waiting for Calico to be ready"
  kubectl -n kube-system rollout status deploy/calico-kube-controllers --timeout=180s || true

  echo "==> applying sandbox RuntimeClasses (gvisor, kata-clh) + admission policy"
  kubectl apply -f "${DATA_PLANE_DIR}/runtimeclass-gvisor.yaml"
  kubectl apply -f "${DATA_PLANE_DIR}/runtimeclass-kata.yaml"
  kubectl apply -f "${DATA_PLANE_DIR}/vap-runtimeclass.yaml"

  import_agents

  echo "==> ready. Point the daemon at this cluster:"
  echo "    OCTOPUS_AGENT_RUNTIME=kubernetes OCTOPUS_KUBECONFIG=\$(k3d kubeconfig write ${CLUSTER})"
}

import_agents() {
  if [ -n "${AGENTS:-}" ]; then
    echo "==> importing agent images into ${CLUSTER}: ${AGENTS}"
    # shellcheck disable=SC2086
    k3d image import -c "${CLUSTER}" ${AGENTS}
  fi
}

cmd_tenant() {
  need kubectl
  need envsubst
  local ws="${1:?usage: k8s-dev.sh tenant <workspace-id> [concurrency]}"
  local limit="${2:-5}"
  echo "==> rendering + applying tenant namespace for ${ws} (concurrency ${limit})"
  TENANT_ID="${ws}" CONCURRENCY_LIMIT="${limit}" \
    "${DATA_PLANE_DIR}/render.sh" tenant-namespace.template.yaml | kubectl apply -f -
}

cmd_down() {
  need k3d
  echo "==> deleting cluster ${CLUSTER}"
  k3d cluster delete "${CLUSTER}"
}

case "${1:-}" in
  up)     cmd_up ;;
  down)   cmd_down ;;
  tenant) shift; cmd_tenant "$@" ;;
  *) echo "usage: $0 {up|down|tenant <workspace-id> [concurrency]}" >&2; exit 2 ;;
esac
