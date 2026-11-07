package analyze

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/location"
	"github.com/jbrathwa/outflow/internal/model"
)

// Test-only addresses (the embedded seed has no Irish range).
const (
	ipUS2 = "20.60.128.77" // Azure eastus (US), same range as ipUS
	ipIE  = "20.60.50.9"   // Azure northeurope (IE), test DB only
)

func multiDB(t *testing.T) *location.DB {
	t.Helper()
	dir := t.TempDir()
	w := func(n, s string) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("cloud.csv", "prefix,provider,region,service,global\n"+
		"20.60.40.0/24,azure,westeurope,Storage,false\n"+
		"20.60.128.0/24,azure,eastus,Storage,false\n"+
		"20.60.50.0/24,azure,northeurope,Storage,false\n")
	w("cdn.csv", "prefix,name\n")
	w("geo.csv", "start_ip,end_ip,country\n")
	w("VERSION", "test\n")
	db, err := location.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// multiSnapshot has one workload that looked up one domain resolving to ips; the
// firewall event carries no destination IP, so the IP comes from outflow's resolution.
func multiSnapshot(ips ...string) model.Snapshot {
	s := build([]string{"shop/web"}, []flow{{"shop/web", "multi.example.com", ips[0], "allow", 2}})
	s.Resolutions[0].IPs = ips
	s.EgressEvents[0].DestIP = ""
	return s
}

func multiDest(t *testing.T, s model.Snapshot, cfg config.Config) Destination {
	t.Helper()
	w := row(t, Build(s, multiDB(t), cfg), "shop/web")
	if len(w.Destinations) != 1 {
		t.Fatalf("destinations = %+v", w.Destinations)
	}
	return w.Destinations[0]
}

func TestMultiIPSameCountryKeepsStatus(t *testing.T) {
	d := multiDest(t, multiSnapshot(ipUS, ipUS2), rule("warn", "US"))
	if d.Status != Inside || len(d.OtherLocations) != 0 || d.Note != "" {
		t.Errorf("dest = %+v", d)
	}
	if !reflect.DeepEqual(d.OtherIPs, []string{ipUS2}) {
		t.Errorf("OtherIPs = %v", d.OtherIPs)
	}
}

func TestMultiIPDisagreeIsUnknown(t *testing.T) {
	d := multiDest(t, multiSnapshot(ipUS, ipNL), rule("warn", "US"))
	if d.Status != Unknown || d.Note != "resolves to several locations" || d.Exception != "" {
		t.Errorf("dest = %+v", d)
	}
	if len(d.OtherLocations) != 1 || d.OtherLocations[0].Country != "NL" {
		t.Errorf("OtherLocations = %+v", d.OtherLocations)
	}
}

func TestMultiIPAllOutsideIsOutside(t *testing.T) {
	d := multiDest(t, multiSnapshot(ipNL, ipIE), rule("warn", "US"))
	if d.Status != Outside || d.Note != "" || len(d.OtherLocations) != 1 {
		t.Errorf("dest = %+v", d)
	}
}

func TestMultiIPExceptionWins(t *testing.T) {
	cfg := rule("warn", "US")
	cfg.Residency.Exceptions = []config.Exception{{Domain: "multi.example.com", Reason: "vendor"}}
	d := multiDest(t, multiSnapshot(ipUS, ipNL), cfg)
	if d.Status != Inside || d.Exception != "vendor" || d.Note != "" {
		t.Errorf("dest = %+v", d)
	}
}

func TestMultiIPNeutralListsAllCountries(t *testing.T) {
	w := row(t, Build(multiSnapshot(ipUS, ipNL), multiDB(t), config.Config{}), "shop/web")
	if !reflect.DeepEqual(w.Locations, []string{"NL", "US"}) {
		t.Errorf("locations = %v", w.Locations)
	}
}

func TestFirewallIPHasNoOtherIPs(t *testing.T) {
	s := multiSnapshot(ipUS, ipNL)
	s.EgressEvents[0].DestIP = ipUS
	d := multiDest(t, s, rule("warn", "US"))
	if d.OtherIPs != nil || len(d.OtherLocations) != 0 || d.Status != Inside {
		t.Errorf("dest = %+v", d)
	}
}
