package locdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWeb struct {
	srv      *httptest.Server
	hits     atomic.Int32
	mu       sync.Mutex
	override map[string]func(w http.ResponseWriter, r *http.Request)
	block    chan struct{}
	agents   sync.Map
}

func (f *fakeWeb) set(path string, h func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override[path] = h
}

func newFakeWeb(t *testing.T) (*fakeWeb, Endpoints) {
	t.Helper()
	f := &fakeWeb{override: map[string]func(http.ResponseWriter, *http.Request){}, block: make(chan struct{})}
	t.Cleanup(func() { close(f.block) })
	files := map[string][]byte{
		"/dl/ServiceTags_Public_20261102.json": readFixture(t, "servicetags.json"),
		"/aws":                                 readFixture(t, "aws-ip-ranges.json"),
		"/gcp":                                 readFixture(t, "gcp-cloud.json"),
		"/v4":                                  readFixture(t, "cloudflare-v4.txt"),
		"/v6":                                  readFixture(t, "cloudflare-v6.txt"),
		"/fastly":                              readFixture(t, "fastly.json"),
	}
	geo := gz(t, geoFixture)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.agents.Store(r.Header.Get("User-Agent"), true)
		f.mu.Lock()
		h := f.override[r.URL.Path]
		f.mu.Unlock()
		switch {
		case h != nil:
			h(w, r)
		case r.URL.Path == "/page":
			w.Write([]byte(`<a href="` + f.srv.URL + `/dl/ServiceTags_Public_20261102.json">download</a>`))
		case strings.HasPrefix(r.URL.Path, "/geo/"):
			w.Write(geo)
		case files[r.URL.Path] != nil:
			w.Write(files[r.URL.Path])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	u := f.srv.URL
	return f, Endpoints{
		AzurePage: u + "/page", AWS: u + "/aws", GCP: u + "/gcp",
		CloudflareV4: u + "/v4", CloudflareV6: u + "/v6", Fastly: u + "/fastly",
		GeoPattern: u + "/geo/%s.csv.gz",
	}
}

func wantAll(t *testing.T, res []Result, want Status) {
	t.Helper()
	if len(res) != 5 {
		t.Fatalf("got %d results", len(res))
	}
	for _, r := range res {
		if r.Status != want || r.Err != nil && want != Failed {
			t.Errorf("%s: %s %v, want %s", r.Source, r.Status, r.Err, want)
		}
	}
}

func TestRefreshAll(t *testing.T) {
	f, e := newFakeWeb(t)
	c := cacheAt(t, fetched)
	srcs := Sources(e)
	res := Refresh(context.Background(), c, f.srv.Client(), srcs, false)
	wantAll(t, res, Refreshed)
	for i, r := range res {
		if r.Source != srcs[i].Name || r.Label != srcs[i].Label || r.Version == "" {
			t.Errorf("result %d = %+v", i, r)
		}
	}
	db, issues, err := c.Load(srcs)
	if err != nil || len(issues) != 0 {
		t.Fatalf("load: %v %v", issues, err)
	}
	for _, s := range db.Sources() {
		if s.Sample {
			t.Errorf("%s is still sample data", s.Name)
		}
	}
	if _, ok := f.agents.Load(UserAgent); !ok {
		t.Error("User-Agent not sent")
	}
}

func TestRefreshSkipsFresh(t *testing.T) {
	f, e := newFakeWeb(t)
	now := fetched
	c := Cache{Dir: t.TempDir(), Now: func() time.Time { return now }}
	srcs := Sources(e)
	first := Refresh(context.Background(), c, f.srv.Client(), srcs, false)
	hits := f.hits.Load()
	now = fetched.Add(day)
	res := Refresh(context.Background(), c, f.srv.Client(), srcs, false)
	wantAll(t, res, UpToDate)
	for i, r := range res {
		if r.Version != first[i].Version {
			t.Errorf("%s version %q, want %q", r.Source, r.Version, first[i].Version)
		}
	}
	if f.hits.Load() != hits {
		t.Errorf("made %d requests while up to date", f.hits.Load()-hits)
	}
}

func TestRefreshForce(t *testing.T) {
	f, e := newFakeWeb(t)
	c := cacheAt(t, fetched)
	srcs := Sources(e)
	wantAll(t, Refresh(context.Background(), c, f.srv.Client(), srcs, false), Refreshed)
	wantAll(t, Refresh(context.Background(), c, f.srv.Client(), srcs, true), Refreshed)
}

func TestRefreshOneFails(t *testing.T) {
	f, e := newFakeWeb(t)
	f.set("/aws", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	res := Refresh(context.Background(), cacheAt(t, fetched), f.srv.Client(), Sources(e), false)
	for _, r := range res {
		if r.Source == "aws" {
			if r.Status != Failed || r.Err == nil || !strings.Contains(r.Err.Error(), "HTTP 500") || r.Version != "" {
				t.Errorf("aws = %+v", r)
			}
		} else if r.Status != Refreshed {
			t.Errorf("%s = %+v", r.Source, r)
		}
	}
}

func TestRefreshKeepsOldDataOnBadBody(t *testing.T) {
	f, e := newFakeWeb(t)
	c := cacheAt(t, fetched)
	srcs := Sources(e)
	Refresh(context.Background(), c, f.srv.Client(), srcs, false)
	before, _ := c.Meta("azure")
	f.set("/page", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>no link</html>")) })
	res := Refresh(context.Background(), c, f.srv.Client(), srcs, true)
	if res[0].Source != "azure" || res[0].Status != Failed || res[0].Err == nil {
		t.Fatalf("azure = %+v", res[0])
	}
	after, ok := c.Meta("azure")
	if !ok || !reflect.DeepEqual(before, after) {
		t.Errorf("meta changed: %+v -> %+v", before, after)
	}
}

func TestRefreshDeadline(t *testing.T) {
	f, e := newFakeWeb(t)
	f.set("/gcp", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-f.block:
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := Refresh(ctx, cacheAt(t, fetched), f.srv.Client(), Sources(e), false)
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s", d)
	}
	for _, r := range res {
		if r.Source == "gcp" {
			if r.Status != Failed || r.Err == nil || !strings.Contains(r.Err.Error(), "context deadline") {
				t.Errorf("gcp = %+v", r)
			}
		} else if r.Status != Refreshed {
			t.Errorf("%s = %+v", r.Source, r)
		}
	}
}

func TestRefreshUnwritableCache(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("cannot make a directory read-only here")
	}
	f, e := newFakeWeb(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	res := Refresh(context.Background(), Cache{Dir: dir, Now: func() time.Time { return fetched }}, f.srv.Client(), Sources(e), false)
	wantAll(t, res, Failed)
	for _, r := range res {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "could not save location data") {
			t.Errorf("%s err = %v", r.Source, r.Err)
		}
	}
}

func TestHostsList(t *testing.T) {
	want := []string{"www.microsoft.com", "download.microsoft.com", "ip-ranges.amazonaws.com", "www.gstatic.com", "www.cloudflare.com", "api.fastly.com", "download.db-ip.com"}
	if got := DefaultEndpoints().Hosts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
