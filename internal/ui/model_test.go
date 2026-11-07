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
	v = sized(New(analyze.Report{}, nil).WithCoverage(doctor.None), 100, 30).View()
	if !strings.Contains(v, "coverage none — run outflow doctor") || !strings.Contains(v, "No workloads in snapshot") {
		t.Errorf("empty view should keep the coverage hint:\n%s", v)
	}
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

func TestNoLineExceedsWidth(t *testing.T) {
	for _, ruled := range []bool{true, false} {
		rep := demoReport(t, ruled)
		for _, size := range [][2]int{{80, 24}, {60, 24}, {40, 10}} {
			m := sized(New(rep, nil).WithCoverage(doctor.Partial), size[0], size[1])
			lines := strings.Split(m.View(), "\n")
			if len(lines) > size[1] {
				t.Errorf("ruled=%v %dx%d: %d lines", ruled, size[0], size[1], len(lines))
			}
			for _, l := range lines {
				if lipgloss.Width(l) > size[0] {
					t.Errorf("ruled=%v %dx%d: line too wide (%d): %q", ruled, size[0], size[1], lipgloss.Width(l), l)
				}
			}
			press(m, tea.KeyDown)
		}
	}
}

func keys(m Model, s string) Model {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	return m
}

func shown(pane, id string) bool { return strings.Contains(pane, fit(id, 14, false)[:13]) }

func tableSide(v string) string { // left-hand pane
	var b strings.Builder
	for _, l := range strings.Split(v, "\n") {
		if l2, _, ok := strings.Cut(l, "│"); ok {
			b.WriteString(l2 + "\n")
		}
	}
	return b.String()
}

func TestFilterSlash(t *testing.T) {
	rep := demoReport(t, true)
	m := sized(New(rep, nil), 140, 40)
	m = keys(m, "/")
	m = keys(m, "LEDGER") // case-insensitive, and q-free letters go to the input
	v := m.View()
	if !strings.Contains(v, "filter: LEDGER") {
		t.Errorf("header should show filter:\n%s", v)
	}
	tbl := tableSide(v)
	if !strings.Contains(tbl, "payments/ledger-worker") {
		t.Errorf("match missing:\n%s", tbl)
	}
	for _, w := range rep.Workloads {
		if !strings.Contains(strings.ToLower(w.ID), "ledger") && strings.Contains(tbl, w.ID) {
			t.Errorf("%s should be filtered out", w.ID)
		}
	}
	m = press(m, tea.KeyEnter) // keeps the filter, closes the input
	if v := m.View(); !strings.Contains(v, "filter: LEDGER") {
		t.Errorf("enter should keep the filter:\n%s", v)
	}
	m = press(keys(m, "/"), tea.KeyEsc) // esc clears
	if v := m.View(); strings.Contains(v, "filter:") {
		t.Errorf("esc should clear the filter:\n%s", v)
	}
}

func TestFilterQDoesNotQuitButCtrlCDoes(t *testing.T) {
	m := keys(sized(New(demoReport(t, true), nil), 140, 40), "/q")
	if !strings.Contains(m.View(), "filter: q") {
		t.Errorf("q should reach the input:\n%s", m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || cmd() != tea.Quit() {
		t.Errorf("ctrl+c must quit while filtering")
	}
}

func TestFilterNoMatch(t *testing.T) {
	m := keys(sized(New(demoReport(t, true), nil), 140, 40), "/zzzz-nothing")
	v := m.View()
	if !strings.Contains(v, "No workloads match") {
		t.Errorf("want empty-result message:\n%s", v)
	}
	press(m, tea.KeyDown) // must not panic
	keys(m, "e")
	m = press(m, tea.KeyEnter)
	m = keys(m, "e")
	if !strings.Contains(m.View(), "No destination selected") {
		t.Errorf("overlay with no rows:\n%s", m.View())
	}
}

func TestStatusFilterCycle(t *testing.T) {
	rep := demoReport(t, true)
	m := sized(New(rep, nil), 140, 40)
	for _, step := range []struct {
		name string
		want analyze.Status
	}{{"outside", analyze.Outside}, {"unknown", analyze.Unknown}, {"inside", analyze.Inside}} {
		m = keys(m, "f")
		v := m.View()
		if !strings.Contains(v, "status: "+step.name) {
			t.Errorf("header missing status: %s:\n%s", step.name, v)
		}
		tbl := tableSide(v)
		for _, w := range rep.Workloads {
			if (w.Status == step.want) != shown(tbl, w.ID) {
				t.Errorf("%s: row %s (status %s) shown=%v", step.name, w.ID, w.Status, shown(tbl, w.ID))
			}
		}
	}
	m = keys(m, "f")
	v := m.View()
	if strings.Contains(v, "status:") {
		t.Errorf("cycle should return to all:\n%s", v)
	}
	for _, w := range rep.Workloads {
		if !shown(tableSide(v), w.ID) {
			t.Errorf("all should show %s", w.ID)
		}
	}
}

func TestStatusFilterNeutralNoop(t *testing.T) {
	m := sized(New(demoReport(t, false), nil), 140, 40)
	before := m.View()
	if after := keys(m, "f").View(); after != before {
		t.Errorf("f must do nothing in neutral mode:\n%s", after)
	}
}

func TestDestinationFocus(t *testing.T) {
	rep := demoReport(t, true)
	m := sized(New(rep, nil), 140, 40)
	if strings.Contains(m.View(), "▌ stledgerweu") {
		t.Fatalf("no destination cursor while the table is focused")
	}
	m = press(m, tea.KeyEnter)
	cursorLine := func(m Model) string {
		for _, l := range strings.Split(m.View(), "\n") {
			if _, r, ok := strings.Cut(l, "│"); ok && strings.Contains(r, "▌") {
				return r
			}
		}
		return ""
	}
	first := cursorLine(m)
	if first == "" {
		t.Fatalf("enter should show a destination cursor:\n%s", m.View())
	}
	m = press(m, tea.KeyDown)
	second := cursorLine(m)
	if second == "" || second == first {
		t.Errorf("down should move the destination cursor: %q -> %q", first, second)
	}
	if !strings.Contains(m.View(), rep.Workloads[0].ID) || strings.Contains(tableSide(m.View()), "▌") == false {
		t.Errorf("table selection must stay on the workload")
	}
	m = press(m, tea.KeyEsc)
	if cursorLine(m) != "" && strings.Contains(cursorLine(m), "stledgerweu") {
		t.Errorf("esc should drop the destination cursor")
	}
}

func TestDetailNotesFollowFocus(t *testing.T) {
	rep := demoReport(t, true)
	// find a workload with at least two destinations that have notes
	for i, w := range rep.Workloads {
		n := 0
		for _, d := range w.Destinations {
			if len(destNotes(d)) > 0 {
				n++
			}
		}
		if n < 2 {
			continue
		}
		m := sized(New(rep, nil), 140, 80)
		for range i {
			m = press(m, tea.KeyDown)
		}
		all := m.View()
		m = press(m, tea.KeyEnter)
		focused := m.View()
		d0 := New(rep, nil).sortedDests(w)[0]
		for _, d := range w.Destinations {
			for _, note := range destNotes(d) {
				if !strings.Contains(all, note) {
					t.Errorf("table focus should list %q", note)
				}
				if (d.Domain == d0.Domain && d.IP == d0.IP) != strings.Contains(focused, note) {
					t.Errorf("detail focus: note %q presence wrong", note)
				}
			}
		}
		return
	}
	t.Skip("no workload with two annotated destinations")
}

func TestExceptionSnippet(t *testing.T) {
	rep := demoReport(t, true)
	m := sized(New(rep, nil), 140, 40)
	m = keys(m, "e")
	v := m.View()
	for _, want := range []string{"Add to outflow.yaml (copy by hand)", "residency:", "exceptions:", "- domain: stledgerweu.blob.core.windows.net", "workload: payments/ledger-worker", `reason: ""`} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay missing %q:\n%s", want, v)
		}
	}
	if v := keys(m, "e").View(); strings.Contains(v, "Add to outflow.yaml") {
		t.Errorf("e should close the overlay")
	}
	if v := press(m, tea.KeyEsc).View(); strings.Contains(v, "Add to outflow.yaml") {
		t.Errorf("esc should close the overlay")
	}
	// detail focus uses the selected destination
	m = press(press(sized(New(rep, nil), 140, 40), tea.KeyEnter), tea.KeyDown)
	want := New(rep, nil).sortedDests(rep.Workloads[0])[1]
	v = keys(m, "e").View()
	field := "domain: " + want.Domain
	if want.Domain == "" {
		field = "cidr: " + want.IP + "/32"
	}
	if !strings.Contains(v, field) {
		t.Errorf("overlay should use destination 1 (%s):\n%s", field, v)
	}
}

func TestExceptionSnippetCIDRAndScope(t *testing.T) {
	rep := analyze.Report{RuleConfigured: true, Workloads: []analyze.WorkloadResult{
		{ID: "(node) aks-1", NodeLevel: true, Destinations: []analyze.Destination{{Observation: analyze.Observation{IP: "203.0.113.9"}}}},
	}}
	v := keys(sized(New(rep, nil), 100, 30), "e").View()
	if !strings.Contains(v, "- cidr: 203.0.113.9/32") || strings.Contains(v, "workload:") {
		t.Errorf("node-level cidr snippet wrong:\n%s", v)
	}
}

func TestExceptionNoDestination(t *testing.T) {
	rep := analyze.Report{Workloads: []analyze.WorkloadResult{{ID: "a/b"}}}
	v := keys(sized(New(rep, nil), 100, 30), "e").View()
	if !strings.Contains(v, "No destination selected") {
		t.Errorf("overlay:\n%s", v)
	}
}

func TestRerunCallsFunc(t *testing.T) {
	rep := demoReport(t, true)
	calls := 0
	newRep := rep
	newRep.Workloads = rep.Workloads[1:] // first workload vanished
	rerun := func() analyze.Report { calls++; return newRep }
	m := sized(New(rep, rerun), 140, 40)
	m = press(press(m, tea.KeyDown), tea.KeyDown) // select index 2
	id := rep.Workloads[2].ID
	m = keys(m, "r")
	if calls != 1 {
		t.Fatalf("rerun calls = %d", calls)
	}
	if m.cursor < 0 || m.cursor >= len(newRep.Workloads) || newRep.Workloads[m.cursor].ID != id {
		t.Errorf("selection should follow %s, cursor=%d", id, m.cursor)
	}
	// shrink to one row: cursor clamps
	one := rep
	one.Workloads = rep.Workloads[:1]
	m2 := keys(sized(New(rep, func() analyze.Report { return one }), 140, 40), "G")
	m2 = keys(m2, "r")
	if m2.cursor != 0 {
		t.Errorf("cursor should clamp, got %d", m2.cursor)
	}
	keys(sized(New(rep, nil), 140, 40), "r") // nil rerun is a no-op
}

func TestHelpToggle(t *testing.T) {
	m := sized(New(demoReport(t, true), nil), 140, 40)
	short := m.View()
	for _, want := range []string{"↑/↓ move", "enter detail", "/ filter", "f status", "e exception", "r rerun", "? help", "q quit"} {
		if !strings.Contains(short, want) {
			t.Errorf("short help missing %q:\n%s", want, short)
		}
	}
	m = keys(m, "?")
	full := m.View()
	if !strings.Contains(full, "scroll detail down") {
		t.Errorf("full help should list every binding:\n%s", full)
	}
	if v := keys(m, "?").View(); strings.Contains(v, "scroll detail down") {
		t.Errorf("? should toggle back")
	}
}

func TestQuit(t *testing.T) {
	m := sized(New(demoReport(t, true), nil), 140, 40)
	for _, msg := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'q'}}, {Type: tea.KeyCtrlC}} {
		_, cmd := m.Update(msg)
		if cmd == nil || cmd() != tea.Quit() {
			t.Errorf("%v should return tea.Quit", msg)
		}
	}
}

func TestNoLineExceedsWidthInteractive(t *testing.T) {
	for _, ruled := range []bool{true, false} {
		rep := demoReport(t, ruled)
		for _, size := range [][2]int{{140, 40}, {80, 24}, {60, 24}, {40, 10}} {
			base := sized(New(rep, nil).WithCoverage(doctor.Partial), size[0], size[1])
			states := map[string]Model{
				"overlay":       keys(base, "e"),
				"overlay-focus": keys(press(base, tea.KeyEnter), "e"),
				"filter-open":   keys(base, "/payments-and-a-very-long-filter-text-that-overflows"),
				"filter-closed": press(keys(base, "/led"), tea.KeyEnter),
				"no-match":      keys(base, "/zzz"),
				"status":        keys(base, "f"),
				"help":          keys(base, "?"),
				"focus":         press(base, tea.KeyEnter),
			}
			for name, m := range states {
				lines := strings.Split(m.View(), "\n")
				if len(lines) > size[1] {
					t.Errorf("%s ruled=%v %dx%d: %d lines", name, ruled, size[0], size[1], len(lines))
				}
				for _, l := range lines {
					if lipgloss.Width(l) > size[0] {
						t.Errorf("%s ruled=%v %dx%d: line too wide (%d): %q", name, ruled, size[0], size[1], lipgloss.Width(l), l)
					}
				}
			}
		}
	}
}

func TestDestinationsWorstFirst(t *testing.T) {
	rep := demoReport(t, true)
	v := sized(New(rep, nil), 140, 40).View()
	var got []string
	for _, d := range []string{"stledgerweu", "login.microsoftonline", "api.github.com"} {
		got = append(got, d)
		if lineIndex(strings.Split(v, "\n"), d) < 0 {
			t.Fatalf("missing %s:\n%s", d, v)
		}
	}
	lines := strings.Split(v, "\n")
	if !(lineIndex(lines, got[0]) < lineIndex(lines, got[1]) && lineIndex(lines, got[1]) < lineIndex(lines, got[2])) {
		t.Errorf("want outside, unknown, inside order:\n%s", v)
	}
	if rep.Workloads[0].Destinations[0].Domain != "api.github.com" {
		t.Errorf("report order must stay untouched")
	}
}

func TestSortedDestsTieBreak(t *testing.T) {
	mk := func(dom string, hits int, st analyze.Status) analyze.Destination {
		return analyze.Destination{Observation: analyze.Observation{Domain: dom, Hits: hits}, Status: st}
	}
	w := analyze.WorkloadResult{Destinations: []analyze.Destination{
		mk("b.in", 5, analyze.Inside), mk("d.den", 9, analyze.OutsideDenied), mk("a.in", 5, analyze.Inside),
		mk("u", 1, analyze.Unknown), mk("o", 1, analyze.Outside), mk("c.in", 50, analyze.Inside),
	}}
	order := func(m Model) string {
		var s []string
		for _, d := range m.sortedDests(w) {
			s = append(s, d.Domain)
		}
		return strings.Join(s, ",")
	}
	if got := order(New(analyze.Report{RuleConfigured: true}, nil)); got != "o,u,d.den,c.in,a.in,b.in" {
		t.Errorf("ruled order = %s", got)
	}
	if got := order(New(analyze.Report{}, nil)); got != "c.in,d.den,a.in,b.in,o,u" {
		t.Errorf("neutral order = %s", got)
	}
}

func TestFilterEscKeepsSelection(t *testing.T) {
	rep := demoReport(t, true)
	m := sized(New(rep, nil), 140, 40)
	m = keys(m, "/ledger") // filtered cursor 0 is the same row index as unfiltered 0, so pick another
	m = press(m, tea.KeyEsc)
	m = press(m, tea.KeyDown)
	m = press(m, tea.KeyDown) // select index 2 unfiltered
	want := m.selectedID()
	m = keys(m, "/")
	m = keys(m, strings.ToLower(want[:3]))
	if m.selectedID() != want {
		t.Fatalf("anchor lost while typing: %s", m.selectedID())
	}
	m = press(m, tea.KeyEsc)
	if got := m.selectedID(); got != want {
		t.Errorf("esc moved selection from %s to %s", want, got)
	}
}

func TestRerunDropsStatusFilterWhenNeutral(t *testing.T) {
	rep := demoReport(t, true)
	neutral := demoReport(t, false)
	m := keys(sized(New(rep, func() analyze.Report { return neutral }), 140, 40), "f")
	m = keys(m, "r")
	if m.statusFilter != "" || strings.Contains(m.View(), "status:") {
		t.Errorf("status filter should reset:\n%s", m.View())
	}
}

func TestReselectGoneDropsDetailFocus(t *testing.T) {
	rep := demoReport(t, true)
	m := press(sized(New(rep, func() analyze.Report { r := rep; r.Workloads = rep.Workloads[1:]; return r }), 140, 40), tea.KeyEnter)
	m = keys(m, "r")
	if m.detailFocus {
		t.Errorf("detail focus should drop when the workload is gone")
	}
}

func TestEgressColumnShowsPorts(t *testing.T) {
	for _, ruled := range []bool{true, false} {
		v := tableSide(sized(New(demoReport(t, ruled), nil), 140, 40).View())
		for _, want := range []string{"internet:443", "allowlist:443"} {
			if !strings.Contains(v, want) {
				t.Errorf("ruled=%v: table missing %q:\n%s", ruled, want, v)
			}
		}
	}
}
