package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/doctor"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
)

// coverageJSON is the report's coverage section.
type coverageJSON struct {
	Level  string   `json:"level"`
	Can    []string `json:"can"`
	Cannot []string `json:"cannot"`
}

type reportJSON struct {
	analyze.Report
	Coverage coverageJSON `json:"coverage"`
}

// WriteJSON writes the report plus coverage as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, r analyze.Report, cov doctor.Coverage) error {
	out := reportJSON{Report: r, Coverage: coverageJSON{Level: string(cov.Level), Can: nz(cov.Can), Cannot: nz(cov.Cannot)}}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func scan(stdout, stderr io.Writer, snap model.Snapshot, db *location.DB, cfg config.Config,
	cfgFound bool, format, failOn string, allowPartial bool) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "outflow: %v\n", err)
		return exitError
	}
	report := analyze.Build(snap, db, cfg)
	cov := doctor.Run(snap, db, cfg, cfgFound).Coverage

	// Validate the gate before printing anything: a bad gate must not look like a pass.
	if failOn != "" {
		if !report.RuleConfigured {
			return fail(errors.New("--fail-on needs a residency rule in outflow.yaml"))
		}
		if failOn == "unknown" && cfg.Residency.UnknownLocation == "ignore" {
			return fail(errors.New("--fail-on unknown conflicts with unknown_location: ignore"))
		}
		switch {
		case cov.Level == doctor.None:
			return fail(errors.New("--fail-on cannot judge anything: coverage is NONE (no traffic sources); run outflow doctor"))
		case cov.Level != doctor.Full && !allowPartial:
			return fail(fmt.Errorf("--fail-on with %s coverage could miss violations; fix the sources (outflow doctor) or pass --allow-partial", cov.Level))
		}
	}

	var err error
	if format == "md" {
		err = WriteMarkdown(stdout, report, cov)
	} else {
		err = WriteJSON(stdout, report, cov)
	}
	if err != nil {
		return fail(err)
	}
	if failOn == "" {
		return exitOK
	}
	if cov.Level != doctor.Full {
		fmt.Fprintf(stderr, "outflow: warning: %s coverage; a clean result may be missing violations\n", cov.Level)
	}
	broken := report.Counts.Outside > 0
	if failOn == "unknown" || cfg.Residency.UnknownLocation == "fail" {
		broken = broken || report.Counts.Unknown > 0
	}
	if broken {
		return exitRule
	}
	return exitOK
}
