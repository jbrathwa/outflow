package collect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/analyze"
	"github.com/jbrathwa/outflow/internal/model"
)

type fakeCluster struct {
	st  ClusterState
	err error
}

func (f fakeCluster) Read(context.Context) (ClusterState, error)  { return f.st, f.err }
func (f fakeCluster) Probe(context.Context) (ClusterState, error) { return f.st, f.err }

type fakeEnricher struct{}

func (fakeEnricher) Enrich(_ context.Context, st *ClusterState) []model.SourceStatus {
	st.Cluster.Region = "eastus"
	return []model.SourceStatus{{ID: model.SrcAKS, State: "ok"}}
}

type fakePods struct{ pods []model.Pod }

func (f fakePods) PodHistory(_ context.Context, sc Scope) ([]model.Pod, model.SourceStatus) {
	return f.pods, model.SourceStatus{ID: model.SrcPodInventory, State: "ok", Detail: strings.Join(sc.NodeNames, ",")}
}
func (f fakePods) Probe(context.Context, Scope) model.SourceStatus {
	return model.SourceStatus{ID: model.SrcPodInventory, State: "ok", Detail: "probe"}
}

type fakeDNS struct{ l []model.DNSLookup }

func (f fakeDNS) Lookups(context.Context, Scope) ([]model.DNSLookup, []model.SourceStatus) {
	return f.l, []model.SourceStatus{{ID: model.SrcCoreDNSCollected, State: "ok"}, {ID: model.SrcCoreDNSFormat, State: "ok"}}
}
func (f fakeDNS) Probe(context.Context, Scope) []model.SourceStatus {
	return []model.SourceStatus{{ID: model.SrcCoreDNSCollected, State: "ok", Detail: "probe"}}
}

type fakeEgress struct {
	ev  []model.EgressEvent
	ips []string
}

func (f *fakeEgress) Events(_ context.Context, sc Scope) ([]model.EgressEvent, string, model.SourceStatus) {
	f.ips = sc.NodeIPs
	return f.ev, "fw-hub-eus", model.SourceStatus{ID: model.SrcFirewall, State: "ok"}
}
func (f *fakeEgress) Probe(context.Context, Scope) (string, model.SourceStatus) {
	return "fw-hub-eus", model.SourceStatus{ID: model.SrcFirewall, State: "ok", Detail: "probe"}
}

type fakeResolver struct {
	calls   int
	domains []string
}

func (f *fakeResolver) Resolve(_ context.Context, d []string) []model.Resolution {
	f.calls++
	f.domains = d
	return []model.Resolution{{Domain: d[0], IPs: []string{"203.0.113.9"}, Source: "outflow"}}
}

var t0 = time.Date(2026, 11, 7, 10, 30, 45, 0, time.UTC)

func baseState() ClusterState {
	return ClusterState{
		Cluster:   model.Cluster{Name: "aks-prod-eus"},
		Workloads: []model.Workload{{Namespace: "app", Name: "web", Kind: "Deployment"}},
		Pods:      []model.Pod{{Namespace: "app", Name: "web-1", IP: "10.0.0.5"}},
		Nodes:     []model.Node{{Name: "node-a", IPs: []string{"10.1.0.4", "10.1.0.5"}}},
		Sources:   []model.SourceStatus{{ID: model.SrcK8sAPI, State: "ok"}},
	}
}

func TestCollectAssemblesSnapshot(t *testing.T) {
	eg := &fakeEgress{ev: []model.EgressEvent{{Domain: "Api.Example.", Count: 1}, {Domain: "nodot"}, {}}}
	res := &fakeResolver{}
	c := Collector{
		Cluster: fakeCluster{st: baseState()},
		Platform: func(model.Cluster, []string) (Platform, error) {
			return Platform{
				Name:     "azure",
				Enricher: fakeEnricher{},
				Pods: fakePods{pods: []model.Pod{
					{Namespace: "app", Name: "web-1", IP: "10.0.0.5"},
					{Namespace: "app", Name: "web-0", IP: "10.0.0.4"},
				}},
				DNS:    fakeDNS{l: []model.DNSLookup{{PodIP: "10.0.0.5", Domain: "b.example", Count: 1}, {Domain: "API.example"}}},
				Egress: eg,
				Final:  func() []model.SourceStatus { return []model.SourceStatus{{ID: model.SrcAzureAccess, State: "ok"}} },
			}, nil
		},
		Resolver: res,
		Now:      func() time.Time { return t0 },
	}
	snap, err := c.Collect(context.Background(), Options{Since: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if snap.SchemaVersion != model.SchemaVersion {
		t.Errorf("schema %d", snap.SchemaVersion)
	}
	wantEnd := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	if !snap.Window.End.Equal(wantEnd) || !snap.Window.Start.Equal(wantEnd.Add(-time.Hour)) {
		t.Errorf("window %v", snap.Window)
	}
	if len(snap.Pods) != 2 || snap.Pods[0].Name != "web-1" || snap.Pods[1].Name != "web-0" {
		t.Errorf("pods %+v", snap.Pods)
	}
	if len(snap.DNSLookups) != 2 || len(snap.EgressEvents) != 3 {
		t.Errorf("lookups/events %d %d", len(snap.DNSLookups), len(snap.EgressEvents))
	}
	if snap.Cluster.Firewall != "fw-hub-eus" || snap.Cluster.Region != "eastus" {
		t.Errorf("cluster %+v", snap.Cluster)
	}
	if got := strings.Join(res.domains, ","); got != "api.example,b.example" {
		t.Errorf("resolver domains %q", got)
	}
	if len(snap.Resolutions) != 1 {
		t.Errorf("resolutions %+v", snap.Resolutions)
	}
	if strings.Join(eg.ips, ",") != "10.1.0.4,10.1.0.5" {
		t.Errorf("scope ips %v", eg.ips)
	}
	var ids []string
	for _, s := range snap.Sources {
		ids = append(ids, s.ID)
	}
	want := "k8s-api,aks-resource,pod-inventory,coredns-logs-collected,coredns-log-format,firewall-logs,azure-access"
	if strings.Join(ids, ",") != want {
		t.Errorf("status order %v", ids)
	}
}

func TestCollectNilMembersAreMissing(t *testing.T) {
	c := Collector{
		Cluster:  fakeCluster{st: baseState()},
		Platform: func(model.Cluster, []string) (Platform, error) { return Platform{Name: "generic"}, nil },
		Now:      func() time.Time { return t0 },
	}
	snap, err := c.Collect(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.SourceStatus{}
	for _, s := range snap.Sources {
		got[s.ID] = s
	}
	for _, id := range []string{model.SrcFirewall, model.SrcCoreDNSCollected, model.SrcPodInventory} {
		if s := got[id]; s.State != "missing" || s.Detail != "not available in profile generic" {
			t.Errorf("%s = %+v", id, s)
		}
	}
	if snap.Window.End.Sub(snap.Window.Start) != 24*time.Hour {
		t.Errorf("default window %v", snap.Window)
	}
	if len(snap.Pods) != 1 {
		t.Errorf("pods %+v", snap.Pods)
	}
}

func TestCollectNilPlatformFuncIsGeneric(t *testing.T) {
	c := Collector{Cluster: fakeCluster{st: baseState()}, Now: func() time.Time { return t0 }}
	snap, err := c.Collect(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range snap.Sources {
		if s.ID == model.SrcFirewall && strings.HasSuffix(s.Detail, "generic") {
			found = true
		}
	}
	if !found {
		t.Errorf("sources %+v", snap.Sources)
	}
}

func TestCollectClusterErrorIsFatal(t *testing.T) {
	boom := errors.New("boom")
	c := Collector{Cluster: fakeCluster{err: boom}}
	if _, err := c.Collect(context.Background(), Options{}); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
	if _, err := c.Probe(context.Background(), Options{}); !errors.Is(err, boom) {
		t.Errorf("probe err %v", err)
	}
}

func TestCollectPlatformErrorIsWrapped(t *testing.T) {
	boom := errors.New("no platform")
	c := Collector{
		Cluster:  fakeCluster{st: baseState()},
		Platform: func(model.Cluster, []string) (Platform, error) { return Platform{}, boom },
	}
	if _, err := c.Collect(context.Background(), Options{}); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
}

func TestProbeSkipsResolution(t *testing.T) {
	res := &fakeResolver{}
	c := Collector{
		Cluster: fakeCluster{st: baseState()},
		Platform: func(model.Cluster, []string) (Platform, error) {
			return Platform{Name: "azure", Pods: fakePods{}, DNS: fakeDNS{}, Egress: &fakeEgress{}}, nil
		},
		Resolver: res,
		Now:      func() time.Time { return t0 },
	}
	snap, err := c.Probe(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.calls != 0 {
		t.Errorf("resolver called %d times", res.calls)
	}
	if snap.Cluster.Firewall != "fw-hub-eus" {
		t.Errorf("firewall %q", snap.Cluster.Firewall)
	}
	for _, s := range snap.Sources {
		if s.ID == model.SrcFirewall && s.Detail != "probe" {
			t.Errorf("probe not used: %+v", s)
		}
	}
}

func TestMergePodsLiveWins(t *testing.T) {
	live := []model.Pod{{Namespace: "a", Name: "p", IP: "10.0.0.1", WorkloadID: "live"}}
	hist := []model.Pod{
		{Namespace: "a", Name: "p", IP: "10.0.0.1", WorkloadID: "hist"},
		{Namespace: "a", Name: "p", IP: "10.0.0.2"},
	}
	got := mergePods(live, hist)
	if len(got) != 2 || got[0].WorkloadID != "live" || got[1].IP != "10.0.0.2" {
		t.Errorf("%+v", got)
	}
}

func TestMergeLiveWins(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	live := []model.Pod{{Namespace: "shop", Name: "api-1", IP: "10.244.1.9", WorkloadID: "shop/api", Start: t0}}
	hist := []model.Pod{
		{Namespace: "shop", Name: "api-1", IP: "10.244.1.9", WorkloadID: "shop/stale", Start: t0.Add(-time.Hour), End: t0.Add(time.Minute)},
		{Namespace: "shop", Name: "old-1", IP: "10.244.1.8", WorkloadID: "shop/old", Start: t0.Add(-time.Hour), End: t0},
	}
	got := mergePods(live, hist)
	if len(got) != 2 || got[0].WorkloadID != "shop/api" || !got[0].End.IsZero() || got[1].Name != "old-1" {
		t.Fatalf("%+v", got)
	}
}

func TestMergeReusedIPByTime(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 11, 7, h, m, 0, 0, time.UTC) }
	live := []model.Pod{{Namespace: "shop", Name: "b-1", IP: "10.244.1.9", WorkloadID: "shop/b", Start: day(10, 40)}}
	hist := []model.Pod{{Namespace: "shop", Name: "a-1", IP: "10.244.1.9", WorkloadID: "shop/a", Start: day(10, 0), End: day(10, 30)}}
	snap := model.Snapshot{
		Pods: mergePods(live, hist),
		DNSLookups: []model.DNSLookup{
			{PodIP: "10.244.1.9", Domain: "api.example.com", Bin: day(10, 20), Count: 1},
			{PodIP: "10.244.1.9", Domain: "api.example.com", Bin: day(10, 45), Count: 1},
		},
	}
	subjects := map[string]bool{}
	for _, o := range analyze.Attribute(snap, analyze.DefaultJoinWindow) {
		subjects[o.Subject] = true
	}
	if len(subjects) != 2 || !subjects["shop/a"] || !subjects["shop/b"] {
		t.Fatalf("%v", subjects)
	}
}
