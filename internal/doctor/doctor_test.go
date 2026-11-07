package doctor

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"

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
