// Package lafake is an in-process fake Log Analytics (and ARM GET) server for tests.
package lafake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
)

// Call is one recorded query request.
type Call struct {
	Name, Workspace string
	Window          model.Window
	KQL             string // full query text, including the leading name comment
	Auth, Prefer    string // Authorization and Prefer request headers
}

// Response is what a Handler returns. With ErrorCode set the body carries an error.
type Response struct {
	Columns                 []string
	Rows                    [][]any
	ErrorCode, ErrorMessage string
}

// Handler answers one named query.
type Handler func(c Call) (Response, int)

// Server is the fake. URL is its base endpoint (use it as the Log Analytics Endpoint).
type Server struct {
	URL string

	srv    *httptest.Server
	routes map[string]Handler

	mu    sync.Mutex
	calls []Call
	arm   map[string]any
}

// New starts a fake server routed by the `// outflow:<name>` comment on the first query line.
func New(t *testing.T, routes map[string]Handler) *Server {
	t.Helper()
	s := &Server{routes: routes, arm: map[string]any{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Calls returns a copy of the recorded query calls.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// ARM registers a GET route that answers with body as JSON. Unknown paths are 404.
func (s *Server) ARM(path string, body any) {
	s.mu.Lock()
	s.arm[path] = body
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg}}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	const pre, suf = "/workspaces/", "/query"
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, pre) || !strings.HasSuffix(r.URL.Path, suf) {
		s.mu.Lock()
		body, ok := s.arm[r.URL.Path]
		s.mu.Unlock()
		if r.Method == http.MethodGet && ok {
			writeJSON(w, 200, body)
			return
		}
		writeJSON(w, 404, errBody("NotFound", "no route for "+r.URL.Path))
		return
	}
	ws := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, pre), suf)
	var req struct{ Query, Timespan string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, errBody("BadArgumentError", "bad body"))
		return
	}
	first, _, _ := strings.Cut(req.Query, "\n")
	name := strings.TrimSpace(strings.TrimPrefix(first, "// outflow:"))
	if !strings.HasPrefix(first, "// outflow:") {
		name = ""
	}
	c := Call{Name: name, Workspace: ws, KQL: req.Query, Auth: r.Header.Get("Authorization"), Prefer: r.Header.Get("Prefer")}
	if a, b, ok := strings.Cut(req.Timespan, "/"); ok {
		c.Window.Start, _ = time.Parse(time.RFC3339, a)
		c.Window.End, _ = time.Parse(time.RFC3339, b)
	}
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()

	if strings.HasPrefix(ws, "denied-") {
		writeJSON(w, 403, errBody("InsufficientAccessError", "The provided credentials have insufficient access"))
		return
	}
	h, ok := s.routes[name]
	if !ok {
		writeJSON(w, 400, errBody("BadArgumentError", "no route for "+name))
		return
	}
	resp, status := h(c)
	if resp.ErrorCode != "" && status != 200 {
		writeJSON(w, status, errBody(resp.ErrorCode, resp.ErrorMessage))
		return
	}
	cols := make([]map[string]string, len(resp.Columns))
	for i, n := range resp.Columns {
		cols[i] = map[string]string{"name": n, "type": "string"}
	}
	rows := resp.Rows
	if rows == nil {
		rows = [][]any{}
	}
	out := map[string]any{"tables": []any{map[string]any{"name": "PrimaryResult", "columns": cols, "rows": rows}}}
	if resp.ErrorCode != "" {
		out["error"] = errBody(resp.ErrorCode, resp.ErrorMessage)["error"]
	}
	writeJSON(w, status, out)
}
