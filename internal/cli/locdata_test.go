package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/locdata"
	"github.com/jbrathwa/outflow/internal/model"
)

const geoCSV = "192.0.2.0,192.0.2.255,SE\n198.51.100.0,198.51.100.255,ZZ\n2001:db8::,2001:db8::ffff,DE\n"

// locEnv is a fake download server plus a private cache folder.
type locEnv struct {
	hits atomic.Int32
	dir  string
	fail map[string]bool // paths answering 500
}

func (e *locEnv) failing(p string) bool {
	for k := range e.fail {
		if strings.HasPrefix(p, k) {
			return true
		}
	}
	return false
}

// withLocData points locEndpoints at a fake server serving the locdata fixtures,
// uses a fresh cache folder and switches auto-refresh on.
func withLocData(t *testing.T) *locEnv {
	t.Helper()
	e := &locEnv{dir: t.TempDir(), fail: map[string]bool{}}
	t.Setenv("OUTFLOW_CACHE_DIR", e.dir)
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "locdata", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var gzb bytes.Buffer
	w := gzip.NewWriter(&gzb)
	w.Write([]byte(geoCSV))
	w.Close()
	files := map[string][]byte{
		"/dl/ServiceTags_Public_20261102.json": read("servicetags.json"),
		"/aws":                                 read("aws-ip-ranges.json"),
		"/gcp":                                 read("gcp-cloud.json"),
		"/v4":                                  read("cloudflare-v4.txt"),
		"/v6":                                  read("cloudflare-v6.txt"),
		"/fastly":                              read("fastly.json"),
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		p := r.URL.Path
		switch {
		case e.failing(p):
			http.Error(w, "boom", 500)
		case p == "/page":
			w.Write([]byte(`<a href="` + srv.URL + `/dl/ServiceTags_Public_20261102.json">download</a>`))
		case strings.HasPrefix(p, "/geo/"):
			w.Write(gzb.Bytes())
		case files[p] != nil:
			w.Write(files[p])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldEP, oldAuto := locEndpoints, autoRefresh
	t.Cleanup(func() { locEndpoints, autoRefresh = oldEP, oldAuto })
	u := srv.URL
	locEndpoints = locdata.Endpoints{
		AzurePage: u + "/page", AWS: u + "/aws", GCP: u + "/gcp",
		CloudflareV4: u + "/v4", CloudflareV6: u + "/v6", Fastly: u + "/fastly",
		GeoPattern: u + "/geo/%s.csv.gz",
	}
	autoRefresh = true
	return e
}

func TestRefreshLine(t *testing.T) {
	r := func(src string, s locdata.Status) locdata.Result { return locdata.Result{Source: src, Status: s} }
	got := refreshLine([]locdata.Result{
		r("azure", locdata.Refreshed), r("aws", locdata.Refreshed), r("gcp", locdata.Refreshed),
		r("cdn", locdata.Refreshed), r("geo", locdata.UpToDate),
	})
	if want := "outflow: location data: refreshed azure, aws, gcp, cdn (geo up to date)"; got != want {
		t.Errorf("got %q", got)
	}
	if got := refreshLine([]locdata.Result{r("azure", locdata.Refreshed), r("aws", locdata.Failed)}); got != "outflow: location data: refreshed azure" {
		t.Errorf("got %q", got)
	}
	if got := refreshLine([]locdata.Result{r("azure", locdata.UpToDate), r("aws", locdata.Failed)}); got != "" {
		t.Errorf("got %q", got)
	}
	if got := refreshLine(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestWarnLines(t *testing.T) {
	e := withLocData(t)
	// Cache azure nine days ago.
	now := time.Now()
	c := locdata.Cache{Dir: e.dir, Now: func() time.Time { return now.Add(-9 * 24 * time.Hour) }}
	srcs := locdata.Sources(locEndpoints)
	res := locdata.Refresh(t.Context(), c, http.DefaultClient, srcs[:1], true)
	if res[0].Status != locdata.Refreshed {
		t.Fatalf("seed: %+v", res[0])
	}
	db, _, err := locdata.Cache{Dir: e.dir}.Load(srcs)
	if err != nil {
		t.Fatal(err)
	}
	failed := []locdata.Result{
		{Source: "azure", Label: "Azure service tags", Status: locdata.Failed, Err: errors.New("HTTP 500 from example.test")},
		{Source: "aws", Label: "AWS IP ranges", Status: locdata.Failed, Err: errors.New("timeout")},
		{Source: "gcp", Status: locdata.Refreshed},
	}
	got := warnLines(failed, []locdata.LoadIssue{{Source: "cdn", Err: errors.New("bad csv")}}, db)
	want := []string{
		"outflow: warning: could not refresh Azure service tags (HTTP 500 from example.test); using data from " +
			now.Add(-9*24*time.Hour).Format("2006-01-02") + " (9 days old)",
		"outflow: warning: could not refresh AWS IP ranges (timeout); using built-in sample data",
		"outflow: warning: cached CDN ranges could not be read (bad csv); using built-in sample data",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestUpdateDataAllOK(t *testing.T) {
	e := withLocData(t)
	code, stdout, stderr := run(t, "update-data")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 7 || lines[0] != "Downloads public files only; no Azure or Kubernetes access is used." {
		t.Fatalf("stdout:\n%s", stdout)
	}
	if lines[1] != "✔ Azure service tags"+strings.Repeat(" ", 10)+"20261102" {
		t.Errorf("line 1 = %q", lines[1])
	}
	for _, l := range lines[1:6] {
		if !strings.HasPrefix(l, "✔ ") {
			t.Errorf("line %q", l)
		}
	}
	if lines[6] != "Saved in "+e.dir {
		t.Errorf("last = %q", lines[6])
	}
}

func TestUpdateDataOneFails(t *testing.T) {
	e := withLocData(t)
	e.fail["/aws"] = true
	code, stdout, _ := run(t, "update-data")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout, "✘ AWS IP ranges") || !strings.Contains(stdout, "HTTP 500 from 127.0.0.1:") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if strings.Count(stdout, "✔") != 4 {
		t.Errorf("want four ok lines:\n%s", stdout)
	}
	c := locdata.Cache{Dir: e.dir}
	if _, ok := c.Meta("azure"); !ok {
		t.Error("azure not saved")
	}
	if _, ok := c.Meta("aws"); ok {
		t.Error("aws saved despite failure")
	}
}

func TestUpdateDataRejectsFlags(t *testing.T) {
	e := withLocData(t)
	code, stdout, stderr := run(t, "update-data", "--demo")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "Usage:") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if e.hits.Load() != 0 {
		t.Error("downloaded despite bad flag")
	}
}

func TestUpdateDataDebug(t *testing.T) {
	withLocData(t)
	code, _, stderr := run(t, "update-data", "--debug")
	if code != 0 || !strings.Contains(stderr, "location data azure") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
}

func TestSnapshotNeverDownloads(t *testing.T) {
	e := withLocData(t)
	if code, _, _ := run(t, "update-data"); code != 0 {
		t.Fatal("seed failed")
	}
	seeded := e.hits.Load()
	p := snapshotFile(t, model.Snapshot{SchemaVersion: model.SchemaVersion})
	code, stdout, stderr := run(t, "scan", "--snapshot", p)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if e.hits.Load() != seeded {
		t.Errorf("snapshot scan made %d requests", e.hits.Load()-seeded)
	}
	var m struct {
		LocationData string `json:"locationData"`
	}
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.LocationData, "azure 20261102") {
		t.Errorf("locationData = %q", m.LocationData)
	}
}

func TestDemoUsesSampleOnly(t *testing.T) {
	e := withLocData(t)
	if code, _, _ := run(t, "update-data"); code != 0 {
		t.Fatal("seed failed")
	}
	seeded := e.hits.Load()
	code, stdout, _ := run(t, "scan", "--demo")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout, `"locationData": "seed-2026-11-07"`) {
		t.Errorf("stdout lacks sample version")
	}
	if e.hits.Load() != seeded {
		t.Error("demo downloaded")
	}
}

func TestNoRefreshFlag(t *testing.T) {
	e := withLocData(t)
	withLive(t, aksState("AKS"), true)
	code, _, stderr := run(t, "scan", "--workspace", wsA, "--no-refresh")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if e.hits.Load() != 0 {
		t.Errorf("requests = %d", e.hits.Load())
	}
	if strings.Contains(stderr, "location data") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestLiveScanRefreshes(t *testing.T) {
	e := withLocData(t)
	withLive(t, aksState("AKS"), true)
	code, _, stderr := run(t, "scan", "--workspace", wsA)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "outflow: location data: refreshed azure, aws, gcp, cdn, geo") {
		t.Errorf("stderr = %q", stderr)
	}
	if e.hits.Load() == 0 {
		t.Error("no requests")
	}
	// A second run finds everything fresh and prints nothing.
	_, _, stderr = run(t, "scan", "--workspace", wsA)
	if strings.Contains(stderr, "location data") {
		t.Errorf("second run stderr = %q", stderr)
	}
}

func TestLiveScanRefreshFails(t *testing.T) {
	e := withLocData(t)
	for _, p := range []string{"/page", "/aws", "/gcp", "/v4", "/geo/"} {
		e.fail[p] = true
	}
	withLive(t, aksState("AKS"), true)
	code, stdout, stderr := run(t, "scan", "--workspace", wsA)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if n := strings.Count(stderr, "outflow: warning: could not refresh"); n != 5 {
		t.Errorf("warnings = %d, stderr:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, "using built-in sample data") || !strings.Contains(stdout, "shop/storefront") {
		t.Errorf("stderr=%q stdout=%q", stderr, stdout)
	}
}
