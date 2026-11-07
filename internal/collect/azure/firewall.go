package azure

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
)

var _ collect.EgressLogSource = (*FirewallSource)(nil)

// FirewallSource reads Azure Firewall application- and network-rule events from the
// firewall workspace, filtered to this cluster's node IPs.
type FirewallSource struct {
	LA     *LogAnalytics
	Target *Target
}

const (
	noFirewallWorkspace = "no workspace (set sources.firewall_workspace or --workspace)"
	noFirewallNodeIPs   = "no node IPs to filter firewall logs on"
	noFirewallTables    = "no Azure Firewall logs in workspace %s in the last 24h (send firewall logs with resource-specific tables, docs/setup.md#azure-firewall)"
)

func (f *FirewallSource) workspace() string {
	if f.Target == nil {
		return ""
	}
	return f.Target.FirewallWorkspace
}

// firewallName returns the last path segment of an Azure resource ID.
func firewallName(resourceID string) string {
	id := strings.TrimRight(resourceID, "/")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

const fwDetectKQL = `union isfuzzy=true (AZFWApplicationRule | take 1 | extend t="app"), (AZFWNetworkRule | take 1 | extend t="net"), (AzureDiagnostics | where Category in ("AzureFirewallApplicationRule","AzureFirewallNetworkRule") | take 1 | extend t="legacy") | project t, _ResourceId`

func fwTimeFilter(w model.Window) string {
	return "| where TimeGenerated between (" + kqlTime(w.Start) + " .. " + kqlTime(w.End) + ")\n"
}

func fwAppKQL(w model.Window, ips []string) string {
	return "AZFWApplicationRule\n" + fwTimeFilter(w) +
		"| where SourceIp in (" + kqlStrings(ips) + ")\n" +
		"| summarize count = count() by SourceIp, Fqdn, DestinationPort, Protocol, Action, Rule, bin = bin(TimeGenerated, 1m), _ResourceId"
}

func fwNetKQL(w model.Window, ips []string) string {
	return "AZFWNetworkRule\n" + fwTimeFilter(w) +
		"| where SourceIp in (" + kqlStrings(ips) + ")\n" +
		"| summarize count = count() by SourceIp, DestinationIp, DestinationPort, Protocol, Action, Rule, bin = bin(TimeGenerated, 1m), _ResourceId"
}

// fwLegacyKQL parses msg_s lines such as
// "HTTPS request from 10.224.0.4:51234 to api.example.com:443. Action: Allow. Rule Collection: rc. Rule: r".
func fwLegacyKQL(w model.Window, ips []string) string {
	return "AzureDiagnostics\n" + fwTimeFilter(w) +
		`| where Category in ("AzureFirewallApplicationRule","AzureFirewallNetworkRule")` + "\n" +
		"| extend msg = tostring(msg_s)\n" +
		`| parse msg with Protocol " request from " SourceIp ":" * " to " Destination ":" DestinationPort:string ". " rest` + "\n" +
		"| where SourceIp in (" + kqlStrings(ips) + ")\n" +
		`| extend Action = extract(@"Action: (\w+)", 1, msg), Rule = extract(@"(?:^|\s)Rule: ([^.]*)", 1, msg)` + "\n" +
		"| summarize count = count() by SourceIp, Destination, DestinationPort, Protocol, Action, Rule, bin = bin(TimeGenerated, 1m), _ResourceId"
}

// fwProbeNodesKQL asks whether any firewall row came from this cluster's nodes, in the
// tables detect found (resource-specific ones win over legacy, as in Events).
func fwProbeNodesKQL(d fwDetect, ips []string) string {
	in := "| where SourceIp in (" + kqlStrings(ips) + ")\n"
	if !d.app && !d.net {
		return "AzureDiagnostics\n" +
			`| where Category in ("AzureFirewallApplicationRule","AzureFirewallNetworkRule")` + "\n" +
			"| extend msg = tostring(msg_s)\n" +
			`| parse msg with * " request from " SourceIp ":" *` + "\n" +
			in + "| take 1\n| project SourceIp, _ResourceId"
	}
	var tables []string
	if d.app {
		tables = append(tables, "AZFWApplicationRule")
	}
	if d.net {
		tables = append(tables, "AZFWNetworkRule")
	}
	return "union isfuzzy=true " + strings.Join(tables, ", ") + "\n" + in + "| take 1\n| project SourceIp, _ResourceId"
}

type fwDetect struct {
	app, net, legacy bool
	name             string
	st               *model.SourceStatus // non-nil on failure
}

func (f *FirewallSource) detect(ctx context.Context, ws string, end time.Time) fwDetect {
	w := model.Window{Start: end.Add(-24 * time.Hour), End: end}
	t, err := f.LA.Query(ctx, "fw-detect", ws, fwDetectKQL, w)
	if err != nil {
		st := tableFailure(model.SrcFirewall, ws, err, noFirewallTables)
		return fwDetect{st: &st}
	}
	var d fwDetect
	ti, ri := t.Col("t"), t.Col("_ResourceId")
	for _, r := range t.Rows {
		if ti >= 0 && ti < len(r) {
			switch r[ti] {
			case "app":
				d.app = true
			case "net":
				d.net = true
			case "legacy":
				d.legacy = true
			}
		}
		if d.name == "" && ri >= 0 && ri < len(r) {
			if s, ok := r[ri].(string); ok {
				d.name = firewallName(s)
			}
		}
	}
	if !d.app && !d.net && !d.legacy {
		st := model.SourceStatus{ID: model.SrcFirewall, State: "missing", Detail: fmt.Sprintf(noFirewallTables, collect.MaskID(ws))}
		d.st = &st
	}
	return d
}

func (d fwDetect) kinds() string {
	var k []string
	if d.app {
		k = append(k, "app rules")
	}
	if d.net {
		k = append(k, "network rules")
	}
	return strings.Join(k, ", ")
}

// Probe reports whether firewall logs are present, which kinds, and whether any of them
// came from this cluster's nodes in the last 24h.
func (f *FirewallSource) Probe(ctx context.Context, sc collect.Scope) (string, model.SourceStatus) {
	ws := f.workspace()
	if ws == "" {
		return "", model.SourceStatus{ID: model.SrcFirewall, State: "missing", Detail: noFirewallWorkspace}
	}
	d := f.detect(ctx, ws, sc.Window.End)
	if d.st != nil {
		return "", *d.st
	}
	if len(sc.NodeIPs) == 0 {
		return d.name, model.SourceStatus{ID: model.SrcFirewall, State: "warn", Detail: noFirewallNodeIPs}
	}
	w := model.Window{Start: sc.Window.End.Add(-24 * time.Hour), End: sc.Window.End}
	t, err := f.LA.Query(ctx, "fw-probe-nodes", ws, fwProbeNodesKQL(d, sc.NodeIPs), w)
	if err != nil {
		return "", tableFailure(model.SrcFirewall, ws, err, noFirewallTables)
	}
	if len(t.Rows) == 0 {
		return d.name, model.SourceStatus{ID: model.SrcFirewall, State: "warn", Detail: "no firewall events from this cluster's nodes in the last 24h"}
	}
	if !d.app && !d.net {
		return d.name, model.SourceStatus{ID: model.SrcFirewall, State: "warn", Detail: "legacy AzureDiagnostics tables (parsed best effort)"}
	}
	return d.name, model.SourceStatus{ID: model.SrcFirewall, State: "ok", Detail: "firewall logs present (" + d.kinds() + ")"}
}

// Events returns per-minute firewall events from this cluster's nodes.
func (f *FirewallSource) Events(ctx context.Context, sc collect.Scope) ([]model.EgressEvent, string, model.SourceStatus) {
	ws := f.workspace()
	if ws == "" {
		return nil, "", model.SourceStatus{ID: model.SrcFirewall, State: "missing", Detail: noFirewallWorkspace}
	}
	if len(sc.NodeIPs) == 0 {
		return nil, "", model.SourceStatus{ID: model.SrcFirewall, State: "missing", Detail: noFirewallNodeIPs}
	}
	d := f.detect(ctx, ws, sc.Window.End)
	if d.st != nil {
		return nil, "", *d.st
	}
	type query struct {
		name   string
		kql    func(model.Window, []string) string
		destCo string
	}
	var qs []query
	legacy := !d.app && !d.net
	switch {
	case legacy:
		qs = []query{{"fw-legacy", fwLegacyKQL, "Destination"}}
	default:
		if d.app {
			qs = append(qs, query{"fw-app", fwAppKQL, "Fqdn"})
		}
		if d.net {
			qs = append(qs, query{"fw-net", fwNetKQL, "DestinationIp"})
		}
	}
	var out []model.EgressEvent
	name := d.name
	rows, skipped, truncated := 0, 0, false
	for _, q := range qs {
		t, tr, err := f.LA.QuerySplit(ctx, q.name, ws,
			func(w model.Window) string { return q.kql(w, sc.NodeIPs) }, sc.Window)
		if err != nil {
			return nil, "", tableFailure(model.SrcFirewall, ws, err, noFirewallTables)
		}
		truncated = truncated || tr
		rows += len(t.Rows)
		cols := fwCols{
			src: t.Col("SourceIp"), dest: t.Col(q.destCo), port: t.Col("DestinationPort"), proto: t.Col("Protocol"),
			action: t.Col("Action"), rule: t.Col("Rule"), bin: t.Col("bin"), cnt: t.Col("count"), rid: t.Col("_ResourceId"), destIsName: q.name != "fw-net", legacy: legacy,
		}
		if len(t.Rows) > 0 && (cols.src < 0 || cols.dest < 0 || cols.port < 0 || cols.bin < 0 || cols.cnt < 0) {
			return nil, "", model.SourceStatus{ID: model.SrcFirewall, State: "warn", Detail: "unexpected result columns"}
		}
		for _, r := range t.Rows {
			ev, rid, ok := cols.event(r)
			if !ok {
				skipped++
				continue
			}
			if name == "" && rid != "" {
				name = firewallName(rid)
			}
			out = append(out, ev)
		}
	}
	var last time.Time
	total := 0
	for _, e := range out {
		total += e.Count
		if e.Bin.After(last) {
			last = e.Bin
		}
	}
	st := model.SourceStatus{ID: model.SrcFirewall, State: "ok", LastRecord: last}
	if legacy {
		st.State = "warn"
		st.Detail = fmt.Sprintf("%d events from legacy AzureDiagnostics tables (parsed best effort)", total)
	} else {
		st.Detail = fmt.Sprintf("%d events (%s)", total, d.kinds())
	}
	if rows == 0 && !truncated {
		st.State, st.Detail = "warn", "no firewall events from this cluster's nodes in the window"
	}
	if skipped > 0 {
		st.State = "warn"
		st.Detail += fmt.Sprintf(" (%d rows unreadable)", skipped)
	}
	if truncated {
		st.State, st.Detail = "warn", "truncated: result too large even after splitting the window"
	}
	return out, name, st
}

type fwCols struct {
	src, dest, port, proto, action, rule, bin, cnt, rid int
	destIsName                                          bool // Fqdn or legacy Destination, not DestinationIp
	legacy                                              bool // parsed from AzureDiagnostics msg_s
}

func (c fwCols) event(r []any) (model.EgressEvent, string, bool) {
	str := func(i int) (string, bool) {
		if i < 0 || i >= len(r) {
			return "", false
		}
		s, ok := r[i].(string)
		return s, ok
	}
	num := func(i int) (int, bool) {
		if i < 0 || i >= len(r) {
			return 0, false
		}
		switch v := r[i].(type) {
		case float64:
			return int(v), true
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			return n, err == nil
		}
		return 0, false
	}
	src, ok1 := str(c.src)
	dest, ok2 := str(c.dest)
	bs, ok3 := str(c.bin)
	port, ok4 := num(c.port)
	cnt, ok5 := num(c.cnt)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || src == "" {
		return model.EgressEvent{}, "", false
	}
	bt, err := time.Parse(time.RFC3339, bs)
	if err != nil {
		return model.EgressEvent{}, "", false
	}
	proto, _ := str(c.proto)
	action, _ := str(c.action)
	rule, _ := str(c.rule)
	rid, _ := str(c.rid)
	ev := model.EgressEvent{
		NodeIP: src, Protocol: strings.ToUpper(strings.TrimSpace(proto)), Port: port,
		Action: strings.ToLower(strings.TrimSpace(action)), Rule: strings.TrimSpace(rule), Bin: bt.UTC(), Count: cnt,
	}
	// A legacy line whose Action did not parse may be either verdict: do not guess.
	if c.legacy && ev.Action == "" {
		return model.EgressEvent{}, "", false
	}
	dest = strings.TrimSpace(dest)
	if _, err := netip.ParseAddr(dest); err == nil || !c.destIsName {
		ev.DestIP = dest
	} else {
		ev.Domain = model.NormalizeDomain(dest)
	}
	if ev.DestIP == "" && ev.Domain == "" {
		return model.EgressEvent{}, "", false
	}
	return ev, rid, true
}
