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

const dnsWS = "00000000-0000-0000-0000-00000000000a"

var dnsEnd = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func dnsScope() collect.Scope {
	return collect.Scope{
		Window:    model.Window{Start: dnsEnd.Add(-24 * time.Hour), End: dnsEnd},
		NodeNames: []string{"aks-np1-vmss0001", `odd"node\x`},
	}
}

func dnsSource(url, ws string) *DNSSource {
	la := NewLogAnalytics(StaticToken("tok"))
	la.Endpoint = url
	return &DNSSource{LA: la, Target: &Target{DNSWorkspace: ws}}
}

func TestParseCoreDNSLine(t *testing.T) {
	cases := []struct {
		name, line, client, qname, rcode string
		ok                               bool
	}{
		{"ipv4", `[INFO] 10.244.1.5:52345 - 12345 "A IN api.example.com. udp 32 false 512" NOERROR qr,rd,ra 106 0.000123s`, "10.244.1.5", "api.example.com", "NOERROR", true},
		{"ipv6", `[INFO] [fd00::5]:52345 - 12345 "A IN api.example.com. udp 32 false 512" NOERROR qr,rd,ra 106 0.000123s`, "fd00::5", "api.example.com", "NOERROR", true},
		{"aaaa upper", `[INFO] 10.244.1.5:1 - 7 "AAAA IN API.Example.COM. udp 32 false 512" NOERROR qr 1 0.1s`, "10.244.1.5", "api.example.com", "NOERROR", true},
		{"nxdomain", `[INFO] 10.244.1.5:1 - 7 "A IN api.example.com.default.svc.cluster.local. udp 32 false 512" NXDOMAIN qr 1 0.1s`, "10.244.1.5", "api.example.com.default.svc.cluster.local", "NXDOMAIN", true},
		{"json quoted", `"[INFO] 10.244.1.5:52345 - 1 \"A IN api.example.com. udp 32 false 512\" NOERROR qr 1 0.1s"`, "10.244.1.5", "api.example.com", "NOERROR", true},
		{"garbage", `hello world`, "", "", "", false},
		{"empty", ``, "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, q, rc, ok := ParseCoreDNSLine(c.line)
			if ok != c.ok {
				t.Fatalf("ok=%v", ok)
			}
			if !ok {
				return
			}
			if cl.String() != c.client || q != c.qname || rc != c.rcode {
				t.Fatalf("got %s %s %s", cl, q, rc)
			}
		})
	}
}

func TestExternalName(t *testing.T) {
	for q, want := range map[string]bool{
		"api.example.com":          true,
		"api.example.com.":         true,
		"a.b.cluster.local":        false,
		"kubernetes.default.svc":   false,
		"host.local":               false,
		"vm.internal":              false,
		"vm.internal.cloudapp.net": false,
		"5.1.244.10.in-addr.arpa":  false,
		"5.0.0.0.ip6.arpa":         false,
		"localhost":                false,
		"":                         false,
		"api.example.com.default.svc.cluster.local": false,
	} {
		if got := externalName(q); got != want {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
}

func lookupsServer(t *testing.T, rows [][]any, code string, status int) *lafake.Server {
	return lafake.New(t, map[string]lafake.Handler{
		"dns-lookups": func(c lafake.Call) (lafake.Response, int) {
			if code != "" {
				return lafake.Response{ErrorCode: code, ErrorMessage: "Failed to resolve table 'ContainerLogV2' at https://api.loganalytics.io/v1/workspaces/" + dnsWS}, status
			}
			return lafake.Response{Columns: []string{"podIp", "domain", "bin", "count"}, Rows: rows}, 200
		},
	})
}

func TestLookupsMapsRows(t *testing.T) {
	s := lookupsServer(t, [][]any{
		{"10.244.1.5", "api.example.com", "2026-11-07T09:00:00Z", float64(3)},
		{"10.244.1.6", "api.example.com", "2026-11-07T09:01:00Z", float64(2)},
	}, "", 0)
	got, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	if len(got) != 2 || got[0].PodIP != "10.244.1.5" || got[0].Count != 3 || !got[0].Bin.Equal(time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", got)
	}
	if len(sts) != 1 || sts[0].ID != model.SrcCoreDNSCollected || sts[0].State != "ok" || sts[0].Detail != "2 lookups from 2 pod IPs" {
		t.Fatalf("%+v", sts)
	}
	if !sts[0].LastRecord.Equal(time.Date(2026, 11, 7, 9, 1, 0, 0, time.UTC)) {
		t.Fatalf("lastRecord %v", sts[0].LastRecord)
	}
}

func TestLookupsUnreadableRowsWarn(t *testing.T) {
	s := lookupsServer(t, [][]any{
		{"10.244.1.5", "api.example.com", "2026-11-07T09:00:00Z", float64(3)},
		{"10.244.1.5", "api.example.com", "garbage", float64(3)},
		{"10.244.1.5", "api.example.com", "2026-11-07T09:00:00Z", "x"},
	}, "", 0)
	got, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	if len(got) != 1 || sts[0].State != "warn" || !strings.HasSuffix(sts[0].Detail, "(2 rows unreadable)") {
		t.Fatalf("%+v %+v", got, sts)
	}
}

func TestLookupsEmptyWarns(t *testing.T) {
	s := lookupsServer(t, nil, "", 0)
	_, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	if sts[0].State != "warn" || sts[0].Detail != "no CoreDNS query lines in the window" {
		t.Fatalf("%+v", sts)
	}
}

func TestLookupsNoWorkspace(t *testing.T) {
	s := lookupsServer(t, nil, "", 0)
	d := dnsSource(s.URL, "")
	got, sts := d.Lookups(context.Background(), dnsScope())
	ps := d.Probe(context.Background(), dnsScope())
	if got != nil || sts[0].State != "missing" || sts[0].Detail != "no workspace (set sources.dns_workspace or --workspace)" || ps[0].State != "missing" || ps[0].ID != model.SrcCoreDNSFormat {
		t.Fatalf("%+v %+v", sts, ps)
	}
	if len(s.Calls()) != 0 {
		t.Fatal("made a call")
	}
}

func TestLookupsForbidden(t *testing.T) {
	s := lookupsServer(t, nil, "InsufficientAccessError", 403)
	_, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	if sts[0].State != "missing" || sts[0].Detail != "no access to workspace …000a (needs Log Analytics Reader)" {
		t.Fatalf("%+v", sts)
	}
}

func TestLookupsNoTable(t *testing.T) {
	s := lookupsServer(t, nil, "SemanticError", 400)
	_, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	want := "ContainerLogV2 not found in workspace …000a (enable Container Insights with ContainerLogV2 and include kube-system)"
	if sts[0].State != "missing" || sts[0].Detail != want {
		t.Fatalf("%+v", sts)
	}
}

func TestLookupsOtherErrorHidesRawText(t *testing.T) {
	s := lookupsServer(t, nil, "InternalServerError", 500)
	_, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	if sts[0].State != "missing" || sts[0].Detail != "query failed (workspace …000a)" {
		t.Fatalf("%+v", sts)
	}
}

func TestLookupsTruncated(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{
		"dns-lookups": func(c lafake.Call) (lafake.Response, int) {
			return lafake.Response{ErrorCode: "E_QUERY_RESULT_SET_TOO_LARGE", ErrorMessage: "big"}, 400
		},
	})
	_, sts := dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	if sts[0].State != "warn" || sts[0].Detail != "truncated: result too large even after splitting the window" {
		t.Fatalf("%+v", sts)
	}
}

func TestDNSQueryFiltersNodes(t *testing.T) {
	s := lookupsServer(t, nil, "", 0)
	dnsSource(s.URL, dnsWS).Lookups(context.Background(), dnsScope())
	calls := s.Calls()
	if len(calls) == 0 {
		t.Fatal("no call")
	}
	k := calls[0].KQL
	for _, want := range []string{`"aks-np1-vmss0001"`, `"odd\"node\\x"`, "datetime(2026-11-06T10:00:00Z)", "datetime(2026-11-07T10:00:00Z)", "kube-system", "coredns"} {
		if !strings.Contains(k, want) {
			t.Errorf("KQL missing %s:\n%s", want, k)
		}
	}
}

func sampleServer(t *testing.T, lines []string) *lafake.Server {
	return lafake.New(t, map[string]lafake.Handler{
		"dns-sample": func(c lafake.Call) (lafake.Response, int) {
			var rows [][]any
			for _, l := range lines {
				rows = append(rows, []any{l})
			}
			return lafake.Response{Columns: []string{"line"}, Rows: rows}, 200
		},
	})
}

const goodLine = `[INFO] 10.244.1.5:52345 - 1 "A IN api.example.com. udp 32 false 512" NOERROR qr 1 0.1s`

func TestProbeFormatOK(t *testing.T) {
	s := sampleServer(t, []string{goodLine, goodLine})
	sts := dnsSource(s.URL, dnsWS).Probe(context.Background(), dnsScope())
	if len(sts) != 1 || sts[0].ID != model.SrcCoreDNSFormat || sts[0].State != "ok" || sts[0].Detail != "2/2 sample lines recognised" {
		t.Fatalf("%+v", sts)
	}
	w := s.Calls()[0].Window
	if !w.End.Equal(dnsEnd) || !w.Start.Equal(dnsEnd.Add(-time.Hour)) {
		t.Fatalf("window %+v", w)
	}
}

func TestProbeFormatWarn(t *testing.T) {
	bad := "client 10.244.9.9 asked fd00::5 something odd that goes on and on and on for a long while"
	s := sampleServer(t, []string{goodLine, bad, goodLine})
	sts := dnsSource(s.URL, dnsWS).Probe(context.Background(), dnsScope())
	if sts[0].State != "warn" || !strings.HasPrefix(sts[0].Detail, "1/3 sample lines not recognised (first: client x.x.x.x asked x.x.x.x") {
		t.Fatalf("%+v", sts)
	}
	if strings.Contains(sts[0].Detail, "10.244") || strings.Contains(sts[0].Detail, "fd00") {
		t.Fatal("IP leaked")
	}
}

func TestProbeNoLines(t *testing.T) {
	s := sampleServer(t, nil)
	sts := dnsSource(s.URL, dnsWS).Probe(context.Background(), dnsScope())
	if sts[0].State != "missing" || sts[0].Detail != "no CoreDNS query lines in the last hour" {
		t.Fatalf("%+v", sts)
	}
}
