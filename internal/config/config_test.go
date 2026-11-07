package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValid(t *testing.T) {
	c, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r := c.Residency
	if r == nil {
		t.Fatal("residency nil")
	}
	if len(r.Allow.Countries) != 1 || r.Allow.Countries[0] != "US" {
		t.Errorf("countries = %v", r.Allow.Countries)
	}
	if len(r.Allow.CloudRegions) != 2 || r.Allow.CloudRegions[1] != "azure:westus2" {
		t.Errorf("regions = %v", r.Allow.CloudRegions)
	}
	if len(r.Exceptions) != 1 || r.Exceptions[0].Domain != "api.stripe.com" {
		t.Errorf("exceptions = %+v", r.Exceptions)
	}
	if r.UnknownLocation != "warn" {
		t.Errorf("unknown_location = %q", r.UnknownLocation)
	}
}

func TestLoadDefaultsAndCase(t *testing.T) {
	c, err := Load(write(t, "residency:\n  allow:\n    cloud_regions: [Azure:EastUS]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Residency.Allow.CloudRegions[0]; got != "azure:eastus" {
		t.Errorf("region = %q", got)
	}
	if c.Residency.UnknownLocation != "warn" {
		t.Errorf("default unknown_location = %q", c.Residency.UnknownLocation)
	}
}

func TestLoadEmptyPath(t *testing.T) {
	c, err := Load("")
	if err != nil || c.Residency != nil {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	c, err := Load(write(t, ""))
	if err != nil || c.Residency != nil {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func write(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "outflow.yaml")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown key", "residency:\n  allow:\n    countries: [US]\n  bogus: 1\n", "bogus"},
		{"lowercase country", "residency:\n  allow:\n    countries: [us]\n", "residency.allow.countries[0]"},
		{"3-letter country", "residency:\n  allow:\n    countries: [USA]\n", "residency.allow.countries[0]"},
		{"region no provider", "residency:\n  allow:\n    cloud_regions: [eastus]\n", "residency.allow.cloud_regions[0]"},
		{"bad provider", "residency:\n  allow:\n    cloud_regions: [oci:x]\n", "residency.allow.cloud_regions[0]"},
		{"bad unknown_location", "residency:\n  allow:\n    countries: [US]\n  unknown_location: maybe\n", "residency.unknown_location"},
		{"no reason", "residency:\n  allow:\n    countries: [US]\n  exceptions:\n    - domain: a.com\n", "residency.exceptions[0].reason"},
		{"domain and cidr", "residency:\n  allow:\n    countries: [US]\n  exceptions:\n    - domain: a.com\n      cidr: 10.0.0.0/8\n      reason: x\n", "residency.exceptions[0]"},
		{"neither domain nor cidr", "residency:\n  allow:\n    countries: [US]\n  exceptions:\n    - reason: x\n", "residency.exceptions[0]"},
		{"bad cidr", "residency:\n  allow:\n    countries: [US]\n  exceptions:\n    - cidr: nope\n      reason: x\n", "residency.exceptions[0].cidr"},
		{"empty allow", "residency:\n  allow: {}\n", "residency.allow"},
		{"residency empty", "residency:\n", "residency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("want error")
	}
}

func TestLoadDefault(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadDefault(dir)
	if err != nil || c.Residency != nil {
		t.Fatalf("absent: %+v, %v", c, err)
	}
	b, _ := os.ReadFile("testdata/valid.yaml")
	if err := os.WriteFile(filepath.Join(dir, "outflow.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadDefault(dir)
	if err != nil || c.Residency == nil {
		t.Fatalf("present: %+v, %v", c, err)
	}
}

func TestExceptionMatches(t *testing.T) {
	var none netip.Addr
	ip := netip.MustParseAddr("10.1.2.3")
	cases := []struct {
		name   string
		e      Exception
		domain string
		ip     netip.Addr
		wl     string
		want   bool
	}{
		{"wildcard sub", Exception{Domain: "*.stripe.com"}, "api.stripe.com", none, "", true},
		{"wildcard apex", Exception{Domain: "*.stripe.com"}, "stripe.com", none, "", false},
		{"wildcard lookalike", Exception{Domain: "*.stripe.com"}, "evilstripe.com", none, "", false},
		{"exact normalized", Exception{Domain: "api.stripe.com"}, "API.STRIPE.COM.", none, "", true},
		{"exact other", Exception{Domain: "api.stripe.com"}, "x.stripe.com", none, "", false},
		{"domain exc ignores ip", Exception{Domain: "api.stripe.com"}, "", ip, "", false},
		{"cidr hit", Exception{CIDR: "10.0.0.0/8"}, "", ip, "", true},
		{"cidr miss", Exception{CIDR: "192.168.0.0/16"}, "", ip, "", false},
		{"cidr zero ip", Exception{CIDR: "10.0.0.0/8"}, "", none, "", false},
		{"cidr ignores domain", Exception{CIDR: "10.0.0.0/8"}, "10.1.2.3", none, "", false},
		{"workload match", Exception{Domain: "a.com", Workload: "ns/app"}, "a.com", none, "ns/app", true},
		{"workload mismatch", Exception{Domain: "a.com", Workload: "ns/app"}, "a.com", none, "ns/other", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Matches(tc.domain, tc.ip, tc.wl); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

const (
	guidA = "00000000-0000-0000-0000-00000000000a"
	guidB = "00000000-0000-0000-0000-00000000000b"
	aksID = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-eus/providers/Microsoft.ContainerService/managedClusters/aks-prod-eus"
)

func TestSourcesParse(t *testing.T) {
	y := "sources:\n  dns_workspace: " + guidA + "\n  pod_inventory_workspace: " + guidB +
		"\n  firewall_workspace: " + strings.ToUpper(guidA) + "\n  aks_cluster: " + aksID + "\n"
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Sources
	if s.DNSWorkspace != guidA || s.PodInventoryWorkspace != guidB || s.FirewallWorkspace != strings.ToUpper(guidA) || s.AKSCluster != aksID {
		t.Errorf("unexpected sources: %+v", s)
	}
}

func TestSourcesRejectBadGUID(t *testing.T) {
	_, err := Parse([]byte("sources:\n  dns_workspace: x\n"))
	if err == nil || !strings.Contains(err.Error(), `sources.dns_workspace: "x" is not a Log Analytics workspace ID (a GUID)`) {
		t.Fatalf("got %v", err)
	}
	_, err = Parse([]byte("sources:\n  firewall_workspace: nope\n"))
	if err == nil || !strings.Contains(err.Error(), "sources.firewall_workspace") {
		t.Fatalf("got %v", err)
	}
}

func TestSourcesRejectBadAKSID(t *testing.T) {
	_, err := Parse([]byte("sources:\n  aks_cluster: /subscriptions/x\n"))
	if err == nil || !strings.Contains(err.Error(), "sources.aks_cluster:") || !strings.Contains(err.Error(), "is not an AKS resource ID") {
		t.Fatalf("got %v", err)
	}
}

func TestSourcesOnlyKeepsNeutral(t *testing.T) {
	c, err := Parse([]byte("sources:\n  dns_workspace: " + guidA + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Residency != nil {
		t.Error("residency should stay nil")
	}
}

func TestWithWorkspaceOverridesAll(t *testing.T) {
	s := Sources{DNSWorkspace: guidA, AKSCluster: aksID}
	got := s.WithWorkspace(guidB)
	if got.DNSWorkspace != guidB || got.PodInventoryWorkspace != guidB || got.FirewallWorkspace != guidB || got.AKSCluster != aksID {
		t.Errorf("got %+v", got)
	}
	if s.DNSWorkspace != guidA {
		t.Error("receiver mutated")
	}
	if same := s.WithWorkspace(""); same != s {
		t.Errorf("empty id changed sources: %+v", same)
	}
}
