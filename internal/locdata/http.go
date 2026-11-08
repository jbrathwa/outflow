package locdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// UserAgent is sent with every request; the CLI sets "outflow/" + Version.
var UserAgent = "outflow"

// maxBody caps every downloaded body.
const maxBody = 64 << 20

// statusError is a non-200 response; it reads as "HTTP <code> from <host>".
type statusError struct {
	Code int
	Host string
}

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d from %s", e.Code, e.Host) }

// httpOpen performs an anonymous GET and returns the body for streaming.
// The caller must close it. A non-200 status is a *statusError.
func httpOpen(ctx context.Context, hc *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &statusError{resp.StatusCode, req.URL.Host}
	}
	return resp.Body, nil
}

// httpGet performs an anonymous GET and returns at most limit bytes of the body.
func httpGet(ctx context.Context, hc *http.Client, url string, limit int64) ([]byte, error) {
	body, err := httpOpen(ctx, hc, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, limit))
}
