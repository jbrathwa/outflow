package locdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// maxBody caps every downloaded body.
const maxBody = 64 << 20

// httpGet performs an anonymous GET and returns at most limit bytes of the body.
// A non-200 status reads as "HTTP <code> from <host>".
func httpGet(ctx context.Context, hc *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Host)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
