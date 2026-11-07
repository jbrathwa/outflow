// Package cli is outflow's testable command layer: flag parsing, snapshot and
// config resolution, and the documented exit codes (0 clean, 2 rule broken, 1 error).
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/fixtures"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitRule  = 2
)

// TUIFunc runs the interactive UI for an analyzed report.
type TUIFunc func(report analyze.Report, snap model.Snapshot, db *location.DB, cfg config.Config) error

const usage = `outflow shows which Kubernetes workloads reach the internet, and where.

Usage:
  outflow [flags]            interactive view
  outflow scan [flags]       print a report and exit by --fail-on
  outflow doctor [flags]     check data sources and coverage
  outflow version

Common flags:
  --demo                 use the built-in demo snapshot
  --demo-rule            apply the built-in demo residency rule
  --snapshot <file>      read a snapshot JSON file
  --dump-demo            write the demo snapshot JSON to stdout
  --config <file>        residency config (default ./outflow.yaml if present)
  --context, --profile, --since, --workspace
                         accepted; used by live collection (not available yet)

Scan flags:
  --format json|md       output format (default json)
  --json                 same as --format json
  --fail-on outside|unknown
                         exit 2 when the residency rule is broken
  --allow-partial        allow --fail-on with partial coverage

Exit codes: 0 clean, 2 rule broken per --fail-on, 1 tool, setup or config error.
`

// common holds the flags shared by every command.
type common struct {
	demo, demoDegraded, demoRule, dumpDemo bool
	snapshot, config                       string
	context, profile, since, workspace     string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.BoolVar(&c.demo, "demo", false, "")
	fs.BoolVar(&c.demoDegraded, "demo-degraded", false, "")
	fs.BoolVar(&c.demoRule, "demo-rule", false, "")
	fs.BoolVar(&c.dumpDemo, "dump-demo", false, "")
	fs.StringVar(&c.snapshot, "snapshot", "", "")
	fs.StringVar(&c.config, "config", "", "")
	fs.StringVar(&c.context, "context", "", "")
	fs.StringVar(&c.profile, "profile", "", "")
	fs.StringVar(&c.since, "since", "", "")
	fs.StringVar(&c.workspace, "workspace", "", "")
}

// Run executes the outflow CLI and returns its exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, tui TUIFunc) int {
	cmd := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "outflow dev")
		return exitOK
	case "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "", "scan", "doctor":
	default:
		fmt.Fprintf(stderr, "outflow: unknown command %q\n\n%s", cmd, usage)
		return exitError
	}

	fs := flag.NewFlagSet("outflow "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.register(fs)
	var format, failOn string
	var allowPartial, asJSON bool
	if cmd == "scan" {
		fs.StringVar(&format, "format", "json", "")
		fs.BoolVar(&asJSON, "json", false, "")
		fs.StringVar(&failOn, "fail-on", "", "")
		fs.BoolVar(&allowPartial, "allow-partial", false, "")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		fmt.Fprintf(stderr, "outflow: %v\n\n%s", err, usage)
		return exitError
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "outflow: unexpected argument %q\n\n%s", fs.Arg(0), usage)
		return exitError
	}
	if c.dumpDemo {
		if _, err := stdout.Write(fixtures.DemoJSON()); err != nil {
			fmt.Fprintf(stderr, "outflow: write demo: %v\n", err)
			return exitError
		}
		return exitOK
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "outflow: %v\n", err)
		return exitError
	}

	if cmd == "scan" {
		if asJSON && format != "json" {
			return fail(fmt.Errorf("--json conflicts with --format %s", format))
		}
		if format != "json" && format != "md" {
			return fail(fmt.Errorf("--format must be json or md, got %q", format))
		}
		if failOn != "" && failOn != "outside" && failOn != "unknown" {
			return fail(fmt.Errorf("--fail-on must be outside or unknown, got %q", failOn))
		}
	}
	snap, err := c.loadSnapshot()
	if err != nil {
		return fail(err)
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return fail(err)
	}
	db, err := location.Embedded()
	if err != nil {
		return fail(fmt.Errorf("location data: %w", err))
	}
	cfgFound := cfg.Residency != nil

	switch cmd {
	case "doctor":
		res := doctor.Run(snap, db, cfg, cfgFound)
		doctor.Print(stdout, res, useColor(stdout))
		if res.Failed() {
			return exitError
		}
		return exitOK
	case "scan":
		return scan(stdout, stderr, snap, db, cfg, cfgFound, format, failOn, allowPartial)
	}
	report := analyze.Build(snap, db, cfg)
	if tui == nil {
		return fail(errors.New("interactive view is not available"))
	}
	if err := tui(report, snap, db, cfg); err != nil {
		return fail(err)
	}
	return exitOK
}

func (c *common) loadSnapshot() (model.Snapshot, error) {
	n := 0
	for _, b := range []bool{c.demo, c.demoDegraded, c.snapshot != ""} {
		if b {
			n++
		}
	}
	switch {
	case n > 1:
		return model.Snapshot{}, errors.New("--demo and --snapshot are mutually exclusive")
	case c.demo:
		s, err := fixtures.Demo()
		if err != nil {
			return s, fmt.Errorf("demo snapshot: %w", err)
		}
		return s, nil
	case c.demoDegraded:
		return fixtures.DemoDegraded(), nil
	case c.snapshot != "":
		b, err := os.ReadFile(c.snapshot)
		if err != nil {
			return model.Snapshot{}, fmt.Errorf("read snapshot %s: %w", c.snapshot, err)
		}
		var s model.Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return s, fmt.Errorf("parse snapshot %s: %w", c.snapshot, err)
		}
		if s.SchemaVersion != model.SchemaVersion {
			return s, fmt.Errorf("snapshot %s: schemaVersion %d is not supported (want %d)", c.snapshot, s.SchemaVersion, model.SchemaVersion)
		}
		return s, nil
	}
	return model.Snapshot{}, errors.New("live collection is not available yet; use --demo or --snapshot <file>")
}

func (c *common) loadConfig() (config.Config, error) {
	switch {
	case c.demoRule && c.config != "":
		return config.Config{}, errors.New("--demo-rule and --config are mutually exclusive")
	case c.demoRule:
		return fixtures.DemoResidency()
	case c.config != "":
		return config.Load(c.config)
	}
	return config.LoadDefault(".")
}

// noColor reports whether NO_COLOR asks for no colour: set to a non-empty value (no-color.org).
func noColor() bool { return os.Getenv("NO_COLOR") != "" }

// useColor enables ANSI only for a terminal stdout unless NO_COLOR is set.
func useColor(w io.Writer) bool {
	if noColor() {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
