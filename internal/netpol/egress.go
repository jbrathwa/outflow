// Package netpol derives a workload's egress mode from Kubernetes NetworkPolicies.
package netpol

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/jbrathwa/outflow/internal/model"
)

// Mode is the egress posture of a workload.
type Mode string

const (
	Unrestricted Mode = "unrestricted"
	Internet     Mode = "internet"
	Allowlist    Mode = "allowlist"
	Internal     Mode = "internal"
	DenyAll      Mode = "deny-all"
	HostNetwork  Mode = "hostNetwork"
)

// Result is the evaluated egress posture.
type Result struct {
	Mode     Mode     `json:"mode"`
	Ports    string   `json:"ports"`    // e.g. "443" or "80,443"; empty when unrestricted or not uniform
	Policies []string `json:"policies"` // names of policies selecting the workload
}

// String renders "mode" or "mode:ports".
func (r Result) String() string {
	if r.Ports == "" {
		return string(r.Mode)
	}
	return string(r.Mode) + ":" + r.Ports
}

// DefaultPrivate returns the built-in private ranges plus the given pod and
// service CIDRs. Unparseable extra CIDRs are skipped.
func DefaultPrivate(podCIDRs, serviceCIDRs []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		"169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "fe80::/10",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	for _, s := range append(append([]string{}, podCIDRs...), serviceCIDRs...) {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// Evaluate determines the egress mode of w under policies. private lists the
// ranges treated as cluster-internal.
func Evaluate(w model.Workload, policies []model.NetworkPolicy, private []netip.Prefix) Result {
	if w.HostNetwork {
		return Result{Mode: HostNetwork}
	}
	var res Result
	var rules []model.EgressRule
	for _, p := range policies {
		if p.Namespace != w.Namespace || !Matches(p.PodSelector, w.Labels) || !hasEgress(p) {
			continue
		}
		res.Policies = append(res.Policies, p.Name)
		rules = append(rules, p.Egress...)
	}
	if len(res.Policies) == 0 {
		res.Mode = Unrestricted
		return res
	}

	// Classify each rule; track the best mode and the rules that grant it.
	best := DenyAll
	var granting []model.EgressRule
	for _, r := range rules {
		m := classify(r, private)
		switch {
		case rank(m) > rank(best):
			best, granting = m, []model.EgressRule{r}
		case m == best && m != DenyAll:
			granting = append(granting, r)
		}
	}
	res.Mode = best
	if best != DenyAll {
		res.Ports = uniformPorts(granting)
	}
	return res
}

// hasEgress reports whether p restricts egress. Per Kubernetes defaulting, an
// empty policyTypes list means Egress only when egress rules are present.
func hasEgress(p model.NetworkPolicy) bool {
	if len(p.PolicyTypes) == 0 {
		return len(p.Egress) > 0
	}
	for _, t := range p.PolicyTypes {
		if t == "Egress" {
			return true
		}
	}
	return false
}

func rank(m Mode) int {
	switch m {
	case Internet:
		return 3
	case Allowlist:
		return 2
	case Internal:
		return 1
	}
	return 0
}

// classify returns the strongest mode granted by one rule. Peers with an
// invalid CIDR are skipped; a rule whose peers are all invalid grants nothing.
func classify(r model.EgressRule, private []netip.Prefix) Mode {
	if len(r.To) == 0 {
		return Internet
	}
	best := DenyAll
	for _, peer := range r.To {
		m := Internal
		if peer.IPBlock != nil {
			p, err := netip.ParsePrefix(peer.IPBlock.CIDR)
			if err != nil {
				continue
			}
			p = p.Masked()
			switch {
			case p.Bits() == 0:
				m = Internet
			case !insidePrivate(p, private):
				m = Allowlist
			}
		}
		if rank(m) > rank(best) {
			best = m
		}
	}
	return best
}

func insidePrivate(p netip.Prefix, private []netip.Prefix) bool {
	for _, q := range private {
		if q.Addr().Is4() == p.Addr().Is4() && q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// uniformPorts returns the shared port list when every rule lists ports and all
// lists are identical, else "".
func uniformPorts(rules []model.EgressRule) string {
	var first string
	for i, r := range rules {
		s := renderPorts(r.Ports)
		if s == "" || (i > 0 && s != first) {
			return ""
		}
		first = s
	}
	return first
}

// renderPorts lists a rule's ports; "" when the rule allows all ports, which is
// also the case when any spec omits its port.
func renderPorts(ports []model.PortSpec) string {
	var parts []string
	for _, p := range ports {
		if p.Port == 0 {
			return ""
		}
		s := strconv.Itoa(p.Port)
		if p.EndPort > p.Port {
			s += "-" + strconv.Itoa(p.EndPort)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}
