package k8s

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func ctrl(kind, name string) []metav1.OwnerReference {
	t := true
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: &t}}
}

func pod(ns, name, ip string, owners []metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, OwnerReferences: owners},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{PodIP: ip},
	}
}

func TestReadDeploymentChain(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
			}},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-abc", OwnerReferences: ctrl("Deployment", "web")}},
		func() *corev1.Pod {
			p := pod("shop", "web-abc-1", "10.0.0.5", ctrl("ReplicaSet", "web-abc"))
			st := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
			p.Status.StartTime = &st
			return p
		}(),
		pod("shop", "nope", "", nil),
	)
	st, err := NewFromClients(cs, nil, "ctx1").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Workloads) != 1 || st.Workloads[0].ID() != "shop/web" || st.Workloads[0].Kind != "Deployment" ||
		st.Workloads[0].Labels["app"] != "web" {
		t.Fatalf("workloads: %+v", st.Workloads)
	}
	if len(st.Pods) != 1 {
		t.Fatalf("pods (podless IP must be skipped): %+v", st.Pods)
	}
	p := st.Pods[0]
	if p.WorkloadID != "shop/web" || p.IP != "10.0.0.5" || p.Node != "node-a" || !p.Start.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("pod: %+v", p)
	}
	if st.Cluster.Name != "ctx1" || st.Cluster.Context != "ctx1" {
		t.Fatalf("cluster: %+v", st.Cluster)
	}
	if len(st.Sources) != 1 || st.Sources[0].ID != model.SrcK8sAPI || st.Sources[0].State != "ok" ||
		st.Sources[0].Detail != "read 1 workloads, 1 pods, 0 nodes" {
		t.Fatalf("sources: %+v", st.Sources)
	}
}

func TestReadCronJobChain(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "nightly"},
			Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"job": "n"}}},
			}}},
		},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "nightly-1", OwnerReferences: ctrl("CronJob", "nightly")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "migrate"}},
		pod("ops", "nightly-1-x", "10.0.0.6", ctrl("Job", "nightly-1")),
	)
	st, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range st.Workloads {
		ids = append(ids, w.Kind+":"+w.ID())
	}
	if !reflect.DeepEqual(ids, []string{"Job:ops/migrate", "CronJob:ops/nightly"}) {
		t.Fatalf("workloads: %v", ids)
	}
	if st.Pods[0].WorkloadID != "ops/nightly" {
		t.Fatalf("pod: %+v", st.Pods[0])
	}
}

func TestReadBarePod(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("dbg", "shell", "10.0.0.7", nil))
	st, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Workloads) != 1 || st.Workloads[0].Kind != "Pod" || st.Workloads[0].ID() != "dbg/shell" {
		t.Fatalf("workloads: %+v", st.Workloads)
	}
	if st.Pods[0].WorkloadID != "dbg/shell" {
		t.Fatalf("pod: %+v", st.Pods[0])
	}
}

func TestReadHostNetworkDaemonSet(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "agent"},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"k": "agent"}},
				Spec:       corev1.PodSpec{HostNetwork: true},
			}},
		},
		pod("kube-system", "agent-1", "10.0.0.8", ctrl("DaemonSet", "agent")),
	)
	st, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w := st.Workloads[0]
	if w.Kind != "DaemonSet" || !w.HostNetwork || st.Pods[0].WorkloadID != "kube-system/agent" {
		t.Fatalf("got %+v / %+v", w, st.Pods[0])
	}
}

func TestReadNodeIPsAndPodCIDRs(t *testing.T) {
	node := func(name, pid string, cidrs []string, addrs ...corev1.NodeAddress) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.NodeSpec{ProviderID: pid, PodCIDRs: cidrs}, Status: corev1.NodeStatus{Addresses: addrs}}
	}
	cs := fake.NewSimpleClientset(
		node("n2", "prov://2", []string{"10.244.1.0/24", "fd00:1::/64"},
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.1.0.5"},
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "fd00::5"},
			corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.9"}),
		node("n1", "prov://1", []string{"10.244.0.0/24", "10.244.1.0/24"},
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.1.0.4"}),
	)
	st, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantNodes := []model.Node{{Name: "n1", IPs: []string{"10.1.0.4"}}, {Name: "n2", IPs: []string{"10.1.0.5", "fd00::5"}}}
	if !reflect.DeepEqual(st.Nodes, wantNodes) {
		t.Fatalf("nodes: %+v", st.Nodes)
	}
	if !reflect.DeepEqual(st.Cluster.PodCIDRs, []string{"10.244.0.0/24", "10.244.1.0/24", "fd00:1::/64"}) {
		t.Fatalf("cidrs: %v", st.Cluster.PodCIDRs)
	}
	if !reflect.DeepEqual(st.ProviderIDs, []string{"prov://1", "prov://2"}) {
		t.Fatalf("provider ids: %v", st.ProviderIDs)
	}
}

func TestReadNetworkPolicyMapping(t *testing.T) {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	end := int32(9000)
	np := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "egress"},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels:      map[string]string{"app": "web"},
				MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"a", "b"}}},
			},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
			Egress: []netv1.NetworkPolicyEgressRule{
				{
					To: []netv1.NetworkPolicyPeer{
						{IPBlock: &netv1.IPBlock{CIDR: "0.0.0.0/0", Except: []string{"10.0.0.0/8"}}},
						{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "x"}}},
					},
					Ports: []netv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 8000}, EndPort: &end},
						{Protocol: &udp, Port: &intstr.IntOrString{Type: intstr.String, StrVal: "dns"}},
						{Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 443}},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(np)
	st, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := model.NetworkPolicy{
		Namespace: "shop", Name: "egress",
		PodSelector: model.LabelSelector{MatchLabels: map[string]string{"app": "web"},
			MatchExpressions: []model.LabelExpr{{Key: "tier", Operator: "In", Values: []string{"a", "b"}}}},
		PolicyTypes: []string{"Egress"},
		Egress: []model.EgressRule{{
			To: []model.Peer{
				{IPBlock: &model.IPBlock{CIDR: "0.0.0.0/0", Except: []string{"10.0.0.0/8"}}},
				{NamespaceSelector: &model.LabelSelector{MatchLabels: map[string]string{"team": "x"}}},
			},
			Ports: []model.PortSpec{
				{Protocol: "TCP", Port: 8000, EndPort: 9000},
				{Protocol: "UDP", Port: 0},
				{Protocol: "TCP", Port: 443},
			},
		}},
	}
	if len(st.NetworkPolicies) != 1 || !reflect.DeepEqual(st.NetworkPolicies[0], want) {
		t.Fatalf("got %+v", st.NetworkPolicies)
	}
}

func forbid(resource string) func(k8stesting.Action) (bool, runtime.Object, error) {
	return func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", nil)
	}
}

func TestReadForbiddenOptionalKindWarns(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("a", "p", "10.0.0.1", nil))
	cs.PrependReactor("list", "cronjobs", forbid("cronjobs"))
	st, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := st.Sources[0]
	if s.State != "warn" || !strings.Contains(s.Detail, "cronjobs") {
		t.Fatalf("source: %+v", s)
	}
	if len(st.Pods) != 1 {
		t.Fatal("pods should still be read")
	}
}

func TestReadForbiddenPodsFails(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", forbid("pods"))
	_, err := NewFromClients(cs, nil, "c").Read(context.Background())
	if err == nil || err.Error() != "kubernetes: cannot list pods: forbidden (apply deploy/clusterrole.yaml)" {
		t.Fatalf("err: %v", err)
	}
}
