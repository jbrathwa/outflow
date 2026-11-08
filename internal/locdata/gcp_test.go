package locdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConvertGCP(t *testing.T) {
	ds, err := convertGCP(strings.NewReader(string(readFixture(t, "gcp-cloud.json"))))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Version != "2026-11-08T03:12:44.1234" {
		t.Fatalf("version %q", ds.Version)
	}
	want := cloudHeader +
		"192.0.2.0/24,gcp,us-central1,Google Cloud,false\n" +
		"198.51.100.0/24,gcp,,Google Cloud,true\n" +
		"2001:db8::/32,gcp,europe-west1,Google Cloud,false\n" +
		"203.0.113.0/24,gcp,,Google Cloud,true\n"
	if got := string(ds.Files["cloud.csv"]); got != want {
		t.Fatalf("cloud.csv:\n%s\nwant:\n%s", got, want)
	}
	if _, ok := ds.Files["cdn.csv"]; ok {
		t.Fatal("GCP must not produce cdn.csv")
	}
}

func TestConvertGCPRejectsEmpty(t *testing.T) {
	for _, in := range []string{`{"prefixes":[]}`, `<html><body>Denied</body></html>`} {
		ds, err := convertGCP(strings.NewReader(in))
		if err == nil || ds.Files != nil || !strings.Contains(err.Error(), "GCP") {
			t.Fatalf("%q: got %v %v", in, ds, err)
		}
	}
}

func TestFetchGCP(t *testing.T) {
	body := readFixture(t, "gcp-cloud.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ok" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	ds, err := fetchGCP(srv.URL+"/ok")(context.Background(), srv.Client(), time.Now())
	if err != nil || len(ds.Files["cloud.csv"]) == 0 {
		t.Fatalf("got %+v %v", ds, err)
	}
	if _, err := fetchGCP(srv.URL+"/bad")(context.Background(), srv.Client(), time.Now()); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("got %v", err)
	}
}
