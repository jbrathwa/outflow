package azure

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/model"
)

// Config builds the azure platform.
type Config struct {
	Sources                 config.Sources
	Tokens                  TokenSource
	LAEndpoint, ARMEndpoint string // empty = public cloud
}

// NewPlatform assembles the azure profile: one Log Analytics client, one ARM client and
// one shared Target (config workspaces win; the enricher only fills empty ones).
func NewPlatform(cfg Config) collect.Platform {
	log := newAccessLog()
	la := NewLogAnalytics(cfg.Tokens)
	if cfg.LAEndpoint != "" {
		la.Endpoint = cfg.LAEndpoint
	}
	la.OnResult = log.workspace
	arm := &ARM{Endpoint: "https://management.azure.com", Tokens: cfg.Tokens, HTTP: &http.Client{Timeout: time.Minute}, OnResult: log.armCall}
	if cfg.ARMEndpoint != "" {
		arm.Endpoint = cfg.ARMEndpoint
	}
	tg := &Target{
		DNSWorkspace:      cfg.Sources.DNSWorkspace,
		PodWorkspace:      cfg.Sources.PodInventoryWorkspace,
		FirewallWorkspace: cfg.Sources.FirewallWorkspace,
	}
	return collect.Platform{
		Name:     "azure",
		Enricher: &Enricher{ARM: arm, AKSCluster: cfg.Sources.AKSCluster, Target: tg},
		Pods:     &PodSource{LA: la, Target: tg},
		DNS:      &DNSSource{LA: la, Target: tg},
		Egress:   &FirewallSource{LA: la, Target: tg},
		Final:    func() []model.SourceStatus { return []model.SourceStatus{log.final()} },
	}
}

// TestEndpointsFromEnv reads the OUTFLOW_TEST_LA_ENDPOINT / OUTFLOW_TEST_ARM_ENDPOINT test
// hooks (internal, not user documented). ok is false when both are unset. A set value must
// be http://127.0.0.1:<port> or http://[::1]:<port>; anything else is an error. The Log
// Analytics value is the base URL including /v1.
func TestEndpointsFromEnv() (la, arm string, ok bool, err error) {
	la, arm = os.Getenv("OUTFLOW_TEST_LA_ENDPOINT"), os.Getenv("OUTFLOW_TEST_ARM_ENDPOINT")
	if la == "" && arm == "" {
		return "", "", false, nil
	}
	for _, v := range []string{la, arm} {
		if v == "" {
			continue
		}
		u, perr := url.Parse(v)
		if perr != nil || u.Scheme != "http" || u.Port() == "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
			return "", "", false, errors.New("OUTFLOW_TEST_* must point at 127.0.0.1")
		}
	}
	return la, arm, true, nil
}
