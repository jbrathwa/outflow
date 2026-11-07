package k8s

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jbrathwa/outflow/internal/model"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

func TestClusterRoleMatchesRequiredPermissions(t *testing.T) {
	data, err := os.ReadFile("../../../deploy/clusterrole.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cr rbacv1.ClusterRole
	if err := yaml.UnmarshalStrict(data, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Kind != "ClusterRole" || cr.Name != "outflow-reader" {
		t.Fatalf("got %s/%s", cr.Kind, cr.Name)
	}
	got := map[string]bool{}
	for _, r := range cr.Rules {
		for _, v := range r.Verbs {
			if v != "get" && v != "list" && v != "watch" {
				t.Errorf("forbidden verb %q", v)
			}
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					names := r.ResourceNames
					if len(names) == 0 {
						names = []string{""}
					}
					for _, n := range names {
						got[fmt.Sprintf("%s|%s|%s|%s", g, res, v, n)] = true
					}
				}
			}
		}
	}
	want := map[string]bool{}
	for _, p := range RequiredPermissions {
		want[fmt.Sprintf("%s|%s|%s|%s", p.Group, p.Resource, p.Verb, p.Name)] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("ClusterRole lacks %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("ClusterRole has extra %s", k)
		}
	}
}

// denyingClient answers every SelfSubjectAccessReview; deny reports whether a request is denied.
func denyingClient(deny func(a *authv1.ResourceAttributes) bool) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(act k8stesting.Action) (bool, runtime.Object, error) {
		r := act.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		r.Status.Allowed = !deny(r.Spec.ResourceAttributes)
		return true, r, nil
	})
	return cs
}

func TestAccessAllGranted(t *testing.T) {
	cs := denyingClient(func(*authv1.ResourceAttributes) bool { return false })
	st := NewFromClients(cs, nil, "c").checkAccess(context.Background())
	want := fmt.Sprintf("all %d permissions granted", len(RequiredPermissions))
	if st.ID != model.SrcK8sAccess || st.State != "ok" || st.Detail != want {
		t.Fatalf("%+v", st)
	}
}

func TestAccessMissingRequired(t *testing.T) {
	cs := denyingClient(func(a *authv1.ResourceAttributes) bool {
		return (a.Resource == "cronjobs" && a.Verb == "list") || a.Resource == "globalnetworkpolicies"
	})
	st := NewFromClients(cs, nil, "c").checkAccess(context.Background())
	if st.State != "missing" || !strings.Contains(st.Detail, "list cronjobs.batch") ||
		!strings.Contains(st.Detail, "apply deploy/clusterrole.yaml and bind it to your identity") {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(strings.SplitN(st.Detail, "; apply", 2)[0], "globalnetworkpolicies") {
		t.Fatalf("optional listed among required: %q", st.Detail)
	}
}

func TestAccessOnlyOptionalMissing(t *testing.T) {
	cs := denyingClient(func(a *authv1.ResourceAttributes) bool {
		return a.Group == "crd.projectcalico.org" || a.Resource == "configmaps"
	})
	st := NewFromClients(cs, nil, "c").checkAccess(context.Background())
	if st.State != "warn" || !strings.Contains(st.Detail, "get configmaps (kube-system/coredns-custom)") ||
		!strings.Contains(st.Detail, "get globalnetworkpolicies.crd.projectcalico.org") {
		t.Fatalf("%+v", st)
	}
}

func TestAccessReviewError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("boom")
	})
	st := NewFromClients(cs, nil, "c").checkAccess(context.Background())
	if st.State == "ok" || !strings.Contains(st.Detail, fmt.Sprintf("not checked: %d permissions (30 required)", len(RequiredPermissions))) {
		t.Fatalf("%+v", st)
	}
}

func TestProbe(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Spec:       corev1.NodeSpec{ProviderID: "kind://docker/x/n1", PodCIDRs: []string{"10.244.0.0/24"}},
	})
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(act k8stesting.Action) (bool, runtime.Object, error) {
		r := act.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		r.Status.Allowed = true
		return true, r, nil
	})
	st, err := NewFromClients(cs, nil, "ctx1").Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Cluster.Name != "ctx1" || len(st.Nodes) != 1 || len(st.ProviderIDs) != 1 || len(st.Cluster.PodCIDRs) != 1 {
		t.Fatalf("%+v", st)
	}
	var ids []string
	for _, s := range st.Sources {
		ids = append(ids, s.ID+":"+s.State)
	}
	sort.Strings(ids)
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "k8s-api:ok") || !strings.Contains(joined, "k8s-access:ok") {
		t.Fatalf("sources %v", ids)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() != "list" && a.GetVerb() != "get" && a.GetVerb() != "create" {
			t.Errorf("verb %s", a.GetVerb())
		}
		if a.GetResource().Resource == "pods" {
			t.Errorf("Probe touched pods")
		}
	}
}

func TestProbeNodesForbidden(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", fmt.Errorf("no"))
	})
	_, err := NewFromClients(cs, nil, "c").Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot list nodes: forbidden") {
		t.Fatalf("err %v", err)
	}
}
