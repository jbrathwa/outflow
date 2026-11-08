package locdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFindServiceTagsURL(t *testing.T) {
	url, date, err := findServiceTagsURL(readFixture(t, "azure-download-page.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(url, "/ServiceTags_Public_20261102.json") || !strings.HasPrefix(url, "https://") || date != "20261102" {
		t.Fatalf("got %q %q", url, date)
	}
}

func TestFindServiceTagsURLPageChanged(t *testing.T) {
	_, _, err := findServiceTagsURL([]byte(`<html><a href="https://example.test/other.json">x</a></html>`))
	if err == nil || err.Error() != "Azure download page changed (no ServiceTags_Public link)" {
		t.Fatalf("got %v", err)
	}
}

func TestConvertAzureRules(t *testing.T) {
	ds, err := convertAzure(strings.NewReader(string(readFixture(t, "servicetags.json"))), "20261102")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Version != "20261102" {
		t.Fatalf("version %q", ds.Version)
	}
	wantCloud := cloudHeader +
		"192.0.2.0/24,azure,westeurope,Storage,false\n" +
		"198.51.100.0/24,azure,,AzureActiveDirectory,true\n" +
		"203.0.113.128/25,azure,,AzureFrontDoor,true\n"
	if got := string(ds.Files["cloud.csv"]); got != wantCloud {
		t.Fatalf("cloud.csv:\n%s\nwant:\n%s", got, wantCloud)
	}
	wantCDN := "prefix,name\n203.0.113.0/24,Azure Front Door\n"
	if got := string(ds.Files["cdn.csv"]); got != wantCDN {
		t.Fatalf("cdn.csv:\n%s", got)
	}
}

func TestConvertAzureKeepsIPv6AndSkipsBadPrefixes(t *testing.T) {
	in := `{"values":[{"name":"Storage.WestEurope","properties":{"region":"westeurope","addressPrefixes":["2001:db8:1::5/48","not-a-prefix","192.0.2.7/24"]}}]}`
	ds, err := convertAzure(strings.NewReader(in), "v")
	if err != nil {
		t.Fatal(err)
	}
	want := cloudHeader +
		"192.0.2.0/24,azure,westeurope,Storage,false\n" +
		"2001:db8:1::/48,azure,westeurope,Storage,false\n"
	if got := string(ds.Files["cloud.csv"]); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestConvertAzureRejectsEmpty(t *testing.T) {
	for _, in := range []string{`{"values":[]}`, `<html><body>Access denied</body></html>`} {
		ds, err := convertAzure(strings.NewReader(in), "v")
		if err == nil || ds.Files != nil {
			t.Fatalf("%q: want error and no dataset, got %v %v", in, ds, err)
		}
	}
}

func TestFetchAzure(t *testing.T) {
	jsonStatus := http.StatusOK
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Write([]byte(`<a href="` + srv.URL + `/dl/ServiceTags_Public_20261102.json">download</a>`))
		case "/dl/ServiceTags_Public_20261102.json":
			if jsonStatus != http.StatusOK {
				http.Error(w, "gone", jsonStatus)
				return
			}
			w.Write(readFixture(t, "servicetags.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ds, err := fetchAzure(srv.URL+"/page")(context.Background(), srv.Client(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ds.Version != "20261102" || len(ds.Files["cloud.csv"]) == 0 || len(ds.Files["cdn.csv"]) == 0 {
		t.Fatalf("bad dataset %+v", ds)
	}

	jsonStatus = http.StatusNotFound
	_, err = fetchAzure(srv.URL+"/page")(context.Background(), srv.Client(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("got %v", err)
	}
}
