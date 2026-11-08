package cli

import (
	"fmt"
	"io"
	"sort"
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
	writeLocationData(&b, r.LocationSources)

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
				cell(dest(d)), cell(destLoc(d)), cell(string(d.Evidence)), action(d)))
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

// destLoc is the location cell: the primary location, plus " +N" for other locations.
func destLoc(d analyze.Destination) string {
	l := locLabel(d.Location)
	if n := len(d.OtherLocations); n > 0 {
		l += fmt.Sprintf(" +%d", n)
	}
	return l
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

// writeLocationData writes the location data line and, when some source is
// stale or built-in sample data, a warning line.
func writeLocationData(b *strings.Builder, srcs []location.SourceInfo) {
	if len(srcs) == 0 {
		return
	}
	var names, warn []string
	for _, s := range srcs {
		if s.Sample {
			names = append(names, s.Name+" sample")
			warn = append(warn, s.Name+" is built-in sample data")
			continue
		}
		names = append(names, s.Name+" "+s.Version)
		if s.Stale {
			warn = append(warn, fmt.Sprintf("%s is %d days old", s.Name, s.AgeDays))
		}
	}
	fmt.Fprintf(b, "Location data: %s\n\n", strings.Join(names, " · "))
	if len(warn) > 0 {
		fmt.Fprintf(b, "> Location data may be out of date: %s.\n\n", strings.Join(warn, "; "))
	}
}

// unmappedRegions lists cloud regions of destinations that have no country,
// as sorted "provider/region (N destinations)".
func unmappedRegions(r analyze.Report) []string {
	counts := map[string]int{}
	for _, wl := range r.Workloads {
		for _, d := range wl.Destinations {
			l := d.Location
			if l.Provider == "" || l.Region == "" || l.Global || location.RegionCountry(l.Provider, l.Region) != "" {
				continue
			}
			counts[l.Provider+"/"+l.Region]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = fmt.Sprintf("%s (%d destinations)", k, counts[k])
	}
	return out
}
