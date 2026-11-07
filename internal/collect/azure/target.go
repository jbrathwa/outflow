package azure

// Target names the Log Analytics workspaces to query. The ARM enricher fills it
// from the cluster's diagnostic settings; flags and config may override it.
type Target struct {
	DNSWorkspace, PodWorkspace, FirewallWorkspace string
	// AKSResourceID is the managed cluster's ARM resource ID, set by the enricher once the
	// cluster was read; empty when it is not known.
	AKSResourceID string
}
