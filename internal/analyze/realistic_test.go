package analyze

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/model"
)

// realisticSnapshot builds 3 nodes (10.224.0.4-6), 12 pods of 6 workloads, Overlay pod IPs 10.244.x.y.
// Each workload looks its domains up every 5 minutes (cached client) for 2 hours and opens
// firewall connections 0-4 minutes after each lookup; pods on the same node never share a domain
// except ns-b/shared-a and ns-b/shared-b, which both use "shared.example".
func realisticSnapshot() model.Snapshot {
	start := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	s := model.Snapshot{SchemaVersion: 1, Window: model.Window{Start: start, End: start.Add(2 * time.Hour)}}
	for i := 0; i < 3; i++ {
		s.Nodes = append(s.Nodes, model.Node{Name: fmt.Sprintf("node-%d", i), IPs: []string{fmt.Sprintf("10.224.0.%d", 4+i)}})
	}
	type wl struct {
		ns, name, domain string
		nodes            [2]int
	}
	wls := []wl{
		{"ns-a", "web", "web.example", [2]int{0, 1}},
		{"ns-a", "api", "api.example", [2]int{1, 2}},
		{"ns-a", "jobs", "jobs.example", [2]int{2, 0}},
		{"ns-a", "mail", "mail.example", [2]int{0, 2}},
		{"ns-b", "shared-a", "shared.example", [2]int{0, 1}},
		{"ns-b", "shared-b", "shared.example", [2]int{0, 1}},
	}
	for wi, w := range wls {
		s.Workloads = append(s.Workloads, model.Workload{Namespace: w.ns, Name: w.name, Kind: "Deployment"})
		for pi, n := range w.nodes {
			ip := fmt.Sprintf("10.244.%d.%d", n, 10+wi*2+pi)
			s.Pods = append(s.Pods, model.Pod{
				Namespace: w.ns, Name: fmt.Sprintf("%s-%d", w.name, pi), WorkloadID: w.ns + "/" + w.name,
				Node: fmt.Sprintf("node-%d", n), IP: ip, Start: start.Add(-time.Hour),
			})
			for k := 0; k < 24; k++ {
				bin := start.Add(time.Duration(k) * 5 * time.Minute)
				s.DNSLookups = append(s.DNSLookups, model.DNSLookup{PodIP: ip, Domain: w.domain, Bin: bin, Count: 1})
				s.EgressEvents = append(s.EgressEvents, model.EgressEvent{
					NodeIP: fmt.Sprintf("10.224.0.%d", 4+n), Domain: w.domain, Protocol: "https", Port: 443,
					Action: "allow", Bin: bin.Add(time.Duration((k+wi+pi)%5) * time.Minute), Count: 1,
				})
			}
		}
	}
	return s
}

func TestRealisticDefaultWindowConfirmsCachedLookups(t *testing.T) {
	obs := Attribute(realisticSnapshot(), DefaultJoinWindow)
	if len(obs) == 0 {
		t.Fatal("no observations")
	}
	for _, o := range obs {
		if o.Action != "allow" {
			continue
		}
		if o.NodeLevel || strings.HasPrefix(o.Subject, "(node)") {
			t.Errorf("allowed hit landed on a node row: %+v", o)
		}
		switch o.Domain {
		case "shared.example":
			other := "ns-b/shared-b"
			if o.Subject == other {
				other = "ns-b/shared-a"
			}
			if o.Evidence != Likely || len(o.AlsoLikely) != 1 || o.AlsoLikely[0] != other {
				t.Errorf("shared row = %+v, want Likely naming %s", o, other)
			}
		default:
			if o.Evidence != Confirmed {
				t.Errorf("%s %s evidence = %s, want confirmed", o.Subject, o.Domain, o.Evidence)
			}
		}
	}
}

func TestRealisticOneMinuteWindowFallsBackToNodeLevel(t *testing.T) {
	obs := Attribute(realisticSnapshot(), time.Minute)
	var nodeHits, total int
	for _, o := range obs {
		if o.Action != "allow" {
			continue
		}
		total += o.Hits
		if o.NodeLevel {
			nodeHits += o.Hits
		}
	}
	if total == 0 || nodeHits*2 < total {
		t.Errorf("node-level hits = %d of %d, want at least half", nodeHits, total)
	}
}

func TestBuildRecordsJoinWindow(t *testing.T) {
	rep := BuildWith(realisticSnapshot(), seedDB(t), config.Config{}, Options{JoinWindow: 2 * time.Minute})
	if rep.JoinWindow != "2m0s" {
		t.Errorf("JoinWindow = %q, want 2m0s", rep.JoinWindow)
	}
	if got := Build(realisticSnapshot(), seedDB(t), config.Config{}).JoinWindow; got != "5m0s" {
		t.Errorf("default JoinWindow = %q, want 5m0s", got)
	}
}
