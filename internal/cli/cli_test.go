package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/fixtures"
)

var update = flag.Bool("update", false, "rewrite golden files")

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errb, nil)
	return code, out.String(), errb.String()
}

func writeFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func snapshotFile(t *testing.T, s model.Snapshot) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "snap.json", b)
}

func TestNoSourceErrors(t *testing.T) {
	code, _, stderr := run(t, "scan")
	if code != 1 || !strings.Contains(stderr, "live collection is not available yet; use --demo or --snapshot <file>") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestScanDemoJSON(t *testing.T) {
	code, stdout, _ := run(t, "scan", "--demo")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatal(err)
	}
	if m["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion = %v", m["schemaVersion"])
	}
	cov, ok := m["coverage"].(map[string]any)
	if !ok || cov["level"] != "FULL" || m["locationData"] == "" {
		t.Errorf("coverage/locationData missing: %v", m["coverage"])
	}
	if !strings.HasSuffix(stdout, "}\n") || !strings.Contains(stdout, "\n  \"cluster\"") {
		t.Error("want 2-space indent and trailing newline")
	}
}

func TestScanDemoRuleFailOutside(t *testing.T) {
	if code, _, _ := run(t, "scan", "--demo", "--demo-rule", "--fail-on", "outside"); code != 2 {
		t.Fatalf("code=%d", code)
	}
	// --fail-on may also precede the subcommand's other flags, with -single-dash form.
	if code, _, _ := run(t, "scan", "-demo", "-demo-rule", "-fail-on", "unknown"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestScanFailOnWithoutRule(t *testing.T) {
	code, _, stderr := run(t, "scan", "--demo", "--fail-on", "outside")
	if code != 1 || !strings.Contains(stderr, "--fail-on needs a residency rule in outflow.yaml") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestScanFailOnBadValue(t *testing.T) {
	if code, _, _ := run(t, "scan", "--demo", "--demo-rule", "--fail-on", "inside"); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestScanPartialCoverage(t *testing.T) {
	p := snapshotFile(t, fixtures.DemoDegraded())
	code, stdout, stderr := run(t, "scan", "--snapshot", p, "--demo-rule", "--fail-on", "outside")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "PARTIAL") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, _ = run(t, "scan", "--snapshot", p, "--demo-rule", "--fail-on", "outside", "--allow-partial")
	if code != 2 {
		t.Fatalf("allow-partial code=%d", code)
	}
}

func TestScanPartialCleanWarns(t *testing.T) {
	// Allowing everything seen: a clean result on partial data still warns.
	cfg := writeFile(t, "outflow.yaml", []byte("residency:\n  allow:\n    countries: [US, NL, IE, FR]\n  unknown_location: ignore\n"))
	p := snapshotFile(t, fixtures.DemoDegraded())
	code, _, stderr := run(t, "scan", "--snapshot", p, "--config", cfg, "--fail-on", "outside", "--allow-partial")
	if code != 0 || !strings.Contains(stderr, "warning") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestScanEmptySnapshotFailOn(t *testing.T) {
	p := snapshotFile(t, model.Snapshot{SchemaVersion: model.SchemaVersion})
	for _, extra := range [][]string{nil, {"--allow-partial"}} {
		args := append([]string{"scan", "--snapshot", p, "--demo-rule", "--fail-on", "outside"}, extra...)
		if code, _, _ := run(t, args...); code != 1 {
			t.Fatalf("%v: code=%d, want 1", extra, code)
		}
	}
	if code, _, _ := run(t, "scan", "--snapshot", p); code != 0 {
		t.Fatalf("plain scan of empty snapshot should render, code=%d", code)
	}
}

func TestScanBadConfig(t *testing.T) {
	cfg := writeFile(t, "outflow.yaml", []byte("residency:\n  allow:\n    countries: [usa]\n"))
	code, _, stderr := run(t, "scan", "--demo", "--config", cfg)
	if code != 1 || !strings.Contains(stderr, "countries") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestBadSnapshots(t *testing.T) {
	for name, content := range map[string]string{"garbage": "nope", "version": `{"schemaVersion":99}`} {
		p := writeFile(t, name+".json", []byte(content))
		code, _, stderr := run(t, "scan", "--snapshot", p)
		if code != 1 || !strings.Contains(stderr, name+".json") {
			t.Errorf("%s: code=%d stderr=%q", name, code, stderr)
		}
	}
	if code, _, _ := run(t, "scan", "--snapshot", "/nonexistent/x.json"); code != 1 {
		t.Errorf("missing file code=%d", code)
	}
}

func TestMutualExclusion(t *testing.T) {
	p := snapshotFile(t, fixtures.DemoDegraded())
	if code, _, _ := run(t, "scan", "--demo", "--snapshot", p); code != 1 {
		t.Errorf("demo+snapshot code=%d", code)
	}
	if code, _, _ := run(t, "scan", "--demo", "--demo-degraded"); code != 1 {
		t.Errorf("demo+degraded code=%d", code)
	}
	if code, _, _ := run(t, "scan", "--demo", "--demo-rule", "--config", p); code != 1 {
		t.Errorf("demo-rule+config code=%d", code)
	}
}

func TestScanMarkdownGolden(t *testing.T) {
	code, stdout, _ := run(t, "scan", "--demo", "--demo-rule", "--format", "md")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	golden := filepath.Join("testdata", "scan-demo-rule.md")
	if *update {
		if err := os.WriteFile(golden, []byte(stdout), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(want) {
		t.Errorf("markdown differs from golden:\n%s", stdout)
	}
}

func TestMarkdownNoViolations(t *testing.T) {
	var b bytes.Buffer
	if err := WriteMarkdown(&b, analyze.Report{RuleConfigured: true, Rule: "allow US"}, doctor.Coverage{Level: doctor.Full}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "No destinations outside the allowed locations.") {
		t.Errorf("got %q", b.String())
	}
}

func TestMarkdownNeutral(t *testing.T) {
	_, stdout, _ := run(t, "scan", "--demo", "--format", "md")
	if !strings.Contains(stdout, "rule: none") || !strings.Contains(stdout, "| Status | Workload |") {
		t.Errorf("got %q", stdout)
	}
}

func TestDumpDemo(t *testing.T) {
	code, stdout, _ := run(t, "--dump-demo", "--demo-rule")
	if code != 0 || stdout != string(fixtures.DemoJSON()) {
		t.Fatalf("code=%d", code)
	}
}

func TestDoctorDegradedExit(t *testing.T) {
	code, stdout, _ := run(t, "doctor", "--demo-degraded")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Error("buffer output must be plain")
	}
	if code, _, _ := run(t, "doctor", "--demo"); code != 0 {
		t.Errorf("doctor --demo code=%d", code)
	}
}

func TestTUICalled(t *testing.T) {
	called := false
	tui := func(r analyze.Report, s model.Snapshot, db *location.DB, c config.Config) error {
		called = r.Counts.Workloads > 0 && db != nil
		return nil
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"--demo"}, nil, &out, &errb, tui); code != 0 || !called {
		t.Fatalf("code=%d called=%v stderr=%q", code, called, errb.String())
	}
}

func TestVersionAndHelp(t *testing.T) {
	if _, out, _ := run(t, "version"); out != "outflow dev\n" {
		t.Errorf("version = %q", out)
	}
	for _, a := range []string{"--help", "-h", "scan"} {
		args := []string{a}
		if a == "scan" {
			args = []string{"scan", "-h"}
		}
		code, out, _ := run(t, args...)
		if code != 0 || !strings.Contains(out, "Usage:") || strings.Contains(out, "demo-degraded") {
			t.Errorf("%v: code=%d", args, code)
		}
	}
}

func TestUnknownFlagAndCommand(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"scan", "--bogus"}, {"frobnicate"}, {"scan", "extra"}} {
		code, out, stderr := run(t, args...)
		if code != 1 || out != "" || !strings.Contains(stderr, "Usage:") {
			t.Errorf("%v: code=%d out=%q", args, code, out)
		}
	}
}
