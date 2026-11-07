package main

import (
	"os"

	"github.com/jbrathwa/outflow/internal/cli"
	"github.com/jbrathwa/outflow/internal/ui"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, ui.Run))
}
