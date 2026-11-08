package locdata

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/location"
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
	Host   string // host of the source's first URL, for debug output
	MaxAge time.Duration
	Fetch  Fetcher
}

// Endpoints holds the URLs the sources download from.
type Endpoints struct {
	AzurePage, AWS, GCP, CloudflareV4, CloudflareV6, Fastly, GeoPattern string
}

// DefaultEndpoints returns the real download URLs.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		AzurePage:    "https://www.microsoft.com/en-us/download/details.aspx?id=56519",
		AWS:          "https://ip-ranges.amazonaws.com/ip-ranges.json",
		GCP:          "https://www.gstatic.com/ipranges/cloud.json",
		CloudflareV4: "https://www.cloudflare.com/ips-v4",
		CloudflareV6: "https://www.cloudflare.com/ips-v6",
		Fastly:       "https://api.fastly.com/public-ip-list",
		GeoPattern:   "https://download.db-ip.com/free/dbip-country-lite-%s.csv.gz",
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.ReplaceAll(raw, "%s", "x"))
	if err != nil {
		return ""
	}
	return u.Host
}

// Hosts lists the distinct hostnames the sources contact. The Azure JSON file
// lives on download.microsoft.com, which only the page's link reveals.
func (e Endpoints) Hosts() []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	add(hostOf(e.AzurePage))
	add("download.microsoft.com")
	for _, u := range []string{e.AWS, e.GCP, e.CloudflareV4, e.CloudflareV6, e.Fastly, e.GeoPattern} {
		add(hostOf(u))
	}
	return out
}

// Sources returns the refreshable sources in location.SourceNames order.
func Sources(e Endpoints) []Source {
	fetch := map[string]Fetcher{
		"azure": fetchAzure(e.AzurePage),
		"aws":   fetchAWS(e.AWS),
		"gcp":   fetchGCP(e.GCP),
		"cdn":   fetchCDN(e.CloudflareV4, e.CloudflareV6, e.Fastly),
		"geo":   fetchGeo(e.GeoPattern),
	}
	first := map[string]string{
		"azure": e.AzurePage, "aws": e.AWS, "gcp": e.GCP, "cdn": e.CloudflareV4, "geo": e.GeoPattern,
	}
	srcs := make([]Source, 0, len(location.SourceNames))
	for _, name := range location.SourceNames {
		maxAge := 7 * day
		if name == "geo" {
			maxAge = 30 * day
		}
		srcs = append(srcs, Source{
			Name:   name,
			Label:  location.SourceLabel(name),
			Host:   hostOf(first[name]),
			MaxAge: maxAge,
			Fetch:  fetch[name],
		})
	}
	return srcs
}
