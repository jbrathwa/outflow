package doctor

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/fixtures"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func db(t *testing.T) *location.DB {
	t.Helper()
	d, err := location.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func demoCfg(t *testing.T) config.Config {
	t.Helper()
	c, err := fixtures.DemoResidency()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func find(t *testing.T, r Result, name string) Check {
	t.Helper()
	for _, g := range r.Groups {
		for _, c := range g.Checks {
			if c.Name == name {
				return c
			}
		}
	}
	t.Fatalf("check %q not found", name)
	return Check{}
}

func TestDoctorDemoFull(t *testing.T) {
	s, err := fixtures.Demo()
	if err != nil {
		t.Fatal(err)
	}
	r := Run(s, db(t), demoCfg(t), true)
	if r.Coverage.Level != Full || r.Failed() {
		t.Fatalf("level=%s failed=%v, want FULL/false", r.Coverage.Level, r.Failed())
	}
	if r.Profile != "azure" {
		t.Errorf("profile = %q", r.Profile)
	}
	if d := find(t, r, "Rule").Detail; d != "outflow.yaml · allow US · 1 exception" {
		t.Errorf("rule detail = %q", d)
	}
}

func TestDoctorDegraded(t *testing.T) {
	r := Run(fixtures.DemoDegraded(), db(t), demoCfg(t), true)
	if c := find(t, r, "CoreDNS query logs"); c.State != Fail || c.Fix == "" {
		t.Errorf("coredns check = %+v", c)
	}
	if r.Coverage.Level != Partial {
		t.Errorf("level = %s", r.Coverage.Level)
	}
	if !strings.Contains(strings.Join(r.Coverage.Cannot, "\n"), "which workload") {
		t.Errorf("cannot = %v", r.Coverage.Cannot)
	}
}

func TestDoctorOverlayRequiresDNS(t *testing.T) {
	r := Run(fixtures.DemoDegraded(), db(t), demoCfg(t), true) // azure-cni-overlay
	if !r.Failed() {
		t.Fatal("overlay CNI without DNS logs must fail")
	}
	if !find(t, r, "CoreDNS query logs").Required || !find(t, r, "Log collection").Required {
		t.Error("coredns checks should be required on overlay")
	}
}

func TestDoctorNonOverlayDNSOptional(t *testing.T) {
	s := fixtures.DemoDegraded()
	s.Cluster.CNI = "azure"
	r := Run(s, db(t), demoCfg(t), true)
	if r.Failed() {
		t.Fatal("firewall logs ok on a non-SNAT CNI should pass")
	}
	if find(t, r, "CoreDNS query logs").Required {
		t.Error("coredns should be optional here")
	}
	// Neither source available: both become required.
	for i := range s.Sources {
		if s.Sources[i].ID == "firewall-logs" {
			s.Sources[i].State = "missing"
		}
	}
	if !Run(s, db(t), demoCfg(t), true).Failed() {
		t.Error("no traffic source should fail")
	}
}

func TestDoctorNoSources(t *testing.T) {
	r := Run(model.Snapshot{}, db(t), config.Config{}, false)
	if r.Coverage.Level != None || !r.Failed() {
		t.Fatalf("level=%s failed=%v", r.Coverage.Level, r.Failed())
	}
	if c := find(t, r, "Kubernetes API"); c.State != Fail || c.Detail != "not checked" || !c.Required {
		t.Errorf("api = %+v", c)
	}
	if r.Profile != "generic" {
		t.Errorf("profile = %q", r.Profile)
	}
}

func TestDoctorNodeLocalDNS(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.NodeLocalDNS = true
	r := Run(s, db(t), demoCfg(t), true)
	c := find(t, r, "Local DNS cache")
	if c.State != Fail || c.Detail != "NodeLocal DNSCache found" {
		t.Errorf("nodelocal = %+v", c)
	}
	if r.Coverage.Level != Partial {
		t.Errorf("level = %s, want PARTIAL", r.Coverage.Level)
	}
	if strings.Contains(strings.Join(r.Coverage.Can, "\n"), "which workload made") {
		t.Errorf("can must not claim attribution: %v", r.Coverage.Can)
	}
	if !strings.Contains(strings.Join(r.Coverage.Cannot, "\n"), "which workload made") {
		t.Errorf("cannot = %v", r.Coverage.Cannot)
	}
}

func TestDoctorNodeLocalDNSRequiredWhenSNAT(t *testing.T) {
	s, _ := fixtures.Demo() // azure-cni-overlay: pods leave with node IPs
	s.Cluster.NodeLocalDNS = true
	r := Run(s, db(t), demoCfg(t), true)
	if !find(t, r, "Local DNS cache").Required || !r.Failed() {
		t.Error("NodeLocal DNS on an overlay CNI hides attribution and must fail doctor")
	}
	s.Cluster.CNI = "azure"
	r = Run(s, db(t), demoCfg(t), true)
	if find(t, r, "Local DNS cache").Required || r.Failed() {
		t.Error("NodeLocal DNS on a non-SNAT CNI only degrades coverage")
	}
}

func TestDoctorPolicyEngine(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.PolicyEngine = ""
	if c := find(t, Run(s, db(t), config.Config{}, false), "NetworkPolicy engine"); c.State != Warn || c.Why != "policies not enforced" {
		t.Errorf("empty engine = %+v", c)
	}
	s.Cluster.PolicyEngine, s.Cluster.CalicoCRDPolicies = "calico", 2
	if c := find(t, Run(s, db(t), config.Config{}, false), "NetworkPolicy engine"); c.State != Warn || c.Why != "egress modes may be incomplete" {
		t.Errorf("calico CRD = %+v", c)
	}
}

func TestDoctorResidencyNotConfiguredIsInfo(t *testing.T) {
	s, _ := fixtures.Demo()
	r := Run(s, db(t), config.Config{}, false)
	if c := find(t, r, "Rule"); c.State != Info || c.Detail != "not configured" {
		t.Errorf("rule = %+v", c)
	}
	if r.Failed() {
		t.Error("unconfigured residency must not fail")
	}
}

func TestPrintPlainGolden(t *testing.T) {
	r := Run(fixtures.DemoDegraded(), db(t), demoCfg(t), true)
	var buf bytes.Buffer
	Print(&buf, r, false)
	if strings.Contains(buf.String(), "\x1b") {
		t.Error("plain output contains ANSI codes")
	}
	const golden = "testdata/degraded.txt"
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("output differs from %s\n--- got\n%s\n--- want\n%s", golden, buf.String(), want)
	}
}

func TestPrintColor(t *testing.T) {
	r := Run(fixtures.DemoDegraded(), db(t), demoCfg(t), true)
	var buf bytes.Buffer
	Print(&buf, r, true)
	if !strings.Contains(buf.String(), "\x1b[31m✖") {
		t.Error("expected red fail glyph")
	}
}

func setState(s *model.Snapshot, id, state string) {
	for i := range s.Sources {
		if s.Sources[i].ID == id {
			s.Sources[i].State = state
		}
	}
}

func TestDoctorWarnSourcesAreAvailable(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.CNI = "azure"
	setState(&s, "firewall-logs", "warn")
	setState(&s, "coredns-log-plugin", "warn")
	setState(&s, "coredns-logs-collected", "warn")
	r := Run(s, db(t), demoCfg(t), true)
	if r.Failed() || r.Coverage.Level != Partial {
		t.Errorf("non-SNAT warn: failed=%v level=%s", r.Failed(), r.Coverage.Level)
	}

	s, _ = fixtures.Demo() // overlay
	setState(&s, "coredns-log-plugin", "warn")
	r = Run(s, db(t), demoCfg(t), true)
	if r.Failed() || r.Coverage.Level != Partial {
		t.Errorf("overlay warn: failed=%v level=%s", r.Failed(), r.Coverage.Level)
	}
}

func TestDoctorPartialExplainsOutboundAndInventory(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.OutboundType = "loadBalancer"
	r := Run(s, db(t), demoCfg(t), true)
	if r.Coverage.Level != Partial || !strings.Contains(strings.Join(r.Coverage.Cannot, "\n"), "skips the firewall") {
		t.Errorf("outbound: level=%s cannot=%v", r.Coverage.Level, r.Coverage.Cannot)
	}

	s, _ = fixtures.Demo()
	setState(&s, "pod-inventory", "missing")
	r = Run(s, db(t), demoCfg(t), true)
	if r.Coverage.Level != Partial || !strings.Contains(strings.Join(r.Coverage.Cannot, "\n"), "pods that no longer exist") {
		t.Errorf("inventory: level=%s cannot=%v", r.Coverage.Level, r.Coverage.Cannot)
	}
}

func TestDoctorAKSLocalDNSFails(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.AKSLocalDNS = true
	r := Run(s, db(t), demoCfg(t), true)
	c := find(t, r, "Local DNS cache")
	if c.State != Fail || c.Detail != "AKS LocalDNS enabled" || !c.Required || !r.Failed() {
		t.Errorf("aks localdns = %+v failed=%v", c, r.Failed())
	}
	if !strings.Contains(c.Why, "hides which pod sent each query") {
		t.Errorf("why = %q", c.Why)
	}
	if !strings.Contains(strings.Join(r.Coverage.Cannot, "\n"), "a node-local DNS cache hides the client pod") {
		t.Errorf("cannot = %v", r.Coverage.Cannot)
	}
}

func TestDoctorNodeLocalStillFails(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.NodeLocalDNS = true
	r := Run(s, db(t), demoCfg(t), true)
	c := find(t, r, "Local DNS cache")
	if c.State != Fail || c.Detail != "NodeLocal DNSCache found" {
		t.Errorf("nodelocal = %+v", c)
	}
}

func TestDoctorLogFormatWarnIsPartial(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Sources = append(s.Sources, model.SourceStatus{ID: model.SrcCoreDNSFormat, State: "warn", Detail: "2 of 20 lines unparsed"})
	r := Run(s, db(t), demoCfg(t), true)
	c := find(t, r, "CoreDNS log format")
	if c.State != Warn || c.Required {
		t.Errorf("format = %+v", c)
	}
	if r.Coverage.Level != Partial {
		t.Errorf("level = %s, want PARTIAL", r.Coverage.Level)
	}
	if !strings.Contains(strings.Join(r.Coverage.Cannot, "\n"), "some CoreDNS lines may be missed → check the CoreDNS log format") {
		t.Errorf("cannot = %v", r.Coverage.Cannot)
	}
	// order: right after Log collection
	var names []string
	for _, ck := range r.Groups[1].Checks {
		names = append(names, ck.Name)
	}
	for i, n := range names {
		if n == "Log collection" && names[i+1] != "CoreDNS log format" {
			t.Errorf("order = %v", names)
		}
	}
	s2, _ := fixtures.Demo()
	r2 := Run(s2, db(t), demoCfg(t), true)
	for _, g := range r2.Groups {
		for _, ck := range g.Checks {
			if ck.Name == "CoreDNS log format" || ck.Name == "AKS resource" {
				t.Errorf("%s must be absent without its source", ck.Name)
			}
		}
	}
}

func TestDoctorAKSResource(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Sources = append(s.Sources, model.SourceStatus{ID: model.SrcAKS, State: "missing", Detail: "no Reader on the AKS resource"})
	r := Run(s, db(t), demoCfg(t), true)
	c := find(t, r, "AKS resource")
	if c.State != Fail || c.Required {
		t.Errorf("aks = %+v", c)
	}
	g := r.Groups[0]
	if g.Name != "Cluster" || g.Checks[len(g.Checks)-1].Name != "AKS resource" {
		t.Errorf("AKS resource must end the Cluster group: %+v", g.Checks)
	}
	if r.Coverage.Level != Full {
		t.Errorf("level = %s", r.Coverage.Level)
	}
}

func TestDoctorAccessFromSources(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Sources = append(s.Sources,
		model.SourceStatus{ID: model.SrcK8sAccess, State: "ok", Detail: "all read permissions granted"},
		model.SourceStatus{ID: model.SrcAzureAccess, State: "missing", Detail: "cannot query the workspace"})
	r := Run(s, db(t), demoCfg(t), true)
	k := find(t, r, "Kubernetes")
	a := find(t, r, "Azure")
	if k.State != OK || !k.Required {
		t.Errorf("k8s = %+v", k)
	}
	if a.State != Fail || a.Required || !strings.Contains(a.Fix, "Log Analytics Reader on the workspace (docs/setup.md#roles)") {
		t.Errorf("azure = %+v", a)
	}
	if r.Failed() {
		t.Error("azure access must not fail doctor")
	}
}

func TestDoctorAccessAbsentIsInfo(t *testing.T) {
	s, _ := fixtures.Demo()
	r := Run(s, db(t), demoCfg(t), true)
	g := r.Groups[len(r.Groups)-1]
	if g.Name != "Access" || len(g.Checks) != 1 || g.Checks[0].State != Info || g.Checks[0].Detail != "checked in live mode" {
		t.Errorf("access = %+v", g)
	}
	s.Sources = append(s.Sources, model.SourceStatus{ID: model.SrcK8sAccess, State: "ok", Detail: "ok"})
	r = Run(s, db(t), demoCfg(t), true)
	if az := find(t, r, "Azure"); az.State != Info || az.Detail != "not checked" {
		t.Errorf("azure = %+v", az)
	}
}

func TestDoctorK8sAccessMissingFails(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Sources = append(s.Sources, model.SourceStatus{ID: model.SrcK8sAccess, State: "missing", Detail: "cannot list pods"})
	r := Run(s, db(t), demoCfg(t), true)
	k := find(t, r, "Kubernetes")
	if k.State != Fail || !k.Required || !r.Failed() || !strings.Contains(k.Fix, "apply deploy/clusterrole.yaml (docs/usage.md#permissions)") {
		t.Errorf("k8s = %+v failed=%v", k, r.Failed())
	}
}

func TestDoctorOutboundUnknownIsNotChecked(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.OutboundType = ""
	c := find(t, Run(s, db(t), demoCfg(t), true), "Outbound path")
	if c.State != Warn || c.Detail != "not checked (AKS resource not read)" || c.Without != "" || c.Fix != "" {
		t.Errorf("outbound = %+v", c)
	}
	s.Cluster.Platform = ""
	c = find(t, Run(s, db(t), demoCfg(t), true), "Outbound path")
	if c.State != Warn || c.Detail != "not checked" || c.Without != "" || c.Fix != "" {
		t.Errorf("generic outbound = %+v", c)
	}
	s.Cluster.OutboundType = "loadBalancer"
	c = find(t, Run(s, db(t), demoCfg(t), true), "Outbound path")
	if c.State != Warn || c.Detail != "loadBalancer" || c.Fix == "" {
		t.Errorf("loadBalancer outbound = %+v", c)
	}
}

func TestDoctorAzureAccessFixNamesTheRole(t *testing.T) {
	const ws = "grant Log Analytics Reader on the workspace (docs/setup.md#roles)"
	const aks = "grant Reader on the AKS resource (docs/setup.md#roles)"
	for detail, want := range map[string]string{
		"no access to AKS resource (Reader)":                                               aks,
		"no access to workspace …000b (needs Log Analytics Reader)":                        ws,
		"no access to workspace …000b (needs Log Analytics Reader), AKS resource (Reader)": ws + "; " + aks,
		"no successful Azure calls":                                                        ws,
	} {
		s, _ := fixtures.Demo()
		s.Sources = append(s.Sources, model.SourceStatus{ID: model.SrcAzureAccess, State: "missing", Detail: detail})
		if a := find(t, Run(s, db(t), demoCfg(t), true), "Azure"); a.Fix != want {
			t.Errorf("%q: fix %q, want %q", detail, a.Fix, want)
		}
	}
}

func TestDoctorFirewallNoAccessFix(t *testing.T) {
	s, _ := fixtures.Demo()
	for i := range s.Sources {
		if s.Sources[i].ID == model.SrcFirewall {
			s.Sources[i].State, s.Sources[i].Detail = "missing", "no access to workspace …000a (needs Log Analytics Reader)"
		}
	}
	if fw := find(t, Run(s, db(t), demoCfg(t), true), "Firewall logs"); fw.Fix != "grant Log Analytics Reader on the workspace (docs/setup.md#roles)" {
		t.Errorf("fix %q", fw.Fix)
	}
}

func TestDoctorProfileFromSnapshot(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Profile = "generic" // e.g. --profile generic on an AKS cluster
	if r := Run(s, db(t), demoCfg(t), true); r.Profile != "generic" {
		t.Errorf("profile = %q", r.Profile)
	}
}

func demoSnap(t *testing.T) model.Snapshot {
	t.Helper()
	s, err := fixtures.Demo()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func locGroup(t *testing.T, r Result) []Check {
	t.Helper()
	for _, g := range r.Groups {
		if g.Name == "Location" {
			return g.Checks
		}
	}
	t.Fatal("no Location group")
	return nil
}

func TestDoctorLocationRowsSample(t *testing.T) {
	rows := locGroup(t, Run(demoSnap(t), db(t), demoCfg(t), true))
	if len(rows) != 5 {
		t.Fatalf("got %d rows", len(rows))
	}
	for _, c := range rows {
		if c.State != Warn || c.Detail != "built-in sample only" || c.Fix == "" || c.Without == "" {
			t.Errorf("row = %+v", c)
		}
	}
	if rows[0].Name != "Azure service tags" || rows[4].Name != "DB-IP country database" {
		t.Errorf("names = %q .. %q", rows[0].Name, rows[4].Name)
	}
}

func partsDB(t *testing.T, azureAge int, geoAge int, geoStale bool) *location.DB {
	t.Helper()
	d, err := location.Build([]location.Part{
		{Info: location.SourceInfo{Name: "azure", Version: "20261102", AgeDays: azureAge}, FS: fstest.MapFS{}},
		{Info: location.SourceInfo{Name: "geo", Version: "202610", AgeDays: geoAge, Stale: geoStale}, FS: fstest.MapFS{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDoctorLocationRowsFreshAndStale(t *testing.T) {
	rows := locGroup(t, Run(demoSnap(t), partsDB(t, 6, 40, true), demoCfg(t), true))
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if c := rows[0]; c.State != OK || c.Detail != "20261102 · 6 days old" || c.Why != "" || c.Fix != "" {
		t.Errorf("fresh = %+v", c)
	}
	c := rows[1]
	if c.State != Warn || c.Detail != "202610 · 40 days old" || c.Why != "older than 30 days" ||
		c.Fix != "run a live scan or outflow update-data" {
		t.Errorf("stale = %+v", c)
	}
}

func TestDoctorLocationNotLoaded(t *testing.T) {
	rows := locGroup(t, Run(demoSnap(t), nil, demoCfg(t), true))
	if len(rows) != 1 || rows[0].Name != "Location data" || rows[0].State != Fail || rows[0].Detail != "not loaded" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestLocationRowsDoNotChangeCoverage(t *testing.T) {
	s := demoSnap(t)
	sample := Run(s, db(t), demoCfg(t), true)
	fresh := Run(s, partsDB(t, 1, 1, false), demoCfg(t), true)
	if sample.Coverage.Level != fresh.Coverage.Level {
		t.Errorf("sample %s vs fresh %s", sample.Coverage.Level, fresh.Coverage.Level)
	}
}

func TestPrintStaleLocationRows(t *testing.T) {
	d, err := location.Build([]location.Part{
		{Info: location.SourceInfo{Name: "azure", Version: "20261030", AgeDays: 9, Stale: true}, FS: fstest.MapFS{}},
		{Info: location.SourceInfo{Name: "geo", Version: "202610", AgeDays: 40, Stale: true}, FS: fstest.MapFS{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	Print(&buf, Run(demoSnap(t), d, demoCfg(t), true), false)
	out := buf.String()
	for _, want := range []string{"20261030 · 9 days old", "older than 7 days", "older than 30 days",
		"fix: run a live scan or outflow update-data"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
