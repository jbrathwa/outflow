#!/usr/bin/env bash
# Delete the outflow-dev kind cluster and its restricted kubeconfig.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
kind delete cluster --name outflow-dev
rm -f "$here/.kubeconfig"
