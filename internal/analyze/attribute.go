// Package analyze turns a snapshot into egress observations and reports.
// It must not import UI, Kubernetes or cloud SDK packages.
package analyze

import (
	"slices"
	"sort"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
)

// Evidence says how sure outflow is that a subject made the observed traffic.
type Evidence string

const (
	Confirmed Evidence = "confirmed"  // exactly one subject looked the domain up on that node
	Likely    Evidence = "likely"     // several subjects on that node looked it up
	DNSOnly   Evidence = "DNS only"   // looked up, but no firewall event joined
	NodeLevel Evidence = "node-level" // firewall traffic tied only to a node (hostNetwork lookup or none)
)

// JoinBins is how many one-minute bins after a lookup a firewall event may still join it.
const JoinBins = 1

// Observation is one subject reaching one destination on one port with one action.
type Observation struct {
	Subject    string    `json:"subject"` // workload ID, "(node) <name>", or "unknown pod (<ip>)"
	NodeLevel  bool      `json:"nodeLevel"`
	Domain     string    `json:"domain"`
	IP         string    `json:"ip"`
	IPSource   string    `json:"ipSource"` // "outflow" | "firewall log" | ""
	Port       int       `json:"port"`
	Action     string    `json:"action"` // "allow" | "deny" | "" (DNS only)
	Evidence   Evidence  `json:"evidence"`
	Hits       int       `json:"hits"`
	LastSeen   time.Time `json:"lastSeen"`
	AlsoLikely []string  `json:"alsoLikely"`
}

// lookup is a DNS lookup resolved to its subject.
type lookup struct {
	model.DNSLookup
	subject   string
	nodeLevel bool
	node      string // "" when the pod is unknown
	matched   bool
}

// candidate is a subject that may have made an egress event.
type candidate struct {
	subject   string
	nodeLevel bool
	domain    string
}

// Attribute joins CoreDNS lookups with firewall events by node, domain and time,
// and returns merged observations sorted by subject, destination, port and action.
func Attribute(s model.Snapshot) []Observation {
	nodeByIP := map[string]string{}
	for _, n := range s.Nodes {
		for _, ip := range n.IPs {
			nodeByIP[ip] = n.Name
		}
	}
	res := resolutionIndex(s.Resolutions)
	lookups := resolveLookups(s, nodeByIP)

	var obs []Observation
	for _, e := range s.EgressEvents {
		node, ok := nodeByIP[e.NodeIP]
		if !ok {
			continue // another cluster behind the same firewall
		}
		obs = append(obs, attributeEvent(e, node, lookups, res)...)
	}
	for _, l := range lookups {
		if l.matched {
			continue
		}
		o := Observation{
			Subject: l.subject, NodeLevel: l.nodeLevel, Domain: l.Domain,
			Evidence: DNSOnly, Hits: l.Count, LastSeen: l.Bin,
		}
		if ips := res[l.Domain]; len(ips) > 0 {
			o.IP, o.IPSource = ips[0], "outflow"
		}
		// A node-IP (hostNetwork) lookup with no firewall match stays on the node row
		// but is DNS only: nothing shows that traffic left.
		obs = append(obs, o)
	}
	return merge(obs)
}

// resolutionIndex maps each normalized domain to the IPs of its resolutions, in input order.
func resolutionIndex(rs []model.Resolution) map[string][]string {
	idx := map[string][]string{}
	for _, r := range rs {
		d := model.NormalizeDomain(r.Domain)
		idx[d] = append(idx[d], r.IPs...)
	}
	return idx
}

// resolveLookups finds the subject of each lookup: the node for a node IP, otherwise
// the pod holding that IP at the lookup's bin (pod IPs are reused over time).
func resolveLookups(s model.Snapshot, nodeByIP map[string]string) []*lookup {
	podsByIP := map[string][]model.Pod{}
	for _, p := range s.Pods {
		podsByIP[p.IP] = append(podsByIP[p.IP], p)
	}
	out := make([]*lookup, 0, len(s.DNSLookups))
	for _, d := range s.DNSLookups {
		d.Domain = model.NormalizeDomain(d.Domain)
		l := &lookup{DNSLookup: d, subject: "unknown pod (" + d.PodIP + ")"}
		if node, ok := nodeByIP[d.PodIP]; ok {
			l.subject, l.nodeLevel, l.node = nodeSubject(node), true, node
		} else {
			for _, p := range podsByIP[d.PodIP] {
				if alive(p, d.Bin) {
					l.subject, l.node = p.WorkloadID, p.Node
					break
				}
			}
		}
		out = append(out, l)
	}
	return out
}

func alive(p model.Pod, t time.Time) bool {
	return !t.Before(p.Start) && (p.End.IsZero() || t.Before(p.End))
}

func nodeSubject(node string) string { return "(node) " + node }

// attributeEvent turns one firewall event into observations for its candidate subjects.
func attributeEvent(e model.EgressEvent, node string, lookups []*lookup, res map[string][]string) []Observation {
	domain := model.NormalizeDomain(e.Domain)
	var cands []candidate
	seen := map[string]bool{}
	for _, l := range lookups {
		if l.node != node || !inWindow(l.Bin, e.Bin) {
			continue
		}
		if domain != "" {
			if l.Domain != domain {
				continue
			}
		} else if e.DestIP == "" || !slices.Contains(res[l.Domain], e.DestIP) {
			continue
		}
		l.matched = true
		if !seen[l.subject] {
			seen[l.subject] = true
			cands = append(cands, candidate{l.subject, l.nodeLevel, l.Domain})
		}
	}

	base := Observation{Port: e.Port, Action: e.Action, Hits: e.Count, LastSeen: e.Bin}
	if len(cands) == 0 {
		o := base
		o.Subject, o.NodeLevel, o.Evidence, o.Domain = nodeSubject(node), true, NodeLevel, domain
		o.IP, o.IPSource = eventIP(e, domain, res)
		return []Observation{o}
	}
	unknown := unknownSources(e.Bin, cands, lookups)
	out := make([]Observation, 0, len(cands))
	for _, c := range cands {
		o := base
		o.Subject, o.NodeLevel, o.Domain = c.subject, c.nodeLevel, c.domain
		o.IP, o.IPSource = eventIP(e, c.domain, res)
		o.Evidence = Confirmed
		if len(cands) > 1 || len(unknown) > 0 {
			o.Evidence = Likely
			for _, other := range cands {
				if other.subject != c.subject {
					o.AlsoLikely = append(o.AlsoLikely, other.subject)
				}
			}
			o.AlsoLikely = append(o.AlsoLikely, unknown...)
			sort.Strings(o.AlsoLikely)
		}
		if c.nodeLevel {
			o.Evidence = NodeLevel
		}
		out = append(out, o)
	}
	return out
}

// unknownSources returns the sorted, deduped subjects of unknown-pod lookups (node unknown,
// so any node) for a candidate's domain within the event's join window. Any of them could
// have made the call, so they rule out Confirmed.
func unknownSources(eventBin time.Time, cands []candidate, lookups []*lookup) []string {
	var out []string
	for _, l := range lookups {
		if l.node != "" || !inWindow(l.Bin, eventBin) || slices.Contains(out, l.subject) {
			continue
		}
		if slices.ContainsFunc(cands, func(c candidate) bool { return c.domain == l.Domain }) {
			out = append(out, l.subject)
		}
	}
	sort.Strings(out)
	return out
}

// inWindow reports whether an event bin is the lookup bin or up to JoinBins minutes later.
func inWindow(lookupBin, eventBin time.Time) bool {
	return !eventBin.Before(lookupBin) && !eventBin.After(lookupBin.Add(JoinBins*time.Minute))
}

func eventIP(e model.EgressEvent, domain string, res map[string][]string) (string, string) {
	if e.DestIP != "" {
		return e.DestIP, "firewall log"
	}
	if ips := res[domain]; len(ips) > 0 {
		return ips[0], "outflow"
	}
	return "", ""
}

type mergeKey struct {
	subject, dest string
	port          int
	action        string
}

func keyOf(o Observation) mergeKey {
	dest := o.Domain
	if dest == "" {
		dest = o.IP
	}
	return mergeKey{o.Subject, dest, o.Port, o.Action}
}

// merge sums hits per key, keeps the latest LastSeen and the least certain evidence,
// and sorts the result deterministically.
func merge(obs []Observation) []Observation {
	idx := map[mergeKey]int{}
	var out []Observation
	for _, o := range obs {
		k := keyOf(o)
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, o)
			continue
		}
		m := &out[i]
		m.Hits += o.Hits
		if o.LastSeen.After(m.LastSeen) {
			m.LastSeen = o.LastSeen
		}
		if m.IP == "" {
			m.IP, m.IPSource = o.IP, o.IPSource
		}
		if o.Evidence == Likely && m.Evidence == Confirmed {
			m.Evidence = Likely
		}
		for _, a := range o.AlsoLikely {
			if !slices.Contains(m.AlsoLikely, a) {
				m.AlsoLikely = append(m.AlsoLikely, a)
			}
		}
		sort.Strings(m.AlsoLikely)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := keyOf(out[i]), keyOf(out[j])
		if a.subject != b.subject {
			return a.subject < b.subject
		}
		if a.dest != b.dest {
			return a.dest < b.dest
		}
		if a.port != b.port {
			return a.port < b.port
		}
		return a.action < b.action
	})
	return out
}
