package azure

// Target names the Log Analytics workspaces to query. The ARM enricher fills it
// from the cluster's diagnostic settings; flags and config may override it.
type Target struct {
	DNSWorkspace, PodWorkspace, FirewallWorkspace string
}
