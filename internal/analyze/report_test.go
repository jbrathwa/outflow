package analyze

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/config"
)

// Embedded seed IPs used below.
const (
	ipUS      = "20.60.128.10" // Azure eastus (US)
	ipNL      = "20.60.40.4"   // Azure westeurope (NL)
	ipUnknown = "192.0.2.1"    // no location
)

func TestClassify(t *testing.T) {
	us := &config.Residency{Allow: config.Allow{Countries: []string{"US"}}, UnknownLocation: "warn"}
	usExc := &config.Residency{
		Allow:           config.Allow{Countries: []string{"US"}},
		Exceptions:      []config.Exception{{Domain: "*.stripe.com", Reason: "PCI vendor"}},
		UnknownLocation: "warn",
	}
	eastus := &config.Residency{Allow: config.Allow{CloudRegions: []string{"azure:eastus"}}, UnknownLocation: "warn"}

	locUS := location.Location{Country: "US", Provider: "azure", Region: "eastus", HowKnown: location.CloudRange}
	locNL := location.Location{Country: "NL", Provider: "azure", Region: "westeurope", HowKnown: location.CloudRange}
	locGlobal := location.Location{Provider: "azure", Global: true, HowKnown: location.CloudRange}
	locCentral := location.Location{Country: "US", Provider: "azure", Region: "centralus", HowKnown: location.CloudRange}
	locGeoUS := location.Location{Country: "US", HowKnown: location.GeoIP}
	locNone := location.Location{HowKnown: location.None}

	dest := func(domain, action string, ev Evidence, loc location.Location) Destination {
		return Destination{Observation: Observation{Domain: domain, Action: action, Evidence: ev}, Location: loc}
	}
	cases := []struct {
		name       string
		d          Destination
		r          *config.Residency
		want       Status
		wantReason string
	}{
		{"US inside", dest("a.com", "allow", Confirmed, locUS), us, Inside, ""},
		{"NL outside", dest("a.com", "allow", Confirmed, locNL), us, Outside, ""},
		{"NL deny", dest("a.com", "deny", Confirmed, locNL), us, OutsideDenied, ""},
		{"NL DNS only", dest("a.com", "", DNSOnly, locNL), us, Unknown, ""},
		{"global", dest("a.com", "allow", Confirmed, locGlobal), us, Unknown, ""},
		{"no location", dest("a.com", "allow", Confirmed, locNone), us, Unknown, ""},
		{"no location deny", dest("a.com", "deny", Confirmed, locNone), us, Unknown, ""},
		{"exception", dest("API.Stripe.com.", "allow", Confirmed, locNL), usExc, Inside, "PCI vendor"},
		{"exception lookalike", dest("evilstripe.com", "allow", Confirmed, locNL), usExc, Outside, ""},
		{"country allows region", dest("a.com", "allow", Confirmed, locCentral), us, Inside, ""},
		{"region only, GeoIP US", dest("a.com", "allow", Confirmed, locGeoUS), eastus, Outside, ""},
		{"region match", dest("a.com", "allow", Confirmed, locUS), eastus, Inside, ""},
		{"no rule", dest("a.com", "allow", Confirmed, locNL), nil, Neutral, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := Classify(tc.d, tc.r, "payments/api")
			if got != tc.want || reason != tc.wantReason {
				t.Errorf("got (%q, %q), want (%q, %q)", got, reason, tc.want, tc.wantReason)
			}
		})
	}
}

func TestClassifyExceptionWorkloadScope(t *testing.T) {
	r := &config.Residency{
		Allow:           config.Allow{Countries: []string{"US"}},
		Exceptions:      []config.Exception{{Domain: "a.com", Workload: "payments/api", Reason: "r"}},
		UnknownLocation: "warn",
	}
	d := Destination{Observation: Observation{Domain: "a.com", Action: "allow", Evidence: Confirmed},
		Location: location.Location{Country: "NL", HowKnown: location.GeoIP}}
	if s, _ := Classify(d, r, "payments/api"); s != Inside {
		t.Errorf("scoped workload: %q", s)
	}
	if s, _ := Classify(d, r, "shop/web"); s != Outside {
		t.Errorf("other workload: %q", s)
	}
}

// flow is one workload reaching one destination; build turns flows into a snapshot.
type flow struct {
	wl, domain, ip, action string
	hits                   int
}

// build makes a snapshot with one node, one pod per workload and, per flow, a DNS lookup
// plus (unless action is "") a firewall event. wl "" makes a node-level event (no lookup).
func build(workloads []string, flows []flow) model.Snapshot {
	s := model.Snapshot{Nodes: []model.Node{{Name: "aks-node-1", IPs: []string{"10.224.0.4"}}}}
	podIP := map[string]string{}
	for i, w := range workloads {
		ns, name, _ := strings.Cut(w, "/")
		s.Workloads = append(s.Workloads, model.Workload{Namespace: ns, Name: name, Kind: "Deployment"})
		ip := fmt.Sprintf("10.244.1.%d", 10+i)
		podIP[w] = ip
		s.Pods = append(s.Pods, model.Pod{Namespace: ns, Name: name + "-1", WorkloadID: w, Node: "aks-node-1", IP: ip, Start: at(-60)})
	}
	for _, f := range flows {
		s.Resolutions = append(s.Resolutions, model.Resolution{Domain: f.domain, IPs: []string{f.ip}, Source: "outflow", At: at(0)})
		if f.wl != "" {
			s.DNSLookups = append(s.DNSLookups, model.DNSLookup{PodIP: podIP[f.wl], Domain: f.domain, Bin: at(0), Count: 1})
		}
		if f.action != "" {
			s.EgressEvents = append(s.EgressEvents, model.EgressEvent{
				NodeIP: "10.224.0.4", Domain: f.domain, DestIP: f.ip, Protocol: "TCP", Port: 443,
				Action: f.action, Bin: at(0), Count: f.hits,
			})
		}
	}
	return s
}

func seedDB(t *testing.T) *location.DB {
	t.Helper()
	db, err := location.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func rule(unknown string, countries ...string) config.Config {
	return config.Config{Residency: &config.Residency{Allow: config.Allow{Countries: countries}, UnknownLocation: unknown}}
}

func row(t *testing.T, r Report, id string) WorkloadResult {
	t.Helper()
	for _, w := range r.Workloads {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("no row %q in %+v", id, r.Workloads)
	return WorkloadResult{}
}

func TestBuildWorkloadStatusIgnoresDenied(t *testing.T) {
	s := build([]string{"payments/api"}, []flow{
		{"payments/api", "us.example.com", ipUS, "allow", 3},
		{"payments/api", "nl.example.com", ipNL, "deny", 2},
	})
	r := Build(s, seedDB(t), rule("warn", "US"))
	w := row(t, r, "payments/api")
	if w.Status != Inside || w.Outside != 0 || w.Unknown != 0 {
		t.Errorf("row = %+v", w)
	}
	if len(w.Destinations) != 2 {
		t.Fatalf("destinations = %+v", w.Destinations)
	}
	for _, d := range w.Destinations {
		want := Inside
		if d.Domain == "nl.example.com" {
			want = OutsideDenied
		}
		if d.Status != want {
			t.Errorf("%s status %q want %q", d.Domain, d.Status, want)
		}
	}
	if r.Counts.Outside != 0 || r.Counts.Inside != 1 || r.Counts.Destinations != 2 {
		t.Errorf("counts = %+v", r.Counts)
	}
}

func TestBuildDeniedNeverCounts(t *testing.T) {
	s := build([]string{"payments/api", "jobs/blocked"}, []flow{
		{"payments/api", "us.example.com", ipUS, "allow", 1},
		{"payments/api", "x.example.com", ipUnknown, "deny", 4},
		{"jobs/blocked", "nl.example.com", ipNL, "deny", 1},
		{"jobs/blocked", "y.example.com", ipUnknown, "deny", 1},
	})
	r := Build(s, seedDB(t), rule("warn", "US"))
	w := row(t, r, "payments/api")
	if w.Status != Inside || w.Unknown != 0 {
		t.Errorf("payments row = %+v", w)
	}
	for _, d := range w.Destinations {
		if d.Domain == "x.example.com" && d.Status != Unknown {
			t.Errorf("denied unknown status = %q", d.Status)
		}
	}
	if b := row(t, r, "jobs/blocked"); b.Status != NoTraffic || b.Outside != 0 || b.Unknown != 0 || len(b.Destinations) != 2 {
		t.Errorf("all-denied row = %+v", b)
	}
	if c := r.Counts; c.Unknown != 0 || c.Outside != 0 || c.Inside != 1 || c.Destinations != 4 {
		t.Errorf("counts = %+v", c)
	}
}

func TestBuildNeutralMode(t *testing.T) {
	s := build([]string{"shop/web", "jobs/idle", "payments/api"}, []flow{
		{"shop/web", "us.example.com", ipUS, "allow", 1},
		{"shop/web", "nl.example.com", ipNL, "allow", 1},
		{"payments/api", "nl2.example.com", ipNL, "allow", 9},
	})
	r := Build(s, seedDB(t), config.Config{})
	if r.RuleConfigured || r.Rule != "" {
		t.Errorf("rule = %v %q", r.RuleConfigured, r.Rule)
	}
	if r.LocationData != "seed-2026-11-07" {
		t.Errorf("location data = %q", r.LocationData)
	}
	var ids []string
	for _, w := range r.Workloads {
		ids = append(ids, w.ID)
		if w.Status != Neutral || w.Outside != 0 || w.Unknown != 0 {
			t.Errorf("%s: %+v", w.ID, w)
		}
		for _, d := range w.Destinations {
			if d.Status != Neutral || d.Exception != "" {
				t.Errorf("%s dest %s: %q", w.ID, d.Domain, d.Status)
			}
		}
	}
	if got := strings.Join(ids, ","); got != "shop/web,payments/api,jobs/idle" {
		t.Errorf("order = %s", got)
	}
	if got := strings.Join(row(t, r, "shop/web").Locations, ", "); got != "NL, US" {
		t.Errorf("locations = %q", got)
	}
	if got := row(t, r, "jobs/idle").Locations; len(got) != 0 {
		t.Errorf("idle locations = %v", got)
	}
	c := r.Counts
	if c.Workloads != 3 || c.Destinations != 3 || c.Countries != 2 || c.Outside != 0 || c.Unknown != 0 || c.Inside != 0 {
		t.Errorf("counts = %+v", c)
	}
}

func TestBuildLocationsGlobalAndUnknown(t *testing.T) {
	s := build([]string{"shop/web"}, []flow{
		{"shop/web", "cdn.example.com", "104.16.1.1", "allow", 1},
		{"shop/web", "x.example.com", ipUnknown, "allow", 1},
		{"shop/web", "nl.example.com", ipNL, "allow", 1},
	})
	r := Build(s, seedDB(t), config.Config{})
	if got := strings.Join(row(t, r, "shop/web").Locations, ", "); got != "NL, global, unknown" {
		t.Errorf("locations = %q", got)
	}
}

func TestBuildSortOrder(t *testing.T) {
	s := build([]string{"a/one", "b/two", "c/unk", "d/in", "e/idle", "f/busy"}, []flow{
		{"a/one", "a.example.com", ipNL, "allow", 1},
		{"b/two", "b1.example.com", ipNL, "allow", 1},
		{"b/two", "b2.example.com", ipNL, "allow", 1},
		{"c/unk", "c.example.com", ipUnknown, "allow", 1},
		{"d/in", "d.example.com", ipUS, "allow", 1},
		{"f/busy", "f.example.com", ipNL, "allow", 5},
		{"", "node.example.com", ipNL, "allow", 50}, // node-level: last in the Outside group
	})
	cfg := rule("warn", "US")
	cfg.Residency.Allow.CloudRegions = []string{"azure:eastus"}
	r := Build(s, seedDB(t), cfg)

	var got []string
	for _, w := range r.Workloads {
		got = append(got, w.ID+"="+string(w.Status))
	}
	want := "b/two=outside,f/busy=outside,a/one=outside,(node) aks-node-1=outside,c/unk=unknown,d/in=inside,e/idle=no traffic"
	if strings.Join(got, ",") != want {
		t.Errorf("order\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	if !r.RuleConfigured || r.Rule != "allow US, azure:eastus" {
		t.Errorf("rule = %v %q", r.RuleConfigured, r.Rule)
	}
	if n := row(t, r, "(node) aks-node-1"); !n.NodeLevel || n.HostNetwork || n.Egress.Mode != "" {
		t.Errorf("node row = %+v", n)
	}
	if b := row(t, r, "b/two"); b.Outside != 2 || b.Egress.Mode != "unrestricted" {
		t.Errorf("b row = %+v", b)
	}
	c := r.Counts
	if c.Workloads != 7 || c.Destinations != 7 || c.Outside != 5 || c.Unknown != 1 || c.Inside != 1 || c.Countries != 2 {
		t.Errorf("counts = %+v", c)
	}
}

func TestBuildUnknownIgnore(t *testing.T) {
	s := build([]string{"shop/web", "jobs/dark"}, []flow{
		{"shop/web", "us.example.com", ipUS, "allow", 1},
		{"shop/web", "x.example.com", ipUnknown, "allow", 1},
		{"jobs/dark", "z.example.com", ipUnknown, "allow", 1},
	})
	warn := Build(s, seedDB(t), rule("warn", "US"))
	if w := row(t, warn, "shop/web"); w.Status != Unknown || w.Unknown != 1 || warn.Counts.Unknown != 2 {
		t.Errorf("warn: row %+v counts %+v", w, warn.Counts)
	}
	ign := Build(s, seedDB(t), rule("ignore", "US"))
	w := row(t, ign, "shop/web")
	if w.Status != Inside || w.Unknown != 0 || ign.Counts.Unknown != 0 || ign.Counts.Inside != 1 {
		t.Errorf("ignore: row %+v counts %+v", w, ign.Counts)
	}
	for _, d := range w.Destinations {
		if d.Domain == "x.example.com" && d.Status != Unknown {
			t.Errorf("ignored destination still listed as unknown, got %q", d.Status)
		}
	}
	if d := row(t, ign, "jobs/dark"); d.Status != NoTraffic || len(d.Destinations) != 1 {
		t.Errorf("all-ignored row = %+v", d)
	}
	if len(w.Destinations) != 2 || ign.Counts.Destinations != 3 {
		t.Errorf("destinations dropped: %+v", w.Destinations)
	}
}

func TestBuildEmptySnapshot(t *testing.T) {
	for _, cfg := range []config.Config{{}, rule("warn", "US")} {
		r := Build(model.Snapshot{}, seedDB(t), cfg)
		if r.Counts != (Counts{}) {
			t.Errorf("counts = %+v", r.Counts)
		}
		if r.Workloads == nil || len(r.Workloads) != 0 {
			t.Errorf("workloads = %#v", r.Workloads)
		}
		b, err := json.Marshal(r)
		if err != nil || !strings.Contains(string(b), `"workloads":[]`) {
			t.Errorf("json = %s, %v", b, err)
		}
	}
}

func TestBuildSaaSAndJSON(t *testing.T) {
	s := build([]string{"payments/api"}, []flow{{"payments/api", "API.Stripe.com.", "198.51.100.10", "allow", 1}})
	r := Build(s, seedDB(t), rule("warn", "US"))
	d := row(t, r, "payments/api").Destinations[0]
	if d.SaaS != location.IsSaaS("api.stripe.com") {
		t.Errorf("saas = %v", d.SaaS)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"schemaVersion":1`, `"ruleConfigured":true`, `"locationData":`, `"counts":`, `"destinations":`, `"subject":"payments/api"`, `"evidence":"confirmed"`, `"lastSeen":`, `"howKnown":`, `"mode":"unrestricted"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("json missing %s: %s", k, b)
		}
	}
}
