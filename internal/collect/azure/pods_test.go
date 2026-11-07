package azure

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/collect/azure/lafake"
	"github.com/jbrathwa/outflow/internal/model"
)

func TestWorkloadFor(t *testing.T) {
	cases := []struct{ name, ns, kind, ctl, want string }{
		{"deployment rs", "shop", "ReplicaSet", "api-7d9f8c6b5d", "shop/api"},
		{"rs multi dash", "shop", "ReplicaSet", "web-front-6c8b9d7f4", "shop/web-front"},
		{"rs hash alphabet miss", "shop", "ReplicaSet", "api-aeiou", "shop/api-aeiou"},
		{"rs no dash", "shop", "ReplicaSet", "standalone", "shop/standalone"},
		{"cronjob job", "ops", "Job", "nightly-28712345", "ops/nightly"},
		{"cronjob short digits", "ops", "Job", "nightly-1234567", "ops/nightly-1234567"},
		{"plain job", "ops", "Job", "migrate", "ops/migrate"},
		{"statefulset", "data", "StatefulSet", "db", "data/db"},
		{"daemonset", "kube-system", "DaemonSet", "agent", "kube-system/agent"},
		{"empty kind", "shop", "", "x", "shop/x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workloadFor(c.ns, c.kind, c.ctl); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

const podWS = "00000000-0000-0000-0000-00000000000a"

var podEnd = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func podScope() collect.Scope {
	return collect.Scope{
		Window:    model.Window{Start: podEnd.Add(-24 * time.Hour), End: podEnd},
		NodeNames: []string{"aks-np1-vmss0001", `odd"node\x`},
	}
}

func podSource(url, ws string) *PodSource {
	la := NewLogAnalytics(StaticToken("tok"))
	la.Endpoint = url
	return &PodSource{LA: la, Target: &Target{PodWorkspace: ws}}
}

var podCols = []string{"Namespace", "Name", "PodIp", "Computer", "ControllerKind", "ControllerName", "first", "last"}

func podServer(t *testing.T, rows [][]any, code string, status int) *lafake.Server {
	return lafake.New(t, map[string]lafake.Handler{
		"pod-inventory": func(c lafake.Call) (lafake.Response, int) {
			if code != "" {
				return lafake.Response{ErrorCode: code, ErrorMessage: "Failed to resolve table 'KubePodInventory'"}, status
			}
			return lafake.Response{Columns: podCols, Rows: rows}, 200
		},
		"pod-inventory-probe": func(c lafake.Call) (lafake.Response, int) {
			if code != "" {
				return lafake.Response{ErrorCode: code, ErrorMessage: "Failed to resolve table 'KubePodInventory'"}, status
			}
			return lafake.Response{Columns: []string{"Computer"}, Rows: rows}, 200
		},
	})
}

func TestPodHistoryEndsAfterLastSeen(t *testing.T) {
	s := podServer(t, [][]any{
		{"shop", "api-7d9f8c6b5d-x7k2p", "10.244.1.9", "aks-np1-vmss0001", "ReplicaSet", "api-7d9f8c6b5d", "2026-11-07T08:00:00Z", "2026-11-07T09:00:00Z"},
		{"shop", "api-7d9f8c6b5d-q9w4z", "10.244.1.10", "aks-np1-vmss0001", "ReplicaSet", "api-7d9f8c6b5d", "2026-11-07T08:00:00Z", "2026-11-07T09:58:00Z"},
		{"shop", "bare", "10.244.1.11", "aks-np1-vmss0001", "", "", "2026-11-07T08:00:00Z", "2026-11-07T09:00:00Z"},
	}, "", 0)
	got, st := podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	p := got[0]
	if p.WorkloadID != "shop/api" || p.Node != "aks-np1-vmss0001" || p.IP != "10.244.1.9" ||
		!p.Start.Equal(time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)) || !p.End.Equal(time.Date(2026, 11, 7, 9, 2, 0, 0, time.UTC)) {
		t.Fatalf("%+v", p)
	}
	if got[2].WorkloadID != "shop/bare" {
		t.Fatalf("%+v", got[2])
	}
	if st.ID != model.SrcPodInventory || st.State != "ok" || st.Detail != "3 pods (2 no longer running)" {
		t.Fatalf("%+v", st)
	}
}

func TestPodHistoryMergesSplitWindows(t *testing.T) {
	calls := 0
	s := lafake.New(t, map[string]lafake.Handler{
		"pod-inventory": func(c lafake.Call) (lafake.Response, int) {
			calls++
			if calls == 1 {
				return lafake.Response{ErrorCode: "E_QUERY_RESULT_SET_TOO_LARGE", ErrorMessage: "big"}, 400
			}
			first, last := "2026-11-06T20:00:00Z", "2026-11-06T21:00:00Z"
			if calls == 3 {
				first, last = "2026-11-07T08:00:00Z", "2026-11-07T09:00:00Z"
			}
			return lafake.Response{Columns: podCols, Rows: [][]any{{"shop", "api-1", "10.244.1.9", "n1", "StatefulSet", "api", first, last}}}, 200
		},
	})
	got, st := podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	if len(got) != 1 || !got[0].Start.Equal(time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)) || !got[0].End.Equal(time.Date(2026, 11, 7, 9, 2, 0, 0, time.UTC)) || st.Detail != "1 pods (1 no longer running)" {
		t.Fatalf("%+v %+v", got, st)
	}
}

func TestPodHistoryUnreadableRowsWarn(t *testing.T) {
	s := podServer(t, [][]any{
		{"shop", "a", "10.244.1.9", "n1", "StatefulSet", "a", "2026-11-07T08:00:00Z", "2026-11-07T09:59:00Z"},
		{"shop", "b", "10.244.1.10", "n1", "StatefulSet", "b", "garbage", "2026-11-07T09:59:00Z"},
		{"shop", "c", 7, "n1", "StatefulSet", "c", "2026-11-07T08:00:00Z", "2026-11-07T09:59:00Z"},
	}, "", 0)
	got, st := podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	if len(got) != 1 || st.State != "warn" || !strings.HasSuffix(st.Detail, "(2 rows unreadable)") {
		t.Fatalf("%+v %+v", got, st)
	}
}

func TestPodHistoryEmptyWarns(t *testing.T) {
	s := podServer(t, nil, "", 0)
	_, st := podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	if st.State != "warn" || st.Detail != "no KubePodInventory rows in the window" {
		t.Fatalf("%+v", st)
	}
}

func TestPodHistoryMissing(t *testing.T) {
	for name, c := range map[string]struct {
		code   string
		status int
		ws     string
		want   string
	}{
		"forbidden": {"InsufficientAccessError", 403, podWS, "no access to workspace …000a (needs Log Analytics Reader)"},
		"no table":  {"SemanticError", 400, podWS, "KubePodInventory not found in workspace …000a (enable Container Insights)"},
		"other":     {"InternalServerError", 500, podWS, "query failed (workspace …000a)"},
		"none":      {"", 0, "", "no workspace (set sources.pod_inventory_workspace or --workspace)"},
	} {
		t.Run(name, func(t *testing.T) {
			s := podServer(t, nil, c.code, c.status)
			p := podSource(s.URL, c.ws)
			got, st := p.PodHistory(context.Background(), podScope())
			pr := p.Probe(context.Background(), podScope())
			if got != nil || st.ID != model.SrcPodInventory || st.State != "missing" || st.Detail != c.want {
				t.Fatalf("%+v", st)
			}
			if pr.ID != model.SrcPodInventory || pr.State != "missing" || pr.Detail != c.want {
				t.Fatalf("probe %+v", pr)
			}
			if c.ws == "" && len(s.Calls()) != 0 {
				t.Fatal("made a call")
			}
		})
	}
}

func TestPodHistoryTruncated(t *testing.T) {
	s := podServer(t, nil, "E_QUERY_RESULT_SET_TOO_LARGE", 400)
	_, st := podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	if st.State != "warn" || st.Detail != "truncated: result too large even after splitting the window" {
		t.Fatalf("%+v", st)
	}
}

func TestPodQueryShape(t *testing.T) {
	s := podServer(t, nil, "", 0)
	podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	k := s.Calls()[0].KQL
	for _, want := range []string{`"aks-np1-vmss0001"`, `"odd\"node\\x"`, "datetime(2026-11-06T09:00:00Z)", "datetime(2026-11-07T10:00:00Z)", "KubePodInventory", "isnotempty(PodIp)", "summarize"} {
		if !strings.Contains(k, want) {
			t.Errorf("KQL missing %s:\n%s", want, k)
		}
	}
}

func TestPodProbe(t *testing.T) {
	s := podServer(t, [][]any{{"aks-np1-vmss0001"}}, "", 0)
	st := podSource(s.URL, podWS).Probe(context.Background(), podScope())
	if st.ID != model.SrcPodInventory || st.State != "ok" || st.Detail != "pod inventory present" {
		t.Fatalf("%+v", st)
	}
	c := s.Calls()[0]
	if !c.Window.Start.Equal(podEnd.Add(-time.Hour)) || !strings.Contains(c.KQL, "take 1") {
		t.Fatalf("%+v", c)
	}
	s = podServer(t, nil, "", 0)
	st = podSource(s.URL, podWS).Probe(context.Background(), podScope())
	if st.State != "warn" || st.Detail != "no KubePodInventory rows in the last hour" {
		t.Fatalf("%+v", st)
	}
}

func TestPodHistoryReportsDepartedNodes(t *testing.T) {
	row := [][]any{{"shop", "a", "10.244.1.9", "aks-np1-vmss0001", "StatefulSet", "a", "2026-11-07T08:00:00Z", "2026-11-07T09:59:00Z"}}
	s := lafake.New(t, map[string]lafake.Handler{
		"pod-inventory": func(lafake.Call) (lafake.Response, int) { return lafake.Response{Columns: podCols, Rows: row}, 200 },
		"pod-inventory-nodes": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"Computer"}, Rows: [][]any{{"AKS-NP1-VMSS0001"}, {"aks-np1-vmss0002"}, {"aks-np1-vmss0003"}}}, 200
		},
	})
	p := podSource(s.URL, podWS)
	p.Target.AKSResourceID = aksID
	_, st := p.PodHistory(context.Background(), podScope())
	if st.State != "warn" || st.Detail != "1 pods (0 no longer running); 2 nodes left during the window — their traffic is not included" {
		t.Fatalf("%+v", st)
	}
	var c lafake.Call
	for _, x := range s.Calls() {
		if x.Name == "pod-inventory-nodes" {
			c = x
		}
	}
	if !strings.Contains(c.KQL, `_ResourceId in~ ("`+aksID+`")`) || !strings.Contains(c.KQL, "distinct Computer") ||
		!c.Window.Start.Equal(podScope().Window.Start) || !c.Window.End.Equal(podEnd) {
		t.Fatalf("%+v", c)
	}
	// One departed node reads in the singular.
	s = lafake.New(t, map[string]lafake.Handler{
		"pod-inventory": func(lafake.Call) (lafake.Response, int) { return lafake.Response{Columns: podCols, Rows: row}, 200 },
		"pod-inventory-nodes": func(lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"Computer"}, Rows: [][]any{{"aks-np1-vmss0002"}}}, 200
		},
	})
	p = podSource(s.URL, podWS)
	p.Target.AKSResourceID = aksID
	if _, st = p.PodHistory(context.Background(), podScope()); !strings.HasSuffix(st.Detail, "; 1 node left during the window — their traffic is not included") {
		t.Fatalf("%+v", st)
	}
}

func TestPodHistoryNoAKSIDNoNodeQuery(t *testing.T) {
	s := podServer(t, nil, "", 0)
	podSource(s.URL, podWS).PodHistory(context.Background(), podScope())
	for _, c := range s.Calls() {
		if c.Name == "pod-inventory-nodes" {
			t.Fatal("pod-inventory-nodes queried without an AKS resource ID")
		}
	}
}
