package lafake_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/collect/azure/lafake"
)

func TestUnknownQueryIs400(t *testing.T) {
	s := lafake.New(t, nil)
	resp, err := http.Post(s.URL+"/workspaces/w/query", "application/json",
		strings.NewReader(`{"query":"// outflow:nope\nT","timespan":"2026-11-01T00:00:00Z/2026-11-01T01:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(b), "no route for nope") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestARM(t *testing.T) {
	s := lafake.New(t, nil)
	s.ARM("/subscriptions/s/x", map[string]any{"a": 1})
	resp, _ := http.Get(s.URL + "/subscriptions/s/x")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"a":1`) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	resp, _ = http.Get(s.URL + "/nope")
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
}
