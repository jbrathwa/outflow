package analyze

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/netpol"
	"github.com/jbrathwa/outflow/internal/config"
)

// ReportSchemaVersion is the report JSON schema version written by this build.
const ReportSchemaVersion = 1

// Destination is one observation with its location and residency status.
type Destination struct {
	Observation
	Location  location.Location `json:"location"`
	Status    Status            `json:"status"`
	Exception string            `json:"exception"` // reason of the matching exception
	SaaS      bool              `json:"saas"`
}

// RowKind says what a report row stands for.
type RowKind string

const (
	KindWorkload   RowKind = "workload"    // a controller from the snapshot
	KindNode       RowKind = "node"        // "(node) <name>": traffic tied only to a node
	KindUnknownPod RowKind = "unknown pod" // "unknown pod (<ip>)": pod not in pod history
)

// WorkloadResult is one report row: a workload, a node, or an unknown pod.
type WorkloadResult struct {
	ID           string        `json:"id"`
	Kind         RowKind       `json:"kind"`
	NodeLevel    bool          `json:"nodeLevel"`
	HostNetwork  bool          `json:"hostNetwork"`
	Egress       netpol.Result `json:"egress"`
	Status       Status        `json:"status"`
	Locations    []string      `json:"locations"` // sorted countries, then "global", then "unknown"
	Destinations []Destination `json:"destinations"`
	Outside      int           `json:"outside"`
	Unknown      int           `json:"unknown"`
}

// Counts are per-destination totals for the report header.
type Counts struct {
	Workloads    int `json:"workloads"`
	Destinations int `json:"destinations"`
	Countries    int `json:"countries"`
	Outside      int `json:"outside"`
	Unknown      int `json:"unknown"`
	Inside       int `json:"inside"`
}

// Report is the analyzed snapshot consumed by the TUI, scan output and exit codes.
type Report struct {
	SchemaVersion  int              `json:"schemaVersion"`
	Cluster        model.Cluster    `json:"cluster"`
	Window         model.Window     `json:"window"`
	RuleConfigured bool             `json:"ruleConfigured"`
	Rule           string           `json:"rule"`
	LocationData   string           `json:"locationData"`
	Counts         Counts           `json:"counts"`
	Workloads      []WorkloadResult `json:"workloads"`
}

// Build attributes the snapshot's egress, locates and classifies every destination,
// and returns sorted rows: every workload, plus node-level and unknown-pod subjects.
func Build(s model.Snapshot, db *location.DB, cfg config.Config) Report {
	r := cfg.Residency
	rep := Report{
		SchemaVersion:  ReportSchemaVersion,
		Cluster:        s.Cluster,
		Window:         s.Window,
		RuleConfigured: r != nil,
		Rule:           ruleString(r),
		LocationData:   db.Version(),
		Workloads:      []WorkloadResult{},
	}

	private := netpol.DefaultPrivate(s.Cluster.PodCIDRs, s.Cluster.ServiceCIDRs)
	idx := map[string]int{}
	for _, w := range s.Workloads {
		if _, ok := idx[w.ID()]; ok {
			continue
		}
		idx[w.ID()] = len(rep.Workloads)
		rep.Workloads = append(rep.Workloads, WorkloadResult{
			ID: w.ID(), Kind: KindWorkload, HostNetwork: w.HostNetwork,
			Egress: netpol.Evaluate(w, s.NetworkPolicies, private),
		})
	}
	for _, o := range Attribute(s) {
		i, ok := idx[o.Subject]
		if !ok {
			i = len(rep.Workloads)
			idx[o.Subject] = i
			rep.Workloads = append(rep.Workloads, WorkloadResult{ID: o.Subject, Kind: subjectKind(o), NodeLevel: o.NodeLevel})
		}
		d := Destination{Observation: o, Location: locate(db, o.IP), SaaS: location.IsSaaS(o.Domain)}
		d.Status, d.Exception = Classify(d, r, o.Subject)
		rep.Workloads[i].Destinations = append(rep.Workloads[i].Destinations, d)
	}

	countries := map[string]bool{}
	for i := range rep.Workloads {
		w := &rep.Workloads[i]
		finishRow(w, r)
		rep.Counts.Destinations += len(w.Destinations)
		for _, d := range w.Destinations {
			if d.Location.Known() {
				countries[d.Location.Country] = true
			}
			if r == nil || !counts(d, r) {
				continue
			}
			switch d.Status {
			case Outside:
				rep.Counts.Outside++
			case Unknown:
				rep.Counts.Unknown++
			case Inside:
				rep.Counts.Inside++
			}
		}
	}
	rep.Counts.Workloads = len(rep.Workloads)
	rep.Counts.Countries = len(countries)
	sortRows(rep.Workloads, r != nil)
	return rep
}

// subjectKind classifies an observation subject that is not a snapshot workload.
// A pod whose workload is missing from the snapshot still counts as a workload.
func subjectKind(o Observation) RowKind {
	switch {
	case o.NodeLevel:
		return KindNode
	case isUnknownPod(o.Subject):
		return KindUnknownPod
	}
	return KindWorkload
}

// locate looks up a destination IP; an empty or unparsable IP has no location.
func locate(db *location.DB, ip string) location.Location {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return location.Location{HowKnown: location.None}
	}
	return db.Lookup(a)
}

// finishRow fills a row's locations, counted totals and worst-of status.
func finishRow(w *WorkloadResult, r *config.Residency) {
	if w.Destinations == nil {
		w.Destinations = []Destination{}
	}
	w.Locations = locations(w.Destinations)
	if r == nil {
		w.Status = Neutral
		return
	}
	if len(w.Destinations) == 0 {
		w.Status = NoTraffic
		return
	}
	w.Status = NoTraffic // stays so when nothing counts (all denied or ignored)
	for _, d := range w.Destinations {
		if !counts(d, r) {
			continue
		}
		switch d.Status {
		case Outside:
			w.Outside++
		case Unknown:
			w.Unknown++
		}
		if rank(d.Status) < rank(w.Status) {
			w.Status = d.Status
		}
	}
}

// locations lists sorted distinct countries, then "global", then "unknown".
func locations(ds []Destination) []string {
	seen := map[string]bool{}
	var out []string
	var global, unknown bool
	for _, d := range ds {
		switch l := d.Location; {
		case l.Known():
			if !seen[l.Country] {
				seen[l.Country] = true
				out = append(out, l.Country)
			}
		case l.Global:
			global = true
		default:
			unknown = true
		}
	}
	sort.Strings(out)
	if global {
		out = append(out, "global")
	}
	if unknown {
		out = append(out, "unknown")
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func hits(w WorkloadResult) int {
	n := 0
	for _, d := range w.Destinations {
		n += d.Hits
	}
	return n
}

// sortRows orders rows worst status first (node-level last within a status) when a rule
// is set, otherwise by destination count; ties fall back to hits, then ID.
func sortRows(ws []WorkloadResult, ruled bool) {
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		if ruled {
			if ra, rb := rank(a.Status), rank(b.Status); ra != rb {
				return ra < rb
			}
			if a.NodeLevel != b.NodeLevel {
				return !a.NodeLevel
			}
			if a.Outside != b.Outside {
				return a.Outside > b.Outside
			}
			if a.Unknown != b.Unknown {
				return a.Unknown > b.Unknown
			}
		} else if len(a.Destinations) != len(b.Destinations) {
			return len(a.Destinations) > len(b.Destinations)
		}
		if ha, hb := hits(a), hits(b); ha != hb {
			return ha > hb
		}
		return a.ID < b.ID
	})
}

// ruleString renders the allow list, e.g. "allow US, azure:eastus"; "" without a rule.
func ruleString(r *config.Residency) string {
	if r == nil {
		return ""
	}
	parts := append(append([]string{}, r.Allow.Countries...), r.Allow.CloudRegions...)
	return "allow " + strings.Join(parts, ", ")
}

// SortedDestinations returns a copy of ds in display order: worst status first when
// ruled (outside, unknown, outside (denied), inside), then hits descending, then
// domain or IP. The TUI and the Markdown report share this order.
func SortedDestinations(ds []Destination, ruled bool) []Destination {
	out := append([]Destination(nil), ds...)
	order := func(s Status) int {
		if !ruled {
			return 0
		}
		switch s {
		case Outside:
			return 0
		case Unknown:
			return 1
		case OutsideDenied:
			return 2
		case Inside:
			return 3
		}
		return 4
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := order(a.Status), order(b.Status); ra != rb {
			return ra < rb
		}
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}
		return a.Name() < b.Name()
	})
	return out
}

// Name is the destination's domain, or its IP when the domain is unknown.
func (d Destination) Name() string {
	if d.Domain != "" {
		return d.Domain
	}
	return d.IP
}
