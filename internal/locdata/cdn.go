package locdata

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

func convertCDN(cfV4, cfV6, fastly io.Reader, version string) (Dataset, error) {
	var rows []cdnRow
	add := func(s, name string) {
		if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			rows = append(rows, cdnRow{p.Masked(), name})
		}
	}
	for _, r := range []io.Reader{cfV4, cfV6} {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				add(line, "Cloudflare")
			}
		}
		if err := sc.Err(); err != nil {
			return Dataset{}, fmt.Errorf("read Cloudflare ranges: %w", err)
		}
	}
	var f struct {
		Addresses     []string `json:"addresses"`
		IPv6Addresses []string `json:"ipv6_addresses"`
	}
	if err := json.NewDecoder(fastly).Decode(&f); err != nil {
		return Dataset{}, fmt.Errorf("decode Fastly public-ip-list: %w", err)
	}
	for _, s := range append(f.Addresses, f.IPv6Addresses...) {
		add(s, "Fastly")
	}
	if len(rows) == 0 {
		return Dataset{}, errors.New("CDN range lists (Cloudflare, Fastly) contain no prefixes")
	}
	return Dataset{Version: version, Files: map[string][]byte{"cdn.csv": cdnCSV(rows)}}, nil
}

// fetchCDN downloads all three CDN lists; if any fails, the whole source fails.
func fetchCDN(cfV4, cfV6, fastly string) Fetcher {
	return func(ctx context.Context, hc *http.Client, now time.Time) (Dataset, error) {
		var bodies [3]io.Reader
		for i, u := range []string{cfV4, cfV6, fastly} {
			b, err := httpGet(ctx, hc, u, maxBody)
			if err != nil {
				return Dataset{}, err
			}
			bodies[i] = strings.NewReader(string(b))
		}
		return convertCDN(bodies[0], bodies[1], bodies[2], now.UTC().Format("2006-01-02"))
	}
}
