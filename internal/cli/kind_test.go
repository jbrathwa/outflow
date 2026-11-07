//go:build kind

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/collect/azure/lafake"
	"github.com/jbrathwa/outflow/internal/collect/k8s"
	"github.com/jbrathwa/outflow/internal/model"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Run with: hack/kind/up.sh && OUTFLOW_KIND_KUBECONFIG=hack/kind/.kubeconfig go test -tags kind ./internal/cli/ -run Kind -v

type kindResolver struct{}

func (kindResolver) Resolve(_ context.Context, domains []string) []model.Resolution {
	known := map[string]string{"files.example": "203.0.113.10", "api.collector.example": "198.51.100.20"}
	var out []model.Resolution
	for _, d := range domains {
		if ip, ok := known[d]; ok {
			out = append(out, model.Resolution{Domain: d, IPs: []string{ip}, Source: "outflow", Resolver: "test", At: time.Now().UTC()})
		}
	}
	return out
}

func kindKubeconfig(t *testing.T) string {
	t.Helper()
	p := os.Getenv("OUTFLOW_KIND_KUBECONFIG")
	if p == "" {
		t.Skip("OUTFLOW_KIND_KUBECONFIG not set (run hack/kind/up.sh)")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// kindEnv points the CLI at the kind cluster and a fake Log Analytics built from its real
// node names, node IPs and pod IPs.
func kindEnv(t *testing.T) {
	t.Helper()
	kc := kindKubeconfig(t)
	t.Setenv("KUBECONFIG", kc)
	src, err := k8s.New(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := src.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	nodeIPs := map[string]string{}
	for _, n := range st.Nodes {
		if len(n.IPs) > 0 {
			nodeIPs[n.Name] = n.IPs[0]
		}
	}
	podFor := func(workload string) model.Pod {
		for _, p := range st.Pods {
			if p.WorkloadID == workload {
				return p
			}
		}
		t.Fatalf("no pod for %s", workload)
		return model.Pod{}
	}
	ledger, collector := podFor("payments/ledger-worker"), podFor("analytics/event-collector")
	now := time.Now().UTC().Truncate(time.Minute)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	var allPods [][]any
	for _, p := range st.Pods {
		if p.IP == "" || p.Node == "" {
			continue
		}
		ctlKind, ctlName := "ReplicaSet", p.WorkloadID[strings.Index(p.WorkloadID, "/")+1:]
		allPods = append(allPods, []any{p.Namespace, p.Name, p.IP, p.Node, ctlKind, ctlName, ts(-time.Hour), ts(-time.Minute)})
	}
	// Other-node network-rule source: any node that is not the ledger worker's.
	otherNode := ""
	for n := range nodeIPs {
		if n != ledger.Node && n != collector.Node {
			otherNode = n
		}
	}
	if otherNode == "" {
		otherNode = collector.Node
	}
	// Pod inventory rows are only needed for pods whose controller name needs stripping; the
	// live pods already carry workload IDs, so reuse them verbatim as controller names.
	rows := [][]any{}
	for _, r := range allPods {
		ctl := r[5].(string)
		rows = append(rows, []any{r[0], r[1], r[2], r[3], "Deployment", ctl, r[6], r[7]})
	}
	srv := lafake.New(t, map[string]lafake.Handler{
		"dns-lookups": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"podIp", "domain", "bin", "count"}, Rows: [][]any{
				{ledger.IP, "files.example", ts(-10 * time.Minute), float64(4)},
				{collector.IP, "api.collector.example", ts(-10 * time.Minute), float64(2)},
			}}, 200
		},
		"dns-sample": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"line"}, Rows: [][]any{
				{`[INFO] ` + ledger.IP + `:41000 - 1 "A IN files.example. udp 30 false 512" NOERROR qr,rd,ra 60 0.001s`},
			}}, 200
		},
		"pod-inventory": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"Namespace", "Name", "PodIp", "Computer", "ControllerKind", "ControllerName", "first", "last"}, Rows: rows}, 200
		},
		"pod-inventory-probe": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"Computer"}, Rows: [][]any{{ledger.Node}}}, 200
		},
		"fw-detect": func(lafake.Call) (lafake.Response, int) {
			rid := "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-hub/providers/Microsoft.Network/azureFirewalls/fw-hub-eus"
			return lafake.Response{Columns: []string{"t", "_ResourceId"}, Rows: [][]any{{"app", rid}, {"net", rid}}}, 200
		},
		"fw-probe-nodes": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"SourceIp", "_ResourceId"}, Rows: [][]any{{nodeIPs[ledger.Node], ""}}}, 200
		},
		"fw-app": func(lafake.Call) (lafake.Response, int) {
			cols := []string{"SourceIp", "Fqdn", "DestinationPort", "Protocol", "Action", "Rule", "bin", "count", "_ResourceId"}
			return lafake.Response{Columns: cols, Rows: [][]any{
				{nodeIPs[ledger.Node], "files.example", float64(443), "HTTPS", "Allow", "allow-web", ts(-7 * time.Minute), float64(4), ""},
				{nodeIPs[collector.Node], "api.collector.example", float64(443), "HTTPS", "Allow", "allow-web", ts(-7 * time.Minute), float64(2), ""},
			}}, 200
		},
		"fw-net": func(lafake.Call) (lafake.Response, int) {
			cols := []string{"SourceIp", "DestinationIp", "DestinationPort", "Protocol", "Action", "Rule", "bin", "count", "_ResourceId"}
			return lafake.Response{Columns: cols, Rows: [][]any{
				{nodeIPs[otherNode], "192.0.2.77", float64(8443), "TCP", "Allow", "allow-net", ts(-30 * time.Minute), float64(1), ""},
			}}, 200
		},
	})
	t.Setenv("OUTFLOW_TEST_LA_ENDPOINT", srv.URL)
	t.Setenv("OUTFLOW_TEST_ARM_ENDPOINT", srv.URL)
	old := deps
	t.Cleanup(func() { deps = old })
	deps.resolver = kindResolver{}
	deps.cluster = func(kubeconfig, ctx string) (collect.ClusterSource, error) { return k8s.New(kubeconfig, ctx) }
}

func kindArgs(cmd string, extra ...string) []string {
	return append([]string{cmd, "--profile", "azure", "--workspace", wsA, "--join-window", "10m"}, extra...)
}

func kindRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errb, nil)
	return code, out.String(), errb.String()
}

func TestKindDoctor(t *testing.T) {
	kindEnv(t)
	code, out, errs := kindRun(t, kindArgs("doctor")...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q\n%s", code, errs, out)
	}
	for _, re := range []string{`✔ Kubernetes\s+.*all`, `✔ CoreDNS query logs`, `Coverage\s+PARTIAL`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("doctor output lacks %q:\n%s", re, out)
		}
	}
}

func TestKindScan(t *testing.T) {
	kindEnv(t)
	code, out, errs := kindRun(t, kindArgs("scan", "--json")...)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	var rep struct {
		Workloads []struct {
			ID           string `json:"id"`
			Kind         string `json:"kind"`
			HostNetwork  bool   `json:"hostNetwork"`
			Destinations []struct {
				Domain   string `json:"domain"`
				Evidence string `json:"evidence"`
			} `json:"destinations"`
		} `json:"workloads"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	var confirmed, hostNet, nodeRow bool
	for _, w := range rep.Workloads {
		switch {
		case w.ID == "payments/ledger-worker":
			for _, d := range w.Destinations {
				if d.Domain == "files.example" && d.Evidence == "confirmed" {
					confirmed = true
				}
			}
		case w.ID == "monitoring/node-exporter" && w.HostNetwork:
			hostNet = true
		case w.Kind == "node" && strings.HasPrefix(w.ID, "(node) "):
			nodeRow = true
		}
	}
	if !confirmed || !hostNet || !nodeRow {
		t.Errorf("confirmed=%v hostNetwork=%v nodeRow=%v\n%s", confirmed, hostNet, nodeRow, out)
	}
	// The CronJob is a workload in the collected snapshot.
	snap := filepath.Join(t.TempDir(), "snap.json")
	if code, _, e := kindRun(t, kindArgs("snapshot", "-o", snap)...); code != 0 {
		t.Fatalf("snapshot code=%d %s", code, e)
	}
	b, _ := os.ReadFile(snap)
	var s model.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	cron := false
	for _, w := range s.Workloads {
		if w.Namespace == "payments" && w.Name == "reconcile" && w.Kind == "CronJob" {
			cron = true
		}
	}
	if !cron {
		t.Error("payments/reconcile CronJob workload missing from snapshot")
	}
}

func TestKindSnapshotRoundTrip(t *testing.T) {
	kindEnv(t)
	snap := filepath.Join(t.TempDir(), "snap.json")
	if code, _, e := kindRun(t, kindArgs("snapshot", "-o", snap)...); code != 0 {
		t.Fatalf("snapshot code=%d %s", code, e)
	}
	_, live, _ := kindRun(t, kindArgs("scan", "--json")...)
	code, off, e := kindRun(t, "scan", "--json", "--snapshot", snap, "--join-window", "10m")
	if code != 0 {
		t.Fatalf("offline scan code=%d %s", code, e)
	}
	// Window and last-seen times move between live runs; compare the workload rows.
	rows := func(s string) string {
		var m struct {
			Workloads json.RawMessage `json:"workloads"`
		}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return string(m.Workloads)
	}
	if rows(live) != rows(off) {
		t.Errorf("live and snapshot reports differ:\nlive=%s\nsnap=%s", rows(live), rows(off))
	}
}

func TestKindRestrictedIdentity(t *testing.T) {
	kc := kindKubeconfig(t)
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(verb, resource string) bool {
		r, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(context.Background(), &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Verb: verb, Resource: resource, Namespace: "default"}},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return r.Status.Allowed
	}
	if ask("create", "pods") {
		t.Error("kubeconfig identity can create pods: not the restricted identity")
	}
	if !ask("list", "pods") {
		t.Error("restricted identity cannot list pods")
	}
}
