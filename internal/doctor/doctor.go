// Package doctor checks which data sources outflow has, from a snapshot.
package doctor

import (
	"fmt"
	"strings"

	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
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
	laReaderFix  = "grant Log Analytics Reader on the workspace (docs/setup.md#roles)"
	aksReaderFix = "grant Reader on the AKS resource (docs/setup.md#roles)"
	corednsFix   = "add `log` to the coredns-custom ConfigMap (docs/setup.md#coredns)"
	dnsWithout   = "destinations only per node, not per workload"
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

	api := fromSource(model.SrcK8sAPI, "Kubernetes API", "", "no workloads, policies or pods can be read",
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
		policy.Why = "egress modes may be incomplete"
		policy.Without = "Calico-specific policies are not analysed; results may overstate reachability"
	}

	outbound := Check{Name: "Outbound path", State: OK, Detail: c.OutboundType}
	switch c.OutboundType {
	case "userDefinedRouting":
		if c.Firewall != "" {
			outbound.Detail += " → Azure Firewall " + c.Firewall
		}
	case "":
		// Never read: say so rather than implying the path skips a firewall.
		outbound.State, outbound.Detail = Warn, "not checked"
		if c.Platform == "AKS" {
			outbound.Detail = "not checked (AKS resource not read)"
		}
	default:
		outbound.State = Warn
		outbound.Without = "egress is not forced through a firewall, so there are no firewall logs to read"
		outbound.Fix = "route outbound traffic through a firewall (outboundType userDefinedRouting)"
	}

	fw := fromSource(model.SrcFirewall, "Firewall logs", "", "what actually left the cluster",
		"send firewall logs to the Log Analytics workspace (resource-specific tables)")
	if strings.HasPrefix(fw.Detail, "no access to workspace") {
		fw.Fix = laReaderFix
	}
	plug := fromSource(model.SrcCoreDNSPlugin, "CoreDNS query logs", "", dnsWithout, corednsFix)
	coll := fromSource(model.SrcCoreDNSCollected, "Log collection", "", dnsWithout,
		"include kube-system in the Container Insights data collection rule")
	plug.Required, coll.Required = snat, snat
	pods := fromSource(model.SrcPodInventory, "Pod inventory", "", "pod IPs cannot be mapped to workloads over time",
		"enable Container Insights so KubePodInventory is collected")

	// Required: K8s API plus at least one traffic source; when the CNI
	// SNATs pod traffic the DNS checks are mandatory (set above), and so is
	// the absence of NodeLocal DNS (below). A source
	// is available when ok or warn, so only fail trips Failed().
	dnsAvail := available(plug.State) && available(coll.State)
	if !snat && !available(fw.State) && !dnsAvail {
		fw.Required, plug.Required, coll.Required = true, true, true
	}

	traffic := []Check{outbound, fw, plug, coll}
	if _, ok := src(model.SrcCoreDNSFormat); ok {
		traffic = append(traffic, fromSource(model.SrcCoreDNSFormat, "CoreDNS log format", "",
			"some CoreDNS lines may be missed", "check the CoreDNS log format"))
	}
	traffic = append(traffic, pods)
	localDNS := c.NodeLocalDNS || c.AKSLocalDNS
	if localDNS {
		var found []string
		if c.NodeLocalDNS {
			found = append(found, "NodeLocal DNSCache found")
		}
		if c.AKSLocalDNS {
			found = append(found, "AKS LocalDNS enabled")
		}
		traffic = append(traffic, Check{Name: "Local DNS cache", State: Fail, Detail: strings.Join(found, " · "),
			Why:     "a node-local DNS cache hides which pod sent each query from CoreDNS logs",
			Without: dnsWithout, Required: snat})
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

	// The profile collection used; older snapshots lack it, so derive it from the platform.
	profile := s.Profile
	if profile == "" {
		profile = "generic"
		if c.Platform == "AKS" {
			profile = "azure"
		}
	}

	clusterChecks := []Check{api, platform, cni, policy}
	if _, ok := src(model.SrcAKS); ok {
		clusterChecks = append(clusterChecks, fromSource(model.SrcAKS, "AKS resource", "",
			"AKS settings (outbound type, LocalDNS) are not read from Azure",
			aksReaderFix))
	}

	r := Result{
		Profile: profile,
		Context: c.Context,
		Groups: []Group{
			{Name: "Cluster", Checks: clusterChecks},
			{Name: "Traffic", Checks: traffic},
			{Name: "Location", Checks: loc},
			{Name: "Residency", Checks: []Check{res}},
			{Name: "Access", Checks: accessChecks(src, fromSource)},
		},
	}
	r.Coverage = coverage(traffic, available(fw.State), dnsAvail, localDNS)
	return r
}

// accessChecks builds the Access group: one check per live access source,
// or a single info line for demo and snapshot data that carries none.
func accessChecks(src func(string) (model.SourceStatus, bool),
	fromSource func(id, name, why, without, fix string) Check) []Check {
	_, k8s := src(model.SrcK8sAccess)
	_, az := src(model.SrcAzureAccess)
	if !k8s && !az {
		return []Check{{State: Info, Detail: "checked in live mode"}}
	}
	notChecked := func(name string) Check { return Check{Name: name, State: Info, Detail: "not checked"} }
	kc, ac := notChecked("Kubernetes"), notChecked("Azure")
	if k8s {
		kc = fromSource(model.SrcK8sAccess, "Kubernetes", "", "some cluster data cannot be read",
			"apply deploy/clusterrole.yaml (docs/usage.md#permissions)")
		kc.Required = true
	}
	if az {
		ac = fromSource(model.SrcAzureAccess, "Azure", "", "no firewall or DNS logs can be queried", laReaderFix)
		// Name the role the detail says is missing: Reader on the AKS resource, Log Analytics
		// Reader on workspaces, or both.
		if ac.Fix != "" && strings.Contains(ac.Detail, "AKS resource") {
			ac.Fix = aksReaderFix
			if strings.Contains(ac.Detail, "workspace") {
				ac.Fix = laReaderFix + "; " + aksReaderFix
			}
		}
	}
	return []Check{kc, ac}
}

// available reports whether a source delivers data: ok, or warn (degraded).
func available(s State) bool { return s == OK || s == Warn }

func coverage(traffic []Check, fwOK, dnsOK, nodeLocal bool) Coverage {
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
	if fwOK && dnsOK && !nodeLocal {
		cv.Can = append(cv.Can, "which workload made each call")
	}
	seen := map[string]bool{}
	add := func(line string) {
		if !seen[line] {
			seen[line] = true
			cv.Cannot = append(cv.Cannot, line)
		}
	}
	for _, c := range traffic {
		if c.State == OK {
			continue
		}
		switch c.Name {
		case "Outbound path":
			add("traffic that skips the firewall")
		case "Firewall logs":
			add("what actually left the cluster → enable firewall logs")
		case "CoreDNS query logs", "Log collection":
			add("which workload made each call → enable CoreDNS logs")
		case "Pod inventory":
			add("workloads for pods that no longer exist → enable Container Insights")
		case "CoreDNS log format":
			add("some CoreDNS lines may be missed → check the CoreDNS log format")
		case "Local DNS cache":
			add("which workload made each call → a node-local DNS cache hides the client pod")
		}
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
