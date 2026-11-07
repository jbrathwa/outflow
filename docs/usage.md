# outflow usage

outflow is read-only. It reads a snapshot of your cluster (built-in demo data or
a saved JSON file today; live collection for AKS is in progress), analyses it,
and shows the result in a TUI or prints it.

## Commands

| Command | What it does |
|---|---|
| `outflow` | Interactive view |
| `outflow scan` | Print a report and exit by `--fail-on` |
| `outflow doctor` | Check data sources and report coverage |
| `outflow version` | Print the version |

Running a command without `--demo` or `--snapshot` currently fails with
`live collection is not available yet; use --demo or --snapshot <file>`.

## Flags

Common flags (all commands):

| Flag | Meaning |
|---|---|
| `--demo` | Use the built-in demo snapshot (fictional cluster `aks-prod-eus`) |
| `--demo-rule` | Apply the built-in demo residency rule, "allow US" with one exception for `api.stripe.com` |
| `--snapshot <file>` | Read a snapshot JSON file. Cannot be combined with `--demo` |
| `--dump-demo` | Write the demo snapshot JSON to stdout and exit. Use it to see the snapshot format or to make a file for `--snapshot` |
| `--config <file>` | Residency config. Default: `./outflow.yaml` if it exists |
| `--context`, `--profile`, `--since`, `--workspace` | Accepted, but only used by live collection, which is not available yet |

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
| `1` | Tool, setup or config error (bad flag, bad `outflow.yaml`, unreadable snapshot, live collection not available) |
| `2` | The residency rule is broken according to `--fail-on` |

Cases that exit 1 on purpose, so a clean result never hides missing data:

- `--fail-on` without a residency rule.
- `--fail-on` with coverage below `FULL`, unless `--allow-partial` is given.
- `--fail-on` with coverage `NONE` (no traffic sources at all), always.
- `--fail-on unknown` with `unknown_location: ignore` in the config, which
  contradict each other.
- `doctor` when a required source is missing (see [Doctor](#doctor)).

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
| `sources.aks_cluster` | ARM resource ID of the AKS cluster, `/subscriptions/<guid>/resourceGroups/<name>/providers/Microsoft.ContainerService/managedClusters/<name>`. Used for ARM checks |

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

Evidence levels:

| Evidence | Meaning |
|---|---|
| `confirmed` | The workload looked the domain up, matching firewall traffic left from its node, and no other workload on that node looked up the same domain in the window |
| `likely` | As `confirmed`, but another workload on the same node looked up the domain too, so the flow cannot be tied to one of them. Each candidate gets `likely`, and the detail pane names the others |
| `DNS only` | A lookup with no matching firewall traffic |
| `node-level` | Firewall traffic tied only to a node: no matching lookup (for example a hard-coded IP), or a lookup from the node IP by a `hostNetwork` pod. Shown as a `(node) <name>` row, never assigned to a workload. A `hostNetwork` lookup with no matching firewall traffic is `DNS only` on that row |

How a location is known: `cloud range` (published Azure, AWS or GCP ranges with
a region; high confidence), `GeoIP` (country database; medium), `anycast/CDN`
(location `global`, so unknown for the rule), or nothing matched (unknown). The
detail pane also says whether the IP was resolved by outflow or came from a
firewall log.

## Snapshots and offline use

```sh
outflow --dump-demo > snapshot.json
outflow scan --snapshot snapshot.json --config outflow.yaml --format md
outflow --snapshot snapshot.json --config outflow.yaml
```

The snapshot is plain JSON. Location is computed when the snapshot is analysed,
so newer location data applies to old snapshots.

## CI example

A scheduled job that fails when a saved snapshot breaks the rule. Live mode comes
later; until then the job analyses a snapshot file you provide.

```yaml
# .github/workflows/residency.yml
name: residency
on:
  schedule:
    - cron: "0 6 * * *"
jobs:
  outflow:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
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
          outflow scan --snapshot snapshot.json --config outflow.yaml \
            --format md --fail-on outside > report.md
          code=$?
          cat report.md >> "$GITHUB_STEP_SUMMARY"
          exit $code
```

Exit 2 means the rule is broken; exit 1 means the job could not judge (bad
config, missing sources, partial coverage). Treat both as failures.

To pin a version instead of `latest`, use
`https://github.com/jbrathwa/outflow/releases/download/v0.1.0` as `base`.

## Doctor

`outflow doctor` prints a checklist grouped as Cluster, Traffic, Location,
Residency and Access. Each line is `✔` (ok), `!` (degrades coverage), `✖`
(missing) or `·` (informational). Missing items include why it matters, what you
lose without it, and a fix. It ends with the coverage verdict and what outflow can
and cannot answer. The fix steps are in [the setup guide](setup.md).

`doctor` exits 1 when a required source is missing. Required are:

- the Kubernetes API;
- at least one traffic source (firewall logs or CoreDNS query logs);
- on Azure CNI Overlay or kubenet, where pods leave with node IPs: the CoreDNS
  query logs (log plugin and log collection) and no NodeLocal DNS Cache.

Anything else only lowers coverage. Partial coverage alone does not fail
`doctor`; `scan --fail-on` fails on it unless you pass `--allow-partial`. See [the demo walkthrough](demo.md#doctor-on-a-degraded-cluster)
for real output.

## Location data

The location data embedded in this build is a small seed subset of published
cloud, CDN and GeoIP ranges. `doctor` reports its version.

IP to Country Lite data by DB-IP (https://db-ip.com), licensed under CC BY 4.0.
