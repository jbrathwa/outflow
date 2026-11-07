package analyze

import (
	"reflect"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
)

var t0 = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

// baseSnapshot has two nodes and three running pods.
func baseSnapshot() model.Snapshot {
	return model.Snapshot{
		Nodes: []model.Node{
			{Name: "aks-node-1", IPs: []string{"10.224.0.4"}},
			{Name: "aks-node-2", IPs: []string{"10.224.0.5"}},
		},
		Pods: []model.Pod{
			{Namespace: "payments", Name: "api-1", WorkloadID: "payments/api", Node: "aks-node-1", IP: "10.244.1.5", Start: at(-60)},
			{Namespace: "shop", Name: "web-1", WorkloadID: "shop/web", Node: "aks-node-1", IP: "10.244.1.6", Start: at(-60)},
			{Namespace: "jobs", Name: "sync-1", WorkloadID: "jobs/sync", Node: "aks-node-2", IP: "10.244.2.7", Start: at(-60)},
		},
		Resolutions: []model.Resolution{
			{Domain: "api.stripe.com", IPs: []string{"198.51.100.10"}, Source: "outflow", At: at(0)},
		},
	}
}

func requireLen(t *testing.T, obs []Observation, n int) {
	t.Helper()
	if len(obs) != n {
		t.Fatalf("got %d observations, want %d: %+v", len(obs), n, obs)
	}
}

func TestAttributeConfirmed(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 2}}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 3},
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(1), Count: 4},
		// two bins later: outside the join window
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(2), Count: 5},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	want := Observation{
		Subject: "(node) aks-node-1", NodeLevel: true, Domain: "api.stripe.com",
		IP: "198.51.100.10", IPSource: "outflow", Port: 443, Action: "allow",
		Evidence: NodeLevel, Hits: 5, LastSeen: at(2),
	}
	if !reflect.DeepEqual(obs[0], want) {
		t.Errorf("node obs = %+v\nwant %+v", obs[0], want)
	}
	want = Observation{
		Subject: "payments/api", Domain: "api.stripe.com",
		IP: "198.51.100.10", IPSource: "outflow", Port: 443, Action: "allow",
		Evidence: Confirmed, Hits: 7, LastSeen: at(1),
	}
	if !reflect.DeepEqual(obs[1], want) {
		t.Errorf("pod obs = %+v\nwant %+v", obs[1], want)
	}
}

func TestAttributeLikelyTwoWorkloadsSameNode(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 1},
		{PodIP: "10.244.1.6", Domain: "api.stripe.com", Bin: at(0), Count: 1},
		// same domain on another node must not become a candidate
		{PodIP: "10.244.2.7", Domain: "api.stripe.com", Bin: at(0), Count: 1},
	}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(1), Count: 6},
	}
	obs := Attribute(s)
	requireLen(t, obs, 3)
	if obs[0].Subject != "jobs/sync" || obs[0].Evidence != DNSOnly {
		t.Errorf("obs[0] = %+v, want jobs/sync DNS only", obs[0])
	}
	for i, tc := range []struct{ subj, other string }{{"payments/api", "shop/web"}, {"shop/web", "payments/api"}} {
		o := obs[i+1]
		if o.Subject != tc.subj || o.Evidence != Likely || o.Hits != 6 || o.Port != 443 || o.Action != "allow" {
			t.Errorf("obs[%d] = %+v, want %s likely 6 hits", i+1, o, tc.subj)
		}
		if !reflect.DeepEqual(o.AlsoLikely, []string{tc.other}) {
			t.Errorf("obs[%d].AlsoLikely = %v, want [%s]", i+1, o.AlsoLikely, tc.other)
		}
	}
}

func TestAttributeDNSOnly(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 2},
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(5), Count: 3},
		{PodIP: "10.244.1.5", Domain: "files.example.com", Bin: at(1), Count: 1},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	want := []Observation{
		{Subject: "payments/api", Domain: "api.stripe.com", IP: "198.51.100.10", IPSource: "outflow", Evidence: DNSOnly, Hits: 5, LastSeen: at(5)},
		{Subject: "payments/api", Domain: "files.example.com", Evidence: DNSOnly, Hits: 1, LastSeen: at(1)},
	}
	if !reflect.DeepEqual(obs, want) {
		t.Errorf("obs = %+v\nwant %+v", obs, want)
	}
}

func TestAttributeNodeLevelNoLookup(t *testing.T) {
	s := baseSnapshot()
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.5", Domain: "mcr.microsoft.com", DestIP: "203.0.113.20", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(3), Count: 9},
	}
	obs := Attribute(s)
	requireLen(t, obs, 1)
	want := Observation{
		Subject: "(node) aks-node-2", NodeLevel: true, Domain: "mcr.microsoft.com",
		IP: "203.0.113.20", IPSource: "firewall log", Port: 443, Action: "allow",
		Evidence: NodeLevel, Hits: 9, LastSeen: at(3),
	}
	if !reflect.DeepEqual(obs[0], want) {
		t.Errorf("obs = %+v\nwant %+v", obs[0], want)
	}
}

func TestAttributeHostNetworkLookupFromNodeIP(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.224.0.4", Domain: "api.stripe.com", Bin: at(0), Count: 1},
		{PodIP: "10.224.0.4", Domain: "telemetry.example.com", Bin: at(0), Count: 2},
	}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 1},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	for _, o := range obs {
		if o.Subject != "(node) aks-node-1" || !o.NodeLevel {
			t.Errorf("obs = %+v, want the (node) aks-node-1 row", o)
		}
	}
	if obs[0].Domain != "api.stripe.com" || obs[0].Action != "allow" || obs[0].Evidence != NodeLevel {
		t.Errorf("obs[0] = %+v, want joined api.stripe.com, node-level", obs[0])
	}
	if obs[1].Domain != "telemetry.example.com" || obs[1].Action != "" || obs[1].Hits != 2 || obs[1].Evidence != DNSOnly {
		t.Errorf("obs[1] = %+v, want unmatched telemetry lookup, DNS only", obs[1])
	}
}

func TestAttributeDenyKeepsAction(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 1}}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 1},
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "deny", Rule: "default-deny", Bin: at(0), Count: 2},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	if obs[0].Action != "allow" || obs[0].Hits != 1 || obs[1].Action != "deny" || obs[1].Hits != 2 {
		t.Errorf("obs = %+v, want separate allow(1) and deny(2)", obs)
	}
	if obs[1].Evidence != Confirmed {
		t.Errorf("deny evidence = %q, want confirmed", obs[1].Evidence)
	}
}

func TestAttributeDropsNonClusterNodeEvents(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 1}}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.50.0.9", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 7},
	}
	obs := Attribute(s)
	requireLen(t, obs, 1)
	if obs[0].Subject != "payments/api" || obs[0].Evidence != DNSOnly {
		t.Errorf("obs = %+v, want only DNS-only lookup", obs[0])
	}
}

func TestAttributeNetworkRuleByResolvedIP(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 1}}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", DestIP: "198.51.100.10", Protocol: "TCP", Port: 443, Action: "allow", Rule: "net-allow", Bin: at(1), Count: 4},
		// no resolution matches this IP → node-level by IP
		{NodeIP: "10.224.0.4", DestIP: "192.0.2.99", Protocol: "UDP", Port: 123, Action: "allow", Bin: at(1), Count: 2},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	want := []Observation{
		{Subject: "(node) aks-node-1", NodeLevel: true, IP: "192.0.2.99", IPSource: "firewall log", Port: 123, Action: "allow", Evidence: NodeLevel, Hits: 2, LastSeen: at(1)},
		{Subject: "payments/api", Domain: "api.stripe.com", IP: "198.51.100.10", IPSource: "firewall log", Port: 443, Action: "allow", Evidence: Confirmed, Hits: 4, LastSeen: at(1)},
	}
	if !reflect.DeepEqual(obs, want) {
		t.Errorf("obs = %+v\nwant %+v", obs, want)
	}
}

func TestAttributePodIPReuse(t *testing.T) {
	s := baseSnapshot()
	s.Pods = []model.Pod{
		{Namespace: "payments", Name: "api-a", WorkloadID: "payments/api", Node: "aks-node-1", IP: "10.244.1.5", Start: at(0), End: at(30)},
		{Namespace: "billing", Name: "worker-b", WorkloadID: "billing/worker", Node: "aks-node-1", IP: "10.244.1.5", Start: at(31)},
	}
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(10), Count: 1},
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(40), Count: 1},
	}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(10), Count: 1},
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(40), Count: 1},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	if obs[0].Subject != "billing/worker" || obs[0].Evidence != Confirmed || !obs[0].LastSeen.Equal(at(40)) {
		t.Errorf("obs[0] = %+v, want billing/worker confirmed at 10:40", obs[0])
	}
	if obs[1].Subject != "payments/api" || obs[1].Evidence != Confirmed || !obs[1].LastSeen.Equal(at(10)) {
		t.Errorf("obs[1] = %+v, want payments/api confirmed at 10:10", obs[1])
	}
}

func TestAttributeDomainNormalization(t *testing.T) {
	s := baseSnapshot()
	s.Resolutions = []model.Resolution{{Domain: "API.STRIPE.COM.", IPs: []string{"198.51.100.10"}, Source: "outflow"}}
	s.DNSLookups = []model.DNSLookup{{PodIP: "10.244.1.5", Domain: "API.Stripe.com.", Bin: at(0), Count: 1}}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 1},
	}
	obs := Attribute(s)
	requireLen(t, obs, 1)
	want := Observation{
		Subject: "payments/api", Domain: "api.stripe.com", IP: "198.51.100.10", IPSource: "outflow",
		Port: 443, Action: "allow", Evidence: Confirmed, Hits: 1, LastSeen: at(0),
	}
	if !reflect.DeepEqual(obs[0], want) {
		t.Errorf("obs = %+v\nwant %+v", obs[0], want)
	}
}

func TestAttributeUnknownPod(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{{PodIP: "10.244.9.9", Domain: "api.stripe.com", Bin: at(0), Count: 2}}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 1},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	if obs[0].Subject != "(node) aks-node-1" || obs[0].Evidence != NodeLevel {
		t.Errorf("obs[0] = %+v, want node-level event", obs[0])
	}
	want := Observation{
		Subject: "unknown pod (10.244.9.9)", Domain: "api.stripe.com", IP: "198.51.100.10", IPSource: "outflow",
		Evidence: DNSOnly, Hits: 2, LastSeen: at(0),
	}
	if !reflect.DeepEqual(obs[1], want) {
		t.Errorf("obs[1] = %+v\nwant %+v", obs[1], want)
	}
}

func TestAttributeEmptySnapshot(t *testing.T) {
	if obs := Attribute(model.Snapshot{}); len(obs) != 0 {
		t.Errorf("obs = %+v, want none", obs)
	}
}

func TestAttributeMergeKeepsLikely(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 1},
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(5), Count: 1},
		{PodIP: "10.244.1.6", Domain: "api.stripe.com", Bin: at(5), Count: 1},
	}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 2},
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(5), Count: 3},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	a := obs[0]
	if a.Subject != "payments/api" || a.Evidence != Likely || a.Hits != 5 || !a.LastSeen.Equal(at(5)) ||
		!reflect.DeepEqual(a.AlsoLikely, []string{"shop/web"}) {
		t.Errorf("obs[0] = %+v, want payments/api likely (confirmed+likely merged), 5 hits", a)
	}
}

func TestAttributeUnknownPodDowngradesConfirmed(t *testing.T) {
	s := baseSnapshot()
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.244.1.5", Domain: "api.stripe.com", Bin: at(0), Count: 1},
		// finished Job pod, not in pod history
		{PodIP: "10.244.3.8", Domain: "API.Stripe.com.", Bin: at(0), Count: 2},
	}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.4", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(1), Count: 3},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	want := []Observation{
		{Subject: "payments/api", Domain: "api.stripe.com", IP: "198.51.100.10", IPSource: "outflow", Port: 443, Action: "allow",
			Evidence: Likely, Hits: 3, LastSeen: at(1), AlsoLikely: []string{"unknown pod (10.244.3.8)"}},
		{Subject: "unknown pod (10.244.3.8)", Domain: "api.stripe.com", IP: "198.51.100.10", IPSource: "outflow",
			Evidence: DNSOnly, Hits: 2, LastSeen: at(0)},
	}
	if !reflect.DeepEqual(obs, want) {
		t.Errorf("obs = %+v\nwant %+v", obs, want)
	}
}

// A hostNetwork pod looks DNS up from its node IP. Without a firewall match the
// lookup proves nothing left, so it is DNS only (still node-level), not node-level evidence.
func TestAttributeNodeLookupWithoutFirewallIsDNSOnly(t *testing.T) {
	s := baseSnapshot()
	s.Resolutions = []model.Resolution{{Domain: "stledgerweu.blob.core.windows.net", IPs: []string{"20.60.40.4"}, Source: "outflow"}}
	s.DNSLookups = []model.DNSLookup{
		{PodIP: "10.224.0.4", Domain: "stledgerweu.blob.core.windows.net", Bin: at(0), Count: 5},
		{PodIP: "10.224.0.5", Domain: "api.stripe.com", Bin: at(0), Count: 1},
	}
	s.EgressEvents = []model.EgressEvent{
		{NodeIP: "10.224.0.5", Domain: "api.stripe.com", Protocol: "TCP", Port: 443, Action: "allow", Bin: at(0), Count: 2},
	}
	obs := Attribute(s)
	requireLen(t, obs, 2)
	want := Observation{
		Subject: "(node) aks-node-1", NodeLevel: true, Domain: "stledgerweu.blob.core.windows.net",
		IP: "20.60.40.4", IPSource: "outflow", Evidence: DNSOnly, Hits: 5, LastSeen: at(0),
	}
	if !reflect.DeepEqual(obs[0], want) {
		t.Errorf("unmatched node lookup = %+v\nwant %+v", obs[0], want)
	}
	if obs[1].Subject != "(node) aks-node-2" || obs[1].Evidence != NodeLevel || !obs[1].NodeLevel {
		t.Errorf("matched node lookup = %+v, want node-level", obs[1])
	}
}
