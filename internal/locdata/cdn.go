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

	"github.com/jbrathwa/outflow/internal/collect"
)

func convertCDN(cfV4, cfV6, fastly io.Reader, version string) (Dataset, error) {
	var rows []cdnRow
	// add parses one prefix and reports whether it was valid.
	add := func(s, name string) bool {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return false
		}
		rows = append(rows, cdnRow{p.Masked(), name})
		return true
	}
	for _, l := range []struct {
		label string
		r     io.Reader
	}{{"Cloudflare IPv4", cfV4}, {"Cloudflare IPv6", cfV6}} {
		n, skipped := 0, 0
		sc := bufio.NewScanner(l.r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if add(line, "Cloudflare") {
				n++
			} else {
				skipped++
			}
		}
		if err := sc.Err(); err != nil {
			return Dataset{}, fmt.Errorf("read %s list: %w", l.label, err)
		}
		if n == 0 {
			return Dataset{}, fmt.Errorf("%s list contains no prefixes", l.label)
		}
		if skipped > 0 {
			collect.Debugf("%s list: skipped %d unparseable prefixes", l.label, skipped)
		}
	}
	var f struct {
		Addresses     []string `json:"addresses"`
		IPv6Addresses []string `json:"ipv6_addresses"`
	}
	if err := json.NewDecoder(fastly).Decode(&f); err != nil {
		return Dataset{}, fmt.Errorf("decode Fastly public-ip-list: %w", err)
	}
	n := 0
	for _, s := range append(f.Addresses, f.IPv6Addresses...) {
		if add(s, "Fastly") {
			n++
		}
	}
	if n == 0 {
		return Dataset{}, errors.New("Fastly list contains no prefixes")
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
