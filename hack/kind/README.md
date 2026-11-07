# kind dev cluster

A local cluster with sample workloads and a read-only identity, for trying outflow live and
for the opt-in end-to-end test. Needs Docker, `kind` and `kubectl` (1.24 or newer).

```sh
hack/kind/up.sh && OUTFLOW_KIND_KUBECONFIG=hack/kind/.kubeconfig go test -tags kind ./internal/cli/ -run Kind -v && hack/kind/down.sh
```

`up.sh` creates the cluster `outflow-dev` (1 control-plane, 2 workers), applies
`workloads.yaml` and `deploy/clusterrole.yaml`, and writes `hack/kind/.kubeconfig` with a
2-hour token for the ServiceAccount `default/outflow-reader`, bound only to `outflow-reader`.
The test never uses the admin context.

The test runs the real CLI against kind plus an in-process fake Log Analytics server, with a
fake DNS resolver. kind has no AKS resource, so the outbound type is unknown and coverage is
PARTIAL; that is expected.
