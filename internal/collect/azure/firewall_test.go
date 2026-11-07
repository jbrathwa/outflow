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

const fwWS = "00000000-0000-0000-0000-00000000000a"
const fwRID = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-hub/providers/Microsoft.Network/azureFirewalls/fw-hub-eus"

var fwEnd = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func fwScope() collect.Scope {
	return collect.Scope{
		Window:  model.Window{Start: fwEnd.Add(-time.Hour), End: fwEnd},
		NodeIPs: []string{"10.224.0.4", "10.224.0.5"},
	}
}

func fwSource(url, ws string) *FirewallSource {
	la := NewLogAnalytics(StaticToken("tok"))
	la.Endpoint = url
	return &FirewallSource{LA: la, Target: &Target{FirewallWorkspace: ws}}
}

var (
	fwDetectCols = []string{"t", "_ResourceId"}
	fwAppCols    = []string{"SourceIp", "Fqdn", "DestinationPort", "Protocol", "Action", "Rule", "bin", "count", "_ResourceId"}
	fwNetCols    = []string{"SourceIp", "DestinationIp", "DestinationPort", "Protocol", "Action", "Rule", "bin", "count", "_ResourceId"}
	fwLegCols    = []string{"SourceIp", "Destination", "DestinationPort", "Protocol", "Action", "Rule", "bin", "count", "_ResourceId"}
)

func fwServer(t *testing.T, detect, app, net, legacy [][]any) *lafake.Server {
	return fwServerProbe(t, detect, app, net, legacy, [][]any{{"10.224.0.4", fwRID}})
}

// fwServerProbe is fwServer with the rows the fw-probe-nodes query returns.
func fwServerProbe(t *testing.T, detect, app, net, legacy, probe [][]any) *lafake.Server {
	h := func(cols []string, rows [][]any) lafake.Handler {
		return func(c lafake.Call) (lafake.Response, int) { return lafake.Response{Columns: cols, Rows: rows}, 200 }
	}
	return lafake.New(t, map[string]lafake.Handler{
		"fw-detect":      h(fwDetectCols, detect),
		"fw-app":         h(fwAppCols, app),
		"fw-net":         h(fwNetCols, net),
		"fw-legacy":      h(fwLegCols, legacy),
		"fw-probe-nodes": h([]string{"SourceIp", "_ResourceId"}, probe),
	})
}

func TestFirewallName(t *testing.T) {
	if got := firewallName(fwRID); got != "fw-hub-eus" {
		t.Fatal(got)
	}
	if got := firewallName(""); got != "" {
		t.Fatal(got)
	}
}

func TestEventsAppAndNetwork(t *testing.T) {
	s := fwServer(t,
		[][]any{{"app", fwRID}, {"net", fwRID}},
		[][]any{{"10.224.0.4", "API.Example.com.", float64(443), "Https", "Allow", "r1", "2026-11-07T09:01:00Z", float64(5), fwRID}},
		[][]any{{"10.224.0.5", "203.0.113.9", "123", "udp", "Deny", "", "2026-11-07T09:02:00Z", float64(2), fwRID}},
		nil)
	evs, name, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if st.ID != model.SrcFirewall || st.State != "ok" || st.Detail != "7 events (app rules, network rules)" {
		t.Fatalf("%+v", st)
	}
	if name != "fw-hub-eus" || len(evs) != 2 {
		t.Fatalf("%q %+v", name, evs)
	}
	a := evs[0]
	if a.NodeIP != "10.224.0.4" || a.Domain != "api.example.com" || a.Protocol != "HTTPS" || a.Port != 443 || a.Action != "allow" || a.Rule != "r1" || a.Count != 5 || !a.Bin.Equal(time.Date(2026, 11, 7, 9, 1, 0, 0, time.UTC)) {
		t.Fatalf("%+v", a)
	}
	n := evs[1]
	if n.DestIP != "203.0.113.9" || n.Domain != "" || n.Port != 123 || n.Protocol != "UDP" || n.Action != "deny" {
		t.Fatalf("%+v", n)
	}
}

func TestEventsOnlyAppQueried(t *testing.T) {
	s := fwServer(t, [][]any{{"app", fwRID}},
		[][]any{{"10.224.0.4", "a.example", float64(443), "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(3), fwRID}}, nil, nil)
	_, _, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if st.Detail != "3 events (app rules)" {
		t.Fatalf("%+v", st)
	}
	for _, c := range s.Calls() {
		if c.Name == "fw-net" || c.Name == "fw-legacy" {
			t.Fatalf("unexpected %s", c.Name)
		}
	}
}

func TestEventsFilterNodeIPs(t *testing.T) {
	s := fwServer(t, [][]any{{"app", fwRID}, {"net", fwRID}}, nil, nil, nil)
	fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	seen := 0
	for _, c := range s.Calls() {
		if c.Name != "fw-app" && c.Name != "fw-net" {
			continue
		}
		seen++
		if !strings.Contains(c.KQL, `SourceIp in ("10.224.0.4", "10.224.0.5")`) {
			t.Fatalf("%s: %s", c.Name, c.KQL)
		}
	}
	if seen < 2 {
		t.Fatal("app and net not both queried")
	}
}

func TestEventsDetectLast24h(t *testing.T) {
	s := fwServer(t, nil, nil, nil, nil)
	fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	c := s.Calls()[0]
	if c.Name != "fw-detect" || !c.Window.Start.Equal(fwEnd.Add(-24*time.Hour)) || !c.Window.End.Equal(fwEnd) {
		t.Fatalf("%+v", c)
	}
}

func TestEventsLegacyParsed(t *testing.T) {
	s := fwServer(t, [][]any{{"legacy", fwRID}}, nil, nil, [][]any{
		{"10.224.0.4", "api.example.com", "443", "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(4), fwRID},
		{"10.224.0.4", "203.0.113.9", float64(123), "TCP", "Deny", "", "2026-11-07T09:01:00Z", float64(1), fwRID},
	})
	evs, name, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if st.State != "warn" || st.Detail != "5 events from legacy AzureDiagnostics tables (parsed best effort)" {
		t.Fatalf("%+v", st)
	}
	if name != "fw-hub-eus" || len(evs) != 2 {
		t.Fatalf("%q %+v", name, evs)
	}
	if evs[0].Domain != "api.example.com" || evs[0].DestIP != "" || evs[0].Protocol != "HTTPS" || evs[0].Rule != "r" {
		t.Fatalf("%+v", evs[0])
	}
	if evs[1].DestIP != "203.0.113.9" || evs[1].Domain != "" || evs[1].Protocol != "TCP" || evs[1].Action != "deny" {
		t.Fatalf("%+v", evs[1])
	}
}

func TestEventsResourceSpecificWinsOverLegacy(t *testing.T) {
	s := fwServer(t, [][]any{{"legacy", fwRID}, {"net", fwRID}}, nil, nil, nil)
	fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	for _, c := range s.Calls() {
		if c.Name == "fw-legacy" {
			t.Fatal("legacy queried")
		}
	}
}

func TestEventsNoRowsWarns(t *testing.T) {
	s := fwServer(t, [][]any{{"app", fwRID}}, nil, nil, nil)
	evs, _, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if len(evs) != 0 || st.State != "warn" || st.Detail != "no firewall events from this cluster's nodes in the window" {
		t.Fatalf("%+v", st)
	}
}

func TestEventsUnreadableRows(t *testing.T) {
	s := fwServer(t, [][]any{{"net", fwRID}}, nil, [][]any{
		{"10.224.0.4", "203.0.113.9", "notaport", "TCP", "Allow", "", "2026-11-07T09:01:00Z", float64(1), fwRID},
		{"10.224.0.4", "203.0.113.9", float64(80), "TCP", "Allow", "", "2026-11-07T09:01:00Z", float64(1), fwRID},
	}, nil)
	evs, _, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if len(evs) != 1 || st.State != "warn" || !strings.Contains(st.Detail, "(1 rows unreadable)") {
		t.Fatalf("%+v", st)
	}
}

func TestEventsNoTablesMissing(t *testing.T) {
	s := fwServer(t, nil, nil, nil, nil)
	_, _, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	want := "no Azure Firewall logs in workspace …000a in the last 24h (send firewall logs with resource-specific tables, docs/setup.md#azure-firewall)"
	if st.State != "missing" || st.Detail != want {
		t.Fatalf("%+v", st)
	}
}

func TestEventsNoWorkspaceNoNodeIPs(t *testing.T) {
	s := fwServer(t, nil, nil, nil, nil)
	_, _, st := fwSource(s.URL, "").Events(context.Background(), fwScope())
	if st.State != "missing" || st.Detail != "no workspace (set sources.firewall_workspace or --workspace)" {
		t.Fatalf("%+v", st)
	}
	sc := fwScope()
	sc.NodeIPs = nil
	_, _, st = fwSource(s.URL, fwWS).Events(context.Background(), sc)
	if st.State != "missing" || st.Detail != "no node IPs to filter firewall logs on" {
		t.Fatalf("%+v", st)
	}
	if len(s.Calls()) != 0 {
		t.Fatal("HTTP call made")
	}
}

func TestEventsForbidden(t *testing.T) {
	s := fwServer(t, nil, nil, nil, nil)
	_, _, st := fwSource(s.URL, "denied-0000000000000000000000000001").Events(context.Background(), fwScope())
	if st.State != "missing" || !strings.HasPrefix(st.Detail, "no access to workspace …0001") {
		t.Fatalf("%+v", st)
	}
}

func TestProbe(t *testing.T) {
	var src collect.EgressLogSource
	s := fwServer(t, [][]any{{"app", ""}, {"net", fwRID}}, nil, nil, nil)
	src = fwSource(s.URL, fwWS)
	name, st := src.Probe(context.Background(), fwScope())
	if name != "fw-hub-eus" || st.ID != model.SrcFirewall || st.State != "ok" || st.Detail != "firewall logs present (app rules, network rules)" {
		t.Fatalf("%q %+v", name, st)
	}
	s = fwServer(t, [][]any{{"legacy", fwRID}}, nil, nil, nil)
	_, st = fwSource(s.URL, fwWS).Probe(context.Background(), fwScope())
	if st.State != "warn" || st.Detail != "legacy AzureDiagnostics tables (parsed best effort)" {
		t.Fatalf("%+v", st)
	}
	s = fwServer(t, nil, nil, nil, nil)
	_, st = fwSource(s.URL, fwWS).Probe(context.Background(), fwScope())
	if st.State != "missing" || !strings.HasPrefix(st.Detail, "no Azure Firewall logs in workspace …000a") {
		t.Fatalf("%+v", st)
	}
	_, st = fwSource(s.URL, "").Probe(context.Background(), fwScope())
	if st.State != "missing" || st.Detail != "no workspace (set sources.firewall_workspace or --workspace)" {
		t.Fatalf("%+v", st)
	}
}

func probeCall(t *testing.T, s *lafake.Server) lafake.Call {
	t.Helper()
	for _, c := range s.Calls() {
		if c.Name == "fw-probe-nodes" {
			return c
		}
	}
	t.Fatal("fw-probe-nodes not queried")
	return lafake.Call{}
}

func TestProbeChecksThisClustersNodes(t *testing.T) {
	s := fwServer(t, [][]any{{"app", fwRID}, {"net", fwRID}}, nil, nil, nil)
	fwSource(s.URL, fwWS).Probe(context.Background(), fwScope())
	c := probeCall(t, s)
	if !c.Window.Start.Equal(fwEnd.Add(-24*time.Hour)) || !c.Window.End.Equal(fwEnd) ||
		!strings.Contains(c.KQL, "AZFWApplicationRule") || !strings.Contains(c.KQL, "AZFWNetworkRule") ||
		!strings.Contains(c.KQL, `SourceIp in ("10.224.0.4", "10.224.0.5")`) || !strings.Contains(c.KQL, "take 1") {
		t.Fatalf("%+v", c)
	}
	// Legacy tables: the probe parses AzureDiagnostics.
	s = fwServer(t, [][]any{{"legacy", fwRID}}, nil, nil, nil)
	fwSource(s.URL, fwWS).Probe(context.Background(), fwScope())
	if c := probeCall(t, s); !strings.Contains(c.KQL, "AzureDiagnostics") || strings.Contains(c.KQL, "AZFW") ||
		!strings.Contains(c.KQL, `SourceIp in ("10.224.0.4", "10.224.0.5")`) {
		t.Fatalf("%+v", c)
	}
}

func TestProbeNoEventsFromNodesWarns(t *testing.T) {
	s := fwServerProbe(t, [][]any{{"app", fwRID}}, nil, nil, nil, nil)
	_, st := fwSource(s.URL, fwWS).Probe(context.Background(), fwScope())
	if st.State != "warn" || st.Detail != "no firewall events from this cluster's nodes in the last 24h" {
		t.Fatalf("%+v", st)
	}
}

func TestProbeNoNodeIPsWarns(t *testing.T) {
	s := fwServer(t, [][]any{{"app", fwRID}}, nil, nil, nil)
	sc := fwScope()
	sc.NodeIPs = nil
	_, st := fwSource(s.URL, fwWS).Probe(context.Background(), sc)
	if st.State != "warn" || st.Detail != "no node IPs to filter firewall logs on" {
		t.Fatalf("%+v", st)
	}
	for _, c := range s.Calls() {
		if c.Name == "fw-probe-nodes" {
			t.Fatal("probe query without node IPs")
		}
	}
}

func TestEventsEmptyDestinationAndBadLegacyActionUnreadable(t *testing.T) {
	s := fwServer(t, [][]any{{"app", fwRID}}, [][]any{
		{"10.224.0.4", "", float64(443), "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(1), fwRID},
		{"10.224.0.4", "a.example", float64(443), "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(1), fwRID},
	}, nil, nil)
	evs, _, st := fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if len(evs) != 1 || st.State != "warn" || !strings.HasSuffix(st.Detail, "(1 rows unreadable)") {
		t.Fatalf("app: %+v %+v", evs, st)
	}
	s = fwServer(t, [][]any{{"legacy", fwRID}}, nil, nil, [][]any{
		{"10.224.0.4", "api.example.com", "443", "HTTPS", "", "r", "2026-11-07T09:01:00Z", float64(4), fwRID},
		{"10.224.0.4", "api.example.com", "443", "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(4), fwRID},
	})
	evs, _, st = fwSource(s.URL, fwWS).Events(context.Background(), fwScope())
	if len(evs) != 1 || !strings.HasSuffix(st.Detail, "(1 rows unreadable)") {
		t.Fatalf("legacy: %+v %+v", evs, st)
	}
}

func TestFirewallNamePrefersThisClustersRows(t *testing.T) {
	other := "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-hub/providers/Microsoft.Network/azureFirewalls/fw-other"
	s := fwServerProbe(t, [][]any{{"app", other}},
		[][]any{{"10.224.0.4", "a.example", float64(443), "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(1), fwRID}},
		nil, nil, [][]any{{"10.224.0.4", fwRID}})
	if _, name, _ := fwSource(s.URL, fwWS).Events(context.Background(), fwScope()); name != "fw-hub-eus" {
		t.Fatalf("events name %q", name)
	}
	if name, _ := fwSource(s.URL, fwWS).Probe(context.Background(), fwScope()); name != "fw-hub-eus" {
		t.Fatalf("probe name %q", name)
	}
	// No resource ID on the cluster's rows: fall back to the detect row.
	s = fwServerProbe(t, [][]any{{"app", other}},
		[][]any{{"10.224.0.4", "a.example", float64(443), "HTTPS", "Allow", "r", "2026-11-07T09:01:00Z", float64(1), ""}},
		nil, nil, [][]any{{"10.224.0.4", ""}})
	if _, name, _ := fwSource(s.URL, fwWS).Events(context.Background(), fwScope()); name != "fw-other" {
		t.Fatalf("fallback events name %q", name)
	}
	if name, _ := fwSource(s.URL, fwWS).Probe(context.Background(), fwScope()); name != "fw-other" {
		t.Fatalf("fallback probe name %q", name)
	}
}
