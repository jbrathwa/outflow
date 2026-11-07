package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/fixtures"
	"github.com/muesli/termenv"
)

func init() { lipgloss.SetColorProfile(termenv.Ascii) }

func demoReport(t *testing.T, ruled bool) analyze.Report {
	t.Helper()
	snap, err := fixtures.Demo()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	if ruled {
		if cfg, err = fixtures.DemoResidency(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := location.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return analyze.Build(snap, db, cfg)
}

func sized(m Model, w, h int) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func press(m Model, k tea.KeyType) Model {
	next, _ := m.Update(tea.KeyMsg{Type: k})
	return next.(Model)
}

func lineIndex(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func TestViewWithRule(t *testing.T) {
	rep := demoReport(t, true)
	v := sized(New(rep, nil), 140, 40).View()
	for _, want := range []string{"rule: allow US", fmt.Sprintf("%d outside", rep.Counts.Outside), "STATUS", "OUT", "UNK", "last 24h"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	lines := strings.Split(v, "\n")
	h := lineIndex(lines, "STATUS")
	if h < 0 || !strings.Contains(lines[h+1], rep.Workloads[0].ID) {
		t.Errorf("first row after header should be %q:\n%s", rep.Workloads[0].ID, v)
	}
	if rep.Workloads[0].ID != "payments/ledger-worker" {
		t.Errorf("first workload = %s", rep.Workloads[0].ID)
	}
}

func TestViewNeutralHasLocationsColumn(t *testing.T) {
	rep := demoReport(t, false)
	v := sized(New(rep, nil), 140, 40).View()
	for _, want := range []string{"LOCATIONS", "no rule", "workloads ·", "destinations ·", "countries"} {
		if !strings.Contains(v, want) {
			t.Errorf("neutral view missing %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"STATUS", "outside]", "✖"} {
		if strings.Contains(v, bad) {
			t.Errorf("neutral view must not contain %q:\n%s", bad, v)
		}
	}
}

func TestStackedBelow110(t *testing.T) {
	rep := demoReport(t, true)
	id := rep.Workloads[0].ID
	stacked := strings.Split(sized(New(rep, nil), 100, 40).View(), "\n")
	row, det := lineIndex(stacked, "STATUS"), lineIndex(stacked, "DESTINATION")
	if row < 0 || det <= row {
		t.Fatalf("stacked: table header line %d, detail header line %d", row, det)
	}
	for _, l := range stacked {
		if lipgloss.Width(l) > 100 {
			t.Errorf("line wider than terminal: %q", l)
		}
	}
	wide := strings.Split(sized(New(rep, nil), 140, 40).View(), "\n")
	i := lineIndex(wide, "STATUS")
	if i < 0 || !strings.Contains(wide[i], id) {
		t.Errorf("side-by-side: header line should hold table header and detail title: %q", wide[max(i, 0)])
	}
	if !strings.Contains(wide[i+1], id) {
		t.Errorf("row line missing %s", id)
	}
}

func TestSelectionUpdatesDetail(t *testing.T) {
	rep := demoReport(t, true)
	m := sized(New(rep, nil), 140, 40)
	detail := func(v string) string { // right-hand pane after the separator
		var b strings.Builder
		for _, l := range strings.Split(v, "\n") {
			if _, r, ok := strings.Cut(l, "│"); ok {
				b.WriteString(r + "\n")
			}
		}
		return b.String()
	}
	first, second := rep.Workloads[0].ID, rep.Workloads[1].ID
	if d := detail(m.View()); !strings.Contains(d, first) || strings.Contains(d, second) {
		t.Fatalf("detail should show %s only:\n%s", first, d)
	}
	m = press(m, tea.KeyDown)
	if d := detail(m.View()); !strings.Contains(d, second) {
		t.Errorf("detail should show %s after KeyDown:\n%s", second, d)
	}
}

func TestEmptyReport(t *testing.T) {
	v := sized(New(analyze.Report{Workloads: []analyze.WorkloadResult{}}, nil), 100, 30).View()
	if !strings.Contains(v, "No workloads in snapshot") {
		t.Errorf("empty view:\n%s", v)
	}
	press(sized(New(analyze.Report{}, nil), 100, 30), tea.KeyDown) // must not panic
}

func TestCoverageHint(t *testing.T) {
	rep := demoReport(t, true)
	if v := sized(New(rep, nil).WithCoverage(doctor.Full), 140, 40).View(); strings.Contains(v, "coverage") {
		t.Errorf("full coverage should show no hint:\n%s", v)
	}
	if v := sized(New(rep, nil).WithCoverage(doctor.Partial), 140, 40).View(); !strings.Contains(v, "coverage partial — run outflow doctor") {
		t.Errorf("missing partial hint:\n%s", v)
	}
	if v := sized(New(rep, nil).WithCoverage(doctor.None), 140, 40).View(); !strings.Contains(v, "coverage none — run outflow doctor") {
		t.Errorf("missing none hint:\n%s", v)
	}
}

func TestDetailNotes(t *testing.T) {
	rep := demoReport(t, true)
	var all strings.Builder
	m := sized(New(rep, nil), 140, 80)
	for range rep.Workloads {
		all.WriteString(m.View())
		m = press(m, tea.KeyDown)
	}
	for _, want := range []string{"server location ≠ data location — check vendor region", "exception:", "IP resolved by outflow", "also:", "(denied)", "egress: no policy selects it (unrestricted)"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("no detail view contained %q", want)
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	for n, want := range map[int]string{7: "7", 999: "999", 1200: "1.2k", 34000: "34k", 1_100_000: "1.1M"} {
		if got := fmtCount(n); got != want {
			t.Errorf("fmtCount(%d) = %s, want %s", n, got, want)
		}
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{24 * time.Hour: "last 24h", 7 * 24 * time.Hour: "last 7d", 90 * time.Minute: "last 90m", 0: ""} {
		if got := windowLabel(base, base.Add(d)); got != want {
			t.Errorf("windowLabel(%v) = %q, want %q", d, got, want)
		}
	}
	if got := windowLabel(base, base.Add(45*time.Minute)); got != "last 45m" {
		t.Errorf("got %q", got)
	}
	if got := fit("payments/ledger-worker", 10, false); got != "payments/…" {
		t.Errorf("fit = %q", got)
	}
}
