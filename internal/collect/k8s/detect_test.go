package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func aksNode(extra map[string]string) corev1.Node {
	labels := map[string]string{
		"kubernetes.azure.com/cluster":  "MC_rg_aks-prod-eus_eastus",
		"topology.kubernetes.io/region": "eastus",
	}
	for k, v := range extra {
		labels[k] = v
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: "azure:///subscriptions/00000000/virtualMachineScaleSets/x/virtualMachines/0"},
	}
}

func ds(ns, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func cm(ns, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: data}
}

var calicoGVRs = map[schema.GroupVersionResource]string{
	{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"}:       "NetworkPolicyList",
	{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}: "GlobalNetworkPolicyList",
}

func calicoObj(kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion("crd.projectcalico.org/v1")
	u.SetKind(kind)
	u.SetName(name)
	if ns != "" {
		u.SetNamespace(ns)
	}
	return u
}

func srcByID(ss []model.SourceStatus, id string) (model.SourceStatus, bool) {
	for _, s := range ss {
		if s.ID == id {
			return s, true
		}
	}
	return model.SourceStatus{}, false
}

func TestDetectAKSOverlay(t *testing.T) {
	n := aksNode(map[string]string{"kubernetes.azure.com/azure-cni-overlay": "true", "kubernetes.azure.com/network-plugin": "azure"})
	c, _ := detect(context.Background(), fake.NewSimpleClientset(), nil, []corev1.Node{n})
	if c.Platform != "AKS" || c.Region != "eastus" || c.CNI != "azure-cni-overlay" {
		t.Fatalf("got %+v", c)
	}
}

func TestDetectAKSAzureAndByo(t *testing.T) {
	c, _ := detect(context.Background(), fake.NewSimpleClientset(), nil, []corev1.Node{aksNode(map[string]string{"kubernetes.azure.com/network-plugin": "azure"})})
	if c.CNI != "azure" {
		t.Fatalf("got %q", c.CNI)
	}
	c, _ = detect(context.Background(), fake.NewSimpleClientset(), nil, []corev1.Node{aksNode(map[string]string{"kubernetes.azure.com/network-plugin": "none"})})
	if c.CNI != "byo" {
		t.Fatalf("got %q", c.CNI)
	}
}

func TestDetectKubenet(t *testing.T) {
	c, _ := detect(context.Background(), fake.NewSimpleClientset(), nil, []corev1.Node{aksNode(map[string]string{"kubernetes.azure.com/network-plugin": "kubenet"})})
	if c.Platform != "AKS" || c.CNI != "kubenet" {
		t.Fatalf("got %+v", c)
	}
}

func TestDetectNotAKS(t *testing.T) {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n", Labels: map[string]string{"kubernetes.azure.com/cluster": "x"}},
		Spec: corev1.NodeSpec{ProviderID: "kind://docker/kind/n"}}
	c, _ := detect(context.Background(), fake.NewSimpleClientset(), nil, []corev1.Node{n})
	if c.Platform != "" {
		t.Fatalf("got %q", c.Platform)
	}
}

func TestDetectKindnet(t *testing.T) {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}, Spec: corev1.NodeSpec{ProviderID: "kind://docker/kind/n"}}
	c, _ := detect(context.Background(), fake.NewSimpleClientset(ds("kube-system", "kindnet")), nil, []corev1.Node{n})
	if c.CNI != "kindnet" || c.Platform != "" || c.PolicyEngine != "" {
		t.Fatalf("got %+v", c)
	}
}

func TestDetectCalicoAndCRDCount(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), calicoGVRs,
		calicoObj("NetworkPolicy", "shop", "a"), calicoObj("NetworkPolicy", "shop", "b"), calicoObj("GlobalNetworkPolicy", "", "g"))
	cs := fake.NewSimpleClientset(ds("calico-system", "calico-node"))
	c, _ := detect(context.Background(), cs, dyn, []corev1.Node{aksNode(nil)})
	if c.PolicyEngine != "calico" || c.CalicoCRDPolicies != 3 {
		t.Fatalf("got %+v", c)
	}
}

func TestDetectPolicyEngines(t *testing.T) {
	for name, want := range map[string]string{"cilium": "cilium", "azure-npm": "azure-npm"} {
		c, _ := detect(context.Background(), fake.NewSimpleClientset(ds("kube-system", name)), nil, nil)
		if c.PolicyEngine != want {
			t.Fatalf("%s: got %q", name, c.PolicyEngine)
		}
	}
}

func TestDetectCalicoCRDAbsent(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), calicoGVRs)
	dyn.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "crd.projectcalico.org", Resource: "networkpolicies"}, "")
	})
	c, ss := detect(context.Background(), fake.NewSimpleClientset(), dyn, nil)
	if c.CalicoCRDPolicies != 0 {
		t.Fatalf("got %d", c.CalicoCRDPolicies)
	}
	if s, ok := srcByID(ss, model.SrcK8sAPI); ok && strings.Contains(s.Detail, "not readable") {
		t.Fatalf("unexpected note %+v", s)
	}
}

func TestDetectCalicoCRDForbidden(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), calicoGVRs)
	dyn.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "networkpolicies"}, "", nil)
	})
	c, ss := detect(context.Background(), fake.NewSimpleClientset(), dyn, nil)
	s, ok := srcByID(ss, model.SrcK8sAPI)
	if c.CalicoCRDPolicies != 0 || !ok || !strings.Contains(s.Detail, "Calico policies not readable") {
		t.Fatalf("got %d %+v", c.CalicoCRDPolicies, ss)
	}
}

func TestDetectNodeLocalDNS(t *testing.T) {
	c, _ := detect(context.Background(), fake.NewSimpleClientset(ds("kube-system", "node-local-dns")), nil, nil)
	if !c.NodeLocalDNS {
		t.Fatal("want NodeLocalDNS")
	}
}

func plugin(t *testing.T, cs *fake.Clientset, nodes []corev1.Node) model.SourceStatus {
	t.Helper()
	_, ss := detect(context.Background(), cs, nil, nodes)
	s, ok := srcByID(ss, model.SrcCoreDNSPlugin)
	if !ok {
		t.Fatalf("no plugin status in %+v", ss)
	}
	return s
}

func TestCoreDNSLogPluginOK(t *testing.T) {
	s := plugin(t, fake.NewSimpleClientset(cm("kube-system", "coredns-custom", map[string]string{"log.override": "log\n"})), []corev1.Node{aksNode(nil)})
	if s.State != "ok" || s.Detail != "log plugin on (coredns-custom)" {
		t.Fatalf("got %+v", s)
	}
}

func TestCoreDNSLogPluginForms(t *testing.T) {
	for _, v := range []string{"  log {\n class all\n}\n", "log . {combined}\n", "example.org:53 {\n  log\n}\n"} {
		s := plugin(t, fake.NewSimpleClientset(cm("kube-system", "coredns-custom", map[string]string{"x.server": v})), []corev1.Node{aksNode(nil)})
		if s.State != "ok" {
			t.Fatalf("%q: %+v", v, s)
		}
	}
}

func TestCoreDNSLogPluginCommentedOut(t *testing.T) {
	s := plugin(t, fake.NewSimpleClientset(cm("kube-system", "coredns-custom", map[string]string{"log.override": "# log\n"})), []corev1.Node{aksNode(nil)})
	if s.State != "missing" || s.Detail != "coredns-custom has no log plugin" {
		t.Fatalf("got %+v", s)
	}
}

func TestCoreDNSCustomMissing(t *testing.T) {
	s := plugin(t, fake.NewSimpleClientset(), []corev1.Node{aksNode(nil)})
	if s.State != "missing" || s.Detail != "coredns-custom ConfigMap not found" {
		t.Fatalf("got %+v", s)
	}
}

func TestCoreDNSCustomForbidden(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "coredns-custom", nil)
	})
	s := plugin(t, cs, []corev1.Node{aksNode(nil)})
	if s.State != "warn" || s.Detail != "not readable" {
		t.Fatalf("got %+v", s)
	}
}

func TestCoreDNSCorefileFallback(t *testing.T) {
	cs := fake.NewSimpleClientset(cm("kube-system", "coredns", map[string]string{"Corefile": ".:53 {\n    errors\n    log\n    forward . /etc/resolv.conf\n}\n"}))
	s := plugin(t, cs, nil)
	if s.State != "ok" || s.Detail != "log plugin on (coredns Corefile)" {
		t.Fatalf("got %+v", s)
	}
}

func TestCoreDNSCorefileNotUsedOnAKS(t *testing.T) {
	cs := fake.NewSimpleClientset(cm("kube-system", "coredns", map[string]string{"Corefile": ".:53 {\n log\n}\n"}))
	s := plugin(t, cs, []corev1.Node{aksNode(nil)})
	if s.State != "missing" {
		t.Fatalf("got %+v", s)
	}
}

func TestDetectCalicoCRDServerErrorFlagged(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), calicoGVRs)
	dyn.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("secret backend detail"))
	})
	_, ss := detect(context.Background(), fake.NewSimpleClientset(), dyn, nil)
	s, ok := srcByID(ss, model.SrcK8sAPI)
	if !ok || s.State != "warn" || s.Detail != "Calico policies not readable" || strings.Contains(s.Detail, "secret") {
		t.Fatalf("got %+v", ss)
	}
}

func TestDetectCalicoPartialFlagged(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), calicoGVRs, calicoObj("NetworkPolicy", "shop", "a"))
	dyn.PrependReactor("list", "globalnetworkpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTimeoutError("slow", 1)
	})
	c, ss := detect(context.Background(), fake.NewSimpleClientset(), dyn, nil)
	if _, ok := srcByID(ss, model.SrcK8sAPI); !ok || c.CalicoCRDPolicies != 1 {
		t.Fatalf("got %d %+v", c.CalicoCRDPolicies, ss)
	}
}

func TestDetectDaemonSetForbiddenNoted(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "daemonsets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "daemonsets"}, "x", nil)
	})
	c, ss := detect(context.Background(), cs, nil, nil)
	s, ok := srcByID(ss, model.SrcK8sAPI)
	if c.PolicyEngine != "" || c.CNI != "" || c.NodeLocalDNS || !ok || s.State != "warn" ||
		!strings.Contains(s.Detail, "DaemonSet check failed: calico-node") || !strings.Contains(s.Detail, "DaemonSet check failed: node-local-dns") {
		t.Fatalf("got %+v %+v", c, ss)
	}
	if strings.Count(s.Detail, "DaemonSet check failed: calico-node") != 1 {
		t.Fatalf("duplicate note: %q", s.Detail)
	}
}

func TestReadMergesDetectNote(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), calicoGVRs)
	dyn.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "networkpolicies"}, "", nil)
	})
	st, err := NewFromClients(fake.NewSimpleClientset(), dyn, "ctx1").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, _ := srcByID(st.Sources, model.SrcK8sAPI)
	if s.State != "warn" || !strings.HasSuffix(s.Detail, "; Calico policies not readable") {
		t.Fatalf("got %+v", st.Sources)
	}
	if n := len(st.Sources); n != 2 {
		t.Fatalf("want k8s-api + coredns plugin, got %+v", st.Sources)
	}
}

func TestCoreDNSCorefileFallbackDetails(t *testing.T) {
	s := plugin(t, fake.NewSimpleClientset(cm("kube-system", "coredns", map[string]string{"Corefile": ".:53 {\n errors\n}\n"})), nil)
	if s.State != "missing" || s.Detail != "coredns-custom ConfigMap not found; Corefile has no log plugin" {
		t.Fatalf("got %+v", s)
	}
	s = plugin(t, fake.NewSimpleClientset(), nil)
	if s.Detail != "coredns-custom ConfigMap not found; Corefile not readable" {
		t.Fatalf("got %+v", s)
	}
}
