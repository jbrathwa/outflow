package azure

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
)

var _ collect.PodHistorySource = (*PodSource)(nil)

// PodSource reads pod history (IP to workload over time) from KubePodInventory.
type PodSource struct {
	LA     *LogAnalytics
	Target *Target
}

const (
	noPodWorkspace = "no workspace (set sources.pod_inventory_workspace or --workspace)"
	noPodTableFmt  = "KubePodInventory not found in workspace %s (enable Container Insights)"
	// inventorySlack is how long a pod is assumed to live past its last inventory record.
	inventorySlack = 2 * time.Minute
	// runningGrace: a pod last seen within this of the window end counts as still running.
	runningGrace = 5 * time.Minute
)

func (p *PodSource) workspace() string {
	if p.Target == nil {
		return ""
	}
	return p.Target.PodWorkspace
}

func podInventoryKQL(w model.Window, nodes []string) string {
	return "KubePodInventory\n" +
		"| where TimeGenerated between (" + kqlTime(w.Start) + " .. " + kqlTime(w.End) + ")\n" +
		"| where Computer in (" + kqlStrings(nodes) + ") and isnotempty(PodIp)\n" +
		"| summarize first = min(TimeGenerated), last = max(TimeGenerated) by Namespace, Name, PodIp, Computer, ControllerKind, ControllerName"
}

// podNodesKQL lists the nodes the AKS cluster reported pods from in the window. The ID is
// matched with in~ (case-insensitive, like =~) so --debug elides it with the other lists.
func podNodesKQL(w model.Window, aksID string) string {
	return "KubePodInventory\n" +
		"| where TimeGenerated between (" + kqlTime(w.Start) + " .. " + kqlTime(w.End) + ")\n" +
		"| where _ResourceId in~ (" + kqlStrings([]string{aksID}) + ")\n" +
		"| distinct Computer"
}

func podProbeKQL(nodes []string) string {
	return "KubePodInventory\n| where Computer in (" + kqlStrings(nodes) + ")\n| take 1"
}

var (
	// rsHashRe is the pod-template-hash alphabet and length (5-10 chars).
	rsHashRe   = regexp.MustCompile(`^[bcdfghjklmnpqrstvwxz2456789]{5,10}$`)
	jobStampRe = regexp.MustCompile(`^\d{8,}$`)
)

// workloadFor returns the "namespace/name" workload ID for a pod's controller.
func workloadFor(namespace, controllerKind, controllerName string) string {
	name := controllerName
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		suffix := name[i+1:]
		switch controllerKind {
		case "ReplicaSet":
			if rsHashRe.MatchString(suffix) {
				name = name[:i]
			}
		case "Job":
			if jobStampRe.MatchString(suffix) {
				name = name[:i]
			}
		}
	}
	return namespace + "/" + name
}

type podKey struct{ ns, name, ip, node string }

// PodHistory returns every pod seen in the window (and the hour before it), with its
// time range from the first to the last inventory record.
func (p *PodSource) PodHistory(ctx context.Context, sc collect.Scope) ([]model.Pod, model.SourceStatus) {
	id := model.SrcPodInventory
	ws := p.workspace()
	if ws == "" {
		return nil, model.SourceStatus{ID: id, State: "missing", Detail: noPodWorkspace}
	}
	w := model.Window{Start: sc.Window.Start.Add(-time.Hour), End: sc.Window.End}
	t, truncated, err := p.LA.QuerySplit(ctx, "pod-inventory", ws,
		func(w model.Window) string { return podInventoryKQL(w, sc.NodeNames) }, w)
	if err != nil {
		return nil, tableFailure(id, ws, err, noPodTableFmt)
	}
	c := map[string]int{}
	for _, n := range []string{"Namespace", "Name", "PodIp", "Computer", "ControllerKind", "ControllerName", "first", "last"} {
		c[n] = t.Col(n)
		if len(t.Rows) > 0 && c[n] < 0 {
			return nil, model.SourceStatus{ID: id, State: "warn", Detail: "unexpected result columns"}
		}
	}
	// A split window can return the same pod once per sub-window: merge their ranges.
	var order []podKey
	pods := map[podKey]*model.Pod{}
	lasts := map[podKey]time.Time{}
	skipped := 0
	for _, r := range t.Rows {
		get := func(n string) string {
			if i := c[n]; i < len(r) {
				s, _ := r[i].(string)
				return s
			}
			return ""
		}
		first, e1 := time.Parse(time.RFC3339, get("first"))
		last, e2 := time.Parse(time.RFC3339, get("last"))
		ns, name, ip := get("Namespace"), get("Name"), get("PodIp")
		if e1 != nil || e2 != nil || ns == "" || name == "" || ip == "" {
			skipped++
			continue
		}
		first, last = first.UTC(), last.UTC()
		k := podKey{ns, name, ip, get("Computer")}
		if pod, ok := pods[k]; ok {
			if first.Before(pod.Start) {
				pod.Start = first
			}
			if last.After(lasts[k]) {
				lasts[k] = last
			}
			continue
		}
		ctl := get("ControllerName")
		if ctl == "" || ctl == "<none>" {
			ctl = name
		}
		order = append(order, k)
		lasts[k] = last
		pods[k] = &model.Pod{Namespace: ns, Name: name, IP: ip, Node: k.node, Start: first,
			WorkloadID: workloadFor(ns, get("ControllerKind"), ctl)}
	}
	out := make([]model.Pod, 0, len(order))
	gone := 0
	var latest time.Time
	for _, k := range order {
		pod := pods[k]
		pod.End = lasts[k].Add(inventorySlack)
		if lasts[k].Before(sc.Window.End.Add(-runningGrace)) {
			gone++
		}
		if lasts[k].After(latest) {
			latest = lasts[k]
		}
		out = append(out, *pod)
	}
	st := model.SourceStatus{ID: id, State: "ok", LastRecord: latest,
		Detail: fmt.Sprintf("%d pods (%d no longer running)", len(out), gone)}
	if len(t.Rows) == 0 && !truncated {
		st.State, st.Detail = "warn", "no KubePodInventory rows in the window"
	}
	if skipped > 0 {
		st.State = "warn"
		st.Detail += fmt.Sprintf(" (%d rows unreadable)", skipped)
	}
	if truncated {
		st.State, st.Detail = "warn", "truncated: result too large even after splitting the window"
	}
	if n := p.departedNodes(ctx, ws, sc); n > 0 {
		word := "nodes"
		if n == 1 {
			word = "node"
		}
		st.State = "warn"
		st.Detail += fmt.Sprintf("; %d %s left during the window — their traffic is not included", n, word)
	}
	return out, st
}

// departedNodes counts nodes of this AKS cluster that reported pods during the window but
// are no longer in the cluster: queries are filtered to current nodes, so their pods, DNS
// and firewall rows are missing. It returns 0 when the AKS resource ID is unknown or the
// query fails (nothing more can be said).
func (p *PodSource) departedNodes(ctx context.Context, ws string, sc collect.Scope) int {
	if p.Target == nil || p.Target.AKSResourceID == "" {
		return 0
	}
	t, err := p.LA.Query(ctx, "pod-inventory-nodes", ws, podNodesKQL(sc.Window, p.Target.AKSResourceID), sc.Window)
	ci := t.Col("Computer")
	if err != nil || ci < 0 {
		return 0
	}
	current := make(map[string]bool, len(sc.NodeNames))
	for _, n := range sc.NodeNames {
		current[strings.ToLower(n)] = true
	}
	gone := map[string]bool{}
	for _, r := range t.Rows {
		if ci < len(r) {
			if c, ok := r[ci].(string); ok && c != "" && !current[strings.ToLower(c)] {
				gone[strings.ToLower(c)] = true
			}
		}
	}
	return len(gone)
}

// Probe checks that KubePodInventory has rows for this cluster's nodes in the last hour.
func (p *PodSource) Probe(ctx context.Context, sc collect.Scope) model.SourceStatus {
	id := model.SrcPodInventory
	ws := p.workspace()
	if ws == "" {
		return model.SourceStatus{ID: id, State: "missing", Detail: noPodWorkspace}
	}
	w := model.Window{Start: sc.Window.End.Add(-time.Hour), End: sc.Window.End}
	t, err := p.LA.Query(ctx, "pod-inventory-probe", ws, podProbeKQL(sc.NodeNames), w)
	if err != nil {
		return tableFailure(id, ws, err, noPodTableFmt)
	}
	if len(t.Rows) == 0 {
		return model.SourceStatus{ID: id, State: "warn", Detail: "no KubePodInventory rows in the last hour"}
	}
	return model.SourceStatus{ID: id, State: "ok", Detail: "pod inventory present"}
}
