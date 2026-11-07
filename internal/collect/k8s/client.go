// Package k8s reads cluster state through client-go. It only issues get/list calls.
package k8s

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// UserAgent is the client user agent; the cli may set it (e.g. "outflow/1.2.3") before New.
var UserAgent = "outflow"

// Source reads the live cluster.
type Source struct {
	cs      kubernetes.Interface
	dyn     dynamic.Interface
	context string
	rest    *rest.Config
}

// New builds a Source from the standard kubeconfig loading rules. A non-empty kubeconfig is an
// explicit path that overrides KUBECONFIG; a non-empty contextName overrides current-context.
func New(kubeconfig, contextName string) (*Source, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: contextName})
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	name := contextName
	if name == "" {
		name = raw.CurrentContext
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cfg.QPS = 20
	cfg.Burst = 40
	cfg.UserAgent = UserAgent
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes dynamic client: %w", err)
	}
	return &Source{cs: cs, dyn: dyn, context: name, rest: cfg}, nil
}

// NewFromClients builds a Source from existing clients (tests). dyn may be nil.
func NewFromClients(cs kubernetes.Interface, dyn dynamic.Interface, contextName string) *Source {
	return &Source{cs: cs, dyn: dyn, context: contextName}
}
