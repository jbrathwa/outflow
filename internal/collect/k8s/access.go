package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ collect.ClusterSource = (*Source)(nil)

// Permission is one verb on one resource, optionally scoped to a namespace and object name.
type Permission struct {
	Group, Resource, Verb, Namespace, Name string
	Optional                               bool
}

// String renders "<verb> <resource>[.<group>]" plus " (<ns>/<name>)" for named objects.
func (p Permission) String() string {
	s := p.Verb + " " + p.Resource
	if p.Group != "" {
		s += "." + p.Group
	}
	if p.Name != "" {
		s += " (" + p.Namespace + "/" + p.Name + ")"
	}
	return s
}

// RequiredPermissions is the single source of truth for what outflow reads; deploy/clusterrole.yaml
// must grant exactly this (a test enforces it).
var RequiredPermissions = buildPermissions()

func buildPermissions() []Permission {
	var out []Permission
	add := func(group string, optional bool, resources ...string) {
		for _, r := range resources {
			for _, v := range []string{"get", "list", "watch"} {
				out = append(out, Permission{Group: group, Resource: r, Verb: v, Optional: optional})
			}
		}
	}
	add("", false, "pods", "nodes", "namespaces")
	add("apps", false, "replicasets", "deployments", "statefulsets", "daemonsets")
	add("batch", false, "jobs", "cronjobs")
	add("networking.k8s.io", false, "networkpolicies")
	add("crd.projectcalico.org", true, "networkpolicies", "globalnetworkpolicies")
	for _, n := range []string{"coredns-custom", "coredns"} {
		out = append(out, Permission{Resource: "configmaps", Verb: "get", Namespace: "kube-system", Name: n, Optional: true})
	}
	return out
}

// checkAccess asks the API server (SelfSubjectAccessReview, about the caller only) whether each
// required permission is granted.
func (s *Source) checkAccess(ctx context.Context) model.SourceStatus {
	var missing, optMissing, unchecked []string
	uncheckedReq := 0
	for _, p := range RequiredPermissions {
		review := &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Group: p.Group, Resource: p.Resource, Verb: p.Verb, Namespace: p.Namespace, Name: p.Name,
			},
		}}
		res, err := s.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		switch {
		case err != nil:
			unchecked = append(unchecked, p.String())
			if !p.Optional {
				uncheckedReq++
			}
		case res.Status.Allowed:
		case p.Optional:
			optMissing = append(optMissing, p.String())
		default:
			missing = append(missing, p.String())
		}
	}
	st := model.SourceStatus{ID: model.SrcK8sAccess}
	var notes []string
	switch {
	case len(missing) > 0:
		st.State = "missing"
		notes = append(notes, "missing: "+strings.Join(missing, ", ")+"; apply deploy/clusterrole.yaml and bind it to your identity")
	case len(optMissing) > 0 || len(unchecked) > 0:
		st.State = "warn"
	default:
		st.State = "ok"
		st.Detail = fmt.Sprintf("all %d permissions granted", len(RequiredPermissions))
		return st
	}
	if len(optMissing) > 0 {
		notes = append(notes, "optional missing: "+strings.Join(optMissing, ", "))
	}
	if len(unchecked) > 0 {
		notes = append(notes, fmt.Sprintf("not checked: %d permissions (%d required)", len(unchecked), uncheckedReq))
	}
	st.Detail = strings.Join(notes, "; ")
	return st
}

// Probe lists nodes (needed for detection and cluster facts), runs detection and the access
// check. It does not list pods, workloads or policies.
func (s *Source) Probe(ctx context.Context) (collect.ClusterState, error) {
	st := collect.ClusterState{Cluster: model.Cluster{Name: s.context, Context: s.context}}
	var rawNodes []corev1.Node
	err := listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.CoreV1().Nodes().List(ctx, o)
		if err != nil {
			return "", err
		}
		rawNodes = append(rawNodes, l.Items...)
		return l.Continue, nil
	})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return st, fmt.Errorf("kubernetes: cannot list nodes: forbidden (apply deploy/clusterrole.yaml)")
		}
		return st, fmt.Errorf("kubernetes: cannot list nodes: %w", err)
	}
	applyNodes(&st, rawNodes)

	det, detSrc := detect(ctx, s.cs, s.dyn, rawNodes)
	st.Cluster.Platform, st.Cluster.Region, st.Cluster.CNI = det.Platform, det.Region, det.CNI
	st.Cluster.PolicyEngine, st.Cluster.NodeLocalDNS, st.Cluster.CalicoCRDPolicies = det.PolicyEngine, det.NodeLocalDNS, det.CalicoCRDPolicies
	st.Sources = []model.SourceStatus{{ID: model.SrcK8sAPI, State: "ok", Detail: fmt.Sprintf("read %d nodes", len(st.Nodes))}}
	st.Sources = mergeDetect(st.Sources, detSrc)
	st.Sources = append(st.Sources, s.checkAccess(ctx))
	return st, nil
}
