// Package collect gathers a model.Snapshot from a cluster and its platform's log sources.
// This package defines the interfaces; implementations live in subpackages.
package collect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
)

// Scope limits platform queries to this cluster and window.
type Scope struct {
	Window    model.Window
	NodeNames []string
	NodeIPs   []string
}

// ClusterState is what the cluster source read from the Kubernetes API.
type ClusterState struct {
	Cluster         model.Cluster
	Workloads       []model.Workload
	Pods            []model.Pod // live pods; End is zero
	Nodes           []model.Node
	NetworkPolicies []model.NetworkPolicy
	ProviderIDs     []string // node spec.providerID values, for platform discovery
	// NodeResourceGroup is the kubernetes.azure.com/cluster node label (the AKS node
	// resource group) of the first labelled node by name; empty when no node has it.
	NodeResourceGroup string
	Sources           []model.SourceStatus
}

// ClusterSource reads the live cluster.
type ClusterSource interface {
	Read(ctx context.Context) (ClusterState, error)  // error only when the API is unusable
	Probe(ctx context.Context) (ClusterState, error) // cluster info + access + plugin checks; no workloads needed
}

// ClusterEnricher adds platform facts (region, outbound type, ...) to the cluster state.
type ClusterEnricher interface {
	Enrich(ctx context.Context, st *ClusterState) []model.SourceStatus
}

// PodHistorySource returns pods that ran during the window.
type PodHistorySource interface {
	PodHistory(ctx context.Context, sc Scope) ([]model.Pod, model.SourceStatus)
	Probe(ctx context.Context, sc Scope) model.SourceStatus
}

// DNSLogSource returns per-minute DNS lookup counts.
type DNSLogSource interface {
	Lookups(ctx context.Context, sc Scope) ([]model.DNSLookup, []model.SourceStatus) // collected + format
	Probe(ctx context.Context, sc Scope) []model.SourceStatus
}

// EgressLogSource returns firewall events for the cluster's node IPs.
type EgressLogSource interface {
	Events(ctx context.Context, sc Scope) (events []model.EgressEvent, firewall string, st model.SourceStatus)
	Probe(ctx context.Context, sc Scope) (firewall string, st model.SourceStatus)
}

// Resolver resolves domains to IPs.
type Resolver interface {
	Resolve(ctx context.Context, domains []string) []model.Resolution
}

// Platform is the set of implementations for a profile. Nil members are "not available in this profile".
type Platform struct {
	Name     string // "azure" | "generic"
	Enricher ClusterEnricher
	Pods     PodHistorySource
	DNS      DNSLogSource
	Egress   EgressLogSource
	Final    func() []model.SourceStatus // optional, e.g. azure-access summary after all queries
}

// Collector assembles a snapshot.
type Collector struct {
	Cluster  ClusterSource
	Platform func(c model.Cluster, providerIDs []string) (Platform, error)
	Resolver Resolver
	Now      func() time.Time // nil = time.Now
}

// Options configures one collection.
type Options struct{ Since time.Duration }

// Collect reads the cluster and every platform source and returns the snapshot.
func (c Collector) Collect(ctx context.Context, opt Options) (model.Snapshot, error) {
	return c.run(ctx, opt, false)
}

// Probe is Collect with the lightweight Probe methods and no DNS resolution.
func (c Collector) Probe(ctx context.Context, opt Options) (model.Snapshot, error) {
	return c.run(ctx, opt, true)
}

func (c Collector) run(ctx context.Context, opt Options, probe bool) (model.Snapshot, error) {
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now()
	}
	since := opt.Since
	if since == 0 {
		since = 24 * time.Hour
	}

	var st ClusterState
	var err error
	if probe {
		st, err = c.Cluster.Probe(ctx)
	} else {
		st, err = c.Cluster.Read(ctx)
	}
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("read cluster: %w", err)
	}

	plat := Platform{Name: "generic"}
	if c.Platform != nil {
		plat, err = c.Platform(st.Cluster, st.ProviderIDs)
		if err != nil {
			return model.Snapshot{}, fmt.Errorf("select platform: %w", err)
		}
	}

	statuses := append([]model.SourceStatus{}, st.Sources...)
	if plat.Enricher != nil {
		statuses = append(statuses, plat.Enricher.Enrich(ctx, &st)...)
	}

	sc := Scope{Window: Window(now, since)}
	for _, n := range st.Nodes {
		sc.NodeNames = append(sc.NodeNames, n.Name)
		sc.NodeIPs = append(sc.NodeIPs, n.IPs...)
	}

	missing := func(id string) model.SourceStatus {
		return model.SourceStatus{ID: id, State: "missing", Detail: "not available in profile " + plat.Name}
	}

	var history []model.Pod
	if plat.Pods == nil {
		statuses = append(statuses, missing(model.SrcPodInventory))
	} else if probe {
		statuses = append(statuses, plat.Pods.Probe(ctx, sc))
	} else {
		var s model.SourceStatus
		history, s = plat.Pods.PodHistory(ctx, sc)
		statuses = append(statuses, s)
	}

	var lookups []model.DNSLookup
	if plat.DNS == nil {
		statuses = append(statuses, missing(model.SrcCoreDNSCollected))
	} else if probe {
		statuses = append(statuses, plat.DNS.Probe(ctx, sc)...)
	} else {
		var ss []model.SourceStatus
		lookups, ss = plat.DNS.Lookups(ctx, sc)
		statuses = append(statuses, ss...)
	}

	var events []model.EgressEvent
	firewall := ""
	if plat.Egress == nil {
		statuses = append(statuses, missing(model.SrcFirewall))
	} else if probe {
		var s model.SourceStatus
		firewall, s = plat.Egress.Probe(ctx, sc)
		statuses = append(statuses, s)
	} else {
		var s model.SourceStatus
		events, firewall, s = plat.Egress.Events(ctx, sc)
		statuses = append(statuses, s)
	}
	if firewall != "" {
		st.Cluster.Firewall = firewall
	}

	var resolutions []model.Resolution
	if !probe && c.Resolver != nil {
		if domains := distinctDomains(lookups, events); len(domains) > 0 {
			resolutions = c.Resolver.Resolve(ctx, domains)
		}
	}

	if plat.Final != nil {
		statuses = append(statuses, plat.Final()...)
	}

	return model.Snapshot{
		SchemaVersion:   model.SchemaVersion,
		Cluster:         st.Cluster,
		Window:          sc.Window,
		Workloads:       st.Workloads,
		Pods:            mergePods(st.Pods, history),
		Nodes:           st.Nodes,
		NetworkPolicies: st.NetworkPolicies,
		DNSLookups:      lookups,
		EgressEvents:    events,
		Resolutions:     resolutions,
		Sources:         statuses,
		Profile:         plat.Name,
	}, nil
}

// mergePods keeps all live pods and appends history pods whose (namespace, name, ip) is not live.
func mergePods(live, history []model.Pod) []model.Pod {
	type key struct{ ns, name, ip string }
	seen := make(map[key]bool, len(live))
	out := append([]model.Pod{}, live...)
	for _, p := range live {
		seen[key{p.Namespace, p.Name, p.IP}] = true
	}
	for _, p := range history {
		k := key{p.Namespace, p.Name, p.IP}
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// distinctDomains returns the sorted, normalized, de-duplicated domains that contain a dot.
func distinctDomains(lookups []model.DNSLookup, events []model.EgressEvent) []string {
	set := map[string]bool{}
	add := func(d string) {
		d = model.NormalizeDomain(d)
		if d != "" && strings.Contains(d, ".") {
			set[d] = true
		}
	}
	for _, l := range lookups {
		add(l.Domain)
	}
	for _, e := range events {
		add(e.Domain)
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
