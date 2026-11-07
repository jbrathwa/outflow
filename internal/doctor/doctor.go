// Package doctor checks which data sources outflow has, from a snapshot.
package doctor

import (
	"fmt"
	"strings"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/config"
)

// State is the outcome of one check.
type State string

const (
	OK   State = "ok"
	Warn State = "warn"
	Fail State = "fail"
	Info State = "info"
)

// Check is one line of the doctor report.
type Check struct {
	Name, Detail, Why, Without, Fix string
	State                           State
	Required                        bool
}

// Group is a titled list of checks.
type Group struct {
	Name   string
	Checks []Check
}

// Level is the overall coverage verdict.
type Level string

const (
	Full    Level = "FULL"
	Partial Level = "PARTIAL"
	None    Level = "NONE"
)

// Coverage says what outflow can and cannot tell with the available sources.
type Coverage struct {
	Level       Level
	Can, Cannot []string
}

// Result is the full doctor report.
type Result struct {
	Profile  string
	Context  string
	Groups   []Group
	Coverage Coverage
}

// Failed reports whether any required check failed.
func (r Result) Failed() bool {
	for _, g := range r.Groups {
		for _, c := range g.Checks {
			if c.Required && c.State == Fail {
				return true
			}
		}
	}
	return false
}

const (
	corednsFix = "add `log` to the coredns-custom ConfigMap (docs/setup.md#coredns)"
	dnsWithout = "destinations only per node, not per workload"
)

// Run evaluates the snapshot against outflow's data-source needs.
func Run(s model.Snapshot, db *location.DB, cfg config.Config, cfgFound bool) Result {
	c := s.Cluster
	src := func(id string) (model.SourceStatus, bool) {
		for _, x := range s.Sources {
			if x.ID == id {
				return x, true
			}
		}
		return model.SourceStatus{}, false
	}
	// fromSource builds a check from a snapshot source.
	fromSource := func(id, name, why, without, fix string) Check {
		ck := Check{Name: name, Without: without, Fix: fix}
		x, ok := src(id)
		switch {
		case !ok:
			ck.State, ck.Detail = Fail, "not checked"
		case x.State == "ok":
			ck.State, ck.Detail = OK, x.Detail
		case x.State == "warn":
			ck.State, ck.Detail = Warn, x.Detail
		default:
			ck.State, ck.Detail = Fail, x.Detail
		}
		if ck.State == OK {
			ck.Without, ck.Fix = "", ""
		} else {
			ck.Why = why
		}
		return ck
	}

	snat := c.CNI == "azure-cni-overlay" || c.CNI == "kubenet"

	api := fromSource("k8s-api", "Kubernetes API", "", "no workloads, policies or pods can be read",
		"check your kubeconfig context and that it has read access to pods, nodes and networkpolicies")
	api.Required = true

	platform := Check{Name: "Platform", State: OK, Detail: c.Platform}
	if c.Region != "" {
		platform.Detail += " · " + c.Region
	}
	if c.Platform == "" {
		platform = Check{Name: "Platform", State: Warn, Detail: "unknown", Without: "platform-specific checks are skipped"}
	}

	cni := Check{Name: "CNI", State: OK, Detail: cniName(c.CNI)}
	if c.CNI == "" {
		cni = Check{Name: "CNI", State: Warn, Detail: "unknown", Without: "cannot tell whether pod IPs are visible outside the node"}
	}
	if snat {
		cni.Why = "pods leave with node IPs → workload names come from DNS logs"
	}

	policy := Check{Name: "NetworkPolicy engine", State: OK, Detail: titleCase(c.PolicyEngine)}
	switch {
	case c.PolicyEngine == "":
		policy = Check{Name: "NetworkPolicy engine", State: Warn, Detail: "none detected", Why: "policies not enforced",
			Without: "NetworkPolicy rules are not enforced, so every workload may reach the internet",
			Fix:     "install a policy engine such as Calico or Cilium"}
	case c.CalicoCRDPolicies > 0:
		policy.State = Warn
		policy.Detail += fmt.Sprintf(" · %d Calico CRD policies", c.CalicoCRDPolicies)
		policy.Without = "Calico-specific policies are not analysed; results may overstate reachability"
	}

	outbound := Check{Name: "Outbound path", State: OK, Detail: c.OutboundType}
	if c.OutboundType == "userDefinedRouting" {
		if c.Firewall != "" {
			outbound.Detail += " → Azure Firewall " + c.Firewall
		}
	} else {
		outbound.State = Warn
		if c.OutboundType == "" {
			outbound.Detail = "unknown"
		}
		outbound.Without = "egress is not forced through a firewall, so there are no firewall logs to read"
		outbound.Fix = "route outbound traffic through a firewall (outboundType userDefinedRouting)"
	}

	fw := fromSource("firewall-logs", "Firewall logs", "", "what actually left the cluster",
		"send firewall logs to the Log Analytics workspace (resource-specific tables)")
	plug := fromSource("coredns-log-plugin", "CoreDNS query logs", "", dnsWithout, corednsFix)
	coll := fromSource("coredns-logs-collected", "Log collection", "", dnsWithout,
		"include kube-system in the Container Insights data collection rule")
	plug.Required, coll.Required = snat, snat
	pods := fromSource("pod-inventory", "Pod inventory", "", "pod IPs cannot be mapped to workloads over time",
		"enable Container Insights so KubePodInventory is collected")

	traffic := []Check{outbound, fw, plug, coll, pods}
	if c.NodeLocalDNS {
		traffic = append(traffic, Check{Name: "NodeLocal DNS", State: Fail, Detail: "workload attribution unavailable",
			Why:     "NodeLocal DNS Cache hides which pod sent each query from CoreDNS logs",
			Without: dnsWithout})
	}

	// Required: K8s API plus at least one traffic source, unless the CNI
	// SNATs pod traffic, where DNS logs are mandatory (set above).
	if !snat {
		if fw.State != OK && (plug.State != OK || coll.State != OK) {
			fw.Required, plug.Required, coll.Required = true, true, true
			traffic[1], traffic[2], traffic[3] = fw, plug, coll
		}
	}

	var loc []Check
	switch {
	case db == nil:
		loc = append(loc, Check{Name: "Cloud IP ranges", State: Fail, Detail: "not loaded"},
			Check{Name: "GeoIP database", State: Warn, Detail: "not loaded"})
	default:
		loc = append(loc, Check{Name: "Cloud IP ranges", State: OK, Detail: db.Version()})
		if db.HasGeoIP() {
			loc = append(loc, Check{Name: "GeoIP database", State: OK, Detail: "country ranges loaded"})
		} else {
			loc = append(loc, Check{Name: "GeoIP database", State: Warn, Detail: "no country ranges",
				Without: "destinations outside known cloud ranges have no country",
				Fix:     "update the location data (outflow update-data)"})
		}
	}

	res := Check{Name: "Rule", State: Info, Detail: "not configured"}
	if cfgFound && cfg.Residency != nil {
		res = Check{Name: "Rule", State: OK, Detail: "outflow.yaml · " + ruleSummary(cfg.Residency)}
	}

	profile := "generic"
	if c.Platform == "AKS" {
		profile = "azure"
	}

	r := Result{
		Profile: profile,
		Context: c.Context,
		Groups: []Group{
			{Name: "Cluster", Checks: []Check{api, platform, cni, policy}},
			{Name: "Traffic", Checks: traffic},
			{Name: "Location", Checks: loc},
			{Name: "Residency", Checks: []Check{res}},
			{Name: "Access", Checks: []Check{{State: Info, Detail: "checked in live mode"}}},
		},
	}
	r.Coverage = coverage(traffic, fw.State == OK, plug.State == OK && coll.State == OK)
	return r
}

func coverage(traffic []Check, fwOK, dnsOK bool) Coverage {
	cv := Coverage{Can: []string{"which workloads may reach the internet"}}
	all := true
	for _, c := range traffic {
		if c.State != OK {
			all = false
		}
	}
	switch {
	case !fwOK && !dnsOK:
		cv.Level = None
	case all:
		cv.Level = Full
	default:
		cv.Level = Partial
	}
	if fwOK {
		cv.Can = append(cv.Can, "what left via the firewall, per node")
	}
	if dnsOK {
		cv.Can = append(cv.Can, "which domains each workload looked up")
	}
	if fwOK && dnsOK {
		cv.Can = append(cv.Can, "which workload made each call")
	}
	if !dnsOK {
		cv.Cannot = append(cv.Cannot, "which workload made each call → enable CoreDNS logs")
	}
	if !fwOK {
		cv.Cannot = append(cv.Cannot, "what actually left the cluster → enable firewall logs")
	}
	return cv
}

func ruleSummary(r *config.Residency) string {
	parts := append([]string{}, r.Allow.Countries...)
	parts = append(parts, r.Allow.CloudRegions...)
	n := len(r.Exceptions)
	word := "exceptions"
	if n == 1 {
		word = "exception"
	}
	return fmt.Sprintf("allow %s · %d %s", strings.Join(parts, ", "), n, word)
}

func cniName(id string) string {
	switch id {
	case "azure-cni-overlay":
		return "Azure CNI Overlay"
	case "azure":
		return "Azure CNI"
	case "kubenet":
		return "kubenet"
	}
	return id
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
