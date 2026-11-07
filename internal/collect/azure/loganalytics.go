package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
)

const maxSplitDepth = 4

// Table is the first table of a query response.
type Table struct {
	Columns []string
	Rows    [][]any
}

// Col returns the index of the named column, or -1.
func (t Table) Col(name string) int {
	for i, c := range t.Columns {
		if c == name {
			return i
		}
	}
	return -1
}

// QueryError is a failed query.
type QueryError struct {
	Status        int
	Code, Message string
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("log analytics: status %d %s: %s", e.Status, e.Code, e.Message)
}

// Forbidden reports a 401/403.
func (e *QueryError) Forbidden() bool { return e.Status == 401 || e.Status == 403 }

// NoTable reports that the queried table does not exist in the workspace.
func (e *QueryError) NoTable() bool {
	return e.Status == 400 && (strings.Contains(e.Message, "Failed to resolve table") || e.Code == "PathNotFoundError")
}

// TooLarge reports that the result exceeded the service limits.
func (e *QueryError) TooLarge() bool {
	return e.Code == "PartialError" || e.Code == "E_QUERY_RESULT_SET_TOO_LARGE" ||
		strings.Contains(e.Message, "E_QUERY_RESULT_SET_TOO_LARGE") ||
		strings.Contains(e.Message, "exceeded the internal limit")
}

// LogAnalytics queries workspaces over the plain REST API.
type LogAnalytics struct {
	Endpoint string
	Tokens   TokenSource
	HTTP     *http.Client
	Timeout  time.Duration // per request, applied through the context
}

// NewLogAnalytics returns a client for the public cloud endpoint.
func NewLogAnalytics(t TokenSource) *LogAnalytics {
	return &LogAnalytics{Endpoint: "https://api.loganalytics.io/v1", Tokens: t, HTTP: &http.Client{}, Timeout: 3 * time.Minute}
}

type errPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type queryResp struct {
	Tables []struct {
		Columns []struct {
			Name string `json:"name"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	} `json:"tables"`
	Error *errPayload `json:"error"`
}

// Query runs kql against one workspace over w. The query is prefixed with a
// "// outflow:<name>" comment so it can be identified in Log Analytics query audit.
func (c *LogAnalytics) Query(ctx context.Context, name, workspace, kql string, w model.Window) (Table, error) {
	collect.Debugf("kql %s: %s", name, strings.ReplaceAll(collect.ElideLists(kql), "\n", " "))
	start := time.Now()
	mask := collect.MaskID(workspace)
	fail := func(e *QueryError) (Table, error) {
		collect.Debugf("query %s workspace %s error=%d %s", name, mask, e.Status, e.Code)
		return Table{}, e
	}

	tok, err := c.Tokens.Token(ctx, ScopeLogAnalytics)
	if err != nil {
		collect.Debugf("query %s workspace %s error=token", name, mask)
		return Table{}, fmt.Errorf("log analytics token: %w", err)
	}
	body, _ := json.Marshal(map[string]string{
		"query":    "// outflow:" + name + "\n" + kql,
		"timespan": w.Start.UTC().Format(time.RFC3339) + "/" + w.End.UTC().Format(time.RFC3339),
	})
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.Endpoint, "/")+"/workspaces/"+url.PathEscape(workspace)+"/query", bytes.NewReader(body))
	if err != nil {
		return Table{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "wait=180")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		collect.Debugf("query %s workspace %s error=transport", name, mask)
		return Table{}, fmt.Errorf("log analytics request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Table{}, fmt.Errorf("log analytics response: %w", err)
	}
	var qr queryResp
	jsonErr := json.Unmarshal(raw, &qr)
	if resp.StatusCode/100 != 2 {
		qe := &QueryError{Status: resp.StatusCode}
		if qr.Error != nil {
			qe.Code, qe.Message = qr.Error.Code, qr.Error.Message
		} else {
			qe.Message = http.StatusText(resp.StatusCode)
		}
		return fail(qe)
	}
	if jsonErr != nil {
		return Table{}, fmt.Errorf("log analytics response: %w", jsonErr)
	}
	if qr.Error != nil {
		return fail(&QueryError{Status: resp.StatusCode, Code: qr.Error.Code, Message: qr.Error.Message})
	}
	var t Table
	if len(qr.Tables) > 0 {
		for _, col := range qr.Tables[0].Columns {
			t.Columns = append(t.Columns, col.Name)
		}
		t.Rows = qr.Tables[0].Rows
	}
	collect.Debugf("query %s workspace %s rows=%d cols=%v %s", name, mask, len(t.Rows), t.Columns, time.Since(start).Round(time.Millisecond))
	return t, nil
}

// QuerySplit is Query, but when the result is too large it halves the window and
// retries each half (at most 4 levels deep). If the deepest level still fails the
// rows collected so far are returned with truncated = true. kql builds the query for
// a given sub-window.
func (c *LogAnalytics) QuerySplit(ctx context.Context, name, workspace string, kql func(model.Window) string, w model.Window) (Table, bool, error) {
	var out Table
	truncated := false
	var run func(w model.Window, depth int) error
	run = func(w model.Window, depth int) error {
		t, err := c.Query(ctx, name, workspace, kql(w), w)
		if err == nil {
			if out.Columns == nil {
				out.Columns = t.Columns
			}
			out.Rows = append(out.Rows, t.Rows...)
			return nil
		}
		qe, ok := err.(*QueryError)
		if !ok || !qe.TooLarge() {
			return err
		}
		mid := w.Start.Add(w.End.Sub(w.Start) / 2).Truncate(time.Minute)
		if depth >= maxSplitDepth || w.End.Sub(w.Start) < 2*time.Minute || !mid.After(w.Start) || !mid.Before(w.End) {
			truncated = true
			return nil
		}
		if err := run(model.Window{Start: w.Start, End: mid}, depth+1); err != nil {
			return err
		}
		return run(model.Window{Start: mid, End: w.End}, depth+1)
	}
	if err := run(w, 0); err != nil {
		return Table{}, truncated, err
	}
	return out, truncated, nil
}
