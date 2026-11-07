package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/collect/azure"
	"github.com/jbrathwa/outflow/internal/collect/azure/lafake"
	"github.com/jbrathwa/outflow/internal/collect/k8s"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/model"
)

const (
	wsA = "00000000-0000-0000-0000-00000000000a"
	wsB = "00000000-0000-0000-0000-00000000000b"
)

type fakeCluster struct {
	st            collect.ClusterState
	err           error
	reads, probes int
}

func (f *fakeCluster) Read(context.Context) (collect.ClusterState, error) {
	f.reads++
	return f.st, f.err
}

func (f *fakeCluster) Probe(context.Context) (collect.ClusterState, error) {
	f.probes++
	return f.st, f.err
}

type fakeResolver struct{ domains []string }

func (r *fakeResolver) Resolve(_ context.Context, domains []string) []model.Resolution {
	r.domains = append(r.domains, domains...)
	return nil
}

func aksState(platform string) collect.ClusterState {
	return collect.ClusterState{
		Cluster:   model.Cluster{Name: "aks-prod-eus", Platform: platform},
		Workloads: []model.Workload{{Namespace: "shop", Name: "storefront", Kind: "Deployment"}},
		Pods:      []model.Pod{{Namespace: "shop", Name: "storefront-1", WorkloadID: "shop/storefront", Node: "aks-np1-vmss0001", IP: "10.244.1.5"}},
		Nodes:     []model.Node{{Name: "aks-np1-vmss0001", IPs: []string{"10.224.0.4"}}},
	}
}

type liveEnv struct {
	cluster     *fakeCluster
	resolver    *fakeResolver
	tokenCalls  int
	tokenErr    error
	gotContext  string
	clusterErr  error
	server      *lafake.Server
	clusterCall int
}

// withLive injects fakes into deps. With testEndpoints it starts a fake Log Analytics/ARM
// server and points the OUTFLOW_TEST_* hooks at it.
func withLive(t *testing.T, st collect.ClusterState, testEndpoints bool) *liveEnv {
	t.Helper()
	e := &liveEnv{cluster: &fakeCluster{st: st}, resolver: &fakeResolver{}}
	old := deps
	t.Cleanup(func() { deps = old })
	deps = liveDeps{
		cluster: func(kubeconfig, ctx string) (collect.ClusterSource, error) {
			e.clusterCall++
			e.gotContext = ctx
			if e.clusterErr != nil {
				return nil, e.clusterErr
			}
			return e.cluster, nil
		},
		tokens: func() (azure.TokenSource, error) {
			e.tokenCalls++
			if e.tokenErr != nil {
				return nil, e.tokenErr
			}
			return azure.StaticToken("tok"), nil
		},
		resolver: e.resolver,
	}
	t.Setenv("OUTFLOW_TEST_LA_ENDPOINT", "")
	t.Setenv("OUTFLOW_TEST_ARM_ENDPOINT", "")
	if testEndpoints {
		e.server = lafake.New(t, map[string]lafake.Handler{
			"dns-lookups": func(lafake.Call) (lafake.Response, int) {
				return lafake.Response{
					Columns: []string{"podIp", "domain", "bin", "count"},
					Rows: [][]any{
						{"10.244.1.5", "api.example.com", "2026-11-07T09:00:00Z", float64(3)},
						{"10.244.1.5", "cdn.example.net", "2026-11-07T09:01:00Z", float64(2)},
					},
				}, 200
			},
		})
		t.Setenv("OUTFLOW_TEST_LA_ENDPOINT", e.server.URL)
		t.Setenv("OUTFLOW_TEST_ARM_ENDPOINT", e.server.URL)
	}
	return e
}

func TestLiveScanJSON(t *testing.T) {
	e := withLive(t, aksState("AKS"), true)
	code, stdout, stderr := run(t, "scan", "--workspace", wsA, "--context", "aks-prod-eus", "--join-window", "10m")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var m struct {
		JoinWindow string `json:"joinWindow"`
		Workloads  []struct {
			ID string `json:"id"`
		} `json:"workloads"`
	}
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatal(err)
	}
	if m.JoinWindow != "10m0s" || len(m.Workloads) == 0 || m.Workloads[0].ID != "shop/storefront" {
		t.Fatalf("got %+v", m)
	}
	if e.cluster.reads != 1 || e.gotContext != "aks-prod-eus" {
		t.Errorf("reads=%d context=%q", e.cluster.reads, e.gotContext)
	}
	if len(e.server.Calls()) == 0 || e.tokenCalls != 0 {
		t.Errorf("calls=%d tokenCalls=%d", len(e.server.Calls()), e.tokenCalls)
	}
	if strings.Join(e.resolver.domains, ",") != "api.example.com,cdn.example.net" {
		t.Errorf("resolved %v", e.resolver.domains)
	}
	if !strings.HasPrefix(k8s.UserAgent, "outflow/") {
		t.Errorf("UserAgent = %q", k8s.UserAgent)
	}
}

func TestLiveDoctorUsesProbe(t *testing.T) {
	e := withLive(t, aksState("AKS"), true)
	_, stdout, stderr := run(t, "doctor", "--workspace", wsA)
	if e.cluster.probes != 1 || e.cluster.reads != 0 {
		t.Fatalf("probes=%d reads=%d", e.cluster.probes, e.cluster.reads)
	}
	if stdout == "" || strings.Contains(stderr, "outflow:") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
	if len(e.resolver.domains) != 0 {
		t.Error("probe must not resolve")
	}
}

func TestLiveClusterError(t *testing.T) {
	e := withLive(t, aksState("AKS"), false)
	e.cluster.err = errors.New("boom")
	code, _, stderr := run(t, "scan")
	if code != 1 || stderr != "outflow: read cluster: boom\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	e.clusterErr = errors.New("load kubeconfig: nope")
	code, _, stderr = run(t, "scan")
	if code != 1 || stderr != "outflow: load kubeconfig: nope\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestNoLiveNotAvailableError(t *testing.T) {
	withLive(t, aksState(""), false)
	_, _, stderr := run(t, "scan")
	if strings.Contains(stderr, "not available yet") {
		t.Fatalf("stderr=%q", stderr)
	}
	if strings.Contains(usage, "not available yet") {
		t.Error("usage still says live collection is not available")
	}
}

func TestSinceInvalid(t *testing.T) {
	e := withLive(t, aksState("AKS"), false)
	code, _, stderr := run(t, "scan", "--since", "1m")
	if code != 1 || !strings.HasPrefix(stderr, "outflow: --since: ") || !strings.Contains(stderr, "out of range") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if e.clusterCall != 0 {
		t.Error("cluster opened before flags were validated")
	}
}

func TestJoinWindowInvalid(t *testing.T) {
	for _, v := range []string{"0s", "2h", "59s", "soon"} {
		code, _, stderr := run(t, "scan", "--demo", "--join-window", v)
		if code != 1 || stderr != "outflow: --join-window must be between 1m and 60m, got "+v+"\n" {
			t.Errorf("%s: code=%d stderr=%q", v, code, stderr)
		}
	}
	for _, v := range []string{"1m", "60m", "1h"} {
		if code, _, stderr := run(t, "scan", "--demo", "--join-window", v); code != 0 {
			t.Errorf("%s: code=%d stderr=%q", v, code, stderr)
		}
	}
}

func TestJoinWindowDemo(t *testing.T) {
	for args, want := range map[string]string{"": "5m0s", "--join-window=15m": "15m0s"} {
		a := []string{"scan", "--demo"}
		if args != "" {
			a = append(a, args)
		}
		_, stdout, _ := run(t, a...)
		var m struct {
			JoinWindow string `json:"joinWindow"`
		}
		if err := json.Unmarshal([]byte(stdout), &m); err != nil || m.JoinWindow != want {
			t.Errorf("%q: joinWindow=%q err=%v", args, m.JoinWindow, err)
		}
	}
}

func TestProfileInvalid(t *testing.T) {
	withLive(t, aksState("AKS"), false)
	code, _, stderr := run(t, "scan", "--profile", "gke")
	if code != 1 || stderr != "outflow: --profile must be auto, azure or generic\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestProfileGenericHasNoAzureCalls(t *testing.T) {
	for _, c := range []struct {
		platform string
		args     []string
	}{{"AKS", []string{"--profile", "generic"}}, {"", nil}, {"", []string{"--profile", "auto"}}} {
		e := withLive(t, aksState(c.platform), false)
		code, _, stderr := run(t, append([]string{"scan", "--workspace", wsA}, c.args...)...)
		if code != 0 || e.tokenCalls != 0 {
			t.Fatalf("%v: code=%d tokens=%d stderr=%q", c.args, code, e.tokenCalls, stderr)
		}
	}
}

func TestProfileAzureForced(t *testing.T) {
	e := withLive(t, aksState(""), true)
	if code, _, stderr := run(t, "scan", "--profile", "azure", "--workspace", wsA); code != 0 || len(e.server.Calls()) == 0 {
		t.Fatalf("code=%d calls=%d stderr=%q", code, len(e.server.Calls()), stderr)
	}
}

func TestWorkspaceFlagOverridesConfig(t *testing.T) {
	cfg := writeFile(t, "outflow.yaml", []byte("sources:\n  dns_workspace: "+wsB+"\n  pod_inventory_workspace: "+wsB+"\n  firewall_workspace: "+wsB+"\n"))
	e := withLive(t, aksState("AKS"), true)
	if code, _, stderr := run(t, "scan", "--config", cfg, "--workspace", wsA); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	calls := e.server.Calls()
	if len(calls) == 0 {
		t.Fatal("no queries")
	}
	for _, c := range calls {
		if c.Workspace != wsA {
			t.Errorf("query %s went to %s", c.Name, c.Workspace)
		}
	}
	// Without the flag the config's workspace is used.
	e = withLive(t, aksState("AKS"), true)
	run(t, "scan", "--config", cfg)
	for _, c := range e.server.Calls() {
		if c.Workspace != wsB {
			t.Errorf("query %s went to %s, want config workspace", c.Name, c.Workspace)
		}
	}
}

func TestWorkspaceInvalid(t *testing.T) {
	withLive(t, aksState("AKS"), false)
	code, _, stderr := run(t, "scan", "--workspace", "law-hub-eus")
	if code != 1 || !strings.Contains(stderr, "--workspace") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestDebugGoesToStderr(t *testing.T) {
	withLive(t, aksState("AKS"), true)
	code, stdout, stderr := run(t, "scan", "--workspace", wsA, "--debug")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if !strings.Contains(stderr, "debug: query") || strings.Contains(stdout, "debug:") {
		t.Errorf("stderr=%q", stderr)
	}
	// Debug output is switched off again after the run.
	_, _, stderr = run(t, "scan", "--workspace", wsA)
	if strings.Contains(stderr, "debug:") {
		t.Errorf("debug stayed on: %q", stderr)
	}
}

func TestAzureSignInErrorExit1(t *testing.T) {
	e := withLive(t, aksState("AKS"), false)
	e.tokenErr = errors.New("no credential found")
	code, stdout, stderr := run(t, "scan", "--workspace", wsA)
	want := "outflow: azure sign-in: no credential found (run az login, or set up workload identity in CI)\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestTestEndpointsMustBeLocal(t *testing.T) {
	withLive(t, aksState("AKS"), false)
	t.Setenv("OUTFLOW_TEST_LA_ENDPOINT", "https://api.loganalytics.io/v1")
	if code, _, stderr := run(t, "scan"); code != 1 || !strings.Contains(stderr, "OUTFLOW_TEST_") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestLiveTUIRefreshCollectsAgain(t *testing.T) {
	e := withLive(t, aksState("AKS"), true)
	var cov doctor.Level
	var refreshed analyze.Report
	tui := func(r analyze.Report, c doctor.Level, refresh RefreshFunc) error {
		cov = c
		var err error
		refreshed, _, err = refresh(context.Background())
		return err
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"--workspace", wsA}, nil, &out, &errb, tui); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	if e.cluster.reads != 2 || cov == "" || len(refreshed.Workloads) == 0 {
		t.Errorf("reads=%d cov=%q refreshed=%d", e.cluster.reads, cov, len(refreshed.Workloads))
	}
}
