package locdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureReader(t *testing.T, name string) *strings.Reader {
	return strings.NewReader(string(readFixture(t, name)))
}

func TestConvertCDN(t *testing.T) {
	ds, err := convertCDN(fixtureReader(t, "cloudflare-v4.txt"), fixtureReader(t, "cloudflare-v6.txt"), fixtureReader(t, "fastly.json"), "2026-11-08")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Version != "2026-11-08" {
		t.Fatalf("version %q", ds.Version)
	}
	want := "prefix,name\n" +
		"192.0.2.0/24,Cloudflare\n" +
		"198.51.100.0/24,Fastly\n" +
		"2001:db8:1::/48,Cloudflare\n" +
		"2001:db8::/32,Fastly\n" +
		"203.0.113.0/24,Cloudflare\n"
	if got := string(ds.Files["cdn.csv"]); got != want {
		t.Fatalf("cdn.csv:\n%s\nwant:\n%s", got, want)
	}
}

func TestConvertCDNRejectsEmpty(t *testing.T) {
	cf4 := "192.0.2.0/24\n"
	cf6 := "2001:db8:1::/48\n"
	fastly := `{"addresses":["198.51.100.0/24"],"ipv6_addresses":[]}`
	cases := []struct{ name, v4, v6, fastly, want string }{
		{"html cloudflare v4", "<html>Denied</html>", cf6, fastly, "Cloudflare IPv4"},
		{"empty cloudflare v6", cf4, "\n", fastly, "Cloudflare IPv6"},
		{"empty fastly", cf4, cf6, `{"addresses":[],"ipv6_addresses":[]}`, "Fastly"},
		{"html fastly", cf4, cf6, `<html>Denied</html>`, "Fastly"},
	}
	for _, c := range cases {
		ds, err := convertCDN(strings.NewReader(c.v4), strings.NewReader(c.v6), strings.NewReader(c.fastly), "v")
		if err == nil || ds.Files != nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: got %v %v", c.name, ds, err)
		}
	}
}

func TestFetchCDN(t *testing.T) {
	bodies := map[string][]byte{
		"/v4":     readFixture(t, "cloudflare-v4.txt"),
		"/v6":     readFixture(t, "cloudflare-v6.txt"),
		"/fastly": readFixture(t, "fastly.json"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 8, 23, 30, 0, 0, time.FixedZone("x", -5*3600))
	ds, err := fetchCDN(srv.URL+"/v4", srv.URL+"/v6", srv.URL+"/fastly")(context.Background(), srv.Client(), now)
	if err != nil || ds.Version != "2026-11-09" || len(ds.Files["cdn.csv"]) == 0 {
		t.Fatalf("got %+v %v", ds, err)
	}
	// One failing URL fails the whole source.
	ds, err = fetchCDN(srv.URL+"/v4", srv.URL+"/missing", srv.URL+"/fastly")(context.Background(), srv.Client(), now)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || ds.Files != nil {
		t.Fatalf("got %+v %v", ds, err)
	}
}
