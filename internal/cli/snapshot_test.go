package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/model"
)

func TestSnapshotWritesFile0600(t *testing.T) {
	withLive(t, aksState("AKS"), true)
	p := filepath.Join(t.TempDir(), "snap.json")
	code, stdout, stderr := run(t, "snapshot", "-o", p, "--workspace", wsA)
	if code != 0 || stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if want := "wrote " + p + " (1 workloads, 2 lookups, 0 firewall events)\n"; stderr != want {
		t.Errorf("stderr=%q want %q", stderr, want)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", st.Mode().Perm(), err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasSuffix(string(b), "}\n") || !strings.Contains(string(b), "\n  \"schemaVersion\": 1") {
		t.Errorf("want indented JSON with trailing newline")
	}
	if code, out, e := run(t, "scan", "--snapshot", p); code != 0 || !strings.Contains(out, "shop/storefront") {
		t.Fatalf("reload: code=%d stderr=%q", code, e)
	}
}

func TestSnapshotTightensExistingFile(t *testing.T) {
	withLive(t, aksState("AKS"), true)
	p := writeFile(t, "old.json", []byte(strings.Repeat("x", 100000)))
	if code, _, stderr := run(t, "snapshot", "--workspace", wsA, "-o", p); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	st, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	var s model.Snapshot
	if st.Mode().Perm() != 0o600 || json.Unmarshal(b, &s) != nil {
		t.Fatalf("mode=%v, json ok=%v", st.Mode().Perm(), json.Unmarshal(b, &s) == nil)
	}
}

func TestSnapshotStdout(t *testing.T) {
	withLive(t, aksState("AKS"), true)
	code, stdout, stderr := run(t, "snapshot", "-o", "-", "--workspace", wsA)
	var s model.Snapshot
	if code != 0 || json.Unmarshal([]byte(stdout), &s) != nil || len(s.Workloads) != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if stderr != "wrote snapshot to stdout (1 workloads, 2 lookups, 0 firewall events)\n" {
		t.Errorf("stderr=%q", stderr)
	}
}

func TestSnapshotRejectsDemo(t *testing.T) {
	withLive(t, aksState("AKS"), false)
	p := snapshotFile(t, model.Snapshot{SchemaVersion: model.SchemaVersion})
	out := filepath.Join(t.TempDir(), "s.json")
	for flag, args := range map[string][]string{
		"--demo": {"--demo"}, "--demo-degraded": {"--demo-degraded"},
		"--snapshot": {"--snapshot", p}, "--demo-rule": {"--demo-rule"}, "--dump-demo": {"--dump-demo"},
	} {
		code, stdout, stderr := run(t, append([]string{"snapshot", "-o", out}, args...)...)
		if code != 1 || stdout != "" || stderr != "outflow: snapshot reads the live cluster; "+flag+" cannot be used\n" {
			t.Errorf("%s: code=%d stdout=%q stderr=%q", flag, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("file written despite the error")
	}
}

func TestSnapshotNeedsOutput(t *testing.T) {
	e := withLive(t, aksState("AKS"), false)
	code, _, stderr := run(t, "snapshot")
	if code != 1 || stderr != "outflow: snapshot needs -o <file> (- for stdout)\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if e.clusterCall != 0 {
		t.Error("cluster opened without -o")
	}
}

func TestSnapshotCollectError(t *testing.T) {
	e := withLive(t, aksState("AKS"), false)
	e.cluster.err = os.ErrPermission
	p := filepath.Join(t.TempDir(), "s.json")
	code, _, stderr := run(t, "snapshot", "-o", p)
	if code != 1 || !strings.HasPrefix(stderr, "outflow: read cluster: ") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("file written despite the error")
	}
}

func TestUsageListsSnapshot(t *testing.T) {
	if !strings.Contains(usage, "snapshot -o <file>") {
		t.Error("usage lacks snapshot -o <file>")
	}
}
