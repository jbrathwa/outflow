package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jbrathwa/outflow/internal/analyze"
)

// geometry is the computed screen layout.
type geometry struct {
	header           int // header lines incl. coverage hint
	bodyH            int
	side             bool
	tableW, tableH   int // tableH counts the column header line
	detailW, detailH int
	sepH             int // stacked: divider lines
	footer           int
}

func (m Model) geom() geometry {
	g := geometry{header: 1, footer: 1, side: m.width >= sideBySideMin}
	if m.coverage != "FULL" && m.coverage != "" {
		g.header = 2
	}
	g.bodyH = max(m.height-g.header-g.footer, 3)
	if g.side {
		g.tableW = max(60, m.width*42/100)
		g.tableH = g.bodyH
		g.detailW = m.width - g.tableW - 3
		g.detailH = g.bodyH
		return g
	}
	g.sepH = 1
	g.tableW, g.detailW = m.width, m.width
	g.tableH = min(max(g.bodyH*2/5, 4), 1+len(m.report.Workloads))
	g.detailH = max(g.bodyH-g.tableH-g.sepH, 1)
	return g
}

func (m Model) tableRows() int { return max(m.geom().tableH-1, 1) }

// layout recomputes sizes and refreshes the detail content after any state change.
func (m *Model) layout() {
	g := m.geom()
	m.detail.Width, m.detail.Height = g.detailW, g.detailH
	rows := g.tableH - 1
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(m.offset, 0)
	if w, ok := m.selected(); ok {
		m.detail.SetContent(m.detailContent(w, g.detailW))
	} else {
		m.detail.SetContent("")
	}
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if len(m.report.Workloads) == 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			m.styles.Empty.Render("No workloads in snapshot"))
	}
	g := m.geom()
	var body string
	table := block(m.tableView(g), g.tableW, g.tableH)
	detail := block(m.detail.View(), g.detailW, g.detailH)
	if g.side {
		sep := m.styles.Divider.Render(strings.TrimSuffix(strings.Repeat(" │ \n", g.bodyH), "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, table, sep, detail)
	} else {
		div := m.styles.Divider.Render(strings.Repeat("─", m.width))
		body = lipgloss.JoinVertical(lipgloss.Left, table, div, detail)
	}
	foot := " " + m.help.ShortHelpView(m.keys.shortHelp())
	return m.headerView() + "\n" + body + "\n" + foot
}

// block pads or crops s to exactly w x h cells.
func block(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	lines = lines[:h]
	for i, l := range lines {
		lines[i] = l + strings.Repeat(" ", max(0, w-lipgloss.Width(l)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) headerView() string {
	st, r := m.styles, m.report
	parts := []string{st.Title.Render("outflow"), r.Cluster.Name}
	if r.RuleConfigured {
		parts = append(parts, "rule: "+strings.TrimPrefix(r.Rule, ""))
	} else {
		parts = append(parts, st.Muted.Render("no rule"))
	}
	line := " " + strings.Join(parts, st.Muted.Render(" · "))
	if r.RuleConfigured {
		line += "   " + strings.Join([]string{
			st.PillOut.Render(fmt.Sprintf("[%d outside]", r.Counts.Outside)),
			st.PillUnk.Render(fmt.Sprintf("[%d unknown]", r.Counts.Unknown)),
			st.PillIn.Render(fmt.Sprintf("[%d inside]", r.Counts.Inside)),
		}, " ")
	} else {
		line += "   " + st.PillNeutral.Render(fmt.Sprintf("%d workloads · %d destinations · %d countries",
			r.Counts.Workloads, r.Counts.Destinations, r.Counts.Countries))
	}
	if w := windowLabel(r.Window.Start, r.Window.End); w != "" {
		line += "   " + st.Muted.Render(w)
	}
	switch m.coverage {
	case "PARTIAL":
		line += "\n " + st.Warn.Render("coverage partial — run outflow doctor")
	case "NONE":
		line += "\n " + st.Warn.Render("coverage none — run outflow doctor")
	}
	return line
}

// col is one table column.
type col struct {
	title string
	w     int // 0 = flexible
	right bool
}

// widths resolves flexible columns against the available width (gap of 1 between columns).
func widths(cols []col, avail int) []int {
	out := make([]int, len(cols))
	fixed, flex := 0, 0
	for i, c := range cols {
		out[i] = c.w
		fixed += c.w
		if c.w == 0 {
			flex++
		}
	}
	rest := max(avail-fixed-(len(cols)-1), flex*8)
	for i, c := range cols {
		if c.w == 0 {
			out[i] = rest / flex
		}
	}
	return out
}

func (m Model) workloadCols() []col {
	if m.report.RuleConfigured {
		return []col{{"STATUS", 7, false}, {"WORKLOAD", 0, false}, {"EGRESS", 12, false},
			{"DEST", 4, true}, {"OUT", 3, true}, {"UNK", 3, true}}
	}
	return []col{{"LOCATIONS", 12, false}, {"WORKLOAD", 0, false}, {"EGRESS", 12, false}, {"DEST", 4, true}}
}

func (m Model) tableView(g geometry) string {
	cols := m.workloadCols()
	ws := widths(cols, g.tableW-2) // 2 = selection marker column
	var heads []string
	for i, c := range cols {
		heads = append(heads, fit(c.title, ws[i], c.right))
	}
	lines := []string{m.styles.ColHead.Render("  " + strings.Join(heads, " "))}
	end := min(m.offset+g.tableH-1, len(m.report.Workloads))
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.workloadRow(m.report.Workloads[i], cols, ws, i == m.cursor))
	}
	return strings.Join(lines, "\n")
}

func (m Model) workloadRow(w analyze.WorkloadResult, cols []col, ws []int, sel bool) string {
	st := m.styles
	var cells []string
	add := func(i int, text string, style lipgloss.Style) {
		s := style
		if sel {
			s = s.Background(colSelBg)
		}
		cells = append(cells, s.Render(fit(text, ws[i], cols[i].right)))
	}
	plain := lipgloss.NewStyle()
	n := 0
	if m.report.RuleConfigured {
		glyph, style := statusGlyph(st, w.Status)
		add(n, glyph, style)
		n++
		add(n, w.ID, st.Bold.UnsetBold())
		n++
		add(n, egressCell(w), st.Muted)
		n++
		add(n, fmt.Sprint(len(w.Destinations)), plain)
		n++
		add(n, fmt.Sprint(w.Outside), pick(w.Outside > 0, st.Out, st.Muted))
		n++
		add(n, fmt.Sprint(w.Unknown), pick(w.Unknown > 0, st.Unk, st.Muted))
	} else {
		add(n, strings.Join(w.Locations, ","), st.Muted)
		n++
		add(n, w.ID, plain)
		n++
		add(n, egressCell(w), st.Muted)
		n++
		add(n, fmt.Sprint(len(w.Destinations)), plain)
	}
	marker := "  "
	if sel {
		marker = st.Selected.Bold(true).Render("▌ ")
	}
	sep := " "
	if sel {
		sep = st.Selected.Render(" ")
	}
	return marker + strings.Join(cells, sep)
}

func pick(c bool, a, b lipgloss.Style) lipgloss.Style {
	if c {
		return a
	}
	return b
}

func statusGlyph(st styles, s analyze.Status) (string, lipgloss.Style) {
	switch s {
	case analyze.Outside:
		return "✖ OUT", st.Out
	case analyze.Unknown:
		return "? UNK", st.Unk
	case analyze.Inside:
		return "✔ IN", st.In
	}
	return "·", st.Muted
}

// destGlyph is the one-cell-plus marker before a destination name.
func destGlyph(st styles, s analyze.Status) (string, lipgloss.Style) {
	switch s {
	case analyze.Outside:
		return "✖", st.Out
	case analyze.OutsideDenied:
		return "✖ (denied)", st.Muted
	case analyze.Unknown:
		return "?", st.Unk
	case analyze.Inside:
		return "✔", st.In
	}
	return "", st.Muted
}

func (m Model) detailContent(w analyze.WorkloadResult, width int) string {
	st := m.styles
	var out []string
	title, sub := " "+w.ID, egressLine(w)
	if lipgloss.Width(title)+3+lipgloss.Width(sub) <= width {
		out = append(out, st.Title.Render(title)+"   "+st.Muted.Render(sub))
	} else {
		out = append(out, st.Title.Render(fit(title, width, false)), " "+st.Muted.Render(fit(sub, width-1, false)))
	}
	out = append(out, "")
	if len(w.Destinations) == 0 {
		return strings.Join(append(out, " "+st.Muted.Render("no destinations observed")), "\n")
	}

	cols := []col{{"DESTINATION", 0, false}, {"LOCATION", 22, false}, {"HOW KNOWN", 12, false}, {"EVIDENCE", 9, false}, {"SEEN", 5, true}}
	if width < 80 {
		cols = append(cols[:2], cols[3:]...) // drop HOW KNOWN when narrow
	}
	ws := widths(cols, width-1)
	var heads []string
	for i, c := range cols {
		heads = append(heads, fit(c.title, ws[i], c.right))
	}
	out = append(out, st.ColHead.Render(" "+strings.Join(heads, " ")))

	var notes []string
	for _, d := range w.Destinations {
		name := d.Domain
		if name == "" {
			name = d.IP
		}
		glyph, gs := destGlyph(st, d.Status)
		if m.report.RuleConfigured && glyph != "" {
			if d.Status == analyze.OutsideDenied {
				name = st.Muted.Render(glyph) + " " + name
			} else {
				name = gs.Render(glyph) + " " + name
			}
		}
		ev := string(d.Evidence)
		if d.Evidence == analyze.Likely {
			ev = "likely"
		}
		vals := []string{name, d.Location.String(), string(d.Location.HowKnown), ev, fmtCount(d.Hits)}
		if len(cols) == 4 {
			vals = append(vals[:2], vals[3:]...)
		}
		var cells []string
		for i, v := range vals {
			cells = append(cells, fit(v, ws[i], cols[i].right))
		}
		out = append(out, " "+strings.Join(cells, " "))
		notes = append(notes, destNotes(d)...)
	}
	if len(notes) > 0 {
		out = append(out, "")
		for _, n := range notes {
			out = append(out, " "+st.Note.Render(fit(n, width-1, false)))
		}
	}
	return strings.Join(out, "\n")
}

// destNotes lists the per-destination remarks shown under the table.
func destNotes(d analyze.Destination) []string {
	name := d.Domain
	if name == "" {
		name = d.IP
	}
	var n []string
	add := func(s string) { n = append(n, name+": "+s) }
	if d.Status == analyze.OutsideDenied {
		add("blocked by the firewall (denied)")
	}
	if d.SaaS {
		add("server location ≠ data location — check vendor region")
	}
	if d.Exception != "" {
		add("exception: " + d.Exception)
	}
	if d.IPSource == "outflow" {
		add("IP resolved by outflow")
	}
	if d.Evidence == analyze.Likely && len(d.AlsoLikely) > 0 {
		add("also: " + strings.Join(d.AlsoLikely, ", "))
	}
	return n
}
