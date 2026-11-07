package azure_test

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/collect/azure"
	"github.com/jbrathwa/outflow/internal/collect/azure/lafake"
	"github.com/jbrathwa/outflow/internal/model"
)

const wsA = "00000000-0000-0000-0000-00000000000a"

var t0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func win(h int) model.Window {
	return model.Window{Start: t0, End: t0.Add(time.Duration(h) * time.Hour)}
}

func client(url string) *azure.LogAnalytics {
	c := azure.NewLogAnalytics(azure.StaticToken("tok-secret"))
	c.Endpoint = url
	return c
}

func TestQuerySendsNameTimespanAndToken(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{
		"nodes": func(c lafake.Call) (lafake.Response, int) { return lafake.Response{Columns: []string{"a"}}, 200 },
	})
	w := win(1)
	if _, err := client(s.URL).Query(context.Background(), "nodes", wsA, "T | take 1", w); err != nil {
		t.Fatal(err)
	}
	calls := s.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	c := calls[0]
	if c.Name != "nodes" || c.Workspace != wsA || !c.Window.Start.Equal(w.Start) || !c.Window.End.Equal(w.End) {
		t.Fatalf("call=%+v", c)
	}
	if !strings.Contains(c.KQL, "T | take 1") {
		t.Fatalf("kql=%q", c.KQL)
	}
	if c.Auth != "Bearer tok-secret" || c.Prefer != "wait=180" {
		t.Fatalf("headers auth=%q prefer=%q", c.Auth, c.Prefer)
	}
}

func TestQueryParsesTable(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{
		"q": func(c lafake.Call) (lafake.Response, int) {
			return lafake.Response{Columns: []string{"ip", "n"}, Rows: [][]any{{"10.0.0.1", 3}, {"10.0.0.2", nil}}}, 200
		},
	})
	tb, err := client(s.URL).Query(context.Background(), "q", wsA, "T", win(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 2 || tb.Col("n") != 1 || tb.Col("zzz") != -1 {
		t.Fatalf("table=%+v", tb)
	}
	if tb.Rows[0][0] != "10.0.0.1" || tb.Rows[0][1] != float64(3) || tb.Rows[1][1] != nil {
		t.Fatalf("rows=%v", tb.Rows)
	}
}

func TestQueryForbidden(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{"q": func(lafake.Call) (lafake.Response, int) { return lafake.Response{}, 200 }})
	_, err := client(s.URL).Query(context.Background(), "q", "denied-ws", "T", win(1))
	qe, ok := err.(*azure.QueryError)
	if !ok || !qe.Forbidden() || qe.Status != 403 {
		t.Fatalf("err=%v", err)
	}
}

func TestQueryNoTable(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{"q": func(lafake.Call) (lafake.Response, int) {
		return lafake.Response{ErrorCode: "SemanticError", ErrorMessage: "Failed to resolve table or column expression named 'AZFWNetworkRule'"}, 400
	}})
	_, err := client(s.URL).Query(context.Background(), "q", wsA, "T", win(1))
	qe, ok := err.(*azure.QueryError)
	if !ok || !qe.NoTable() || qe.Forbidden() || qe.TooLarge() {
		t.Fatalf("err=%v", err)
	}
}

func tooLargeOver6h(c lafake.Call) (lafake.Response, int) {
	if c.Window.End.Sub(c.Window.Start) > 6*time.Hour {
		return lafake.Response{ErrorCode: "PartialError", ErrorMessage: "E_QUERY_RESULT_SET_TOO_LARGE"}, 200
	}
	return lafake.Response{Columns: []string{"v"}, Rows: [][]any{{"x"}}}, 200
}

func kqlFor(w model.Window) string { return "T | where x" }

func TestQuerySplitOnTooLarge(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{"q": tooLargeOver6h})
	tb, trunc, err := client(s.URL).QuerySplit(context.Background(), "q", wsA, kqlFor, win(24))
	if err != nil || trunc {
		t.Fatalf("err=%v trunc=%v", err, trunc)
	}
	// 24h -> 2x12h -> 4x6h: 1 + 2 + 4 = 7 calls, 4 leaf rows.
	if len(tb.Rows) != 4 || len(s.Calls()) != 7 {
		t.Fatalf("rows=%d calls=%d", len(tb.Rows), len(s.Calls()))
	}
}

func TestQuerySplitGivesUp(t *testing.T) {
	var n atomic.Int32
	s := lafake.New(t, map[string]lafake.Handler{"q": func(lafake.Call) (lafake.Response, int) {
		n.Add(1)
		return lafake.Response{ErrorCode: "PartialError", ErrorMessage: "too large"}, 200
	}})
	_, trunc, err := client(s.URL).QuerySplit(context.Background(), "q", wsA, kqlFor, win(24))
	if err != nil || !trunc {
		t.Fatalf("err=%v trunc=%v", err, trunc)
	}
	if n.Load() != 31 { // 1+2+4+8+16
		t.Fatalf("calls=%d", n.Load())
	}
}

func TestQuerySplitOtherErrorPropagates(t *testing.T) {
	s := lafake.New(t, map[string]lafake.Handler{"q": func(lafake.Call) (lafake.Response, int) { return lafake.Response{}, 200 }})
	_, _, err := client(s.URL).QuerySplit(context.Background(), "q", "denied-x", kqlFor, win(24))
	if err == nil {
		t.Fatal("want error")
	}
}

func TestQueryDebugHasNoData(t *testing.T) {
	var buf bytes.Buffer
	collect.SetDebug(&buf)
	defer collect.SetDebug(nil)
	s := lafake.New(t, map[string]lafake.Handler{"q": func(lafake.Call) (lafake.Response, int) {
		return lafake.Response{Columns: []string{"ip"}, Rows: [][]any{{"SECRETROW1"}, {"SECRETROW2"}}}, 200
	}})
	if _, err := client(s.URL).Query(context.Background(), "q", wsA, "T | where Computer in (\"node-a\", \"node-b\")\n| take 1", win(1)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"q", "…000a", "rows=2", "kql q:", "(<2 values>)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	for _, bad := range []string{"SECRETROW", wsA, "tok-secret", "node-a"} {
		if strings.Contains(out, bad) {
			t.Errorf("leaked %q in %q", bad, out)
		}
	}
}
