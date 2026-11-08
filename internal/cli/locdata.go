package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/locdata"
)

// locEndpoints is where location data is downloaded from; tests point it at httptest.
var locEndpoints = locdata.DefaultEndpoints()

// autoRefresh enables the pre-scan download; TestMain turns it off.
var autoRefresh = true

// refreshTimeout caps the whole refresh, all sources together.
const refreshTimeout = 60 * time.Second

// loadLocation builds the location database. --demo and --demo-degraded use the
// built-in sample only. Otherwise it reads the cache, first refreshing stale
// sources for a live scan or interactive run. Refresh problems only warn.
func loadLocation(ctx context.Context, stderr io.Writer, c *common, cmd string) (*location.DB, error) {
	if c.demo || c.demoDegraded {
		return locdata.SampleOnly()
	}
	dir, err := locdata.DefaultDir()
	if err != nil {
		fmt.Fprintf(stderr, "outflow: warning: could not find the location data cache folder (%v); using built-in sample data\n", err)
		return locdata.SampleOnly()
	}
	cache := locdata.Cache{Dir: dir}
	srcs := locdata.Sources(locEndpoints)
	var results []locdata.Result
	if autoRefresh && c.isLive() && !c.noRefresh && (cmd == "" || cmd == "scan") {
		locdata.UserAgent = "outflow/" + Version
		rctx, cancel := context.WithTimeout(ctx, refreshTimeout)
		results = locdata.Refresh(rctx, cache, &http.Client{}, srcs, false)
		cancel()
	}
	db, issues, err := cache.Load(srcs)
	if err != nil {
		return nil, err
	}
	if l := refreshLine(results); l != "" {
		fmt.Fprintln(stderr, l)
	}
	for _, w := range warnLines(results, issues, db) {
		fmt.Fprintln(stderr, w)
	}
	return db, nil
}

// refreshLine summarises a refresh, or returns "" when nothing was refreshed.
func refreshLine(rs []locdata.Result) string {
	var done, fresh []string
	for _, r := range rs {
		switch r.Status {
		case locdata.Refreshed:
			done = append(done, r.Source)
		case locdata.UpToDate:
			fresh = append(fresh, r.Source)
		}
	}
	if len(done) == 0 {
		return ""
	}
	line := "outflow: location data: refreshed " + strings.Join(done, ", ")
	if len(fresh) > 0 {
		line += " (" + strings.Join(fresh, ", ") + " up to date)"
	}
	return line
}

// warnLines reports failed refreshes and unreadable cached sources.
func warnLines(rs []locdata.Result, issues []locdata.LoadIssue, db *location.DB) []string {
	var out []string
	for _, r := range rs {
		if r.Status != locdata.Failed {
			continue
		}
		using := "using built-in sample data"
		for _, s := range db.Sources() {
			if s.Name == r.Source && !s.Sample {
				using = fmt.Sprintf("using data from %s (%d days old)", s.Fetched.Format("2006-01-02"), s.AgeDays)
			}
		}
		out = append(out, fmt.Sprintf("outflow: warning: could not refresh %s (%v); %s", r.Label, r.Err, using))
	}
	for _, i := range issues {
		out = append(out, fmt.Sprintf("outflow: warning: cached %s could not be read (%v); using built-in sample data",
			location.SourceLabel(i.Source), i.Err))
	}
	return out
}

// updateData downloads every location data source now, regardless of age.
func updateData(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("outflow update-data", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var debug bool
	fs.BoolVar(&debug, "debug", false, "")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
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
	if debug {
		collect.SetDebug(stderr)
		defer collect.SetDebug(nil)
	}
	dir, err := locdata.DefaultDir()
	if err != nil {
		fmt.Fprintf(stderr, "outflow: could not find the location data cache folder: %v\n", err)
		return exitError
	}
	locdata.UserAgent = "outflow/" + Version
	srcs := locdata.Sources(locEndpoints)
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	results := locdata.Refresh(ctx, locdata.Cache{Dir: dir}, &http.Client{}, srcs, true)

	width := 0
	for _, r := range results {
		width = max(width, len(r.Label))
	}
	width += 6
	fmt.Fprintln(stdout, "Downloads public files only; no Azure or Kubernetes access is used.")
	code := exitOK
	for _, r := range results {
		if r.Status == locdata.Failed {
			code = exitError
			fmt.Fprintf(stdout, "✘ %-*s%v\n", width, r.Label, r.Err)
			continue
		}
		fmt.Fprintf(stdout, "✔ %-*s%s\n", width, r.Label, r.Version)
	}
	fmt.Fprintf(stdout, "Saved in %s\n", dir)
	return code
}
