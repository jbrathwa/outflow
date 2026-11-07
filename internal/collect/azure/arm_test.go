package azure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/collect/azure/lafake"
	"github.com/jbrathwa/outflow/internal/config"
	"github.com/jbrathwa/outflow/internal/model"
)

const (
	subID    = "00000000-0000-0000-0000-0000000000aa"
	aksID    = "/subscriptions/" + subID + "/resourceGroups/rg-aks-prod/providers/Microsoft.ContainerService/managedClusters/aks-prod-eus"
	wsResID  = "/subscriptions/" + subID + "/resourceGroups/rg-hub/providers/Microsoft.OperationalInsights/workspaces/law-hub-eus"
	wsCustID = "00000000-0000-0000-0000-00000000000a"
	nodeProv = "azure:///subscriptions/" + subID + "/resourceGroups/MC_rg-aks-prod_aks-prod-eus_eastus/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1-1/virtualMachines/0"
)

func clusterBody(props map[string]any) map[string]any {
	return map[string]any{"id": aksID, "name": "aks-prod-eus", "location": "eastus", "properties": props}
}

func newEnricher(srv *lafake.Server, explicit string, tg *Target) *Enricher {
	return &Enricher{ARM: &ARM{Endpoint: srv.URL, Tokens: StaticToken("t")}, AKSCluster: explicit, Target: tg}
}

func enrich(t *testing.T, e *Enricher, provs []string) (collect.ClusterState, model.SourceStatus) {
	t.Helper()
	st := collect.ClusterState{ProviderIDs: provs}
	ss := e.Enrich(context.Background(), &st)
	if len(ss) != 1 || ss[0].ID != model.SrcAKS {
		t.Fatalf("statuses = %+v", ss)
	}
	return st, ss[0]
}

func TestEnrichDiscoversByNodeResourceGroup(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM("/subscriptions/"+subID+"/providers/Microsoft.ContainerService/managedClusters", map[string]any{"value": []any{
		map[string]any{"id": "/subscriptions/" + subID + "/resourceGroups/rg-other/providers/Microsoft.ContainerService/managedClusters/other", "properties": map[string]any{"nodeResourceGroup": "MC_other"}},
		map[string]any{"id": aksID, "properties": map[string]any{"nodeResourceGroup": "mc_RG-AKS-PROD_aks-prod-eus_eastus"}},
	}})
	srv.ARM(aksID, clusterBody(map[string]any{"networkProfile": map[string]any{"outboundType": "userDefinedRouting"}}))
	st, s := enrich(t, newEnricher(srv, "", &Target{}), []string{"bogus", nodeProv})
	if s.State != "ok" || s.Detail != "aks-prod-eus · eastus · outbound userDefinedRouting · AKS LocalDNS not checked" {
		t.Fatalf("%+v", s)
	}
	if st.Cluster.Name != "aks-prod-eus" {
		t.Fatalf("name %q", st.Cluster.Name)
	}
}

func TestEnrichDiscoveryNoneVisible(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM("/subscriptions/"+subID+"/providers/Microsoft.ContainerService/managedClusters", map[string]any{"value": []any{}})
	_, s := enrich(t, newEnricher(srv, "", &Target{}), []string{nodeProv})
	if s.State != "warn" || s.Detail != "AKS resource not found; set sources.aks_cluster (needs Reader on the cluster)" {
		t.Fatalf("%+v", s)
	}
}

func TestEnrichExplicitResourceID(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM(aksID, clusterBody(map[string]any{"networkProfile": map[string]any{"outboundType": "loadBalancer"}}))
	_, s := enrich(t, newEnricher(srv, aksID, &Target{}), nil)
	if s.State != "ok" {
		t.Fatalf("%+v", s)
	}
	_, s = enrich(t, newEnricher(srv, aksID+"-nope", &Target{}), nil)
	if s.State != "missing" || s.Detail != "AKS resource not found: aks-prod-eus-nope" {
		t.Fatalf("%+v", s)
	}
}

func TestEnrichOutboundAndCIDRs(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM(aksID, clusterBody(map[string]any{"networkProfile": map[string]any{
		"outboundType": "userDefinedRouting", "serviceCidrs": []string{"10.0.0.0/16"}, "podCidrs": []string{"10.244.0.0/16"}}}))
	st := collect.ClusterState{Cluster: model.Cluster{CNI: "azure"}}
	newEnricher(srv, aksID, &Target{}).Enrich(context.Background(), &st)
	c := st.Cluster
	if c.OutboundType != "userDefinedRouting" || c.Region != "eastus" || len(c.ServiceCIDRs) != 1 || c.ServiceCIDRs[0] != "10.0.0.0/16" || len(c.PodCIDRs) != 1 || c.CNI != "azure" {
		t.Fatalf("%+v", c)
	}
	// Empty values never overwrite.
	srv.ARM(aksID, clusterBody(map[string]any{"networkProfile": map[string]any{"serviceCidr": "10.1.0.0/16"}}))
	st.Cluster.OutboundType = "keep"
	newEnricher(srv, aksID, &Target{}).Enrich(context.Background(), &st)
	if st.Cluster.OutboundType != "keep" || st.Cluster.ServiceCIDRs[0] != "10.1.0.0/16" || len(st.Cluster.PodCIDRs) != 1 {
		t.Fatalf("%+v", st.Cluster)
	}
}

func TestEnrichOverlayCNI(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM(aksID, clusterBody(map[string]any{"networkProfile": map[string]any{"networkPlugin": "azure", "networkPluginMode": "overlay"}}))
	st := collect.ClusterState{Cluster: model.Cluster{CNI: "azure"}}
	newEnricher(srv, aksID, &Target{}).Enrich(context.Background(), &st)
	if st.Cluster.CNI != "azure-cni-overlay" {
		t.Fatalf("cni %q", st.Cluster.CNI)
	}
}

func TestEnrichLocalDNS(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM(aksID, clusterBody(map[string]any{}))
	srv.ARM(aksID+"/agentPools", map[string]any{"value": []any{
		map[string]any{"properties": map[string]any{"localDNSProfile": map[string]any{"mode": "Disabled"}}},
		map[string]any{"properties": map[string]any{"localDNSProfile": map[string]any{"mode": "Required"}}},
	}})
	st, s := enrich(t, newEnricher(srv, aksID, &Target{}), nil)
	if !st.Cluster.AKSLocalDNS || s.Detail != "aks-prod-eus · eastus · LocalDNS on" {
		t.Fatalf("%+v %+v", st.Cluster, s)
	}
	srv.ARM(aksID+"/agentPools", map[string]any{"value": []any{
		map[string]any{"properties": map[string]any{"localDNSProfile": map[string]any{"mode": "Disabled"}}}}})
	st, s = enrich(t, newEnricher(srv, aksID, &Target{}), nil)
	if st.Cluster.AKSLocalDNS || s.Detail != "aks-prod-eus · eastus" {
		t.Fatalf("%+v %+v", st.Cluster, s)
	}
}

func TestEnrichLocalDNSFieldAbsentIsNotChecked(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARM(aksID, clusterBody(map[string]any{}))
	srv.ARM(aksID+"/agentPools", map[string]any{"value": []any{map[string]any{"properties": map[string]any{"mode": "System"}}}})
	st, s := enrich(t, newEnricher(srv, aksID, &Target{}), nil)
	if st.Cluster.AKSLocalDNS || s.Detail != "aks-prod-eus · eastus · AKS LocalDNS not checked" {
		t.Fatalf("%+v %+v", st.Cluster, s)
	}
}

func wsFake(t *testing.T, props map[string]any) *lafake.Server {
	srv := lafake.New(t, nil)
	srv.ARM(aksID, clusterBody(props))
	srv.ARM(wsResID, map[string]any{"properties": map[string]any{"customerId": wsCustID}})
	return srv
}

func TestEnrichFillsWorkspaces(t *testing.T) {
	srv := wsFake(t, map[string]any{"addonProfiles": map[string]any{"omsagent": map[string]any{"enabled": true,
		"config": map[string]any{"logAnalyticsWorkspaceResourceID": wsResID}}}})
	tg := &Target{PodWorkspace: "configured"}
	enrich(t, newEnricher(srv, aksID, tg), nil)
	if tg.DNSWorkspace != wsCustID || tg.PodWorkspace != "configured" {
		t.Fatalf("%+v", tg)
	}
	// azureMonitorProfile variant
	srv = wsFake(t, map[string]any{"azureMonitorProfile": map[string]any{"containerInsights": map[string]any{"enabled": true, "logAnalyticsWorkspaceResourceId": wsResID}}})
	tg = &Target{}
	enrich(t, newEnricher(srv, aksID, tg), nil)
	if tg.DNSWorkspace != wsCustID || tg.PodWorkspace != wsCustID {
		t.Fatalf("%+v", tg)
	}
}

func TestEnrichNeverGuessesFirewallWorkspace(t *testing.T) {
	srv := wsFake(t, map[string]any{"addonProfiles": map[string]any{"omsagent": map[string]any{"config": map[string]any{"logAnalyticsWorkspaceResourceID": wsResID}}}})
	tg := &Target{}
	enrich(t, newEnricher(srv, aksID, tg), nil)
	if tg.FirewallWorkspace != "" {
		t.Fatalf("%+v", tg)
	}
}

func TestEnrichForbiddenWarns(t *testing.T) {
	srv := forbidden(t)
	e := &Enricher{ARM: &ARM{Endpoint: srv.URL, Tokens: StaticToken("t")}, AKSCluster: aksID, Target: &Target{}}
	_, s := enrich(t, e, nil)
	if s.State != "missing" || s.Detail != "no access to the AKS resource (needs Reader)" {
		t.Fatalf("%+v", s)
	}
}

func TestAzureAccessSummary(t *testing.T) {
	srv := lafake.New(t, map[string]lafake.Handler{"x": func(lafake.Call) (lafake.Response, int) { return lafake.Response{Columns: []string{"a"}}, 200 }})
	srv.ARM(aksID, clusterBody(map[string]any{}))
	p := NewPlatform(Config{Sources: config.Sources{AKSCluster: aksID, DNSWorkspace: "00000000-0000-0000-0000-00000000000a"}, Tokens: StaticToken("t"), LAEndpoint: srv.URL, ARMEndpoint: srv.URL})
	if got := p.Final(); len(got) != 1 || got[0].State != "warn" || got[0].Detail != "no Azure calls made" {
		t.Fatalf("%+v", got)
	}
	st := collect.ClusterState{}
	p.Enricher.Enrich(context.Background(), &st)
	la := p.DNS.(*DNSSource).LA
	w := model.Window{}
	_, _ = la.Query(context.Background(), "x", "00000000-0000-0000-0000-00000000000a", "T", w)
	got := p.Final()
	if got[0].ID != model.SrcAzureAccess || got[0].State != "ok" || got[0].Detail != "Log Analytics Reader on 1 workspaces · Reader on the AKS resource" {
		t.Fatalf("%+v", got)
	}
	_, _ = la.Query(context.Background(), "x", "denied-0000000000000b", "T", w)
	got = p.Final()
	if got[0].State != "missing" || got[0].Detail != "no access to workspace …000b (needs Log Analytics Reader)" {
		t.Fatalf("%+v", got)
	}
}

func TestTestHooksRequireLoopback(t *testing.T) {
	for _, tc := range []struct {
		la, arm string
		ok, err bool
	}{
		{"", "", false, false},
		{"http://127.0.0.1:8080/v1", "http://127.0.0.1:9090", true, false},
		{"http://[::1]:8080/v1", "", true, false},
		{"https://api.loganalytics.io/v1", "", false, true},
		{"", "http://localhost:80", false, true},
		{"http://127.0.0.1.evil.example:80", "", false, true},
		{"http://127.0.0.1", "", false, true},
	} {
		t.Setenv("OUTFLOW_TEST_LA_ENDPOINT", tc.la)
		t.Setenv("OUTFLOW_TEST_ARM_ENDPOINT", tc.arm)
		la, arm, ok, err := TestEndpointsFromEnv()
		if ok != tc.ok || (err != nil) != tc.err || (tc.ok && (la != tc.la || arm != tc.arm)) {
			t.Fatalf("%+v -> %q %q %v %v", tc, la, arm, ok, err)
		}
	}
}

func forbidden(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const (
	listPath = "/subscriptions/" + subID + "/providers/Microsoft.ContainerService/managedClusters"
	nodeRG   = "MC_rg-aks-prod_aks-prod-eus_eastus"
)

func enrichRG(t *testing.T, e *Enricher, provs []string, rg string) (collect.ClusterState, model.SourceStatus) {
	t.Helper()
	st := collect.ClusterState{ProviderIDs: provs, NodeResourceGroup: rg}
	ss := e.Enrich(context.Background(), &st)
	if len(ss) != 1 || ss[0].ID != model.SrcAKS {
		t.Fatalf("statuses = %+v", ss)
	}
	return st, ss[0]
}

func TestEnrichDirectGetFromNodeResourceGroup(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARMStatus(listPath, 403)
	srv.ARM(aksID, clusterBody(map[string]any{"nodeResourceGroup": nodeRG}))
	st, s := enrichRG(t, newEnricher(srv, "", &Target{}), []string{nodeProv}, "mc_rg-aks-prod_aks-prod-eus_eastus")
	if s.State != "ok" || st.Cluster.Name != "aks-prod-eus" {
		t.Fatalf("%+v", s)
	}
	if n := srv.ARMCalls(listPath); n != 0 {
		t.Fatalf("list called %d times", n)
	}
	if n := srv.ARMCalls(aksID); n != 1 {
		t.Fatalf("cluster GET %d times, want 1", n)
	}
}

func TestEnrichDirectGetFallsBackToList(t *testing.T) {
	for _, tc := range []struct{ name, rg string }{
		{"ambiguous", "MC_rg_aks_prod_eus_eastus"},
		{"no prefix", "rg-aks-prod_aks-prod-eus_eastus_x"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := lafake.New(t, nil)
			srv.ARM(listPath, map[string]any{"value": []any{
				map[string]any{"id": aksID, "properties": map[string]any{"nodeResourceGroup": nodeRG}}}})
			srv.ARM(aksID, clusterBody(map[string]any{"nodeResourceGroup": nodeRG}))
			_, s := enrichRG(t, newEnricher(srv, "", &Target{}), []string{nodeProv}, tc.rg)
			if s.State != "ok" || srv.ARMCalls(listPath) != 1 {
				t.Fatalf("%+v list=%d", s, srv.ARMCalls(listPath))
			}
		})
	}
	// The direct GET answers but for a different node resource group: use the list.
	srv := lafake.New(t, nil)
	srv.ARM(listPath, map[string]any{"value": []any{}})
	srv.ARM(aksID, clusterBody(map[string]any{"nodeResourceGroup": "MC_other"}))
	_, s := enrichRG(t, newEnricher(srv, "", &Target{}), []string{nodeProv}, nodeRG)
	if s.State != "warn" || srv.ARMCalls(listPath) != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestEnrichListForbiddenIsNotAnARMDenial(t *testing.T) {
	srv := lafake.New(t, nil)
	srv.ARMStatus(listPath, 403)
	p := NewPlatform(Config{Tokens: StaticToken("t"), LAEndpoint: srv.URL, ARMEndpoint: srv.URL})
	st := collect.ClusterState{ProviderIDs: []string{nodeProv}}
	ss := p.Enricher.Enrich(context.Background(), &st)
	if len(ss) != 1 || ss[0].State != "warn" || ss[0].Detail != "cannot list AKS clusters in the subscription; set sources.aks_cluster" {
		t.Fatalf("%+v", ss)
	}
	if got := p.Final(); got[0].State == "missing" || strings.Contains(got[0].Detail, "AKS resource") {
		t.Fatalf("azure-access %+v", got)
	}
}
