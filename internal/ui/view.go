package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/doctor"
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
	if m.coverageHint() != "" {
		g.header++
	}
	if m.statusLine() != "" {
		g.header++
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
	g.tableH = min(max(g.bodyH*2/5, 4), 1+max(len(m.visible()), 1), max(g.bodyH-2, 1))
	g.detailH = max(g.bodyH-g.tableH-g.sepH, 1)
	return g
}

func (m Model) tableRows() int { return max(m.geom().tableH-1, 1) }

// layout recomputes sizes and refreshes the detail content after any state change.
func (m *Model) layout() {
	g := m.geom()
	m.detail.Width, m.detail.Height = g.detailW, g.detailH
	m.help.Width = max(m.width-1, 0)
	m.input.Width = max(m.width-4, 1)
	rows := g.tableH - 1
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(m.offset, 0)
	if w, ok := m.selected(); ok {
		content, line := m.detailContent(w, g.detailW)
		m.detail.SetContent(content)
		if m.detailFocus { // keep the destination cursor in view
			if line < m.detail.YOffset {
				m.detail.SetYOffset(line)
			} else if line >= m.detail.YOffset+g.detailH {
				m.detail.SetYOffset(line - g.detailH + 1)
			}
		}
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
		hint, h := m.coverageHint(), m.height
		msg := m.styles.Empty.Render("No workloads in snapshot")
		if hint == "" {
			return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, msg)
		}
		return hint + "\n" + lipgloss.Place(m.width, max(h-1, 1), lipgloss.Center, lipgloss.Center, msg)
	}
	g := m.geom()
	var body string
	table := block(m.tableView(g), g.tableW, g.tableH)
	detail := block(m.detail.View(), g.detailW, g.detailH)
	switch {
	case m.overlay:
		body = block(lipgloss.Place(m.width, g.bodyH, lipgloss.Center, lipgloss.Center, m.overlayBox()), m.width, g.bodyH)
	case m.help.ShowAll:
		body = block(indent(m.help.FullHelpView(m.keys.FullHelp())), m.width, g.bodyH)
	case g.side:
		sep := m.styles.Divider.Render(strings.TrimSuffix(strings.Repeat(" │ \n", g.bodyH), "\n"))
		body = lipgloss.JoinHorizontal(lipgloss.Top, table, sep, detail)
	default:
		div := m.styles.Divider.Render(strings.Repeat("─", m.width))
		body = lipgloss.JoinVertical(lipgloss.Left, table, div, detail)
	}
	return clip(m.headerView()+"\n"+body+"\n"+m.footerView(), m.width)
}

// footerView is the filter input, a close hint, or the short key help.
func (m Model) footerView() string {
	switch {
	case m.filtering:
		return " " + m.input.View()
	case m.overlay:
		return " " + m.styles.Muted.Render("esc/e close · copy the snippet into outflow.yaml by hand")
	case m.help.ShowAll:
		return " " + m.styles.Muted.Render("?/esc close help")
	}
	return " " + m.help.ShortHelpView(m.keys.ShortHelp())
}

func indent(s string) string {
	return " " + strings.ReplaceAll(s, "\n", "\n ")
}

// overlayBox is the centered exception snippet; outflow only shows it, never writes it.
func (m Model) overlayBox() string {
	inner := max(min(m.width-4, 64)-2, 1) // border + padding take 4 of the box width
	lines := []string{m.styles.Title.Render(fit("Add to outflow.yaml (copy by hand)", inner, false))}
	snippet := m.exceptionSnippet()
	if snippet == nil {
		lines = append(lines, m.styles.Muted.Render(fit("No destination selected", inner, false)))
	}
	for _, l := range snippet {
		lines = append(lines, fit(l, inner, false))
	}
	return m.styles.Overlay.Render(strings.Join(lines, "\n"))
}

// clip truncates every line to w cells as a last guard against wrapping.
func clip(s string, w int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "")
	}
	return strings.Join(lines, "\n")
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

// coverageHint is the second header line, or "" when coverage is full.
func (m Model) coverageHint() string {
	var t string
	switch m.coverage {
	case doctor.Partial:
		t = "coverage partial — run outflow doctor"
	case doctor.None:
		t = "coverage none — run outflow doctor"
	default:
		return ""
	}
	return " " + m.styles.Warn.Render(fit(t, max(m.width-1, 0), false))
}

// statusLine is the refresh progress or failure line, or "" when idle.
func (m Model) statusLine() string {
	var t string
	switch {
	case m.refreshing:
		t = "refreshing…"
	case m.refreshErr != "":
		t = "refresh failed: " + m.refreshErr
	default:
		return ""
	}
	return " " + m.styles.Warn.Render(fit(t, max(m.width-1, 0), false))
}

// headerView renders the header, fitted to the terminal width. When it is too
// wide the window label goes first, then the rule text is truncated; pills stay.
func (m Model) headerView() string {
	st, r := m.styles, m.report
	left := " " + st.Title.Render("outflow") + st.Muted.Render(" · ") + r.Cluster.Name + st.Muted.Render(" · ")
	if r.RuleConfigured {
		left += "rule: " + r.Rule
	} else {
		left += st.Muted.Render("no rule")
	}
	if f := m.input.Value(); f != "" {
		left += "   " + st.Warn.Render("filter: "+f)
	}
	if m.statusFilter != "" {
		left += "   " + st.Warn.Render("status: "+string(m.statusFilter))
	}
	var pills string
	if r.RuleConfigured {
		pills = strings.Join([]string{
			st.PillOut.Render(fmt.Sprintf("[%d outside]", r.Counts.Outside)),
			st.PillUnk.Render(fmt.Sprintf("[%d unknown]", r.Counts.Unknown)),
			st.PillIn.Render(fmt.Sprintf("[%d inside]", r.Counts.Inside)),
		}, " ")
	} else {
		pills = st.PillNeutral.Render(fmt.Sprintf("%d workloads · %d destinations · %d countries",
			r.Counts.Workloads, r.Counts.Destinations, r.Counts.Countries))
	}
	win := ""
	if w := windowLabel(r.Window.Start, r.Window.End); w != "" {
		win = "   " + st.Muted.Render(w)
	}
	const gap = "   "
	if lipgloss.Width(left)+len(gap)+lipgloss.Width(pills)+lipgloss.Width(win) > m.width {
		win = ""
	}
	room := m.width - len(gap) - lipgloss.Width(pills) - lipgloss.Width(win)
	line := fit(left, max(room, 0), false)
	if room < 1 {
		line = ""
	}
	line = strings.TrimRight(line, " ")
	line += gap + pills + win
	if lipgloss.Width(line) > m.width {
		line = fit(line, m.width, false)
	}
	if h := m.coverageHint(); h != "" {
		line += "\n" + h
	}
	if s := m.statusLine(); s != "" {
		line += "\n" + s
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
		return []col{{"STATUS", 7, false}, {"WORKLOAD", 0, false}, {"EGRESS", 13, false},
			{"DEST", 4, true}, {"OUT", 3, true}, {"UNK", 3, true}}
	}
	return []col{{"LOCATIONS", 12, false}, {"WORKLOAD", 0, false}, {"EGRESS", 13, false}, {"DEST", 4, true}}
}

func (m Model) tableView(g geometry) string {
	cols := m.workloadCols()
	ws := widths(cols, g.tableW-2) // 2 = selection marker column
	var heads []string
	for i, c := range cols {
		heads = append(heads, fit(c.title, ws[i], c.right))
	}
	lines := []string{m.styles.ColHead.Render("  " + strings.Join(heads, " "))}
	rows := m.visible()
	if len(rows) == 0 {
		lines = append(lines, "  "+m.styles.Empty.Render("No workloads match"))
	}
	end := min(m.offset+g.tableH-1, len(rows))
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.workloadRow(m.report.Workloads[rows[i]], cols, ws, i == m.cursor))
	}
	return strings.Join(lines, "\n")
}

func (m Model) workloadRow(w analyze.WorkloadResult, cols []col, ws []int, sel bool) string {
	st := m.styles
	var cells []string
	add := func(i int, text string, style lipgloss.Style) {
		s := style
		if sel {
			s = st.OnSelected(s)
		}
		cells = append(cells, s.Render(fit(text, ws[i], cols[i].right)))
	}
	plain := st.Row
	n := 0
	if m.report.RuleConfigured {
		glyph, style := statusGlyph(st, w.Status)
		add(n, glyph, style)
		n++
		add(n, w.ID, st.Row)
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
		marker = st.Marker.Render("▌ ")
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
	case analyze.OutsideDenied: // EVIDENCE says "denied"
		return "✖", st.Muted
	case analyze.Unknown:
		return "?", st.Unk
	case analyze.Inside:
		return "✔", st.In
	}
	return "", st.Muted
}

const hostNetworkNote = "hostNetwork traffic appears under the (node) row for its node"

// detailContent renders the detail pane and the line index of the destination cursor.
func (m Model) detailContent(w analyze.WorkloadResult, width int) (string, int) {
	st := m.styles
	var out []string
	title, sub := " "+w.ID, egressLine(w)
	if lipgloss.Width(title)+3+lipgloss.Width(sub) <= width {
		out = append(out, st.Title.Render(title)+"   "+st.Muted.Render(sub))
	} else {
		out = append(out, st.Title.Render(fit(title, width, false)), " "+st.Muted.Render(fit(sub, width-1, false)))
	}
	out = append(out, "")
	if w.HostNetwork {
		out = append(out, " "+st.Note.Render(fit(hostNetworkNote, width-1, false)), "")
	}
	dests := m.sortedDests(w)
	if len(dests) == 0 {
		return strings.Join(append(out, " "+st.Muted.Render("no destinations observed")), "\n"), 0
	}

	evW := len("EVIDENCE")
	for _, d := range dests {
		evW = max(evW, ansi.StringWidth(evidenceCell(d)))
	}
	cols := []col{{"DESTINATION", 0, false}, {"LOCATION", 22, false}, {"HOW KNOWN", 12, false},
		{"PORT", 5, true}, {"EVIDENCE", evW, false}, {"SEEN", 5, true}}
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
	cursorLine := 0
	for di, d := range dests {
		focused := m.detailFocus && di == m.destCursor
		name := d.Name()
		glyph, gs := destGlyph(st, d.Status)
		if m.report.RuleConfigured && glyph != "" {
			name = gs.Render(glyph) + " " + name
		}
		vals := []string{name, d.Location.String(), string(d.Location.HowKnown), portCell(d.Port), evidenceCell(d), fmtCount(d.Hits)}
		if len(cols) < len(vals) {
			vals = append(vals[:2], vals[3:]...)
		}
		var cells []string
		for i, v := range vals {
			cells = append(cells, fit(v, ws[i], cols[i].right))
		}
		if focused {
			cursorLine = len(out)
			out = append(out, st.Marker.Render("▌")+st.Selected.Render(ansi.Strip(strings.Join(cells, " "))))
			notes = append(notes, destNotes(d)...)
			continue
		}
		out = append(out, " "+strings.Join(cells, " "))
		if !m.detailFocus {
			notes = append(notes, destNotes(d)...)
		}
	}
	if len(notes) > 0 {
		out = append(out, "")
		for _, n := range notes {
			out = append(out, " "+st.Note.Render(fit(n, width-1, false)))
		}
	}
	return strings.Join(out, "\n"), cursorLine
}

// destNotes lists the per-destination remarks shown under the table.
func destNotes(d analyze.Destination) []string {
	name := d.Name()
	var n []string
	add := func(s string) { n = append(n, name+": "+s) }
	if d.Action == "deny" {
		add("blocked by the firewall (denied)")
	}
	if d.SaaS {
		add("server location ≠ data location — check vendor region")
	}
	if d.Exception != "" {
		add("exception: " + d.Exception)
	}
	switch d.IPSource {
	case "outflow":
		add("IP resolved by outflow")
	case "firewall log":
		add("IP from firewall log")
	}
	if len(d.OtherIPs) > 0 {
		line := "also resolves to: " + strings.Join(d.OtherIPs, ", ")
		if len(d.OtherLocations) > 0 {
			var ls []string
			for _, l := range d.OtherLocations {
				ls = append(ls, l.String())
			}
			line += " (" + strings.Join(ls, ", ") + ")"
		}
		if d.Note != "" {
			line += " — " + d.Note
		}
		add(line)
	} else if d.Note != "" {
		add(d.Note)
	}
	if d.Evidence == analyze.Likely && len(d.AlsoLikely) > 0 {
		add("also: " + strings.Join(d.AlsoLikely, ", "))
	}
	return n
}
