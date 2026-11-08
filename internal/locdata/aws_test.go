package locdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConvertAWS(t *testing.T) {
	ds, err := convertAWS(strings.NewReader(string(readFixture(t, "aws-ip-ranges.json"))))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Version != "2026-11-07-21-13-06" {
		t.Fatalf("version %q", ds.Version)
	}
	want := cloudHeader +
		"192.0.2.0/24,aws,us-east-1,EC2,false\n" +
		"198.51.100.0/24,aws,,AMAZON,true\n" +
		"2001:db8::/32,aws,eu-west-1,EC2,false\n"
	if got := string(ds.Files["cloud.csv"]); got != want {
		t.Fatalf("cloud.csv:\n%s\nwant:\n%s", got, want)
	}
	if got := string(ds.Files["cdn.csv"]); got != "prefix,name\n203.0.113.0/24,CloudFront\n" {
		t.Fatalf("cdn.csv:\n%s", got)
	}
}

func TestConvertAWSRegionalBeatsGlobal(t *testing.T) {
	in := `{"createDate":"v","prefixes":[
	 {"ip_prefix":"192.0.2.0/24","region":"GLOBAL","service":"S3"},
	 {"ip_prefix":"192.0.2.0/24","region":"us-east-1","service":"AMAZON"}]}`
	ds, err := convertAWS(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(ds.Files["cloud.csv"]); got != cloudHeader+"192.0.2.0/24,aws,us-east-1,AMAZON,false\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestConvertAWSRejectsEmpty(t *testing.T) {
	for _, in := range []string{`{"prefixes":[],"ipv6_prefixes":[]}`, `<html><body>Denied</body></html>`} {
		ds, err := convertAWS(strings.NewReader(in))
		if err == nil || ds.Files != nil || !strings.Contains(err.Error(), "AWS") {
			t.Fatalf("%q: got %v %v", in, ds, err)
		}
	}
}

func TestFetchAWS(t *testing.T) {
	body := readFixture(t, "aws-ip-ranges.json")
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "x", status)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	ds, err := fetchAWS(srv.URL)(context.Background(), srv.Client(), time.Now())
	if err != nil || ds.Version != "2026-11-07-21-13-06" {
		t.Fatalf("got %+v %v", ds, err)
	}
	status = http.StatusForbidden
	if _, err := fetchAWS(srv.URL)(context.Background(), srv.Client(), time.Now()); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("got %v", err)
	}
}
