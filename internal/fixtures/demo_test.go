package fixtures

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/netpol"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
)

func demoReport(t *testing.T, cfg config.Config) analyze.Report {
	t.Helper()
	s, err := Demo()
	if err != nil {
		t.Fatalf("Demo: %v", err)
	}
	db, err := location.Embedded()
	if err != nil {
		t.Fatalf("Embedded: %v", err)
	}
	return analyze.Build(s, db, cfg)
}

func ruledReport(t *testing.T) analyze.Report {
	t.Helper()
	cfg, err := DemoResidency()
	if err != nil {
		t.Fatalf("DemoResidency: %v", err)
	}
	return demoReport(t, cfg)
}

func row(t *testing.T, r analyze.Report, id string) analyze.WorkloadResult {
	t.Helper()
	for _, w := range r.Workloads {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("no row %q", id)
	return analyze.WorkloadResult{}
}

func dest(t *testing.T, w analyze.WorkloadResult, domain, action string) analyze.Destination {
	t.Helper()
	for _, d := range w.Destinations {
		if d.Domain == domain && d.Action == action {
			return d
		}
	}
	t.Fatalf("%s: no destination %q action %q in %+v", w.ID, domain, action, w.Destinations)
	return analyze.Destination{}
}

func TestDemoExpectedStatuses(t *testing.T) {
	rep := ruledReport(t)
	want := []struct {
		id     string
		egress string
		status analyze.Status
	}{
		{"payments/ledger-worker", "unrestricted", analyze.Outside},
		{"analytics/event-collector", "internet", analyze.Outside},
		{"ml/model-fetch", "unrestricted", analyze.Unknown},
		{"payments/payment-api", "internet:443", analyze.Inside},
		{"flux-system/source-controller", "unrestricted", analyze.Inside},
		{"shop/storefront", "allowlist:443", analyze.Inside},
		{"shop/cart", "internal", analyze.NoTraffic},
		{"monitoring/node-exporter", "hostNetwork", analyze.NoTraffic},
		{"(node) aks-np1-vmss0003", "", analyze.Outside},
	}
	if len(rep.Workloads) != len(want) {
		ids := []string{}
		for _, w := range rep.Workloads {
			ids = append(ids, w.ID+"="+string(w.Status))
		}
		t.Fatalf("got %d rows %v, want %d", len(rep.Workloads), ids, len(want))
	}
	for _, c := range want {
		w := row(t, rep, c.id)
		if w.Status != c.status {
			t.Errorf("%s: status %q, want %q (destinations %+v)", c.id, w.Status, c.status, w.Destinations)
		}
		if c.egress != "" && w.Egress.String() != c.egress {
			t.Errorf("%s: egress %q, want %q", c.id, w.Egress.String(), c.egress)
		}
	}

	ledger := row(t, rep, "payments/ledger-worker")
	if d := dest(t, ledger, "stledgerweu.blob.core.windows.net", "allow"); d.Status != analyze.Outside || d.Evidence != analyze.Confirmed {
		t.Errorf("ledger blob: %q %q", d.Status, d.Evidence)
	}
	if d := dest(t, ledger, "login.microsoftonline.com", "allow"); d.Status != analyze.Unknown || !d.Location.Global {
		t.Errorf("ledger login: %q %+v", d.Status, d.Location)
	}

	ec := row(t, rep, "analytics/event-collector")
	if d := dest(t, ec, "api.eu.segment.io", "allow"); d.Status != analyze.Outside || d.Evidence != analyze.Confirmed {
		t.Errorf("event-collector eu: %q %q", d.Status, d.Evidence)
	}
	if d := dest(t, ec, "api.segment.io", "allow"); d.Status != analyze.Inside || d.Evidence != analyze.Confirmed {
		t.Errorf("event-collector us: %q %q", d.Status, d.Evidence)
	}
	if d := dest(t, ec, "telemetry.collector.example", "deny"); d.Status != analyze.OutsideDenied {
		t.Errorf("event-collector deny: %q", d.Status)
	}
	if ec.Outside != 1 {
		t.Errorf("event-collector Outside = %d, want 1 (deny must not count)", ec.Outside)
	}

	mf := row(t, rep, "ml/model-fetch")
	if d := dest(t, mf, "models.fetch-hub.example", ""); d.Evidence != analyze.DNSOnly || d.Location.Country != "IE" || d.Status != analyze.Unknown {
		t.Errorf("model-fetch DNS only: %q %q %+v", d.Evidence, d.Status, d.Location)
	}
	if mf.Outside != 0 || mf.Unknown != 3 {
		t.Errorf("model-fetch Outside/Unknown = %d/%d, want 0/3", mf.Outside, mf.Unknown)
	}

	pay := row(t, rep, "payments/payment-api")
	if d := dest(t, pay, "api.stripe.com", "allow"); d.Exception != "Payment processor, DPA signed" || !d.SaaS || d.Evidence != analyze.Confirmed {
		t.Errorf("stripe: exception %q saas %v evidence %q", d.Exception, d.SaaS, d.Evidence)
	}
	if d := dest(t, pay, "hooks.slack.com", "allow"); d.Status != analyze.Inside || d.Evidence != analyze.Confirmed {
		t.Errorf("slack: %q %q", d.Status, d.Evidence)
	}

	sc := row(t, rep, "flux-system/source-controller")
	for _, dom := range []string{"github.com", "ghcr.io"} {
		if d := dest(t, sc, dom, "allow"); d.Status != analyze.Inside || d.Evidence != analyze.Confirmed {
			t.Errorf("source-controller %s: %q %q", dom, d.Status, d.Evidence)
		}
	}

	sf := row(t, rep, "shop/storefront")
	if d := dest(t, sf, "stshopassets.blob.core.windows.net", "allow"); d.Location.HowKnown != location.CloudRange || d.Evidence != analyze.Confirmed {
		t.Errorf("storefront blob: %+v %q", d.Location, d.Evidence)
	}
	dest(t, sf, "api.segment.io", "deny")

	ne := row(t, rep, "monitoring/node-exporter")
	if !ne.HostNetwork || ne.Egress.Mode != netpol.HostNetwork || len(ne.Destinations) != 0 {
		t.Errorf("node-exporter: hostNetwork %v mode %q dests %d", ne.HostNetwork, ne.Egress.Mode, len(ne.Destinations))
	}

	node := row(t, rep, "(node) aks-np1-vmss0003")
	if !node.NodeLevel {
		t.Error("node row not NodeLevel")
	}
	var ntp *analyze.Destination
	for i, d := range node.Destinations {
		if d.IP == "51.15.22.140" {
			ntp = &node.Destinations[i]
		}
	}
	if ntp == nil || ntp.Domain != "" || ntp.Status != analyze.Outside || ntp.Location.HowKnown != location.GeoIP || ntp.Evidence != analyze.NodeLevel {
		t.Errorf("node 51.15.22.140: %+v", ntp)
	}
	if d := dest(t, node, "packages.microsoft.com", "allow"); d.Evidence != analyze.NodeLevel {
		t.Errorf("node lookup evidence %q, want node-level", d.Evidence)
	}
	if node.Outside != 1 {
		t.Errorf("node Outside = %d, want 1", node.Outside)
	}
}

func TestDemoLikely(t *testing.T) {
	rep := ruledReport(t)
	pair := map[string]string{
		"payments/payment-api":          "flux-system/source-controller",
		"flux-system/source-controller": "payments/payment-api",
	}
	for id, other := range pair {
		d := dest(t, row(t, rep, id), "api.github.com", "allow")
		if d.Evidence != analyze.Likely || !slices.Equal(d.AlsoLikely, []string{other}) {
			t.Errorf("%s api.github.com: evidence %q alsoLikely %v, want likely with %s", id, d.Evidence, d.AlsoLikely, other)
		}
	}
	if d := dest(t, row(t, rep, "payments/ledger-worker"), "api.github.com", ""); d.Evidence != analyze.DNSOnly || d.Status != analyze.Inside {
		t.Errorf("ledger api.github.com: %q %q, want DNS only inside", d.Evidence, d.Status)
	}
}

func TestDemoNeutral(t *testing.T) {
	rep := demoReport(t, config.Config{})
	if rep.RuleConfigured {
		t.Error("RuleConfigured with empty config")
	}
	for _, w := range rep.Workloads {
		if w.Status != analyze.Neutral {
			t.Errorf("%s: status %q, want neutral", w.ID, w.Status)
		}
		for _, d := range w.Destinations {
			if d.Status != analyze.Neutral {
				t.Errorf("%s %s: status %q, want neutral", w.ID, d.Domain, d.Status)
			}
		}
	}
}

func TestDemoUsesNoRealData(t *testing.T) {
	s, err := Demo()
	if err != nil {
		t.Fatal(err)
	}
	private := netip.MustParsePrefix("10.0.0.0/8")
	in := func(what, ip string) {
		a, err := netip.ParseAddr(ip)
		if err != nil || !private.Contains(a) {
			t.Errorf("%s IP %q not in 10.0.0.0/8", what, ip)
		}
	}
	for _, n := range s.Nodes {
		for _, ip := range n.IPs {
			in("node "+n.Name, ip)
		}
	}
	for _, p := range s.Pods {
		in("pod "+p.Name, p.IP)
	}
	for _, l := range s.DNSLookups {
		in("lookup", l.PodIP)
	}
	c := s.Cluster
	if c.Name != "aks-prod-eus" || c.Region != "eastus" || c.Firewall != "fw-hub-eus" {
		t.Errorf("cluster %+v is not the fictional demo cluster", c)
	}
}

func TestDemoSnapshotShape(t *testing.T) {
	s, err := Demo()
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != model.SchemaVersion {
		t.Errorf("schemaVersion %d", s.SchemaVersion)
	}
	end := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	if !s.Window.End.Equal(end) || !s.Window.Start.Equal(end.Add(-24*time.Hour)) {
		t.Errorf("window %+v", s.Window)
	}
	want := []string{"k8s-api", "firewall-logs", "coredns-log-plugin", "coredns-logs-collected", "pod-inventory"}
	if len(s.Sources) != len(want) {
		t.Fatalf("sources %+v", s.Sources)
	}
	for i, src := range s.Sources {
		if src.ID != want[i] || src.State != "ok" {
			t.Errorf("source %d = %s/%s, want %s/ok", i, src.ID, src.State, want[i])
		}
	}
	inWindow := func(what string, bin time.Time) {
		if bin.Before(s.Window.Start) || !bin.Before(s.Window.End) || !bin.Equal(bin.Truncate(time.Minute)) {
			t.Errorf("%s bin %s outside window or not minute-aligned", what, bin)
		}
	}
	for _, l := range s.DNSLookups {
		inWindow("lookup "+l.Domain, l.Bin)
	}
	for _, e := range s.EgressEvents {
		inWindow("event "+e.Domain+e.DestIP, e.Bin)
	}
	for _, r := range s.Resolutions {
		if r.Source != "outflow" {
			t.Errorf("resolution %s source %q", r.Domain, r.Source)
		}
	}
	var back model.Snapshot
	if err := json.Unmarshal(DemoJSON(), &back); err != nil {
		t.Fatalf("DemoJSON: %v", err)
	}
	if back.Cluster.Name != s.Cluster.Name || len(back.Pods) != len(s.Pods) {
		t.Error("DemoJSON does not match Demo")
	}
}

func TestDemoDegraded(t *testing.T) {
	d := DemoDegraded()
	if len(d.DNSLookups) != 0 {
		t.Errorf("degraded has %d DNS lookups, want 0", len(d.DNSLookups))
	}
	states := map[string]string{}
	for _, src := range d.Sources {
		states[src.ID] = src.State
	}
	want := map[string]string{
		"k8s-api": "ok", "firewall-logs": "ok", "pod-inventory": "ok",
		"coredns-log-plugin": "missing", "coredns-logs-collected": "missing",
	}
	for id, st := range want {
		if states[id] != st {
			t.Errorf("degraded source %s = %q, want %q", id, states[id], st)
		}
	}
	s, err := Demo()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.DNSLookups) == 0 || s.Sources[2].State != "ok" {
		t.Error("DemoDegraded mutated the shared demo")
	}
}
