package k8s

import (
	"context"
	"strings"

	"github.com/jbrathwa/outflow/internal/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var calicoCRDs = []schema.GroupVersionResource{
	{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"},
	{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"},
}

// detect infers platform, CNI, policy engine and DNS setup. It returns the cluster fields it
// found plus source statuses: coredns-log-plugin, and (only when Calico CRDs are forbidden) a
// k8s-api entry whose Detail is a note the caller appends to its own k8s-api detail.
// dyn may be nil, which skips the Calico CRD count. Get/list calls only.
func detect(ctx context.Context, cs kubernetes.Interface, dyn dynamic.Interface, nodes []corev1.Node) (model.Cluster, []model.SourceStatus) {
	var c model.Cluster
	var out []model.SourceStatus

	var aksNode *corev1.Node
	for i := range nodes {
		n := &nodes[i]
		if strings.HasPrefix(n.Spec.ProviderID, "azure://") {
			if _, ok := n.Labels["kubernetes.azure.com/cluster"]; ok {
				aksNode = n
				break
			}
		}
	}
	if aksNode != nil {
		c.Platform = "AKS"
		c.Region = aksNode.Labels["topology.kubernetes.io/region"]
	}

	var notes []string
	addNote := func(n string) {
		for _, e := range notes {
			if e == n {
				return
			}
		}
		notes = append(notes, n)
	}
	// hasDS is true only when the DaemonSet exists. NotFound means absent; any other error
	// (forbidden, timeout, 5xx) is reported as a note and the answer is "not detected".
	hasDS := func(name string, namespaces ...string) bool {
		failed := false
		for _, ns := range namespaces {
			_, err := cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
			if err == nil {
				return true
			}
			if !apierrors.IsNotFound(err) {
				failed = true
			}
		}
		if failed {
			addNote("DaemonSet check failed: " + name)
		}
		return false
	}

	// CNI.
	if aksNode != nil {
		l := aksNode.Labels
		switch {
		case l["kubernetes.azure.com/azure-cni-overlay"] == "true":
			c.CNI = "azure-cni-overlay"
		case l["kubernetes.azure.com/network-plugin"] == "azure":
			c.CNI = "azure"
		case l["kubernetes.azure.com/network-plugin"] == "kubenet":
			c.CNI = "kubenet"
		case l["kubernetes.azure.com/network-plugin"] == "none":
			c.CNI = "byo"
		}
	}
	if c.CNI == "" {
		switch {
		case hasDS("cilium", "kube-system"):
			c.CNI = "cilium"
		case hasDS("calico-node", "kube-system"):
			c.CNI = "calico"
		case hasDS("kindnet", "kube-system"):
			c.CNI = "kindnet"
		}
	}

	// Policy engine.
	switch {
	case hasDS("calico-node", "calico-system", "kube-system"):
		c.PolicyEngine = "calico"
	case hasDS("cilium", "kube-system"):
		c.PolicyEngine = "cilium"
	case hasDS("azure-npm", "kube-system"):
		c.PolicyEngine = "azure-npm"
	}

	c.NodeLocalDNS = hasDS("node-local-dns", "kube-system")

	// Calico CRD policies.
	if dyn != nil {
		failed := false
		for _, gvr := range calicoCRDs {
			err := listAll(func(o metav1.ListOptions) (string, error) {
				l, err := dyn.Resource(gvr).List(ctx, o)
				if err != nil {
					return "", err
				}
				c.CalicoCRDPolicies += len(l.Items)
				return l.GetContinue(), nil
			})
			// Only NotFound (CRD absent) is silent; anything else may hide policies.
			if err != nil && !apierrors.IsNotFound(err) {
				failed = true
			}
		}
		if failed {
			addNote("Calico policies not readable")
		}
	}
	if len(notes) > 0 {
		out = append(out, model.SourceStatus{ID: model.SrcK8sAPI, State: "warn", Detail: strings.Join(notes, "; ")})
	}

	out = append(out, corednsPlugin(ctx, cs, aksNode != nil))
	return c, out
}

// hasLogLine reports whether text has a line whose first field is `log` (comments ignored).
func hasLogLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if f := strings.Fields(line); len(f) > 0 && f[0] == "log" {
			return true
		}
	}
	return false
}

func corednsPlugin(ctx context.Context, cs kubernetes.Interface, aks bool) model.SourceStatus {
	st := model.SourceStatus{ID: model.SrcCoreDNSPlugin}
	custom, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns-custom", metav1.GetOptions{})
	switch {
	case err == nil:
		for k, v := range custom.Data {
			if (strings.HasSuffix(k, ".override") || strings.HasSuffix(k, ".server")) && hasLogLine(v) {
				st.State, st.Detail = "ok", "log plugin on (coredns-custom)"
				return st
			}
		}
		st.State, st.Detail = "missing", "coredns-custom has no log plugin"
	case apierrors.IsNotFound(err):
		st.State, st.Detail = "missing", "coredns-custom ConfigMap not found"
	case apierrors.IsForbidden(err):
		st.State, st.Detail = "warn", "not readable"
	default:
		st.State, st.Detail = "warn", "get coredns-custom failed"
	}
	if !aks {
		core, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		switch {
		case err != nil:
			st.Detail += "; Corefile not readable"
		case hasLogLine(core.Data["Corefile"]):
			st.State, st.Detail = "ok", "log plugin on (coredns Corefile)"
		default:
			st.Detail += "; Corefile has no log plugin"
		}
	}
	return st
}

// mergeDetect folds detect's statuses into sources: a k8s-api note is appended to the existing
// k8s-api entry (state warn) and everything else is added as its own source. Probe reuses this.
func mergeDetect(sources, det []model.SourceStatus) []model.SourceStatus {
	for _, d := range det {
		merged := false
		if d.ID == model.SrcK8sAPI {
			for i := range sources {
				if sources[i].ID == model.SrcK8sAPI {
					sources[i].State = "warn"
					sources[i].Detail += "; " + d.Detail
					merged = true
					break
				}
			}
		}
		if !merged {
			sources = append(sources, d)
		}
	}
	return sources
}
