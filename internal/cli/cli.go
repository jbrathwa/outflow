// Package cli is outflow's testable command layer: flag parsing, snapshot and
// config resolution, and the documented exit codes (0 clean, 2 rule broken, 1 error).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/fixtures"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitRule  = 2
)

// RefreshFunc re-creates the report and its coverage level (re-collecting in live mode).
// It is an alias so ui.Run stays assignable to TUIFunc without importing cli.
type RefreshFunc = func(context.Context) (analyze.Report, doctor.Level, error)

// TUIFunc runs the interactive UI for an analyzed report.
type TUIFunc func(report analyze.Report, coverage doctor.Level, refresh RefreshFunc) error

const usage = `outflow shows which Kubernetes workloads reach the internet, and where.

Usage:
  outflow [flags]            interactive view
  outflow scan [flags]       print a report and exit by --fail-on
  outflow doctor [flags]     check data sources and coverage
  outflow snapshot -o <file> [flags]
                             collect a snapshot JSON file (- for stdout, mode 0600)
  outflow version

Without --demo or --snapshot, outflow reads the cluster in the current kubeconfig
context and, on AKS, its Log Analytics workspaces (read-only).

Common flags:
  --demo                 use the built-in demo snapshot
  --demo-rule            apply the built-in demo residency rule
  --snapshot <file>      read a snapshot JSON file
  --dump-demo            write the demo snapshot JSON to stdout
  --config <file>        residency config (default ./outflow.yaml if present)
  --context <name>       kubeconfig context (default current-context)
  --profile auto|azure|generic
                         log sources to read (default auto: azure on AKS, else generic)
  --since <duration>     how far back to read, 5m..30d (default 24h; e.g. 90m, 7d)
  --workspace <guid>     Log Analytics workspace for DNS, pod and firewall logs
                         (overrides sources in outflow.yaml)
  --join-window <duration>
                         how long after a DNS lookup a firewall event still counts,
                         1m..60m (default 5m)
  --debug                print query diagnostics to stderr (no log data)

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
	demo, demoDegraded, demoRule, dumpDemo, debug bool
	snapshot, config                              string
	context, profile, since, workspace            string
	joinWindow                                    string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.BoolVar(&c.demo, "demo", false, "")
	fs.BoolVar(&c.demoDegraded, "demo-degraded", false, "")
	fs.BoolVar(&c.demoRule, "demo-rule", false, "")
	fs.BoolVar(&c.dumpDemo, "dump-demo", false, "")
	fs.StringVar(&c.snapshot, "snapshot", "", "")
	fs.StringVar(&c.config, "config", "", "")
	fs.StringVar(&c.context, "context", "", "")
	fs.StringVar(&c.profile, "profile", "auto", "")
	fs.StringVar(&c.since, "since", "", "")
	fs.StringVar(&c.workspace, "workspace", "", "")
	fs.StringVar(&c.joinWindow, "join-window", "5m", "")
	fs.BoolVar(&c.debug, "debug", false, "")
}

// isLive reports whether no offline source (--demo, --demo-degraded, --snapshot) was given.
func (c *common) isLive() bool { return !c.demo && !c.demoDegraded && c.snapshot == "" }

// Run executes the outflow CLI and returns its exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, tui TUIFunc) int {
	cmd := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, versionString(Version, Commit, Date, moduleVersion()))
		return exitOK
	case "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "", "scan", "doctor", "snapshot":
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
	var output string
	if cmd == "snapshot" {
		fs.StringVar(&output, "o", "", "")
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
	if cmd == "snapshot" {
		if err := c.snapshotFlagError(); err != nil {
			fmt.Fprintf(stderr, "outflow: %v\n", err)
			return exitError
		}
		if output == "" {
			fmt.Fprintln(stderr, "outflow: snapshot needs -o <file> (- for stdout)")
			return exitError
		}
	}
	if c.dumpDemo {
		if _, err := stdout.Write(fixtures.DemoJSON()); err != nil {
			fmt.Fprintf(stderr, "outflow: write demo: %v\n", err)
			return exitError
		}
		return exitOK
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "outflow: %s\n", liveMessage(err))
		return exitError
	}
	if c.debug {
		collect.SetDebug(stderr)
		defer collect.SetDebug(nil)
	}
	joinWindow, err := parseJoinWindow(c.joinWindow)
	if err != nil {
		return fail(err)
	}
	opt := analyze.Options{JoinWindow: joinWindow}

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

	// collectSnap returns the snapshot to analyze: the offline one, or a fresh live collection.
	collectSnap := func(context.Context) (model.Snapshot, error) { return snap, nil }
	ctx := context.Background()
	if c.isLive() {
		if cmd == "" && tui == nil {
			return fail(errors.New("interactive view is not available"))
		}
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		l, err := c.newLive(ctx, cfg)
		if err != nil {
			return fail(err)
		}
		collectSnap = func(ctx context.Context) (model.Snapshot, error) { return l.col.Collect(ctx, l.opt) }
		if cmd == "doctor" {
			collectSnap = func(ctx context.Context) (model.Snapshot, error) { return l.col.Probe(ctx, l.opt) }
		}
	}
	snap, err = collectSnap(ctx)
	if err != nil {
		return fail(err)
	}

	switch cmd {
	case "snapshot":
		if err := writeSnapshot(stdout, stderr, output, snap); err != nil {
			return fail(err)
		}
		return exitOK
	case "doctor":
		res := doctor.Run(snap, db, cfg, cfgFound)
		doctor.Print(stdout, res, useColor(stdout))
		if res.Failed() {
			return exitError
		}
		return exitOK
	case "scan":
		return scan(stdout, stderr, snap, db, cfg, cfgFound, opt, format, failOn, allowPartial)
	}
	if tui == nil {
		return fail(errors.New("interactive view is not available"))
	}
	analyzeSnap := func(s model.Snapshot) (analyze.Report, doctor.Level) {
		return analyze.BuildWith(s, db, cfg, opt), doctor.Run(s, db, cfg, cfgFound).Coverage.Level
	}
	report, cov := analyzeSnap(snap)
	refresh := func(ctx context.Context) (analyze.Report, doctor.Level, error) {
		s, err := collectSnap(ctx)
		if err != nil {
			return analyze.Report{}, "", err
		}
		r, l := analyzeSnap(s)
		return r, l, nil
	}
	if err := tui(report, cov, refresh); err != nil {
		return fail(err)
	}
	return exitOK
}

func (c *common) loadSnapshot() (model.Snapshot, error) {
	var set []string
	for _, f := range []struct {
		name string
		on   bool
	}{{"--demo", c.demo}, {"--demo-degraded", c.demoDegraded}, {"--snapshot", c.snapshot != ""}} {
		if f.on {
			set = append(set, f.name)
		}
	}
	switch {
	case len(set) > 1:
		names := strings.Join(set[:len(set)-1], ", ") + " and " + set[len(set)-1]
		return model.Snapshot{}, fmt.Errorf("%s are mutually exclusive", names)
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
	return model.Snapshot{}, nil // live: collected later
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
