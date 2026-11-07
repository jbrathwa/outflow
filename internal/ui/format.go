package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/jbrathwa/outflow/internal/netpol"
	"github.com/jbrathwa/outflow/internal/analyze"
)

// fit truncates s with an ellipsis to w cells and pads it with spaces to exactly w.
func fit(s string, w int, right bool) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	pad := strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
	if right {
		return pad + s
	}
	return s + pad
}

// fmtCount renders hit counts: 999, 1.2k, 34k, 1.1M.
func fmtCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10_000:
		return fmt.Sprintf("%d.%dk", n/1000, n%1000/100)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	case n < 10_000_000:
		return fmt.Sprintf("%d.%dM", n/1_000_000, n%1_000_000/100_000)
	}
	return fmt.Sprintf("%dM", n/1_000_000)
}

// windowLabel renders "last 24h", "last 7d" or "last 90m"; "" for an empty window.
func windowLabel(start, end time.Time) string {
	d := end.Sub(start)
	switch {
	case d <= 0:
		return ""
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("last %dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("last %dh", d/time.Hour)
	}
	return fmt.Sprintf("last %dm", int(d.Round(time.Minute)/time.Minute))
}

// egressLine explains a row's egress posture in one sentence.
func egressLine(w analyze.WorkloadResult) string {
	switch {
	case w.NodeLevel:
		return "traffic seen from this node with no workload attribution"
	case strings.HasPrefix(w.ID, "unknown pod"):
		return "pod no longer in pod history"
	case w.HostNetwork || w.Egress.Mode == netpol.HostNetwork:
		return "egress: hostNetwork — NetworkPolicy does not apply"
	}
	switch w.Egress.Mode {
	case netpol.Unrestricted:
		return "egress: no policy selects it (unrestricted)"
	case netpol.Internet:
		if w.Egress.Ports != "" {
			return "egress: allowed to any internet address on port " + w.Egress.Ports
		}
		return "egress: allowed to any internet address"
	case netpol.Allowlist:
		return "egress: allowlist of public ranges"
	case netpol.Internal:
		return "egress: in-cluster only"
	case netpol.DenyAll:
		return "egress: all egress denied"
	}
	return "egress: " + string(w.Egress.Mode)
}

// egressCell is the short EGRESS column text.
func egressCell(w analyze.WorkloadResult) string {
	switch {
	case w.NodeLevel:
		return "node"
	case strings.HasPrefix(w.ID, "unknown pod"):
		return "unknown"
	case w.HostNetwork:
		return string(netpol.HostNetwork)
	}
	return string(w.Egress.Mode)
}
