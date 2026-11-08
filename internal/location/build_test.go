package location

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuildMergesParts(t *testing.T) {
	az := Part{Info: SourceInfo{Name: "azure", Version: "v1"}, FS: fstest.MapFS{
		"cloud.csv": {Data: []byte("prefix,provider,region,service,global\n192.0.2.0/24,azure,westeurope,Storage,false\n")},
	}}
	geo := Part{Info: SourceInfo{Name: "geo", Version: "v2"}, FS: fstest.MapFS{
		"geo.csv": {Data: []byte("start_ip,end_ip,country\n198.51.100.0,198.51.100.255,FR\n")},
	}}
	db, err := Build([]Part{az, geo})
	if err != nil {
		t.Fatal(err)
	}
	l := db.Lookup(netip.MustParseAddr("192.0.2.10"))
	if l.Provider != "azure" || l.Region != "westeurope" || l.Country != "NL" || l.HowKnown != CloudRange {
		t.Errorf("azure lookup = %+v", l)
	}
	l = db.Lookup(netip.MustParseAddr("198.51.100.7"))
	if l.Country != "FR" || l.HowKnown != GeoIP {
		t.Errorf("geo lookup = %+v", l)
	}
	got := db.Sources()
	if len(got) != 2 || got[0] != az.Info || got[1] != geo.Info {
		t.Errorf("Sources() = %+v", got)
	}
}

func TestBuildMissingFilesOK(t *testing.T) {
	if _, err := Build([]Part{{Info: SourceInfo{Name: "aws"}, FS: fstest.MapFS{}}}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildBadRowNamesSourceAndFile(t *testing.T) {
	p := Part{Info: SourceInfo{Name: "azure"}, FS: fstest.MapFS{
		"cloud.csv": {Data: []byte("prefix,provider,region,service,global\nnot-a-prefix,azure,x,y,false\n")},
	}}
	_, err := Build([]Part{p})
	if err == nil || !strings.Contains(err.Error(), "azure") || !strings.Contains(err.Error(), "cloud.csv:2") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbeddedIsAllSample(t *testing.T) {
	src := embedded(t).Sources()
	if len(src) != len(SourceNames) {
		t.Fatalf("got %d sources", len(src))
	}
	for i, s := range src {
		if !s.Sample || s.Name != SourceNames[i] || s.Version != SampleVersion() {
			t.Errorf("source %d = %+v", i, s)
		}
	}
}

func TestVersionJoined(t *testing.T) {
	db, err := Build([]Part{
		{Info: SourceInfo{Name: "azure", Version: "v1"}, FS: fstest.MapFS{}},
		{Info: SourceInfo{Name: "geo", Version: "v2"}, FS: fstest.MapFS{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Version(); got != "azure v1, geo v2" {
		t.Errorf("Version() = %q", got)
	}
}

func TestSourceLabel(t *testing.T) {
	want := map[string]string{
		"azure": "Azure service tags", "aws": "AWS IP ranges", "gcp": "GCP IP ranges",
		"cdn": "CDN ranges", "geo": "DB-IP country database", "other": "other",
	}
	for n, w := range want {
		if got := SourceLabel(n); got != w {
			t.Errorf("SourceLabel(%q) = %q, want %q", n, got, w)
		}
	}
}

func BenchmarkBuildFull(b *testing.B) {
	var cloud, geo strings.Builder
	cloud.WriteString("prefix,provider,region,service,global\n")
	for i := 0; i < 60000; i++ {
		fmt.Fprintf(&cloud, "%d.%d.%d.0/24,aws,eu-west-1,S,false\n", 1+i/65536%200, i/256%256, i%256)
	}
	geo.WriteString("start_ip,end_ip,country\n")
	for i := 0; i < 600000; i++ {
		if i%10 == 0 {
			fmt.Fprintf(&geo, "2001:db8:%x::,2001:db8:%x:ffff:ffff:ffff:ffff:ffff,DE\n", i/10, i/10)
			continue
		}
		fmt.Fprintf(&geo, "%d.%d.%d.0,%d.%d.%d.255,FR\n", 1+i/65536%200, i/256%256, i%256, 1+i/65536%200, i/256%256, i%256)
	}
	parts := []Part{
		{Info: SourceInfo{Name: "aws"}, FS: fstest.MapFS{"cloud.csv": {Data: []byte(cloud.String())}}},
		{Info: SourceInfo{Name: "geo"}, FS: fstest.MapFS{"geo.csv": {Data: []byte(geo.String())}}},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Build(parts); err != nil {
			b.Fatal(err)
		}
	}
}
