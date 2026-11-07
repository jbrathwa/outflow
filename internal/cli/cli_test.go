package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/fixtures"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
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

func TestScanJSONFlag(t *testing.T) {
	_, want, _ := run(t, "scan", "--demo", "--format", "json")
	for _, args := range [][]string{{"--json"}, {"--json", "--format", "json"}} {
		code, stdout, stderr := run(t, append([]string{"scan", "--demo"}, args...)...)
		if code != 0 || stdout != want {
			t.Errorf("%v: code=%d stderr=%q, want the --format json output", args, code, stderr)
		}
	}
	for _, args := range [][]string{{"--json", "--format", "md"}, {"--format", "md", "--json"}} {
		code, stdout, stderr := run(t, append([]string{"scan", "--demo"}, args...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "--json conflicts with --format md") {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
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
	if code, _, stderr := run(t, "scan", "--demo", "--demo-degraded"); code != 1 || !strings.Contains(stderr, "--demo and --demo-degraded are mutually exclusive") {
		t.Errorf("demo+degraded code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := run(t, "scan", "--demo-degraded", "--snapshot", p); code != 1 || !strings.Contains(stderr, "--demo-degraded and --snapshot are mutually exclusive") {
		t.Errorf("degraded+snapshot code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := run(t, "scan", "--demo", "--demo-degraded", "--snapshot", p); code != 1 || !strings.Contains(stderr, "--demo, --demo-degraded and --snapshot are mutually exclusive") {
		t.Errorf("all three code=%d stderr=%q", code, stderr)
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

func TestMarkdownRowsWorstFirst(t *testing.T) {
	_, stdout, _ := run(t, "scan", "--demo", "--demo-rule", "--format", "md")
	blob := strings.Index(stdout, "| outside | payments/ledger-worker | stledgerweu")
	login := strings.Index(stdout, "| unknown | payments/ledger-worker | login.microsoftonline.com")
	if blob < 0 || login < 0 || blob > login {
		t.Errorf("ledger-worker rows not worst first (outside before unknown):\n%s", stdout)
	}
	// unknown rows tie on status: more hits first.
	hf := strings.Index(stdout, "| ml/model-fetch | huggingface.co")
	gcs := strings.Index(stdout, "| ml/model-fetch | storage.googleapis.com")
	dns := strings.Index(stdout, "| ml/model-fetch | models.fetch-hub.example")
	if hf < 0 || gcs < 0 || dns < 0 || hf > gcs || gcs > dns {
		t.Errorf("model-fetch rows not ordered by hits:\n%s", stdout)
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
	if !strings.Contains(stdout, "rule: none") || !strings.Contains(stdout, "| Locations | Workload |") {
		t.Errorf("got %q", stdout)
	}
	var b bytes.Buffer
	if err := WriteMarkdown(&b, analyze.Report{}, doctor.Coverage{Level: doctor.Full}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "No external destinations observed.") {
		t.Errorf("empty neutral report: %q", b.String())
	}
}

func TestMarkdownActionColumn(t *testing.T) {
	_, stdout, _ := run(t, "scan", "--demo", "--format", "md")
	if !strings.Contains(stdout, "| Evidence | Action |\n|---|---|---|---|---|---|\n") {
		t.Errorf("no Action column:\n%s", stdout)
	}
	for _, want := range []string{
		"| shop/storefront | api.segment.io (54.145.112.20):443 | US (aws:us-east-1) | confirmed | deny |",
		"| shop/storefront | stshopassets.blob.core.windows.net (20.60.128.36):443 | US (azure:eastus) | confirmed | allow |",
		"| models.fetch-hub.example (52.210.17.44) | IE (aws:eu-west-1) | DNS only | — |",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing row %q:\n%s", want, stdout)
		}
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

func TestVersionString(t *testing.T) {
	cases := []struct {
		version, commit, date, module, want string
	}{
		{"dev", "", "", "", "outflow dev"},
		{"dev", "", "", "(devel)", "outflow dev"},
		{"dev", "", "", "v0.2.0", "outflow 0.2.0"},
		{"0.1.0", "2b0a094", "2026-11-08", "", "outflow 0.1.0 (2b0a094, 2026-11-08)"},
		{"0.1.0", "2b0a094", "", "", "outflow 0.1.0 (2b0a094)"},
		{"v0.1.0", "", "", "", "outflow 0.1.0"},
	}
	for _, c := range cases {
		if got := versionString(c.version, c.commit, c.date, c.module); got != c.want {
			t.Errorf("versionString(%q,%q,%q,%q) = %q, want %q", c.version, c.commit, c.date, c.module, got, c.want)
		}
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

// unknownOnlyConfig allows every country the demo reaches, so outside = 0 and only
// unknown (global / DNS-only) destinations remain, at FULL coverage.
func unknownOnlyConfig(t *testing.T, unknown string) string {
	t.Helper()
	y := "residency:\n  allow:\n    countries: [US, NL, IE, FR]\n"
	if unknown != "" {
		y += "  unknown_location: " + unknown + "\n"
	}
	return writeFile(t, "outflow.yaml", []byte(y))
}

func TestUnknownGates(t *testing.T) {
	warn := unknownOnlyConfig(t, "")
	if code, _, e := run(t, "scan", "--demo", "--config", warn, "--fail-on", "outside"); code != 0 {
		t.Fatalf("outside with only unknowns: code=%d %s", code, e)
	}
	if code, _, _ := run(t, "scan", "--demo", "--config", warn, "--fail-on", "unknown"); code != 2 {
		t.Fatalf("--fail-on unknown: code=%d, want 2", code)
	}
	fail := unknownOnlyConfig(t, "fail")
	if code, _, _ := run(t, "scan", "--demo", "--config", fail, "--fail-on", "outside"); code != 2 {
		t.Fatalf("unknown_location: fail: code=%d, want 2", code)
	}
}

func TestFailOnUnknownConflictsWithIgnore(t *testing.T) {
	cfg := unknownOnlyConfig(t, "ignore")
	code, out, stderr := run(t, "scan", "--demo", "--config", cfg, "--fail-on", "unknown")
	if code != 1 || out != "" || !strings.Contains(stderr, "--fail-on unknown conflicts with unknown_location: ignore") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestDumpDemoWriteError(t *testing.T) {
	var errb bytes.Buffer
	if code := Run([]string{"--dump-demo"}, nil, failWriter{}, &errb, nil); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestNoColorNeedsValue(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if noColor() {
		t.Error("empty NO_COLOR must not disable colour")
	}
	t.Setenv("NO_COLOR", "1")
	if !noColor() {
		t.Error("NO_COLOR=1 must disable colour")
	}
}
