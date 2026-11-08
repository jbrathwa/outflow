package locdata

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
)

// Status is the outcome of refreshing one source.
type Status string

const (
	Refreshed Status = "refreshed"
	UpToDate  Status = "up to date"
	Failed    Status = "failed"
)

// Result is the outcome for one source.
type Result struct {
	Source, Label string
	Status        Status
	Version       string
	Err           error
}

// Refresh updates every stale source in parallel and returns one Result per
// source, in srcs order. The caller's ctx sets the deadline. Failures never
// touch previously cached data.
func Refresh(ctx context.Context, c Cache, hc *http.Client, srcs []Source, force bool) []Result {
	results := make([]Result, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = refreshOne(ctx, c, hc, s, force)
		}()
	}
	wg.Wait()
	return results
}

// Due reports whether a non-forced Refresh would download s: it is missing
// from the cache or older than its MaxAge.
func (c Cache) Due(s Source) bool {
	m, ok := c.Meta(s.Name)
	return !ok || c.now().Sub(m.Fetched) > s.MaxAge
}

func refreshOne(ctx context.Context, c Cache, hc *http.Client, s Source, force bool) Result {
	res := Result{Source: s.Name, Label: s.Label}
	if !force && !c.Due(s) {
		m, _ := c.Meta(s.Name)
		res.Status, res.Version = UpToDate, m.Version
		return res
	}
	start := time.Now()
	ds, err := s.Fetch(ctx, hc, c.now())
	dur := time.Since(start).Round(time.Millisecond)
	if err != nil {
		collect.Debugf("location data %s: %s error in %s, 0 rows", s.Name, s.Host, dur)
		res.Status, res.Err = Failed, err
		return res
	}
	collect.Debugf("location data %s: %s ok in %s, %d rows", s.Name, s.Host, dur, countRows(ds))
	if err := c.Save(s.Name, ds); err != nil {
		res.Status, res.Err = Failed, fmt.Errorf("could not save location data (%w)", err)
		return res
	}
	res.Status, res.Version = Refreshed, ds.Version
	return res
}

// countRows counts data lines across the dataset's files, excluding headers.
func countRows(ds Dataset) int {
	n := 0
	for _, b := range ds.Files {
		if l := bytes.Count(b, []byte("\n")); l > 0 {
			n += l - 1
		}
	}
	return n
}
