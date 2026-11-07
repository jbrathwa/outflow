package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jbrathwa/outflow/internal/collect"
	"github.com/jbrathwa/outflow/internal/model"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const pageSize = 500

// listAll pages through a list call; fn receives each page's continue token.
func listAll(fn func(opts metav1.ListOptions) (next string, err error)) error {
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		next, err := fn(opts)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		opts.Continue = next
	}
}

type owner struct{ kind, name string }

func controllerOf(refs []metav1.OwnerReference) (owner, bool) {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return owner{r.Kind, r.Name}, true
		}
	}
	return owner{}, false
}

// Read lists workloads, pods, nodes and NetworkPolicies. It fails only when pods or nodes
// cannot be listed; other failures become warnings in Sources.
func (s *Source) Read(ctx context.Context) (collect.ClusterState, error) {
	st := collect.ClusterState{Cluster: model.Cluster{Name: s.context, Context: s.context}}
	var warns []string
	fatal := func(kind string, err error) error {
		if apierrors.IsForbidden(err) {
			return fmt.Errorf("kubernetes: cannot list %s: forbidden (apply deploy/clusterrole.yaml)", kind)
		}
		return fmt.Errorf("kubernetes: cannot list %s: %w", kind, err)
	}
	warn := func(kind string, err error) {
		if apierrors.IsForbidden(err) {
			warns = append(warns, kind+" forbidden")
		} else {
			warns = append(warns, kind+": "+err.Error())
		}
	}

	// Nodes.
	podCIDRs := map[string]bool{}
	var rawNodes []corev1.Node
	err := listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.CoreV1().Nodes().List(ctx, o)
		if err != nil {
			return "", err
		}
		rawNodes = append(rawNodes, l.Items...)
		for _, n := range l.Items {
			node := model.Node{Name: n.Name, IPs: []string{}}
			for _, a := range n.Status.Addresses {
				if a.Type == corev1.NodeInternalIP {
					node.IPs = append(node.IPs, a.Address)
				}
			}
			sort.Strings(node.IPs)
			st.Nodes = append(st.Nodes, node)
			if n.Spec.ProviderID != "" {
				st.ProviderIDs = append(st.ProviderIDs, n.Spec.ProviderID)
			}
			for _, c := range n.Spec.PodCIDRs {
				podCIDRs[c] = true
			}
			if len(n.Spec.PodCIDRs) == 0 && n.Spec.PodCIDR != "" {
				podCIDRs[n.Spec.PodCIDR] = true
			}
		}
		return l.Continue, nil
	})
	if err != nil {
		return st, fatal("nodes", err)
	}
	for c := range podCIDRs {
		st.Cluster.PodCIDRs = append(st.Cluster.PodCIDRs, c)
	}
	sort.Strings(st.Cluster.PodCIDRs)

	// Controllers.
	workloads := map[string]model.Workload{}
	var collisions []string
	addWL := func(ns, name, kind string, labels map[string]string, hostNet bool) {
		w := model.Workload{Namespace: ns, Name: name, Kind: kind, Labels: labels, HostNetwork: hostNet}
		if w.Labels == nil {
			w.Labels = map[string]string{}
		}
		if prev, ok := workloads[w.ID()]; ok {
			if prev.Kind != kind {
				collisions = append(collisions, fmt.Sprintf("%s (%s, %s)", w.ID(), prev.Kind, kind))
			}
			return // first kind wins; kinds are listed in a fixed order
		}
		workloads[w.ID()] = w
	}
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.AppsV1().Deployments("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, d := range l.Items {
			addWL(d.Namespace, d.Name, "Deployment", d.Spec.Template.Labels, d.Spec.Template.Spec.HostNetwork)
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("deployments", err)
	}
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.AppsV1().StatefulSets("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, d := range l.Items {
			addWL(d.Namespace, d.Name, "StatefulSet", d.Spec.Template.Labels, d.Spec.Template.Spec.HostNetwork)
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("statefulsets", err)
	}
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.AppsV1().DaemonSets("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, d := range l.Items {
			addWL(d.Namespace, d.Name, "DaemonSet", d.Spec.Template.Labels, d.Spec.Template.Spec.HostNetwork)
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("daemonsets", err)
	}
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.BatchV1().CronJobs("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, d := range l.Items {
			t := d.Spec.JobTemplate.Spec.Template
			addWL(d.Namespace, d.Name, "CronJob", t.Labels, t.Spec.HostNetwork)
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("cronjobs", err)
	}

	// Jobs: standalone ones are workloads; all are kept to resolve pod ownership.
	jobOwner := map[string]owner{}
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.BatchV1().Jobs("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, j := range l.Items {
			if c, ok := controllerOf(j.OwnerReferences); ok && c.kind == "CronJob" {
				jobOwner[j.Namespace+"/"+j.Name] = c
				continue
			}
			addJob(addWL, j)
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("jobs", err)
	}

	rsOwner := map[string]owner{}
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.AppsV1().ReplicaSets("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, r := range l.Items {
			if c, ok := controllerOf(r.OwnerReferences); ok {
				rsOwner[r.Namespace+"/"+r.Name] = c
			}
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("replicasets", err)
	}

	// Pods.
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.CoreV1().Pods("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, p := range l.Items {
			if p.Status.PodIP == "" {
				continue
			}
			wid, kind := p.Namespace+"/"+p.Name, "Pod"
			if c, ok := controllerOf(p.OwnerReferences); ok {
				wid, kind = p.Namespace+"/"+c.name, c.kind
				switch c.kind {
				case "ReplicaSet":
					if up, ok := rsOwner[p.Namespace+"/"+c.name]; ok {
						wid, kind = p.Namespace+"/"+up.name, up.kind
					}
				case "Job":
					if up, ok := jobOwner[p.Namespace+"/"+c.name]; ok {
						wid, kind = p.Namespace+"/"+up.name, up.kind
					}
				}
			}
			if _, ok := workloads[wid]; !ok {
				addWL(p.Namespace, wid[len(p.Namespace)+1:], kind, p.Labels, p.Spec.HostNetwork)
			}
			pod := model.Pod{Namespace: p.Namespace, Name: p.Name, WorkloadID: wid, Node: p.Spec.NodeName, IP: p.Status.PodIP}
			if p.Status.StartTime != nil {
				pod.Start = p.Status.StartTime.Time.UTC()
			}
			st.Pods = append(st.Pods, pod)
		}
		return l.Continue, nil
	})
	if err != nil {
		return st, fatal("pods", err)
	}

	// NetworkPolicies.
	err = listAll(func(o metav1.ListOptions) (string, error) {
		l, err := s.cs.NetworkingV1().NetworkPolicies("").List(ctx, o)
		if err != nil {
			return "", err
		}
		for _, p := range l.Items {
			st.NetworkPolicies = append(st.NetworkPolicies, convertPolicy(p))
		}
		return l.Continue, nil
	})
	if err != nil {
		warn("networkpolicies", err)
	}

	for _, w := range workloads {
		st.Workloads = append(st.Workloads, w)
	}
	sort.Slice(st.Workloads, func(i, j int) bool { return st.Workloads[i].ID() < st.Workloads[j].ID() })
	sort.Slice(st.Pods, func(i, j int) bool {
		return st.Pods[i].Namespace+"/"+st.Pods[i].Name < st.Pods[j].Namespace+"/"+st.Pods[j].Name
	})
	sort.Slice(st.Nodes, func(i, j int) bool { return st.Nodes[i].Name < st.Nodes[j].Name })
	sort.Slice(st.NetworkPolicies, func(i, j int) bool {
		a, b := st.NetworkPolicies[i], st.NetworkPolicies[j]
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})
	sort.Strings(st.ProviderIDs)

	src := model.SourceStatus{ID: model.SrcK8sAPI, State: "ok",
		Detail: fmt.Sprintf("read %d workloads, %d pods, %d nodes", len(st.Workloads), len(st.Pods), len(st.Nodes))}
	if len(warns) > 0 {
		src.State = "warn"
		src.Detail += "; incomplete: " + strings.Join(warns, ", ")
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		src.State = "warn"
		src.Detail += "; name collision: " + strings.Join(collisions, ", ")
	}
	det, detSrc := detect(ctx, s.cs, s.dyn, rawNodes)
	st.Cluster.Platform, st.Cluster.Region, st.Cluster.CNI = det.Platform, det.Region, det.CNI
	st.Cluster.PolicyEngine, st.Cluster.NodeLocalDNS, st.Cluster.CalicoCRDPolicies = det.PolicyEngine, det.NodeLocalDNS, det.CalicoCRDPolicies
	st.Sources = []model.SourceStatus{src}
	for _, d := range detSrc {
		if d.ID == model.SrcK8sAPI {
			st.Sources[0].State = "warn"
			st.Sources[0].Detail += "; " + d.Detail
			continue
		}
		st.Sources = append(st.Sources, d)
	}
	return st, nil
}

func addJob(add func(ns, name, kind string, labels map[string]string, hostNet bool), j batchv1.Job) {
	add(j.Namespace, j.Name, "Job", j.Spec.Template.Labels, j.Spec.Template.Spec.HostNetwork)
}

func convertSelector(s metav1.LabelSelector) model.LabelSelector {
	out := model.LabelSelector{MatchLabels: s.MatchLabels}
	for _, e := range s.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, model.LabelExpr{Key: e.Key, Operator: string(e.Operator), Values: e.Values})
	}
	return out
}

func convertPolicy(p netv1.NetworkPolicy) model.NetworkPolicy {
	out := model.NetworkPolicy{Namespace: p.Namespace, Name: p.Name, PodSelector: convertSelector(p.Spec.PodSelector)}
	for _, t := range p.Spec.PolicyTypes {
		out.PolicyTypes = append(out.PolicyTypes, string(t))
	}
	for _, r := range p.Spec.Egress {
		er := model.EgressRule{}
		for _, t := range r.To {
			var peer model.Peer
			if t.IPBlock != nil {
				peer.IPBlock = &model.IPBlock{CIDR: t.IPBlock.CIDR, Except: t.IPBlock.Except}
			}
			if t.PodSelector != nil {
				sel := convertSelector(*t.PodSelector)
				peer.PodSelector = &sel
			}
			if t.NamespaceSelector != nil {
				sel := convertSelector(*t.NamespaceSelector)
				peer.NamespaceSelector = &sel
			}
			er.To = append(er.To, peer)
		}
		for _, pt := range r.Ports {
			ps := model.PortSpec{Protocol: "TCP"}
			if pt.Protocol != nil {
				ps.Protocol = string(*pt.Protocol)
			}
			if pt.Port != nil && pt.Port.Type == intstr.Int {
				ps.Port = int(pt.Port.IntVal)
			}
			if pt.EndPort != nil {
				ps.EndPort = int(*pt.EndPort)
			}
			er.Ports = append(er.Ports, ps)
		}
		out.Egress = append(out.Egress, er)
	}
	return out
}
