package doctor

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/fixtures"
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
	c := find(t, r, "NodeLocal DNS")
	if c.State != Fail || c.Detail != "workload attribution unavailable" {
		t.Errorf("nodelocal = %+v", c)
	}
	if r.Coverage.Level == Full {
		t.Error("coverage should not be FULL")
	}
}

func TestDoctorPolicyEngine(t *testing.T) {
	s, _ := fixtures.Demo()
	s.Cluster.PolicyEngine = ""
	if c := find(t, Run(s, db(t), config.Config{}, false), "NetworkPolicy engine"); c.State != Warn || c.Why != "policies not enforced" {
		t.Errorf("empty engine = %+v", c)
	}
	s.Cluster.PolicyEngine, s.Cluster.CalicoCRDPolicies = "calico", 2
	if c := find(t, Run(s, db(t), config.Config{}, false), "NetworkPolicy engine"); c.State != Warn {
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
