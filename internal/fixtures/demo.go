// Package fixtures holds the built-in, entirely fictional demo snapshot used by
// `outflow --demo`, the docs and tests.
package fixtures

import (
	_ "embed"
	"encoding/json"

	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/model"
)

//go:embed demo.json
var demoJSON []byte

//go:embed demo-residency.yaml
var demoResidency []byte

// DemoJSON returns the demo snapshot exactly as embedded.
func DemoJSON() []byte {
	return append([]byte(nil), demoJSON...)
}

// Demo decodes the demo snapshot: cluster aks-prod-eus with every evidence level
// and residency outcome. Each call returns an independent copy.
func Demo() (model.Snapshot, error) {
	var s model.Snapshot
	err := json.Unmarshal(demoJSON, &s)
	return s, err
}

// DemoDegraded is the demo with CoreDNS query logging off: both CoreDNS sources
// missing and no DNS lookups, so only node-level attribution remains.
func DemoDegraded() model.Snapshot {
	s, err := Demo()
	if err != nil {
		panic("fixtures: embedded demo.json is invalid: " + err.Error())
	}
	s.DNSLookups = []model.DNSLookup{}
	for i, src := range s.Sources {
		switch src.ID {
		case "coredns-log-plugin":
			s.Sources[i] = model.SourceStatus{ID: src.ID, State: "missing", Detail: "log plugin not enabled in coredns-custom"}
		case "coredns-logs-collected":
			s.Sources[i] = model.SourceStatus{ID: src.ID, State: "missing", Detail: "no CoreDNS query lines in ContainerLogV2"}
		}
	}
	return s
}

// DemoResidency parses the embedded demo rule: allow US, with an api.stripe.com exception.
func DemoResidency() (config.Config, error) {
	return config.Parse(demoResidency)
}
