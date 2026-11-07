#!/usr/bin/env bash
# Create the outflow-dev kind cluster, sample workloads and a restricted read-only identity.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
name=outflow-dev
ctx="kind-$name"

if ! kind get clusters | grep -qx "$name"; then
  kind create cluster --config "$here/cluster.yaml"
fi

kubectl --context "$ctx" apply -f "$here/workloads.yaml"
kubectl --context "$ctx" apply -f "$root/deploy/clusterrole.yaml"
kubectl --context "$ctx" -n default create serviceaccount outflow-reader --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
kubectl --context "$ctx" create clusterrolebinding outflow-reader --clusterrole=outflow-reader \
  --serviceaccount=default:outflow-reader --dry-run=client -o yaml | kubectl --context "$ctx" apply -f -
kubectl --context "$ctx" -n payments rollout status deploy/ledger-worker --timeout=120s
kubectl --context "$ctx" -n analytics rollout status deploy/event-collector --timeout=120s
kubectl --context "$ctx" -n shop rollout status deploy/cart --timeout=120s
kubectl --context "$ctx" -n monitoring rollout status ds/node-exporter --timeout=120s

# Restricted kubeconfig: the kind API server with a service-account token, never admin.
token="$(kubectl --context "$ctx" -n default create token outflow-reader --duration 2h)"
kc="$here/.kubeconfig"
kind get kubeconfig --name "$name" >"$kc"
chmod 600 "$kc"
KUBECONFIG="$kc" kubectl config set-credentials outflow-reader --token "$token" >/dev/null
KUBECONFIG="$kc" kubectl config set-context "$ctx" --user outflow-reader >/dev/null
echo "wrote $kc (restricted identity outflow-reader, valid 2h)"
