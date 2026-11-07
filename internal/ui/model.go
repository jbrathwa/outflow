// Package ui is the interactive outflow terminal view (Bubble Tea).
package ui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
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
}

// New builds the screen for a report. rerun re-creates the report on demand.
func New(report analyze.Report, rerun func() analyze.Report) Model {
	h := help.New()
	st := newStyles()
	h.Styles.ShortKey, h.Styles.ShortDesc, h.Styles.ShortSeparator = st.Muted, st.Muted, st.Muted
	return Model{
		report: report, rerun: rerun, coverage: doctor.Full,
		styles: st, keys: newKeyMap(), help: h, detail: viewport.New(0, 0),
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
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Up):
			m.move(-1)
		case key.Matches(msg, m.keys.Down):
			m.move(1)
		case key.Matches(msg, m.keys.PageUp):
			m.move(-m.tableRows())
		case key.Matches(msg, m.keys.PageDown):
			m.move(m.tableRows())
		case key.Matches(msg, m.keys.Top):
			m.move(-len(m.report.Workloads))
		case key.Matches(msg, m.keys.Bottom):
			m.move(len(m.report.Workloads))
		case key.Matches(msg, m.keys.DetailUp):
			m.detail.HalfViewUp()
		case key.Matches(msg, m.keys.DetailDown):
			m.detail.HalfViewDown()
		}
	}
	return m, nil
}

// selected returns the highlighted row, if any.
func (m Model) selected() (analyze.WorkloadResult, bool) {
	if m.cursor < 0 || m.cursor >= len(m.report.Workloads) {
		return analyze.WorkloadResult{}, false
	}
	return m.report.Workloads[m.cursor], true
}

// move shifts the cursor by n rows, clamped, and refreshes the detail pane.
func (m *Model) move(n int) {
	last := len(m.report.Workloads) - 1
	if last < 0 {
		return
	}
	m.cursor = min(max(m.cursor+n, 0), last)
	m.layout()
	m.detail.GotoTop()
}
