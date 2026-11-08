package locdata

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
)

// maxGeoBody caps the compressed DB-IP download.
const maxGeoBody = 256 << 20

// maxGeoDecompressed caps the decompressed DB-IP CSV; a var so tests can lower it.
var maxGeoDecompressed int64 = 1 << 30

var errGeoTooLarge = errors.New("DB-IP: decompressed file too large")

// capReader fails with errGeoTooLarge once more than left bytes are read.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.left -= int64(n); c.left < 0 {
		return 0, errGeoTooLarge
	}
	return n, err
}

// convertGeo reads the gzipped DB-IP country lite CSV and writes geo.csv.
func convertGeo(r io.Reader, month string) (Dataset, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return Dataset{}, errors.New("DB-IP: not a gzip file")
	}
	defer zr.Close()
	cr := csv.NewReader(&capReader{r: io.LimitReader(zr, maxGeoDecompressed+1), left: maxGeoDecompressed})
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	var out bytes.Buffer
	out.WriteString("start_ip,end_ip,country\n")
	n, skipped := 0, 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if errors.Is(err, errGeoTooLarge) {
			return Dataset{}, errGeoTooLarge
		}
		if err != nil {
			return Dataset{}, fmt.Errorf("DB-IP: read database: %w", err)
		}
		if len(rec) < 3 || rec[2] == "ZZ" {
			skipped++
			continue
		}
		start, e1 := netip.ParseAddr(rec[0])
		end, e2 := netip.ParseAddr(rec[1])
		if e1 != nil || e2 != nil || start.Is4() != end.Is4() || start.Compare(end) > 0 {
			skipped++
			continue
		}
		out.WriteString(start.String() + "," + end.String() + "," + rec[2] + "\n")
		n++
	}
	if n == 0 {
		return Dataset{}, errors.New("DB-IP: database contains no country ranges")
	}
	if skipped > 0 {
		collect.Debugf("DB-IP: skipped %d rows (ZZ or unparseable)", skipped)
	}
	return Dataset{Version: month, Files: map[string][]byte{"geo.csv": out.Bytes()}}, nil
}

// fetchGeo downloads this month's database, falling back to last month's on 404.
func fetchGeo(urlPattern string) Fetcher {
	return func(ctx context.Context, hc *http.Client, now time.Time) (Dataset, error) {
		now = now.UTC()
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		ds, err := fetchGeoMonth(ctx, hc, urlPattern, first.Format("2006-01"))
		var se *statusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return fetchGeoMonth(ctx, hc, urlPattern, first.AddDate(0, -1, 0).Format("2006-01"))
		}
		return ds, err
	}
}

func fetchGeoMonth(ctx context.Context, hc *http.Client, urlPattern, month string) (Dataset, error) {
	body, err := httpOpen(ctx, hc, fmt.Sprintf(urlPattern, month))
	if err != nil {
		return Dataset{}, err
	}
	defer body.Close()
	return convertGeo(io.LimitReader(body, maxGeoBody), month)
}
