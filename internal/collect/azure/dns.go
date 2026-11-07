package azure

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
)

var _ collect.DNSLogSource = (*DNSSource)(nil)

// DNSSource reads CoreDNS query lookups from ContainerLogV2 in the DNS workspace.
type DNSSource struct {
	LA     *LogAnalytics
	Target *Target
}

const noDNSWorkspace = "no workspace (set sources.dns_workspace or --workspace)"

// dnsFilter is the shared ContainerLogV2 filter for CoreDNS lines of this cluster's nodes.
func dnsFilter(w model.Window, nodes []string) string {
	return "ContainerLogV2\n" +
		"| where TimeGenerated between (" + kqlTime(w.Start) + " .. " + kqlTime(w.End) + ")\n" +
		`| where PodNamespace == "kube-system" and ContainerName == "coredns" and Computer in (` + kqlStrings(nodes) + ")\n"
}

func dnsLookupsKQL(w model.Window, nodes []string) string {
	return dnsFilter(w, nodes) +
		"| extend line = tostring(LogMessage)\n" +
		`| parse line with * "] " client " - " * "\"" qtype " IN " qname " " * "\" " rcode " " *` + "\n" +
		`| where rcode == "NOERROR" and isnotempty(qname)` + "\n" +
		`| extend podIp = trim(@"[\[\]]", tostring(extract(@"^(.*):\d+$", 1, client)))` + "\n" +
		`| extend domain = tolower(trim_end(@"\.", qname))` + "\n" +
		`| where domain !endswith ".cluster.local" and domain !endswith ".svc" and domain !endswith ".internal.cloudapp.net" and domain contains "."` + "\n" +
		"| summarize count = count() by podIp, domain, bin = bin(TimeGenerated, 1m)"
}

func dnsSampleKQL(w model.Window, nodes []string) string {
	return dnsFilter(w, nodes) + "| take 20\n| project line = tostring(LogMessage)"
}

// queryFailure maps a query error to a status with a fixed phrase; raw error text is never used.
func queryFailure(id, ws string, err error) model.SourceStatus {
	mask := collect.MaskID(ws)
	var qe *QueryError
	switch {
	case errors.As(err, &qe) && qe.Forbidden():
		return model.SourceStatus{ID: id, State: "missing", Detail: fmt.Sprintf("no access to workspace %s (needs Log Analytics Reader)", mask)}
	case errors.As(err, &qe) && qe.NoTable():
		return model.SourceStatus{ID: id, State: "missing", Detail: fmt.Sprintf("ContainerLogV2 not found in workspace %s (enable Container Insights with ContainerLogV2 and include kube-system)", mask)}
	}
	return model.SourceStatus{ID: id, State: "missing", Detail: fmt.Sprintf("query failed (workspace %s)", mask)}
}

func (d *DNSSource) workspace() string {
	if d.Target == nil {
		return ""
	}
	return d.Target.DNSWorkspace
}

// Lookups returns per-minute lookup counts by pod IP and external domain.
func (d *DNSSource) Lookups(ctx context.Context, sc collect.Scope) ([]model.DNSLookup, []model.SourceStatus) {
	ws := d.workspace()
	if ws == "" {
		return nil, []model.SourceStatus{{ID: model.SrcCoreDNSCollected, State: "missing", Detail: noDNSWorkspace}}
	}
	t, truncated, err := d.LA.QuerySplit(ctx, "dns-lookups", ws,
		func(w model.Window) string { return dnsLookupsKQL(w, sc.NodeNames) }, sc.Window)
	if err != nil {
		return nil, []model.SourceStatus{queryFailure(model.SrcCoreDNSCollected, ws, err)}
	}
	ip, dom, bin, cnt := t.Col("podIp"), t.Col("domain"), t.Col("bin"), t.Col("count")
	if len(t.Rows) > 0 && (ip < 0 || dom < 0 || bin < 0 || cnt < 0) {
		return nil, []model.SourceStatus{{ID: model.SrcCoreDNSCollected, State: "warn", Detail: "unexpected result columns"}}
	}
	var out []model.DNSLookup
	var last time.Time
	ips := map[string]bool{}
	skipped := 0
	for _, r := range t.Rows {
		l, ok := lookupRow(r, ip, dom, bin, cnt)
		if !ok {
			skipped++
			continue
		}
		out = append(out, l)
		ips[l.PodIP] = true
		if l.Bin.After(last) {
			last = l.Bin
		}
	}
	st := model.SourceStatus{ID: model.SrcCoreDNSCollected, State: "ok", LastRecord: last,
		Detail: fmt.Sprintf("%d lookups from %d pod IPs", len(out), len(ips))}
	if len(t.Rows) == 0 && !truncated {
		st.State, st.Detail = "warn", "no CoreDNS query lines in the window"
	}
	if skipped > 0 {
		st.State = "warn"
		st.Detail += fmt.Sprintf(" (%d rows unreadable)", skipped)
	}
	if truncated {
		st.State, st.Detail = "warn", "truncated: result too large even after splitting the window"
	}
	return out, []model.SourceStatus{st}
}

func lookupRow(r []any, ip, dom, bin, cnt int) (model.DNSLookup, bool) {
	get := func(i int) any {
		if i < len(r) {
			return r[i]
		}
		return nil
	}
	pod, ok1 := get(ip).(string)
	domain, ok2 := get(dom).(string)
	bs, ok3 := get(bin).(string)
	if !ok1 || !ok2 || !ok3 || pod == "" || domain == "" {
		return model.DNSLookup{}, false
	}
	bt, err := time.Parse(time.RFC3339, bs)
	if err != nil {
		return model.DNSLookup{}, false
	}
	var n int
	switch v := get(cnt).(type) {
	case float64:
		n = int(v)
	case string:
		x, err := strconv.Atoi(v)
		if err != nil {
			return model.DNSLookup{}, false
		}
		n = x
	default:
		return model.DNSLookup{}, false
	}
	return model.DNSLookup{PodIP: pod, Domain: domain, Bin: bt.UTC(), Count: n}, true
}

var (
	ipv4Re = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	ipv6Re = regexp.MustCompile(`(?:[0-9a-fA-F]{0,4}:){2,}[0-9a-fA-F]{0,4}`)
)

func scrubIPs(s string) string {
	return ipv6Re.ReplaceAllString(ipv4Re.ReplaceAllString(s, "x.x.x.x"), "x.x.x.x")
}

// Probe checks, over the last hour, that sample CoreDNS lines match the expected format.
func (d *DNSSource) Probe(ctx context.Context, sc collect.Scope) []model.SourceStatus {
	ws := d.workspace()
	if ws == "" {
		return []model.SourceStatus{{ID: model.SrcCoreDNSFormat, State: "missing", Detail: noDNSWorkspace}}
	}
	w := model.Window{Start: sc.Window.End.Add(-time.Hour), End: sc.Window.End}
	t, err := d.LA.Query(ctx, "dns-sample", ws, dnsSampleKQL(w, sc.NodeNames), w)
	if err != nil {
		return []model.SourceStatus{queryFailure(model.SrcCoreDNSFormat, ws, err)}
	}
	li := t.Col("line")
	var lines []string
	for _, r := range t.Rows {
		if li >= 0 && li < len(r) {
			if s, ok := r[li].(string); ok {
				lines = append(lines, s)
			}
		}
	}
	if len(lines) == 0 {
		return []model.SourceStatus{{ID: model.SrcCoreDNSFormat, State: "missing", Detail: "no CoreDNS query lines in the last hour"}}
	}
	bad, first := 0, ""
	for _, l := range lines {
		if _, _, _, ok := ParseCoreDNSLine(l); !ok {
			if bad == 0 {
				first = l
			}
			bad++
		}
	}
	n := len(lines)
	if bad == 0 {
		return []model.SourceStatus{{ID: model.SrcCoreDNSFormat, State: "ok", Detail: fmt.Sprintf("%d/%d sample lines recognised", n, n)}}
	}
	f := []rune(scrubIPs(strings.TrimSpace(first)))
	if len(f) > 60 {
		f = f[:60]
	}
	return []model.SourceStatus{{ID: model.SrcCoreDNSFormat, State: "warn",
		Detail: fmt.Sprintf("%d/%d sample lines not recognised (first: %s)", bad, n, string(f))}}
}

var coreDNSRe = regexp.MustCompile(`\]\s+(\S+) - \d+ "\S+ IN (\S+) [^"]*" (\S+)`)

// ParseCoreDNSLine parses a CoreDNS "log" plugin line in the default format. The line
// may be JSON-encoded (a quoted string). qname is lowercased without the trailing dot;
// rcode is returned as logged (callers filter to NOERROR).
func ParseCoreDNSLine(line string) (client netip.Addr, qname string, rcode string, ok bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, `"`) {
		u, err := strconv.Unquote(line)
		if err != nil {
			return netip.Addr{}, "", "", false
		}
		line = u
	}
	m := coreDNSRe.FindStringSubmatch(line)
	if m == nil {
		return netip.Addr{}, "", "", false
	}
	var addr netip.Addr
	if ap, err := netip.ParseAddrPort(m[1]); err == nil {
		addr = ap.Addr()
	} else if a, err := netip.ParseAddr(strings.Trim(m[1], "[]")); err == nil {
		addr = a
	} else {
		return netip.Addr{}, "", "", false
	}
	q := strings.TrimSuffix(strings.ToLower(m[2]), ".")
	return addr, q, m[3], true
}

// externalName reports whether qname looks like an internet name rather than a
// cluster, node or reverse-lookup name.
func externalName(qname string) bool {
	q := strings.TrimSuffix(strings.ToLower(qname), ".")
	if !strings.Contains(q, ".") {
		return false
	}
	for _, suf := range []string{".cluster.local", ".svc", ".local", ".internal", ".internal.cloudapp.net", ".in-addr.arpa", ".ip6.arpa"} {
		if strings.HasSuffix(q, suf) {
			return false
		}
	}
	return true
}
