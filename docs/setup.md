# Setting up outflow's data sources

`outflow doctor` lists what outflow needs; this guide covers how to turn each piece on.

## CoreDNS

On AKS, CoreDNS query logging is off by default. Without it outflow can see what left
through the firewall per node, but not which workload made each call. Enable it with the
`coredns-custom` ConfigMap in `kube-system`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  log.override: |
    log
```

Apply it and restart CoreDNS:

```sh
kubectl apply -f coredns-custom.yaml
kubectl -n kube-system rollout restart deployment coredns
```

Container Insights must also collect `kube-system` container logs, so the query lines
reach `ContainerLogV2` where outflow reads them (see the next section).

## Container Insights

Enable the Container Insights add-on on the cluster (on AKS this is the `omsagent`
monitoring add-on) and send it to a Log Analytics workspace. outflow reads two tables:

| Table | What outflow uses it for |
|---|---|
| `ContainerLogV2` | CoreDNS query lines from the `kube-system` namespace: which pod asked for which domain |
| `KubePodInventory` | Pod name, namespace, pod IP and node, to map IPs in the logs back to workloads |

Container log collection must include `kube-system` (it is excluded in some
Container Insights data-collection settings), and the `ContainerLogV2` schema must be on.
outflow filters both tables to this cluster's node names and aggregates in the query, so
only counts leave the workspace, not raw log lines.

On AKS outflow finds the workspace from the add-on configuration, which needs Reader on the AKS resource (see [Roles](#roles)). Otherwise pass
`--workspace <guid>` or set `sources:` in `outflow.yaml` (see the [usage guide](usage.md#residency-rule-outflowyaml)).

## Azure Firewall

outflow reads firewall logs to see what actually left the cluster. In the firewall's
diagnostic settings, send the application rule and network rule logs to a Log Analytics
workspace and choose the **resource-specific** destination tables:

| Table | Log category |
|---|---|
| `AZFWApplicationRule` | Azure Firewall Application Rule |
| `AZFWNetworkRule` | Azure Firewall Network Rule |

The older shared `AzureDiagnostics` table is read on a best-effort basis; resource-specific
tables are more reliable.

The firewall often logs to a different workspace than the cluster (for example a hub
workspace such as `law-hub-eus`). outflow never guesses the firewall workspace: set
`sources.firewall_workspace` in `outflow.yaml`, or pass `--workspace` if one workspace holds
everything. Without firewall logs, coverage drops to PARTIAL or NONE depending on CoreDNS logs.

## Roles

outflow only reads. It needs two kinds of access.

**Kubernetes.** Apply the read-only ClusterRole and bind it to your identity or group:

```sh
kubectl apply -f deploy/clusterrole.yaml
kubectl create clusterrolebinding outflow-readers \
  --clusterrole=outflow-reader --group=outflow-readers
```

What the role grants, and why, is in [Permissions](usage.md#permissions).

**Azure.** Grant **Log Analytics Reader** on each workspace outflow queries (the DNS and
pod workspace, and the firewall workspace if it is a different one):

```sh
az role assignment create \
  --role "Log Analytics Reader" \
  --scope <workspace resource id> \
  --assignee <user, group or service principal id>
```

Also grant **Reader** on the AKS cluster resource so `outflow doctor` can check the
outbound type, network plugin and add-ons. Without it those checks show as not checked and
coverage is lower. Nothing broader is needed.

**Location data refresh.** Before a live scan outflow refreshes stale or missing public
location data. This needs no Azure or Kubernetes access, only outbound HTTPS GETs to
`www.microsoft.com`, `download.microsoft.com`, `ip-ranges.amazonaws.com`,
`www.gstatic.com`, `www.cloudflare.com`, `api.fastly.com` and `download.db-ip.com`.
It sends no cluster data, IPs or credentials. If a feature ever uses Azure access for
location data, it is listed here. In a locked-down network, allow these hosts or pass
`--no-refresh`.

## Signing in

Kubernetes uses your kubeconfig (`--context` picks a context). Azure uses the default
Azure credential chain:

- Locally, run `az login`.
- In CI, use workload identity federation or a service principal. The chain reads the
  standard environment variables (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and either
  `AZURE_FEDERATED_TOKEN_FILE` or `AZURE_CLIENT_SECRET`). See the CI example in the
  [usage guide](usage.md#ci-example).

## Checking it works

```sh
outflow doctor
outflow scan --format md
```

`doctor` lists each source and what is missing. If a result looks wrong, add `--debug`:
outflow prints each query's name, the workspace (last four characters only), how long it
took, the row count and the column names to stderr. It never prints the data.

## Developing

To try live collection against a local cluster, see [hack/kind/README.md](../hack/kind/README.md).
