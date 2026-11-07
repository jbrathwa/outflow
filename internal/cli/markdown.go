package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/location"
)

// WriteMarkdown writes the report as Markdown: header lines, then a table of
// offending destinations (with a rule) or of every destination (without one).
func WriteMarkdown(w io.Writer, r analyze.Report, cov doctor.Coverage) error {
	var b strings.Builder
	rule := r.Rule
	if !r.RuleConfigured || rule == "" {
		rule = "none"
	}
	b.WriteString("# outflow report\n\n")
	fmt.Fprintf(&b, "Cluster **%s** · rule: %s · window %s → %s (UTC, RFC3339)\n\n",
		r.Cluster.Name, rule, r.Window.Start.UTC().Format(time.RFC3339), r.Window.End.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Coverage: %s\n\n", cov.Level)

	var rows []string
	for _, wl := range r.Workloads {
		for _, d := range analyze.SortedDestinations(wl.Destinations, r.RuleConfigured) {
			status := locLabel(d.Location)
			if r.RuleConfigured {
				if !offending(d, r) {
					continue
				}
				status = string(d.Status)
			}
			rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s | %s |", cell(status), cell(wl.ID),
				cell(dest(d)), cell(locLabel(d.Location)), cell(string(d.Evidence)), action(d)))
		}
	}
	first := "Status"
	if !r.RuleConfigured {
		first = "Locations"
	}
	switch {
	case len(rows) == 0 && r.RuleConfigured:
		b.WriteString("No destinations outside the allowed locations.\n")
	case len(rows) == 0:
		b.WriteString("No external destinations observed.\n")
	default:
		fmt.Fprintf(&b, "| %s | Workload | Destination | Location | Evidence | Action |\n|---|---|---|---|---|---|\n", first)
		b.WriteString(strings.Join(rows, "\n") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// offending reports whether a destination counts as outside or unknown. Denied
// traffic never counts; unknown rows are dropped when the rule ignores unknowns
// (the report then counts none).
func offending(d analyze.Destination, r analyze.Report) bool {
	if d.Action == "deny" {
		return false
	}
	switch d.Status {
	case analyze.Outside:
		return true
	case analyze.Unknown:
		return r.Counts.Unknown > 0
	}
	return false
}

func dest(d analyze.Destination) string {
	host := d.Domain
	if host == "" {
		host = d.IP
	} else if d.IP != "" {
		host += " (" + d.IP + ")"
	}
	if d.Port != 0 {
		host += fmt.Sprintf(":%d", d.Port)
	}
	return host
}

// action is the firewall verdict: allow, deny, or "—" when only a lookup was seen.
func action(d analyze.Destination) string {
	if d.Action == "" {
		return "—"
	}
	return cell(d.Action)
}

func locLabel(l location.Location) string {
	switch {
	case l.Known():
		if l.Provider != "" && l.Region != "" {
			return l.Country + " (" + l.Provider + ":" + l.Region + ")"
		}
		return l.Country
	case l.Global:
		return "global"
	}
	return "unknown"
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	if s == "" {
		return "-"
	}
	return s
}
