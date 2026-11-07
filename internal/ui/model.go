// Package ui is the interactive outflow terminal view (Bubble Tea).
package ui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/doctor"
)

// sideBySideMin is the terminal width from which the detail pane sits beside the table.
const sideBySideMin = 110

// Model is the workload-first screen: a workload table and a detail pane.
type Model struct {
	report   analyze.Report
	rerun    func() analyze.Report
	coverage doctor.Level
	styles   styles
	keys     keyMap
	help     help.Model

	width, height int
	cursor        int // index into report.Workloads
	offset        int // first visible table row
	detail        viewport.Model

	input        textinput.Model // the "/" filter; its value is the active filter text
	filtering    bool            // the input is open and owns the keyboard
	statusFilter analyze.Status  // "" = all
	detailFocus  bool            // enter moved focus to the detail pane
	destCursor   int             // highlighted destination while detailFocus
	overlay      bool            // exception snippet box is open
}

// New builds the screen for a report. rerun re-creates the report on demand.
func New(report analyze.Report, rerun func() analyze.Report) Model {
	h := help.New()
	st := newStyles()
	h.Styles.ShortKey, h.Styles.ShortDesc, h.Styles.ShortSeparator = st.Muted, st.Muted, st.Muted
	h.Styles.FullKey, h.Styles.FullDesc, h.Styles.FullSeparator = st.Muted, st.Muted, st.Muted
	h.ShortSeparator = " · "
	in := textinput.New()
	in.Prompt = "/ "
	in.PromptStyle = st.Prompt
	in.Placeholder = "workload"
	return Model{
		report: report, rerun: rerun, coverage: doctor.Full,
		styles: st, keys: newKeyMap(), help: h, input: in, detail: viewport.New(0, 0),
	}
}

// WithCoverage sets the coverage level; anything but FULL shows a hint under the header.
func (m Model) WithCoverage(l doctor.Level) Model {
	m.coverage = l
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.filtering {
		return m.onFilterKey(msg), nil
	}
	if m.overlay {
		switch {
		case key.Matches(msg, m.keys.Back, m.keys.Exception):
			m.overlay = false
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		}
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
	case key.Matches(msg, m.keys.Filter):
		m.filtering = true
		m.input.Focus()
		m.layout()
	case key.Matches(msg, m.keys.Status):
		m.cycleStatus()
	case key.Matches(msg, m.keys.Exception):
		m.overlay = true
	case key.Matches(msg, m.keys.Rerun):
		m.rerunReport()
	case key.Matches(msg, m.keys.Enter):
		if _, ok := m.selected(); ok && !m.detailFocus {
			m.detailFocus, m.destCursor = true, 0
			m.layout()
		}
	case key.Matches(msg, m.keys.Back):
		switch {
		case m.help.ShowAll:
			m.help.ShowAll = false
		case m.detailFocus:
			m.detailFocus = false
			m.layout()
		}
	case key.Matches(msg, m.keys.Up):
		m.step(-1)
	case key.Matches(msg, m.keys.Down):
		m.step(1)
	case key.Matches(msg, m.keys.PageUp):
		m.step(-m.tableRows())
	case key.Matches(msg, m.keys.PageDown):
		m.step(m.tableRows())
	case key.Matches(msg, m.keys.Top):
		m.step(-1 << 30)
	case key.Matches(msg, m.keys.Bottom):
		m.step(1 << 30)
	case key.Matches(msg, m.keys.DetailUp):
		m.detail.HalfViewUp()
	case key.Matches(msg, m.keys.DetailDown):
		m.detail.HalfViewDown()
	}
	return m, nil
}

// onFilterKey handles keys while the filter input is open.
func (m Model) onFilterKey(msg tea.KeyMsg) Model {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
		m.input.Blur()
		m.layout()
		return m
	case tea.KeyEsc:
		m.filtering = false
		m.input.Blur()
		m.input.SetValue("")
	default:
		id := m.selectedID()
		m.input, _ = m.input.Update(msg)
		m.reselect(id)
		return m
	}
	m.reselect(m.selectedID())
	return m
}

// cycleStatus steps all -> outside -> unknown -> inside -> all; neutral mode has no statuses.
func (m *Model) cycleStatus() {
	if !m.report.RuleConfigured {
		return
	}
	next := map[analyze.Status]analyze.Status{
		"": analyze.Outside, analyze.Outside: analyze.Unknown, analyze.Unknown: analyze.Inside, analyze.Inside: "",
	}
	id := m.selectedID()
	m.statusFilter = next[m.statusFilter]
	m.reselect(id)
}

// rerunReport swaps in a fresh report, keeping the selected workload when it still exists.
func (m *Model) rerunReport() {
	if m.rerun == nil {
		return
	}
	id := m.selectedID()
	m.report = m.rerun()
	m.reselect(id)
}

// visible lists indexes into report.Workloads that pass the status and text filters.
func (m Model) visible() []int {
	q := strings.ToLower(m.input.Value())
	var rows []int
	for i, w := range m.report.Workloads {
		if m.statusFilter != "" && w.Status != m.statusFilter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(w.ID), q) {
			continue
		}
		rows = append(rows, i)
	}
	return rows
}

// selected returns the highlighted row, if any.
func (m Model) selected() (analyze.WorkloadResult, bool) {
	rows := m.visible()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return analyze.WorkloadResult{}, false
	}
	return m.report.Workloads[rows[m.cursor]], true
}

func (m Model) selectedID() string {
	w, _ := m.selected()
	return w.ID
}

// selectedDest is the destination `e` acts on: the cursor's while the detail is
// focused, else the selected workload's first.
func (m Model) selectedDest() (analyze.WorkloadResult, analyze.Destination, bool) {
	w, ok := m.selected()
	ds := m.sortedDests(w)
	if !ok || len(ds) == 0 {
		return w, analyze.Destination{}, false
	}
	i := 0
	if m.detailFocus {
		i = min(m.destCursor, len(ds)-1)
	}
	return w, ds[i], true
}

// reselect re-anchors the cursor on workload id after the row set changed; when the
// row is gone the cursor index is clamped instead.
func (m *Model) reselect(id string) {
	rows := m.visible()
	for pos, i := range rows {
		if id != "" && m.report.Workloads[i].ID == id {
			m.cursor = pos
			m.clampDest()
			m.layout()
			return
		}
	}
	m.cursor = min(max(m.cursor, 0), max(len(rows)-1, 0))
	m.destCursor = 0
	m.clampDest()
	m.layout()
	m.detail.GotoTop()
}

func (m *Model) clampDest() {
	w, ok := m.selected()
	if !ok {
		m.detailFocus, m.destCursor = false, 0
		return
	}
	m.destCursor = min(max(m.destCursor, 0), max(len(w.Destinations)-1, 0))
}

// step moves the workload cursor, or the destination cursor while the detail is focused.
func (m *Model) step(n int) {
	if m.detailFocus {
		if w, ok := m.selected(); ok {
			m.destCursor = min(max(m.destCursor+n, 0), max(len(w.Destinations)-1, 0))
			m.layout()
		}
		return
	}
	m.move(n)
}

// move shifts the cursor by n rows, clamped, and refreshes the detail pane.
func (m *Model) move(n int) {
	last := len(m.visible()) - 1
	if last < 0 {
		return
	}
	m.cursor = min(max(m.cursor+n, 0), last)
	m.destCursor = 0
	m.layout()
	m.detail.GotoTop()
}

// exceptionSnippet is the YAML shown in the overlay, or nil without a destination.
func (m Model) exceptionSnippet() []string {
	w, d, ok := m.selectedDest()
	if !ok {
		return nil
	}
	target := "domain: " + d.Domain
	if d.Domain == "" {
		bits := "/32"
		if strings.Contains(d.IP, ":") {
			bits = "/128"
		}
		target = "cidr: " + d.IP + bits
	}
	out := []string{"residency:", "  exceptions:", "    - " + target}
	if !w.NodeLevel && !d.NodeLevel && !strings.HasPrefix(w.ID, "unknown pod (") {
		out = append(out, "      workload: "+w.ID)
	}
	return append(out, `      reason: ""`)
}

// sortedDests is the displayed destination order: worst status first when a rule is
// set (outside, unknown, outside (denied), inside), then hits descending, then name.
func (m Model) sortedDests(w analyze.WorkloadResult) []analyze.Destination {
	ds := append([]analyze.Destination(nil), w.Destinations...)
	rank := func(s analyze.Status) int {
		if !m.report.RuleConfigured {
			return 0
		}
		switch s {
		case analyze.Outside:
			return 0
		case analyze.Unknown:
			return 1
		case analyze.OutsideDenied:
			return 2
		case analyze.Inside:
			return 3
		}
		return 4
	}
	name := func(d analyze.Destination) string {
		if d.Domain != "" {
			return d.Domain
		}
		return d.IP
	}
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if ra, rb := rank(a.Status), rank(b.Status); ra != rb {
			return ra < rb
		}
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}
		return name(a) < name(b)
	})
	return ds
}
