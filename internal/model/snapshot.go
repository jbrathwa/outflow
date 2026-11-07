// Package model holds the snapshot types shared by collectors and the analyzer.
// It must not import UI, Kubernetes or cloud SDK packages.
package model

import (
	"strings"
	"time"
)

// SchemaVersion is the snapshot JSON schema version written by this build.
const SchemaVersion = 1

// Snapshot is the point-in-time capture of cluster state and observed egress.
type Snapshot struct {
	SchemaVersion   int             `json:"schemaVersion"`
	Cluster         Cluster         `json:"cluster"`
	Window          Window          `json:"window"`
	Workloads       []Workload      `json:"workloads"`
	Pods            []Pod           `json:"pods"`
	Nodes           []Node          `json:"nodes"`
	NetworkPolicies []NetworkPolicy `json:"networkPolicies"`
	DNSLookups      []DNSLookup     `json:"dnsLookups"`
	EgressEvents    []EgressEvent   `json:"egressEvents"`
	Resolutions     []Resolution    `json:"resolutions"`
	Sources         []SourceStatus  `json:"sources"`
	// Profile is the collection profile used ("azure", "generic"); empty in older snapshots.
	Profile string `json:"profile,omitempty"`
}

// Cluster describes the cluster and its network setup.
type Cluster struct {
	Name              string   `json:"name"`
	Context           string   `json:"context"`
	Platform          string   `json:"platform"`
	Region            string   `json:"region"`
	CNI               string   `json:"cni"`
	PolicyEngine      string   `json:"policyEngine"`
	OutboundType      string   `json:"outboundType"`
	Firewall          string   `json:"firewall"`
	PodCIDRs          []string `json:"podCidrs"`
	ServiceCIDRs      []string `json:"serviceCidrs"`
	NodeLocalDNS      bool     `json:"nodeLocalDns"`
	AKSLocalDNS       bool     `json:"aksLocalDns,omitempty"`
	CalicoCRDPolicies int      `json:"calicoCrdPolicies"`
}

// Window is the time range the snapshot covers.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Workload is a controller (Deployment, StatefulSet, ...) that owns pods.
type Workload struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Labels      map[string]string `json:"labels"`
	HostNetwork bool              `json:"hostNetwork"`
}

// ID returns the "namespace/name" identifier of the workload.
func (w Workload) ID() string {
	return w.Namespace + "/" + w.Name
}

// Pod is one pod instance. End is the zero time while the pod is still running.
type Pod struct {
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	WorkloadID string    `json:"workloadId"`
	Node       string    `json:"node"`
	IP         string    `json:"ip"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
}

// Node is a cluster node and its addresses.
type Node struct {
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

// NetworkPolicy is a Kubernetes or Calico policy reduced to what the analyzer needs.
type NetworkPolicy struct {
	Namespace   string        `json:"namespace"`
	Name        string        `json:"name"`
	PodSelector LabelSelector `json:"podSelector"`
	PolicyTypes []string      `json:"policyTypes"`
	Egress      []EgressRule  `json:"egress"`
}

// LabelSelector matches labels by exact value and by expression.
type LabelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels"`
	MatchExpressions []LabelExpr       `json:"matchExpressions"`
}

// LabelExpr is a single label selector expression.
type LabelExpr struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

// EgressRule allows traffic to the given peers on the given ports.
type EgressRule struct {
	To    []Peer     `json:"to"`
	Ports []PortSpec `json:"ports"`
}

// Peer is an egress destination: an IP block or a pod/namespace selector.
type Peer struct {
	IPBlock           *IPBlock       `json:"ipBlock"`
	PodSelector       *LabelSelector `json:"podSelector"`
	NamespaceSelector *LabelSelector `json:"namespaceSelector"`
}

// IPBlock is a CIDR with optional exceptions.
type IPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except"`
}

// PortSpec is a protocol and port (or port range when EndPort is set).
type PortSpec struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	EndPort  int    `json:"endPort"`
}

// DNSLookup counts DNS queries from a pod IP for a domain in one minute.
type DNSLookup struct {
	PodIP  string    `json:"podIp"`
	Domain string    `json:"domain"`
	Bin    time.Time `json:"bin"`
	Count  int       `json:"count"`
}

// EgressEvent counts observed outbound connections in one minute.
// Action is "allow" or "deny". Domain is empty for network-rule events.
type EgressEvent struct {
	NodeIP   string    `json:"nodeIp"`
	Domain   string    `json:"domain"`
	DestIP   string    `json:"destIp"`
	Protocol string    `json:"protocol"`
	Port     int       `json:"port"`
	Action   string    `json:"action"`
	Rule     string    `json:"rule"`
	Bin      time.Time `json:"bin"`
	Count    int       `json:"count"`
}

// Resolution maps a domain to the IPs it resolved to.
// Source is "outflow" or "firewall".
type Resolution struct {
	Domain   string    `json:"domain"`
	IPs      []string  `json:"ips"`
	Source   string    `json:"source"`
	Resolver string    `json:"resolver,omitempty"`
	At       time.Time `json:"at"`
}

// Source IDs used in SourceStatus.ID.
const (
	SrcK8sAPI           = "k8s-api"
	SrcK8sAccess        = "k8s-access"
	SrcFirewall         = "firewall-logs"
	SrcCoreDNSPlugin    = "coredns-log-plugin"
	SrcCoreDNSCollected = "coredns-logs-collected"
	SrcCoreDNSFormat    = "coredns-log-format"
	SrcPodInventory     = "pod-inventory"
	SrcAKS              = "aks-resource"
	SrcAzureAccess      = "azure-access"
)

// SourceStatus reports the health of one data source.
// State is "ok", "warn" or "missing".
type SourceStatus struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	Detail     string    `json:"detail"`
	LastRecord time.Time `json:"lastRecord"`
}

// NormalizeDomain lowercases a domain and strips surrounding spaces and a trailing dot,
// so CoreDNS and firewall spellings of the same name compare equal.
func NormalizeDomain(d string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
}
