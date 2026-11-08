package locdata

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const geoFixture = "192.0.2.0,192.0.2.255,SE\n198.51.100.0,198.51.100.255,ZZ\n2001:db8::,2001:db8::ffff,DE\n"

func TestConvertGeo(t *testing.T) {
	ds, err := convertGeo(bytes.NewReader(gz(t, geoFixture+"bad,row,US\n192.0.2.9,2001:db8::1,US\n192.0.2.9,192.0.2.1,US\n")), "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	want := "start_ip,end_ip,country\n192.0.2.0,192.0.2.255,SE\n2001:db8::,2001:db8::ffff,DE\n"
	if ds.Version != "2026-11" || string(ds.Files["geo.csv"]) != want {
		t.Fatalf("got %q %q", ds.Version, ds.Files["geo.csv"])
	}
}

func TestConvertGeoRejectsEmpty(t *testing.T) {
	ds, err := convertGeo(strings.NewReader("<html>Denied</html>"), "2026-11")
	if err == nil || ds.Files != nil || !strings.Contains(err.Error(), "DB-IP: not a gzip file") {
		t.Fatalf("got %v %v", ds, err)
	}
	ds, err = convertGeo(bytes.NewReader(gz(t, "198.51.100.0,198.51.100.255,ZZ\n")), "2026-11")
	if err == nil || ds.Files != nil || !strings.Contains(err.Error(), "DB-IP") {
		t.Fatalf("got %v %v", ds, err)
	}
}

func TestConvertGeoCapsDecompressedSize(t *testing.T) {
	old := maxGeoDecompressed
	t.Cleanup(func() { maxGeoDecompressed = old })
	maxGeoDecompressed = int64(len(geoFixture))
	if _, err := convertGeo(bytes.NewReader(gz(t, geoFixture)), "2026-11"); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	ds, err := convertGeo(bytes.NewReader(gz(t, geoFixture+"192.0.2.9,192.0.2.9,US\n")), "2026-11")
	if err == nil || ds.Files != nil || err.Error() != "DB-IP: decompressed file too large" {
		t.Fatalf("got %v %v", ds, err)
	}
}

func geoServer(t *testing.T, served map[string][]byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchGeoFallsBackToLastMonth(t *testing.T) {
	srv := geoServer(t, map[string][]byte{"/2026-10.csv.gz": gz(t, geoFixture)})
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ds, err := fetchGeo(srv.URL+"/%s.csv.gz")(context.Background(), srv.Client(), now)
	if err != nil || ds.Version != "2026-10" || len(ds.Files["geo.csv"]) == 0 {
		t.Fatalf("got %+v %v", ds, err)
	}
	// This month's file wins when present.
	srv2 := geoServer(t, map[string][]byte{"/2026-11.csv.gz": gz(t, geoFixture), "/2026-10.csv.gz": gz(t, geoFixture)})
	ds, err = fetchGeo(srv2.URL+"/%s.csv.gz")(context.Background(), srv2.Client(), now)
	if err != nil || ds.Version != "2026-11" {
		t.Fatalf("got %+v %v", ds, err)
	}
}

func TestFetchGeoBothMissing(t *testing.T) {
	srv := geoServer(t, nil)
	_, err := fetchGeo(srv.URL+"/%s.csv.gz")(context.Background(), srv.Client(), time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("got %v", err)
	}
}

func TestFetchGeoNon404DoesNotFallBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := fetchGeo(srv.URL+"/%s.csv.gz")(context.Background(), srv.Client(), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("got %v", err)
	}
}
