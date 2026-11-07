package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
)

// API versions. The agentPools version is the current stable one that documents
// localDNSProfile; the first version exposing it was not confirmed, so an absent
// field is reported as "not checked", never as off.
const (
	apiManagedClusters = "2024-09-01"
	apiAgentPools      = "2025-10-01"
	apiWorkspaces      = "2023-09-01"
)

// ARM reads Azure Resource Manager with GET requests only.
type ARM struct {
	Endpoint string // https://management.azure.com
	Tokens   TokenSource
	HTTP     *http.Client
	// OnResult is called after every HTTP response (status 0 on a transport error).
	OnResult func(status int)
}

// get performs GET <endpoint><path>?api-version=<v> and returns the status and body.
func (a *ARM) get(ctx context.Context, path, apiVersion string) (int, []byte, error) {
	tok, err := a.Tokens.Token(ctx, ScopeARM)
	if err != nil {
		return 0, nil, errors.New("arm token unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(a.Endpoint, "/")+path+"?api-version="+url.QueryEscape(apiVersion), nil)
	if err != nil {
		return 0, nil, errors.New("arm request invalid")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	hc := a.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if a.OnResult != nil {
		s := 0
		if err == nil {
			s = resp.StatusCode
		}
		a.OnResult(s)
	}
	if err != nil {
		collect.Debugf("arm GET error=transport")
		return 0, nil, errors.New("arm request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	collect.Debugf("arm GET status=%d", resp.StatusCode)
	return resp.StatusCode, raw, nil
}

// accessLog records the outcome of every Log Analytics and ARM call.
type accessLog struct {
	mu  sync.Mutex
	ws  map[string]*accessEntry // by workspace id
	arm accessEntry
	any bool
}

type accessEntry struct{ ok, denied bool }

func (e *accessEntry) add(status int) {
	switch {
	case status == 401 || status == 403:
		e.denied = true
	case status >= 200 && status < 300:
		e.ok = true
	}
}

func newAccessLog() *accessLog { return &accessLog{ws: map[string]*accessEntry{}} }

func (l *accessLog) workspace(id string, status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.any = true
	e := l.ws[id]
	if e == nil {
		e = &accessEntry{}
		l.ws[id] = e
	}
	// A 400 (e.g. missing table) still proves the credentials were accepted.
	if status == 400 {
		status = 200
	}
	e.add(status)
}

func (l *accessLog) armCall(status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.any = true
	l.arm.add(status)
}

// final builds the azure-access status.
func (l *accessLog) final() model.SourceStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := model.SrcAzureAccess
	if !l.any {
		return model.SourceStatus{ID: id, State: "warn", Detail: "no Azure calls made"}
	}
	var denied []string
	okWS := 0
	for w, e := range l.ws {
		if e.denied && !e.ok {
			denied = append(denied, "workspace "+collect.MaskID(w)+" (needs Log Analytics Reader)")
		} else if e.ok {
			okWS++
		}
	}
	sortStrings(denied)
	if l.arm.denied && !l.arm.ok {
		denied = append(denied, "AKS resource (Reader)")
	}
	if len(denied) > 0 {
		return model.SourceStatus{ID: id, State: "missing", Detail: "no access to " + strings.Join(denied, ", ")}
	}
	var parts []string
	if okWS > 0 {
		parts = append(parts, fmt.Sprintf("Log Analytics Reader on %d workspaces", okWS))
	}
	if l.arm.ok {
		parts = append(parts, "Reader on the AKS resource")
	}
	if len(parts) == 0 {
		return model.SourceStatus{ID: id, State: "warn", Detail: "no successful Azure calls"}
	}
	return model.SourceStatus{ID: id, State: "ok", Detail: strings.Join(parts, " · ")}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Enricher reads the AKS resource and fills cluster facts and empty workspace targets.
type Enricher struct {
	ARM        *ARM
	AKSCluster string // explicit ARM resource ID; empty = discover from node providerIDs
	Target     *Target
}

var _ collect.ClusterEnricher = (*Enricher)(nil)

var providerRE = regexp.MustCompile(`(?i)^azure:///subscriptions/([^/]+)/resourceGroups/([^/]+)/`)

func resourceName(id string) string {
	id = strings.TrimRight(id, "/")
	return id[strings.LastIndex(id, "/")+1:]
}

type managedCluster struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Properties struct {
		NodeResourceGroup string `json:"nodeResourceGroup"`
		NetworkProfile    struct {
			NetworkPlugin     string   `json:"networkPlugin"`
			NetworkPluginMode string   `json:"networkPluginMode"`
			OutboundType      string   `json:"outboundType"`
			ServiceCidr       string   `json:"serviceCidr"`
			ServiceCidrs      []string `json:"serviceCidrs"`
			PodCidr           string   `json:"podCidr"`
			PodCidrs          []string `json:"podCidrs"`
		} `json:"networkProfile"`
		AddonProfiles map[string]struct {
			Config map[string]string `json:"config"`
		} `json:"addonProfiles"`
		AzureMonitorProfile struct {
			ContainerInsights struct {
				WorkspaceID string `json:"logAnalyticsWorkspaceResourceId"`
			} `json:"containerInsights"`
		} `json:"azureMonitorProfile"`
	} `json:"properties"`
}

func (e *Enricher) st(state, detail string) []model.SourceStatus {
	return []model.SourceStatus{{ID: model.SrcAKS, State: state, Detail: detail}}
}

const (
	detailForbidden = "no access to the AKS resource (needs Reader)"
	detailNotFound  = "AKS resource not found; set sources.aks_cluster (needs Reader on the cluster)"
	detailFailed    = "AKS resource read failed"
)

// Enrich implements collect.ClusterEnricher.
func (e *Enricher) Enrich(ctx context.Context, st *collect.ClusterState) []model.SourceStatus {
	id := strings.TrimSpace(e.AKSCluster)
	explicit := id != ""
	if !explicit {
		var s []model.SourceStatus
		id, s = e.discover(ctx, st.ProviderIDs)
		if s != nil {
			return s
		}
	}
	code, raw, err := e.ARM.get(ctx, id, apiManagedClusters)
	if err != nil {
		return e.st("missing", detailFailed)
	}
	switch {
	case code == 401 || code == 403:
		return e.st("missing", detailForbidden)
	case code == 404:
		return e.st("missing", "AKS resource not found: "+resourceName(id))
	case code/100 != 2:
		return e.st("missing", detailFailed)
	}
	var mc managedCluster
	if json.Unmarshal(raw, &mc) != nil {
		return e.st("missing", detailFailed)
	}
	c := &st.Cluster
	name := mc.Name
	if name == "" {
		name = resourceName(id)
	}
	c.Name = name
	if mc.Location != "" {
		c.Region = mc.Location
	}
	np := mc.Properties.NetworkProfile
	if np.OutboundType != "" {
		c.OutboundType = np.OutboundType
	}
	if sc := cidrs(np.ServiceCidrs, np.ServiceCidr); len(sc) > 0 {
		c.ServiceCIDRs = sc
	}
	if pc := cidrs(np.PodCidrs, np.PodCidr); len(pc) > 0 {
		c.PodCIDRs = pc
	}
	if strings.EqualFold(np.NetworkPlugin, "azure") && strings.EqualFold(np.NetworkPluginMode, "overlay") {
		c.CNI = "azure-cni-overlay"
	}

	on, checked := e.localDNS(ctx, id)
	if on {
		c.AKSLocalDNS = true
	}
	wsNote := e.fillWorkspaces(ctx, &mc)

	parts := []string{name}
	if c.Region != "" {
		parts = append(parts, c.Region)
	}
	if c.OutboundType != "" {
		parts = append(parts, "outbound "+c.OutboundType)
	}
	switch {
	case on:
		parts = append(parts, "LocalDNS on")
	case !checked:
		parts = append(parts, "AKS LocalDNS not checked")
	}
	if wsNote != "" {
		parts = append(parts, wsNote)
	}
	return e.st("ok", strings.Join(parts, " · "))
}

func cidrs(list []string, one string) []string {
	if len(list) > 0 {
		return list
	}
	if one != "" {
		return []string{one}
	}
	return nil
}

// discover finds the managed cluster whose node resource group holds this cluster's nodes.
func (e *Enricher) discover(ctx context.Context, providerIDs []string) (string, []model.SourceStatus) {
	var sub, rg string
	for _, p := range providerIDs {
		if m := providerRE.FindStringSubmatch(p); m != nil {
			sub, rg = m[1], m[2]
			break
		}
	}
	if sub == "" {
		return "", e.st("warn", detailNotFound)
	}
	code, raw, err := e.ARM.get(ctx, "/subscriptions/"+sub+"/providers/Microsoft.ContainerService/managedClusters", apiManagedClusters)
	switch {
	case err != nil:
		return "", e.st("missing", detailFailed)
	case code == 401 || code == 403:
		return "", e.st("missing", detailForbidden)
	case code/100 != 2:
		return "", e.st("warn", detailNotFound)
	}
	var list struct {
		Value []managedCluster `json:"value"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return "", e.st("missing", detailFailed)
	}
	for _, m := range list.Value {
		if strings.EqualFold(m.Properties.NodeResourceGroup, rg) && m.ID != "" {
			return m.ID, nil
		}
	}
	return "", e.st("warn", detailNotFound)
}

// localDNS reports whether any agent pool enables AKS LocalDNS, and whether the
// check could be made (some pool carried localDNSProfile).
func (e *Enricher) localDNS(ctx context.Context, id string) (on, checked bool) {
	code, raw, err := e.ARM.get(ctx, id+"/agentPools", apiAgentPools)
	if err != nil || code/100 != 2 {
		return false, false
	}
	var list struct {
		Value []struct {
			Properties struct {
				LocalDNSProfile *struct {
					Mode string `json:"mode"`
				} `json:"localDNSProfile"`
			} `json:"properties"`
		} `json:"value"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return false, false
	}
	for _, p := range list.Value {
		l := p.Properties.LocalDNSProfile
		if l == nil || l.Mode == "" {
			continue
		}
		checked = true
		if strings.EqualFold(l.Mode, "Required") || strings.EqualFold(l.Mode, "Preferred") {
			on = true
		}
	}
	return on, checked
}

// fillWorkspaces fills empty DNS/pod workspaces from the cluster's monitoring settings.
// It never touches the firewall workspace. It returns a short note when it tried and failed.
func (e *Enricher) fillWorkspaces(ctx context.Context, mc *managedCluster) string {
	if e.Target == nil || (e.Target.DNSWorkspace != "" && e.Target.PodWorkspace != "") {
		return ""
	}
	resID := mc.Properties.AzureMonitorProfile.ContainerInsights.WorkspaceID
	if oms, ok := mc.Properties.AddonProfiles["omsagent"]; ok {
		if v := oms.Config["logAnalyticsWorkspaceResourceID"]; v != "" {
			resID = v
		}
	}
	if resID == "" {
		return ""
	}
	code, raw, err := e.ARM.get(ctx, resID, apiWorkspaces)
	var ws struct {
		Properties struct {
			CustomerID string `json:"customerId"`
		} `json:"properties"`
	}
	if err != nil || code/100 != 2 || json.Unmarshal(raw, &ws) != nil || ws.Properties.CustomerID == "" {
		return "workspace not auto-detected"
	}
	if e.Target.DNSWorkspace == "" {
		e.Target.DNSWorkspace = ws.Properties.CustomerID
	}
	if e.Target.PodWorkspace == "" {
		e.Target.PodWorkspace = ws.Properties.CustomerID
	}
	return ""
}
