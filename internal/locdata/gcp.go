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
	"time"
)

type gcpRanges struct {
	CreationTime string `json:"creationTime"`
	Prefixes     []struct {
		IPv4Prefix string `json:"ipv4Prefix"`
		IPv6Prefix string `json:"ipv6Prefix"`
		Service    string `json:"service"`
		Scope      string `json:"scope"`
	} `json:"prefixes"`
}

func convertGCP(r io.Reader) (Dataset, error) {
	var in gcpRanges
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return Dataset{}, fmt.Errorf("decode GCP cloud.json: %w", err)
	}
	regional := map[netip.Prefix]rangeCand{}
	regionless := map[netip.Prefix]rangeCand{}
	valid := 0
	for _, e := range in.Prefixes {
		s := e.IPv4Prefix
		if s == "" {
			s = e.IPv6Prefix
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			continue
		}
		valid++
		p = p.Masked()
		if e.Scope == "" || e.Scope == "global" {
			putCand(regionless, p, rangeCand{"", e.Service}, "")
		} else {
			putCand(regional, p, rangeCand{e.Scope, e.Service}, "")
		}
	}
	if valid == 0 {
		return Dataset{}, errors.New("GCP cloud.json contains no prefixes")
	}
	return Dataset{Version: in.CreationTime, Files: map[string][]byte{
		"cloud.csv": cloudCSV(pickCloud("gcp", regional, regionless)),
	}}, nil
}

// fetchGCP downloads and converts the GCP cloud.json at url.
func fetchGCP(url string) Fetcher {
	return func(ctx context.Context, hc *http.Client, now time.Time) (Dataset, error) {
		body, err := httpGet(ctx, hc, url, maxBody)
		if err != nil {
			return Dataset{}, err
		}
		return convertGCP(bytes.NewReader(body))
	}
}
