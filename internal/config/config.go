// Package config loads and validates outflow.yaml, the optional user-defined
// data-residency rule.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jbrathwa/outflow/internal/model"
)

// Config is the parsed outflow.yaml. Residency is nil when no rule is set.
type Config struct {
	Residency *Residency `yaml:"residency"`
}

// Residency is the allowed-location rule plus its exceptions.
type Residency struct {
	Allow           Allow       `yaml:"allow"`
	Exceptions      []Exception `yaml:"exceptions"`
	UnknownLocation string      `yaml:"unknown_location"` // warn (default) | fail | ignore
}

// Allow lists the permitted countries and cloud regions.
type Allow struct {
	Countries    []string `yaml:"countries"`
	CloudRegions []string `yaml:"cloud_regions"` // provider:region, stored lowercase
}

// Exception exempts a destination (by domain or CIDR) from the rule.
type Exception struct {
	Domain   string `yaml:"domain"`
	CIDR     string `yaml:"cidr"`
	Workload string `yaml:"workload"` // empty = any workload
	Reason   string `yaml:"reason"`
}

// Load reads and validates the config at path. An empty path or empty file
// yields a zero Config.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	c, err := parse(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// LoadDefault loads <dir>/outflow.yaml if it exists, else returns a zero Config.
func LoadDefault(dir string) (Config, error) {
	p := filepath.Join(dir, "outflow.yaml")
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	return Load(p)
}

// Parse validates config YAML held in memory, such as an embedded file.
func Parse(data []byte) (Config, error) {
	return parse(data)
}

func parse(b []byte) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, err
	}
	if c.Residency == nil {
		// `residency:` with nothing under it decodes to nil; reject it.
		var raw map[string]any
		if err := yaml.Unmarshal(b, &raw); err == nil {
			if _, ok := raw["residency"]; ok {
				return Config{}, errors.New("residency: allow must list at least one country or cloud_region")
			}
		}
		return c, nil
	}
	if err := c.Residency.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (r *Residency) validate() error {
	if len(r.Allow.Countries) == 0 && len(r.Allow.CloudRegions) == 0 {
		return errors.New("residency.allow: must list at least one country or cloud_region")
	}
	for i, cc := range r.Allow.Countries {
		if !isCountry(cc) {
			return fmt.Errorf("residency.allow.countries[%d]: %q must be two uppercase letters", i, cc)
		}
	}
	for i, cr := range r.Allow.CloudRegions {
		prov, region, ok := strings.Cut(cr, ":")
		prov, region = strings.ToLower(strings.TrimSpace(prov)), strings.ToLower(strings.TrimSpace(region))
		if !ok || region == "" || (prov != "azure" && prov != "aws" && prov != "gcp") {
			return fmt.Errorf("residency.allow.cloud_regions[%d]: %q must be provider:region with provider azure, aws or gcp", i, cr)
		}
		r.Allow.CloudRegions[i] = prov + ":" + region
	}
	switch r.UnknownLocation {
	case "":
		r.UnknownLocation = "warn"
	case "warn", "fail", "ignore":
	default:
		return fmt.Errorf("residency.unknown_location: %q must be warn, fail or ignore", r.UnknownLocation)
	}
	for i, e := range r.Exceptions {
		p := fmt.Sprintf("residency.exceptions[%d]", i)
		if (e.Domain == "") == (e.CIDR == "") {
			return fmt.Errorf("%s: exactly one of domain or cidr is required", p)
		}
		if e.CIDR != "" {
			if _, err := netip.ParsePrefix(e.CIDR); err != nil {
				return fmt.Errorf("%s.cidr: %q is not a valid CIDR", p, e.CIDR)
			}
		}
		if strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("%s.reason: is required", p)
		}
	}
	return nil
}

func isCountry(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// Matches reports whether the exception covers this destination and workload.
// A domain exception matches only domain (exact, or "*.suffix" for subdomains
// only); a CIDR exception matches only ip.
func (e Exception) Matches(domain string, ip netip.Addr, workloadID string) bool {
	if e.Workload != "" && e.Workload != workloadID {
		return false
	}
	switch {
	case e.Domain != "":
		d, pat := model.NormalizeDomain(domain), model.NormalizeDomain(e.Domain)
		if d == "" {
			return false
		}
		if suffix, ok := strings.CutPrefix(pat, "*."); ok {
			return strings.HasSuffix(d, "."+suffix)
		}
		return d == pat
	case e.CIDR != "":
		p, err := netip.ParsePrefix(e.CIDR)
		if err != nil || !ip.IsValid() {
			return false
		}
		return p.Contains(ip.Unmap())
	}
	return false
}
