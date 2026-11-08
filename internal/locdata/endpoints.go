package locdata

import (
	"context"
	"net/http"
	"time"
)

// Dataset is one downloaded source, ready to be cached.
// Files keys are loader file names: "cloud.csv", "cdn.csv", "geo.csv".
type Dataset struct {
	Version string
	Files   map[string][]byte
}

// Fetcher downloads one source with plain anonymous GETs.
type Fetcher func(ctx context.Context, hc *http.Client, now time.Time) (Dataset, error)

// Source describes one refreshable location data source.
type Source struct {
	Name   string
	Label  string
	MaxAge time.Duration
	Fetch  Fetcher
}
