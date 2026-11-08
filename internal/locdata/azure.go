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

type azureCand struct{ region, service string }

// better reports whether a should replace b for the same prefix: a service
// other than AzureCloud wins, then the lexically smallest service, then region.
func (a azureCand) better(b azureCand) bool {
	if (a.service == "AzureCloud") != (b.service == "AzureCloud") {
		return b.service == "AzureCloud"
	}
	if a.service != b.service {
		return a.service < b.service
	}
	return a.region < b.region
}

func convertAzure(r io.Reader, version string) (Dataset, error) {
	var tags azureTags
	if err := json.NewDecoder(r).Decode(&tags); err != nil {
		return Dataset{}, fmt.Errorf("decode Azure service tags: %w", err)
	}
	regional := map[netip.Prefix]azureCand{}
	regionless := map[netip.Prefix]azureCand{}
	cdn := map[netip.Prefix]bool{}
	put := func(m map[netip.Prefix]azureCand, p netip.Prefix, c azureCand) {
		if old, ok := m[p]; !ok || c.better(old) {
			m[p] = c
		}
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
				put(regional, p, azureCand{v.Properties.Region, service})
			default:
				put(regionless, p, azureCand{"", service})
			}
		}
	}
	var cloud []cloudRow
	for p, c := range regional {
		cloud = append(cloud, cloudRow{p, "azure", c.region, c.service, false})
	}
	for p, c := range regionless {
		if _, ok := regional[p]; !ok {
			cloud = append(cloud, cloudRow{p, "azure", "", c.service, true})
		}
	}
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
		body, err := httpGet(ctx, hc, url, maxBody)
		if err != nil {
			return Dataset{}, err
		}
		return convertAzure(bytes.NewReader(body), date)
	}
}
