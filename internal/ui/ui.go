package ui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/doctor"
)

// Run shows the interactive view until the user quits. Matches cli.TUIFunc.
func Run(report analyze.Report, cov doctor.Level, refresh func(context.Context) (analyze.Report, doctor.Level, error)) error {
	m := New(report, refresh).WithCoverage(cov)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
