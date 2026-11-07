package main

import (
	"errors"
	"os"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/cli"
	"github.com/jbrathwa/outflow/internal/config"
)

// runTUI is a stub until the interactive view is built.
func runTUI(analyze.Report, model.Snapshot, *location.DB, config.Config) error {
	return errors.New("TUI not built yet")
}

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, runTUI))
}
