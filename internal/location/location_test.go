package location

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func embedded(t *testing.T) *DB {
	t.Helper()
	db, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLookup(t *testing.T) {
	db := embedded(t)
	tests := []struct {
		ip       string
		country  string
		provider string
		region   string
		service  string
		global   bool
		how      HowKnown
		str      string
	}{
		{"20.60.40.4", "NL", "azure", "westeurope", "Storage", false, CloudRange, "Azure westeurope (NL)"},
		{"20.60.128.10", "US", "azure", "eastus", "Storage", false, CloudRange, "Azure eastus (US)"},
		{"20.190.151.7", "", "azure", "", "AzureActiveDirectory", true, CloudRange, "global (anycast)"},
		{"52.208.1.1", "IE", "aws", "eu-west-1", "", false, CloudRange, "AWS eu-west-1 (IE)"},
		{"54.144.10.1", "US", "aws", "us-east-1", "", false, CloudRange, "AWS us-east-1 (US)"},
		{"3.210.5.5", "US", "aws", "us-east-1", "", false, CloudRange, "AWS us-east-1 (US)"},
		{"142.250.80.1", "", "gcp", "", "", true, CloudRange, "global (anycast)"},
		{"13.32.10.10", "", "CloudFront", "", "", true, AnycastCDN, "CDN (CloudFront)"},
		{"104.16.1.1", "", "Cloudflare", "", "", true, AnycastCDN, "CDN (Cloudflare)"},
		{"151.101.1.1", "", "Fastly", "", "", true, AnycastCDN, "CDN (Fastly)"},
		{"140.82.112.3", "US", "", "", "", false, GeoIP, "US"},
		{"51.15.22.140", "FR", "", "", "", false, GeoIP, "FR"},
		{"192.0.2.1", "", "", "", "", false, None, "unknown"},
		{"::ffff:51.15.22.140", "FR", "", "", "", false, GeoIP, "FR"},
		{"2001:db8::1", "", "", "", "", false, None, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			l := db.Lookup(netip.MustParseAddr(tc.ip))
			if l.Country != tc.country || l.Provider != tc.provider || l.Region != tc.region ||
				l.Service != tc.service || l.Global != tc.global || l.HowKnown != tc.how {
				t.Errorf("got %+v", l)
			}
			if l.String() != tc.str {
				t.Errorf("String() = %q, want %q", l.String(), tc.str)
			}
		})
	}
}

func TestKnown(t *testing.T) {
	if !(Location{Country: "US"}).Known() || (Location{Country: "US", Global: true}).Known() || (Location{}).Known() {
		t.Error("Known wrong")
	}
}

func TestRegionCountry(t *testing.T) {
	cases := map[[2]string]string{
		{"azure", "westeurope"}: "NL", {"azure", "eastus"}: "US", {"azure", "uksouth"}: "GB",
		{"aws", "eu-west-1"}: "IE", {"aws", "eu-central-1"}: "DE", {"gcp", "europe-west4"}: "NL",
		{"azure", "mars"}: "", {"foo", "eastus"}: "",
	}
	for k, want := range cases {
		if got := RegionCountry(k[0], k[1]); got != want {
			t.Errorf("RegionCountry(%v)=%q want %q", k, got, want)
		}
	}
}

func TestIsSaaS(t *testing.T) {
	for d, want := range map[string]bool{
		"API.Stripe.com.": true, "stripe.com": true, "api.eu.segment.io": true,
		"notstripe.com": false, "example.com": false, "": false,
	} {
		if IsSaaS(d) != want {
			t.Errorf("IsSaaS(%q) want %v", d, want)
		}
	}
}

func TestEmbeddedVersion(t *testing.T) {
	if v := embedded(t).Version(); v != "seed-2026-11-07" {
		t.Errorf("version %q", v)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "cloud.csv") {
		t.Fatalf("want error naming cloud.csv, got %v", err)
	}
	w := func(n, s string) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("cloud.csv", "prefix,provider,region,service,global\n10.0.0.0/8,aws,us-east-1,,false\n")
	w("cdn.csv", "prefix,name\n11.0.0.0/8,Fastly\n")
	w("geo.csv", "start_ip,end_ip,country\n12.0.0.0,12.0.0.255,DE\n")
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "VERSION") {
		t.Fatalf("want error naming VERSION, got %v", err)
	}
	w("VERSION", "v1\n")
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if db.Version() != "v1" {
		t.Errorf("version %q", db.Version())
	}
	if l := db.Lookup(netip.MustParseAddr("10.1.2.3")); l.Country != "US" || l.HowKnown != CloudRange {
		t.Errorf("cloud %+v", l)
	}
	if l := db.Lookup(netip.MustParseAddr("11.1.1.1")); l.HowKnown != AnycastCDN {
		t.Errorf("cdn %+v", l)
	}
	if l := db.Lookup(netip.MustParseAddr("12.0.0.9")); l.Country != "DE" {
		t.Errorf("geo %+v", l)
	}
	w("geo.csv", "start_ip,end_ip,country\nbogus,12.0.0.255,DE\n")
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "geo.csv") {
		t.Errorf("want geo.csv parse error, got %v", err)
	}
}
