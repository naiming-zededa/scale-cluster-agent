#!/usr/bin/env bash
set -euo pipefail

# Multitenancy cleanup for scale-cluster-agent development on macOS (zsh/bash)
# - Stops scale-cluster-agent and kubectl proxy processes
# - Deletes tenant namespaces on the KWOK main cluster (label logical-cluster)
# - Deletes rancher-* KWOK clusters (optionally main-cluster)
# - Clears ~/.scale-cluster-agent/state.json and debug artifacts

INCLUDE_MAIN="false"
ASSUME_YES="false"

usage() {
  cat <<EOF
Usage: $(basename "$0") [--include-main] [-y]

Options:
  --include-main  Also delete the KWOK main cluster (main-cluster)
  -y              Do not prompt for confirmation
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --include-main)
      INCLUDE_MAIN="true"; shift ;;
    -y|--yes)
      ASSUME_YES="true"; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

confirm() {
  if [[ "$ASSUME_YES" == "true" ]]; then return 0; fi
  read -r -p "Proceed with multitenancy cleanup? [y/N] " ans
  [[ "${ans:-}" == "y" || "${ans:-}" == "Y" ]]
}

echo "==> Multitenancy cleanup starting"
confirm || { echo "Aborted."; exit 1; }

HOME_DIR=${HOME}
KWOK_DIR="$HOME_DIR/.kwok/clusters"
MAIN_CLUSTER="main-cluster"
MAIN_KUBECONFIG="$KWOK_DIR/$MAIN_CLUSTER/kubeconfig.yaml"
STATE_DIR="$HOME_DIR/.scale-cluster-agent"
STATE_FILE="$STATE_DIR/state.json"

# 1) Stop running processes
echo "==> Stopping processes (scale-cluster-agent, kubectl proxy)"

# Stop scale-cluster-agent
if pgrep -fl "(^|/)scale-cluster-agent( |$)" >/dev/null 2>&1; then
  pgrep -fl "(^|/)scale-cluster-agent( |$)" || true
  pkill -f "(^|/)scale-cluster-agent( |$)" || true
fi

# Stop kubectl proxies started during debugging
if pgrep -fl "kubectl.*proxy" >/dev/null 2>&1; then
  pgrep -fl "kubectl.*proxy" || true
  pkill -f "kubectl.*proxy" || true
fi

# Give processes a moment to exit
sleep 0.5

# 2) Delete tenant namespaces from main cluster (label logical-cluster)
if [[ -f "$MAIN_KUBECONFIG" ]]; then
  echo "==> Deleting tenant namespaces on $MAIN_CLUSTER (label logical-cluster)"
  set +e
  NS_LIST=$(kubectl --kubeconfig "$MAIN_KUBECONFIG" get ns -l logical-cluster -o name 2>/dev/null)
  RC=$?
  set -e
  if [[ $RC -eq 0 && -n "${NS_LIST:-}" ]]; then
    echo "$NS_LIST" | while read -r ns; do
      [[ -z "$ns" ]] && continue
      echo "Deleting $ns"
      nsName=${ns#namespace/}
      # Best-effort: delete namespace-scoped resources first (non-blocking)
      for rt in pods deploy svc cm secret; do
        kubectl --kubeconfig "$MAIN_KUBECONFIG" -n "$nsName" delete "$rt" --all --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
      done
      # Delete the namespace without waiting to avoid hangs in KWOK
      kubectl --kubeconfig "$MAIN_KUBECONFIG" delete "$ns" --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
    done
  else
    echo "No tenant namespaces found (or kubectl not available)."
  fi
  # Also remove tenant cattle-system-* namespaces created by import (non-blocking)
  echo "==> Deleting tenant cattle-system-* namespaces"
  set +e
  CS_NS=$(kubectl --kubeconfig "$MAIN_KUBECONFIG" get ns -o name | grep '^namespace/cattle-system-' 2>/dev/null)
  set -e
  if [[ -n "${CS_NS:-}" ]]; then
    echo "$CS_NS" | while read -r ns; do
      [[ -z "$ns" ]] && continue
      echo "Deleting $ns"
      kubectl --kubeconfig "$MAIN_KUBECONFIG" delete "$ns" --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
    done
  fi
else
  echo "==> Main kubeconfig not found at $MAIN_KUBECONFIG; skipping namespace cleanup"
fi

# 3) Delete KWOK clusters created by the agent (rancher-*)
echo "==> Deleting KWOK clusters with prefix rancher-*"
if [[ -d "$KWOK_DIR" ]]; then
  shopt -s nullglob
  for d in "$KWOK_DIR"/rancher-*; do
    [[ -d "$d" ]] || continue
    name=$(basename "$d")
    echo "Deleting KWOK cluster: $name"
    if command -v kwokctl >/dev/null 2>&1; then
      kwokctl delete cluster --name "$name" >/dev/null 2>&1 || true
    fi
    rm -rf "$d" || true
  done
  shopt -u nullglob
else
  echo "KWOK clusters directory not found: $KWOK_DIR"
fi

# Optionally delete main cluster
if [[ "$INCLUDE_MAIN" == "true" ]]; then
  echo "==> Deleting KWOK main cluster: $MAIN_CLUSTER"
  if [[ -d "$KWOK_DIR/$MAIN_CLUSTER" ]]; then
    if command -v kwokctl >/dev/null 2>&1; then
      kwokctl delete cluster --name "$MAIN_CLUSTER" >/dev/null 2>&1 || true
    fi
    rm -rf "$KWOK_DIR/$MAIN_CLUSTER" || true
  fi
fi

# 4) Clear agent state and debug artifacts
echo "==> Clearing agent state and debug artifacts"
rm -f "$STATE_FILE" 2>/dev/null || true

# Remove debug YAML files saved by the agent
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEBUG_YAML_DIR="$REPO_ROOT/debug-yaml"
if [[ -d "$DEBUG_YAML_DIR" ]]; then
  rm -rf "$DEBUG_YAML_DIR" || true
  echo "Removed $DEBUG_YAML_DIR"
fi

echo "==> Cleanup complete"

# 5) Show residual listeners (informational)
echo "Open listeners on localhost (post-cleanup):"
if command -v lsof >/dev/null 2>&1; then
  lsof -nP -iTCP -sTCP:LISTEN | grep -E "127.0.0.1|localhost" || true
else
  echo "lsof not available; skipping listener summary."
fi
