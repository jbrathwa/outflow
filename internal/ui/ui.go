package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
)

// Run shows the interactive view until the user quits. Matches cli.TUIFunc.
func Run(report analyze.Report, snap model.Snapshot, db *location.DB, cfg config.Config) error {
	cov := doctor.Run(snap, db, cfg, cfg.Residency != nil).Coverage.Level
	rerun := func() analyze.Report { return analyze.Build(snap, db, cfg) }
	m := New(report, rerun).WithCoverage(cov)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
