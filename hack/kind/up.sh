#!/usr/bin/env bash
# Create the outflow-dev kind cluster, sample workloads and a restricted read-only identity.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
name=outflow-dev
ctx="kind-$name"

# Capture first: under pipefail, `kind get clusters | grep -q` can fail when grep exits early.
clusters="$(kind get clusters 2>/dev/null)"
if ! grep -qx "$name" <<<"$clusters"; then
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
# Built from scratch, so it holds no admin user or client certificate: only the server,
# its CA and the outflow-reader token. The admin kubeconfig is read from a temp file.
token="$(kubectl --context "$ctx" -n default create token outflow-reader --duration 2h)"
admin="$(mktemp)"
trap 'rm -f "$admin"' EXIT
kind get kubeconfig --name "$name" >"$admin"
server="$(KUBECONFIG="$admin" kubectl config view --raw -o jsonpath='{.clusters[0].cluster.server}')"
ca="$(KUBECONFIG="$admin" kubectl config view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
kc="$here/.kubeconfig"
rm -f "$kc"
(umask 077 && : >"$kc")
KUBECONFIG="$kc" kubectl config set-cluster "$ctx" --server "$server" >/dev/null
KUBECONFIG="$kc" kubectl config set "clusters.$ctx.certificate-authority-data" "$ca" >/dev/null
KUBECONFIG="$kc" kubectl config set-credentials outflow-reader --token "$token" >/dev/null
KUBECONFIG="$kc" kubectl config set-context "$ctx" --cluster "$ctx" --user outflow-reader >/dev/null
KUBECONFIG="$kc" kubectl config use-context "$ctx" >/dev/null
echo "wrote $kc (restricted identity outflow-reader, valid 2h)"
