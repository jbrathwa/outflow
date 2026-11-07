package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	start := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	bin := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

	in := Snapshot{
		SchemaVersion: SchemaVersion,
		Cluster: Cluster{
			Name:              "aks-prod-eus",
			Context:           "aks-prod-eus-admin",
			Platform:          "aks",
			Region:            "eastus",
			CNI:               "azure",
			PolicyEngine:      "calico",
			OutboundType:      "userDefinedRouting",
			Firewall:          "fw-hub-eus",
			PodCIDRs:          []string{"10.244.0.0/16"},
			ServiceCIDRs:      []string{"10.0.0.0/16"},
			NodeLocalDNS:      true,
			CalicoCRDPolicies: 3,
		},
		Window: Window{Start: start, End: end},
		Workloads: []Workload{{
			Namespace:   "payments",
			Name:        "checkout",
			Kind:        "Deployment",
			Labels:      map[string]string{"app": "checkout"},
			HostNetwork: false,
		}},
		Pods: []Pod{{
			Namespace:  "payments",
			Name:       "checkout-7d9f-abcde",
			WorkloadID: "payments/checkout",
			Node:       "aks-nodepool1-0",
			IP:         "10.244.1.12",
			Start:      start,
			End:        end,
		}, {
			Namespace:  "payments",
			Name:       "checkout-7d9f-fghij",
			WorkloadID: "payments/checkout",
			Node:       "aks-nodepool1-1",
			IP:         "10.244.2.7",
			Start:      start,
		}},
		Nodes: []Node{{Name: "aks-nodepool1-0", IPs: []string{"10.240.0.4"}}},
		NetworkPolicies: []NetworkPolicy{{
			Namespace:   "payments",
			Name:        "allow-egress",
			PodSelector: LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			PolicyTypes: []string{"Egress"},
			Egress: []EgressRule{{
				To: []Peer{
					{IPBlock: &IPBlock{CIDR: "10.0.0.0/8", Except: []string{"10.1.0.0/16"}}},
					{
						PodSelector: &LabelSelector{
							MatchExpressions: []LabelExpr{{Key: "tier", Operator: "In", Values: []string{"db"}}},
						},
						NamespaceSelector: &LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
					},
				},
				Ports: []PortSpec{{Protocol: "TCP", Port: 443, EndPort: 445}},
			}},
		}},
		DNSLookups: []DNSLookup{{PodIP: "10.244.1.12", Domain: "api.stripe.com", Bin: bin, Count: 4}},
		EgressEvents: []EgressEvent{{
			NodeIP:   "10.240.0.4",
			Domain:   "api.stripe.com",
			DestIP:   "203.0.113.10",
			Protocol: "TCP",
			Port:     443,
			Action:   "allow",
			Rule:     "fw-rule-1",
			Bin:      bin,
			Count:    9,
		}},
		Resolutions: []Resolution{{
			Domain: "api.stripe.com",
			IPs:    []string{"203.0.113.10"},
			Source: "outflow",
			At:     bin,
		}},
		Sources: []SourceStatus{{
			ID:         "k8s-api",
			State:      "ok",
			Detail:     "connected",
			LastRecord: end,
		}},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"schemaVersion":1`) {
		t.Fatalf("JSON missing schemaVersion:1: %s", data)
	}

	var out Snapshot
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch\n in: %+v\nout: %+v", in, out)
	}
}

func TestWorkloadID(t *testing.T) {
	w := Workload{Namespace: "payments", Name: "checkout"}
	if got := w.ID(); got != "payments/checkout" {
		t.Fatalf("ID() = %q, want %q", got, "payments/checkout")
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"API.Stripe.com.": "api.stripe.com",
		" x.io ":          "x.io",
		"api.stripe.com":  "api.stripe.com",
		"":                "",
	}
	for in, want := range cases {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
