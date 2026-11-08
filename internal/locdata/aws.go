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

type awsRanges struct {
	CreateDate string `json:"createDate"`
	Prefixes   []struct {
		IPPrefix string `json:"ip_prefix"`
		Region   string `json:"region"`
		Service  string `json:"service"`
	} `json:"prefixes"`
	IPv6Prefixes []struct {
		IPv6Prefix string `json:"ipv6_prefix"`
		Region     string `json:"region"`
		Service    string `json:"service"`
	} `json:"ipv6_prefixes"`
}

func convertAWS(r io.Reader) (Dataset, error) {
	var in awsRanges
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return Dataset{}, fmt.Errorf("decode AWS ip-ranges: %w", err)
	}
	regional := map[netip.Prefix]rangeCand{}
	regionless := map[netip.Prefix]rangeCand{}
	cdn := map[netip.Prefix]bool{}
	valid := 0
	add := func(s, region, service string) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return
		}
		valid++
		p = p.Masked()
		switch {
		case service == "CLOUDFRONT":
			cdn[p] = true
		case region == "GLOBAL" || region == "":
			putCand(regionless, p, rangeCand{"", service}, "AMAZON")
		default:
			putCand(regional, p, rangeCand{region, service}, "AMAZON")
		}
	}
	for _, e := range in.Prefixes {
		add(e.IPPrefix, e.Region, e.Service)
	}
	for _, e := range in.IPv6Prefixes {
		add(e.IPv6Prefix, e.Region, e.Service)
	}
	if valid == 0 {
		return Dataset{}, errors.New("AWS ip-ranges contain no prefixes")
	}
	// CloudFront prefixes live only in the CDN table, even their AMAZON duplicates.
	for p := range cdn {
		delete(regional, p)
		delete(regionless, p)
	}
	var cdnRows []cdnRow
	for p := range cdn {
		cdnRows = append(cdnRows, cdnRow{p, "CloudFront"})
	}
	return Dataset{Version: in.CreateDate, Files: map[string][]byte{
		"cloud.csv": cloudCSV(pickCloud("aws", regional, regionless)),
		"cdn.csv":   cdnCSV(cdnRows),
	}}, nil
}

// fetchAWS downloads and converts the AWS ip-ranges.json at url.
func fetchAWS(url string) Fetcher {
	return func(ctx context.Context, hc *http.Client, now time.Time) (Dataset, error) {
		body, err := httpGet(ctx, hc, url, maxBody)
		if err != nil {
			return Dataset{}, err
		}
		return convertAWS(bytes.NewReader(body))
	}
}
