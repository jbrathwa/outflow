# outflow usage

outflow is read-only. It reads your cluster live (the Kubernetes API and, on AKS,
Log Analytics), or a saved snapshot or the built-in demo data, analyses it, and
shows the result in a TUI or prints it.

## Commands

| Command | What it does |
|---|---|
| `outflow` | Interactive view |
| `outflow scan` | Print a report and exit by `--fail-on` |
| `outflow doctor` | Check data sources and report coverage |
| `outflow snapshot -o <file>` | Collect live data and save it as a snapshot JSON file (`-` for stdout). The file is written with mode 0600 |
| `outflow update-data` | Download fresh location data now (public files only) |
| `outflow version` | Print the version |

Without `--demo` or `--snapshot`, a command reads the cluster in the current
kubeconfig context. Live collection is read-only. The only files outflow writes are the
one named by `snapshot -o` and the location data cache (see
[Location data](#location-data)).

## Flags

Common flags (all commands):

| Flag | Meaning |
|---|---|
| `--demo` | Use the built-in demo snapshot (fictional cluster `aks-prod-eus`) |
| `--demo-rule` | Apply the built-in demo residency rule, "allow US" with one exception for `api.stripe.com` |
| `--snapshot <file>` | Read a snapshot JSON file. Cannot be combined with `--demo` |
| `--dump-demo` | Write the demo snapshot JSON to stdout and exit. Use it to see the snapshot format or to make a file for `--snapshot` |
| `--config <file>` | Residency config. Default: `./outflow.yaml` if it exists |
| `--context <name>` | Kubeconfig context. Default: the current context |
| `--profile auto\|azure\|generic` | Which log sources to read. `auto` (default) uses `azure` on AKS and `generic` elsewhere |
| `--since <duration>` | How far back to read, `5m` to `30d`. Default `24h`; for example `90m`, `7d` |
| `--workspace <guid>` | Log Analytics workspace for DNS, pod and firewall logs. Overrides `sources` in `outflow.yaml` |
| `--join-window <duration>` | How long after a DNS lookup a firewall event still counts, `1m` to `60m`. Default `5m` |
| `--no-refresh` | Don't download location data before a live scan. See [Location data](#location-data) |
| `--debug` | Print query diagnostics to stderr: query name, workspace (last four characters), duration, row count and column names. Never log data |

`scan` flags:

| Flag | Meaning |
|---|---|
| `--format json\|md` | Output format. `json` is the default; `md` is a short Markdown report for CI job summaries |
| `--json` | Same as `--format json`. Combining it with `--format md` exits 1 |
| `--fail-on outside\|unknown` | Exit 2 when the residency rule is broken. `outside` fails on destinations outside the allowed locations; `unknown` also fails on destinations whose location is unknown. Needs a residency rule |
| `--allow-partial` | Let `--fail-on` run when coverage is `PARTIAL` |

Without `--fail-on`, `scan` prints the report and exits 0.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean: the report was produced and, with `--fail-on`, the rule is not broken |
| `1` | Tool, setup or config error (bad flag, bad `outflow.yaml`, unreadable snapshot, cluster or sign-in failure) |
| `2` | The residency rule is broken according to `--fail-on` |

Cases that exit 1 on purpose, so a clean result never hides missing data:

- `--fail-on` without a residency rule.
- `--fail-on` with coverage below `FULL`, unless `--allow-partial` is given.
- `--fail-on` with coverage `NONE` (no traffic sources at all), always.
- `--fail-on unknown` with `unknown_location: ignore` in the config, which
  contradict each other.
- `doctor` when a required source is missing (see [Doctor](#doctor)).

## Permissions

outflow is read-only: get, list and watch, except `get` only on two ConfigMaps. For Kubernetes, apply [`deploy/clusterrole.yaml`](../deploy/clusterrole.yaml):

| Resources | Why |
|---|---|
| pods, nodes, namespaces | Which workloads run where, node IPs and pod CIDRs, namespace labels for NetworkPolicy selectors |
| replicasets, deployments, statefulsets, daemonsets, jobs, cronjobs | Resolve each pod to its owning workload |
| networkpolicies | The egress rules outflow analyses |
| Calico `networkpolicies`, `globalnetworkpolicies` (optional) | Detect Calico policies outflow cannot evaluate |
| configmaps named `coredns-custom` and `coredns` (optional) | Check whether DNS query logging is on. The ClusterRole restricts ConfigMaps by name only, so it grants `get` on ConfigMaps with those names in any namespace. For a tighter grant, replace that rule with a Role in `kube-system` |

For Azure: **Log Analytics Reader** on each workspace and **Reader** on the AKS resource.
Commands to grant them are in [the setup guide](setup.md#roles).

**Location data refresh.** Before a live scan outflow refreshes stale or missing public
location data. This needs no Azure or Kubernetes access, only outbound HTTPS GETs to
`www.microsoft.com`, `download.microsoft.com`, `ip-ranges.amazonaws.com`,
`www.gstatic.com`, `www.cloudflare.com`, `api.fastly.com` and `download.db-ip.com`.
It sends no cluster data, IPs or credentials. If a feature ever uses Azure access for
location data, it is listed here.

## Residency rule: `outflow.yaml`

The rule is optional. With no `residency` section outflow runs in neutral mode:
no pass/fail colours, a locations column instead of a status, and `--fail-on`
is refused.

```yaml
residency:
  allow:
    countries: [US]                      # ISO 3166-1 alpha-2, uppercase
    cloud_regions: [azure:eastus, aws:us-west-2]
  exceptions:                            # reviewed, with a reason
    - domain: api.stripe.com             # exact, or *.suffix for subdomains
      reason: "Payment processor, DPA signed"
    - cidr: 203.0.113.0/24
      workload: payments/payment-api     # optional: limit to one workload
      reason: "Partner network"
  unknown_location: warn                 # warn | fail | ignore

sources:                                 # used by live collection; all optional
  dns_workspace: 00000000-0000-0000-0000-00000000000a
  pod_inventory_workspace: 00000000-0000-0000-0000-00000000000b
  firewall_workspace: 00000000-0000-0000-0000-00000000000c
  aks_cluster: /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-eus/providers/Microsoft.ContainerService/managedClusters/aks-prod-eus
```

Reference:

| Field | Rules |
|---|---|
| `residency.allow.countries` | Two uppercase letters, e.g. `US`. A cloud region counts as its country, so `US` also allows `azure:centralus` |
| `residency.allow.cloud_regions` | `provider:region` with provider `azure`, `aws` or `gcp`, e.g. `azure:eastus`. Use it to allow a region without allowing a whole country |
| `residency.allow` | At least one country or cloud region is required. A destination is inside if it matches either list |
| `residency.exceptions[]` | Exactly one of `domain` or `cidr`, plus a non-empty `reason`. `workload` (`namespace/name`) is optional and limits the exception to that workload |
| exception `domain` | Matched case-insensitively and ignoring a trailing dot. `*.stripe.com` matches `api.stripe.com` but not `stripe.com` or `evilstripe.com` |
| `residency.unknown_location` | `warn` (default): unknown destinations are shown and counted as unknown. `fail`: they also fail `--fail-on outside`. `ignore`: they are not counted at all |
| `sources.dns_workspace`, `sources.pod_inventory_workspace`, `sources.firewall_workspace` | Log Analytics workspace IDs (GUIDs) read by live collection. `--workspace <id>` sets all three at once |
| `sources.aks_cluster` | ARM resource ID of the AKS cluster, `/subscriptions/<guid>/resourceGroups/<name>/providers/Microsoft.ContainerService/managedClusters/<name>`. Used for ARM checks. When unset, outflow derives it from the default node resource group name (`MC_<group>_<cluster>_<region>`), which needs only Reader on the cluster; otherwise it lists the subscription's clusters, which needs Reader on the subscription |

A config with only `sources` and no `residency` section still runs in neutral mode.

Config problems exit 1 with a message naming the field, for example
`residency.allow.countries[0]: "us" must be two uppercase letters`. Unknown YAML
keys are rejected.

A destination is `inside (exception)` when an exception matches it, even if its
location is outside. Exceptions win over everything else, and the reason is
shown in the TUI.

## How destinations are counted

For a destination whose location is not allowed:

- Firewall **allowed** traffic (evidence `confirmed`, `likely` or `node-level`)
  counts as `outside`.
- Firewall **denied** traffic is shown as `outside (denied)` but does not count
  toward the workload status, the header counts or `--fail-on`. Nothing left.
- `DNS only` (a lookup with no matching firewall traffic) counts as `unknown`,
  not `outside`, because nothing shows the traffic actually left.
- Global, anycast and CDN destinations have no single location, so they count
  as `unknown`.
- A workload's status is its worst destination: outside, then unknown, then
  inside.
- Only nodes in the cluster now are counted. Pod inventory, DNS and firewall
  logs are filtered to the current nodes, so traffic from a node that was
  removed during the window (scale-down, upgrade) is not included. When the
  AKS resource is known, the pod inventory status says how many nodes left.

Evidence levels:

| Evidence | Meaning |
|---|---|
| `confirmed` | The workload looked the domain up, matching firewall traffic left from its node within the join window, and no other workload on that node looked up the same domain in the window |
| `likely` | As `confirmed`, but another workload on the same node looked up the domain too, so the flow cannot be tied to one of them. Each candidate gets `likely`, and the detail pane names the others |
| `DNS only` | A lookup with no matching firewall traffic |
| `node-level` | Firewall traffic tied only to a node: no matching lookup (for example a hard-coded IP), or a lookup from the node IP by a `hostNetwork` pod. Shown as a `(node) <name>` row, never assigned to a workload. A `hostNetwork` lookup with no matching firewall traffic is `DNS only` on that row |

### The join window

Firewall logs show node IPs, not pods, so outflow ties a firewall event to a lookup when
the event follows the lookup on the same node within the join window (default 5 minutes).
Raise `--join-window` (up to `60m`) if clients cache DNS answers for longer, for example
long-lived connections or high TTLs; more `node-level` rows than expected is the sign.
A wider window makes `likely` more common, because more workloads on a node can
match. Lower it (down to `1m`) on busy nodes to reduce ambiguity.

### Domains with several locations

A domain can resolve to several IPs. If those locations rate differently under the rule, the destination is `unknown` with the note "resolves to several
locations", not guessed either way.

How a location is known: `cloud range` (published Azure, AWS or GCP ranges with
a region; high confidence), `GeoIP` (country database; medium), `anycast/CDN`
(location `global`, so unknown for the rule), or nothing matched (unknown). The
detail pane also says whether the IP was resolved by outflow or came from a
firewall log.

## Snapshots and offline use

```sh
outflow snapshot -o snapshot.json --since 24h
outflow --dump-demo > snapshot.json
outflow scan --snapshot snapshot.json --config outflow.yaml --format md
outflow --snapshot snapshot.json --config outflow.yaml
```

The snapshot is plain JSON. Location is computed when the snapshot is analysed,
so the location data in your cache at that moment applies to old snapshots. Reading a
snapshot never downloads anything.

## CI example

A scheduled job that reads the cluster live and fails when the rule is broken. It signs
in to Azure with OIDC (workload identity federation, no stored secret), gets a kubeconfig,
and runs `outflow scan`. The identity needs the access in [Permissions](#permissions), plus permission to fetch cluster credentials for `aks-set-context` (for example the Azure Kubernetes Service Cluster User Role).

```yaml
# .github/workflows/residency.yml
name: residency
on:
  schedule:
    - cron: "0 6 * * *"
permissions:
  id-token: write
  contents: read
jobs:
  outflow:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: azure/login@v2
        with:
          client-id: ${{ secrets.AZURE_CLIENT_ID }}
          tenant-id: ${{ secrets.AZURE_TENANT_ID }}
          subscription-id: ${{ secrets.AZURE_SUBSCRIPTION_ID }}
      - uses: azure/aks-set-context@v4
        with:
          resource-group: rg-aks-prod
          cluster-name: aks-prod-eus
      - name: Install outflow
        run: |
          base=https://github.com/jbrathwa/outflow/releases/latest/download
          cd "$RUNNER_TEMP"
          curl -sSLO "$base/outflow_Linux_x86_64.tar.gz"
          curl -sSLO "$base/checksums.txt"
          sha256sum --ignore-missing -c checksums.txt
          tar xzf outflow_Linux_x86_64.tar.gz outflow
          echo "$RUNNER_TEMP" >> "$GITHUB_PATH"
      - name: Check data residency
        run: |
          set +e
          outflow scan --config outflow.yaml --workspace <workspace-guid> \
            --format md --fail-on outside > report.md
          code=$?
          cat report.md >> "$GITHUB_STEP_SUMMARY"
          exit $code
```

Drop `--workspace` if `outflow.yaml` has `sources`. If the firewall logs to another
workspace, set `sources.firewall_workspace` instead of using `--workspace`.

Exit 2 means the rule is broken; exit 1 means the job could not judge (bad
config, sign-in failure, missing sources, partial coverage). Treat both as failures.

To pin a version instead of `latest`, use
`https://github.com/jbrathwa/outflow/releases/download/v0.1.0` as `base`.

### Offline alternative

If the job cannot reach the cluster, analyse a snapshot file you collected elsewhere
with `outflow snapshot -o`:

```sh
outflow scan --snapshot snapshot.json --config outflow.yaml \
  --format md --fail-on outside > report.md
```

## Doctor

`outflow doctor` prints a checklist grouped as Cluster, Traffic, Location,
Residency and Access. The Location group has one line per location data source (see
[Location data](#location-data)). Each line is `✔` (ok), `!` (degrades coverage, or a
location source needs refreshing, which leaves coverage unchanged), `✖`
(missing) or `·` (informational). Missing items include why it matters, what you
lose without it, and a fix. It ends with the coverage verdict and what outflow can
and cannot answer. The fix steps are in [the setup guide](setup.md).

`doctor` exits 1 when a required source is missing. Required are:

- the Kubernetes API;
- at least one traffic source (firewall logs or CoreDNS query logs);
- on Azure CNI Overlay or kubenet, where pods leave with node IPs: the CoreDNS
  query logs (log plugin and log collection) and no node-local DNS cache
  (NodeLocal DNSCache or AKS LocalDNS).

Anything else only lowers coverage. Partial coverage alone does not fail
`doctor`; `scan --fail-on` fails on it unless you pass `--allow-partial`. See [the demo walkthrough](demo.md#doctor-on-a-degraded-cluster)
for real output.

## Location data

outflow maps IP addresses to cloud regions, CDNs and countries with public lists: Azure
service tags, AWS and GCP IP ranges, Cloudflare and Fastly ranges, and the DB-IP country
database. A small seed subset is built in, enough for `--demo` and for offline use.

Before a live `scan` or interactive run, outflow refreshes any list older than 7 days
(30 days for the country database). It prints one line to stderr before downloading, for
example `outflow: location data: downloading azure, aws, gcp, cdn (up to 60s; --no-refresh to skip)`,
and one after, for example
`outflow: location data: refreshed azure, aws, gcp, cdn (geo up to date)`. Nothing is
printed when every list is fresh. Refreshing takes at most 60 seconds in total. Pass `--no-refresh` to skip it, or run `outflow update-data`
to download everything now, whatever its age. `update-data` prints one line per source and
exits 1 if any failed.

If a download fails, outflow prints a warning such as
`outflow: warning: could not refresh Azure service tags (<error>); using data from 2026-10-30 (9 days old)`
and carries on with the newest data it has, or the built-in sample
data if it has none. A failed refresh never fails the scan.

Downloaded data is kept in a cache folder: `~/Library/Caches/outflow` on macOS and
`~/.cache/outflow` on Linux. Set `OUTFLOW_CACHE_DIR` to use another folder. `--demo` and
`--demo-degraded` use only the built-in sample, so demo output stays the same.
`--snapshot`, `doctor` and `snapshot -o` never download; `--snapshot` reads whatever is in
the cache. The download hosts are
listed under [Permissions](#permissions).

`outflow doctor` shows one row per source in its Location group, with the data's date and
age, for example `20261102 · 6 days old`. A row warns when the source is older than its
limit or when only the built-in sample is available. This does not change the coverage
verdict. The interactive view shows the same warnings in one line under its header, for
example `location data: azure 9 days old · geo sample`, and nothing when all data is fresh.

IP to Country Lite data by DB-IP (https://db-ip.com), licensed under CC BY 4.0.
