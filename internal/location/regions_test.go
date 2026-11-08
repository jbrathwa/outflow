package location

import (
	"net/netip"
	"regexp"
	"testing"
	"testing/fstest"
)

func TestRegionCountryComplete(t *testing.T) {
	want := map[string]string{
		"azure/swedencentral": "SE", "azure/germanywestcentral": "DE", "azure/francecentral": "FR",
		"azure/centralindia": "IN", "azure/japaneast": "JP", "azure/australiaeast": "AU",
		"azure/brazilsouth": "BR", "azure/canadacentral": "CA", "azure/uaenorth": "AE",
		"azure/southafricanorth": "ZA", "azure/switzerlandnorth": "CH", "azure/norwayeast": "NO",
		"azure/koreacentral": "KR", "azure/eastasia": "HK", "azure/southeastasia": "SG",
		"azure/westus3": "US", "azure/polandcentral": "PL", "azure/italynorth": "IT",
		"azure/qatarcentral": "QA", "azure/israelcentral": "IL", "azure/mexicocentral": "MX",
		"azure/spaincentral": "ES", "azure/eastus2euap": "US",
		"aws/ap-southeast-2": "AU", "aws/eu-west-2": "GB", "aws/eu-west-3": "FR", "aws/eu-north-1": "SE",
		"aws/ap-northeast-1": "JP", "aws/ap-south-1": "IN", "aws/sa-east-1": "BR", "aws/ca-central-1": "CA",
		"aws/me-central-1": "AE", "aws/af-south-1": "ZA", "aws/eu-south-2": "ES", "aws/eu-central-2": "CH",
		"aws/ap-east-1": "HK", "aws/us-gov-west-1": "US",
		"gcp/europe-west3": "DE", "gcp/europe-west2": "GB", "gcp/europe-west1": "BE", "gcp/asia-northeast1": "JP",
		"gcp/australia-southeast1": "AU", "gcp/southamerica-east1": "BR", "gcp/northamerica-northeast1": "CA",
		"gcp/asia-south1": "IN", "gcp/me-central1": "QA", "gcp/europe-north1": "FI", "gcp/asia-east1": "TW",
	}
	for k, c := range want {
		p, r := k[:indexByte(k, '/')], k[indexByte(k, '/')+1:]
		if got := RegionCountry(p, r); got != c {
			t.Errorf("RegionCountry(%q,%q)=%q want %q", p, r, got, c)
		}
	}
	iso := regexp.MustCompile(`^[A-Z]{2}$`)
	for k, v := range regionCountries {
		if !iso.MatchString(v) {
			t.Errorf("%s: bad country %q", k, v)
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func unmappedParts(withGeo bool) []Part {
	parts := []Part{{Info: SourceInfo{Name: "azure", Version: "v1"}, FS: fstest.MapFS{
		"cloud.csv": {Data: []byte("prefix,provider,region,service,global\n192.0.2.0/24,azure,newregion9,AzureCloud,false\n")},
	}}}
	if withGeo {
		parts = append(parts, Part{Info: SourceInfo{Name: "geo", Version: "v2"}, FS: fstest.MapFS{
			"geo.csv": {Data: []byte("start_ip,end_ip,country\n192.0.2.0,192.0.2.255,SE\n")},
		}})
	}
	return parts
}

func TestLookupUnmappedRegionUsesGeoIP(t *testing.T) {
	db, err := Build(unmappedParts(true))
	if err != nil {
		t.Fatal(err)
	}
	l := db.Lookup(netip.MustParseAddr("192.0.2.5"))
	want := Location{Provider: "azure", Region: "newregion9", Service: "AzureCloud", Country: "SE", HowKnown: GeoIP}
	if l != want {
		t.Errorf("got %+v want %+v", l, want)
	}
	if s := l.String(); s != "Azure newregion9 (SE)" {
		t.Errorf("String() = %q", s)
	}
}

func TestLookupUnmappedRegionNoGeo(t *testing.T) {
	db, err := Build(unmappedParts(false))
	if err != nil {
		t.Fatal(err)
	}
	l := db.Lookup(netip.MustParseAddr("192.0.2.5"))
	if l.Country != "" || l.HowKnown != CloudRange || l.Known() {
		t.Errorf("got %+v", l)
	}
}
