package locdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
)

var serviceTagsRe = regexp.MustCompile(`https?://[^"'\s<>]+/ServiceTags_Public_(\d{8})\.json`)

// findServiceTagsURL extracts the weekly JSON link and its date from the download page.
func findServiceTagsURL(page []byte) (url, date string, err error) {
	m := serviceTagsRe.FindSubmatch(page)
	if m == nil {
		return "", "", errors.New("Azure download page changed (no ServiceTags_Public link)")
	}
	return string(m[0]), string(m[1]), nil
}

type azureTags struct {
	Values []struct {
		Name       string `json:"name"`
		Properties struct {
			Region          string   `json:"region"`
			AddressPrefixes []string `json:"addressPrefixes"`
		} `json:"properties"`
	} `json:"values"`
}

// rangeCand is one (region, service) candidate for a prefix.
type rangeCand struct{ region, service string }

// better reports whether a should replace b for the same prefix: a service
// other than the aggregate one wins, then the lexically smallest service,
// then region.
func (a rangeCand) better(b rangeCand, aggregate string) bool {
	if (a.service == aggregate) != (b.service == aggregate) {
		return b.service == aggregate
	}
	if a.service != b.service {
		return a.service < b.service
	}
	return a.region < b.region
}

// pickCloud turns regional and regionless candidates into rows: a regional
// candidate beats a regionless (global) one for the same prefix.
func pickCloud(provider string, regional, regionless map[netip.Prefix]rangeCand) []cloudRow {
	var rows []cloudRow
	for p, c := range regional {
		rows = append(rows, cloudRow{p, provider, c.region, c.service, false})
	}
	for p, c := range regionless {
		if _, ok := regional[p]; !ok {
			rows = append(rows, cloudRow{p, provider, "", c.service, true})
		}
	}
	return rows
}

// putCand records c for p unless a better candidate is already there.
func putCand(m map[netip.Prefix]rangeCand, p netip.Prefix, c rangeCand, aggregate string) {
	if old, ok := m[p]; !ok || c.better(old, aggregate) {
		m[p] = c
	}
}

func convertAzure(r io.Reader, version string) (Dataset, error) {
	var tags azureTags
	if err := json.NewDecoder(r).Decode(&tags); err != nil {
		return Dataset{}, fmt.Errorf("decode Azure service tags: %w", err)
	}
	regional := map[netip.Prefix]rangeCand{}
	regionless := map[netip.Prefix]rangeCand{}
	cdn := map[netip.Prefix]bool{}
	put := func(m map[netip.Prefix]rangeCand, p netip.Prefix, c rangeCand) {
		putCand(m, p, c, "AzureCloud")
	}
	for _, v := range tags.Values {
		service, _, _ := strings.Cut(v.Name, ".")
		for _, s := range v.Properties.AddressPrefixes {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				continue
			}
			p = p.Masked()
			switch {
			case v.Name == "AzureFrontDoor.Frontend":
				cdn[p] = true
			case v.Properties.Region != "":
				put(regional, p, rangeCand{v.Properties.Region, service})
			default:
				put(regionless, p, rangeCand{"", service})
			}
		}
	}
	cloud := pickCloud("azure", regional, regionless)
	var cdnRows []cdnRow
	for p := range cdn {
		cdnRows = append(cdnRows, cdnRow{p, "Azure Front Door"})
	}
	if len(cloud) == 0 && len(cdnRows) == 0 {
		return Dataset{}, errors.New("Azure service tags contain no prefixes")
	}
	return Dataset{Version: version, Files: map[string][]byte{
		"cloud.csv": cloudCSV(cloud),
		"cdn.csv":   cdnCSV(cdnRows),
	}}, nil
}

// fetchAzure finds the weekly service tags file via the download page and converts it.
func fetchAzure(pageURL string) Fetcher {
	return func(ctx context.Context, hc *http.Client, now time.Time) (Dataset, error) {
		page, err := httpGet(ctx, hc, pageURL, maxBody)
		if err != nil {
			return Dataset{}, err
		}
		url, date, err := findServiceTagsURL(page)
		if err != nil {
			return Dataset{}, err
		}
		collect.Debugf("location data azure: json from %s", hostOf(url))
		body, err := httpGet(ctx, hc, url, maxBody)
		if err != nil {
			return Dataset{}, err
		}
		return convertAzure(bytes.NewReader(body), date)
	}
}
