package netpol

import (
	"net/netip"
	"testing"

	"github.com/jbrathwa/outflow/internal/model"
)

func pol(ns, name string, types []string, rules ...model.EgressRule) model.NetworkPolicy {
	return model.NetworkPolicy{Namespace: ns, Name: name, PodSelector: model.LabelSelector{}, PolicyTypes: types, Egress: rules}
}

func block(cidr string, except ...string) []model.Peer {
	return []model.Peer{{IPBlock: &model.IPBlock{CIDR: cidr, Except: except}}}
}

var rfc1918 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

func TestEvaluate(t *testing.T) {
	eg := []string{"Egress"}
	w := model.Workload{Namespace: "shop", Name: "api", Labels: map[string]string{"app": "api"}}
	hn := w
	hn.HostNetwork = true
	port := func(p int) []model.PortSpec { return []model.PortSpec{{Protocol: "TCP", Port: p}} }

	tests := []struct {
		name     string
		w        model.Workload
		policies []model.NetworkPolicy
		want     string
	}{
		{"no policies", w, nil, "unrestricted"},
		{"ingress only", w, []model.NetworkPolicy{pol("shop", "in", []string{"Ingress"})}, "unrestricted"},
		{"empty egress rules", w, []model.NetworkPolicy{pol("shop", "deny", eg)}, "deny-all"},
		{"pod selector peer", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: []model.Peer{{PodSelector: &model.LabelSelector{}}}})}, "internal"},
		{"private ipblock", w, []model.NetworkPolicy{pol("shop", "p", eg, model.EgressRule{To: block("10.0.0.0/8")})}, "internal"},
		{"public ipblock", w, []model.NetworkPolicy{pol("shop", "p", eg, model.EgressRule{To: block("20.60.0.0/16")})}, "allowlist"},
		{"0.0.0.0/0 except private, 443", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("0.0.0.0/0", rfc1918...), Ports: port(443)})}, "internet:443"},
		{"ipv6 any", w, []model.NetworkPolicy{pol("shop", "p", eg, model.EgressRule{To: block("::/0")})}, "internet"},
		{"empty To", w, []model.NetworkPolicy{pol("shop", "p", eg, model.EgressRule{})}, "internet"},
		{"hostNetwork", hn, []model.NetworkPolicy{pol("shop", "p", eg)}, "hostNetwork"},
		{"other namespace ignored", w, []model.NetworkPolicy{pol("other", "p", eg)}, "unrestricted"},
		{"selector miss ignored", w, []model.NetworkPolicy{{Namespace: "shop", Name: "p", PolicyTypes: eg,
			PodSelector: model.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}}, "unrestricted"},
		{"internal + allowlist", w, []model.NetworkPolicy{
			pol("shop", "a", eg, model.EgressRule{To: block("10.1.0.0/16")}),
			pol("shop", "b", eg, model.EgressRule{To: block("20.60.0.0/16")}),
		}, "allowlist"},
		{"invalid cidr skipped", w, []model.NetworkPolicy{pol("shop", "p", eg, model.EgressRule{To: block("not-a-cidr")})}, "deny-all"},
		{"invalid cidr beside valid", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: append(block("bogus"), block("10.0.0.0/8")...)})}, "internal"},
		{"identical ports joined", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("20.60.0.0/16"), Ports: []model.PortSpec{{Port: 80}, {Port: 443}}},
			model.EgressRule{To: block("20.61.0.0/16"), Ports: []model.PortSpec{{Port: 80}, {Port: 443}}})}, "allowlist:80,443"},
		{"differing ports blank", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("20.60.0.0/16"), Ports: port(80)},
			model.EgressRule{To: block("20.61.0.0/16"), Ports: port(443)})}, "allowlist"},
		{"one rule without ports blank", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("20.60.0.0/16"), Ports: port(80)},
			model.EgressRule{To: block("20.61.0.0/16")})}, "allowlist"},
		{"port range", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("20.60.0.0/16"), Ports: []model.PortSpec{{Port: 8000, EndPort: 8080}}})}, "allowlist:8000-8080"},
		{"protocol-only port spec blank", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("20.60.0.0/16"), Ports: []model.PortSpec{{Protocol: "TCP", Port: 443}, {Protocol: "UDP"}}})}, "allowlist"},
		{"lower modes' ports ignored", w, []model.NetworkPolicy{pol("shop", "p", eg,
			model.EgressRule{To: block("10.0.0.0/8")},
			model.EgressRule{To: block("20.60.0.0/16"), Ports: port(443)})}, "allowlist:443"},
	}
	private := DefaultPrivate(nil, nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.w, tt.policies, private).String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEvaluatePolicies(t *testing.T) {
	w := model.Workload{Namespace: "shop", Name: "api"}
	r := Evaluate(w, []model.NetworkPolicy{
		pol("shop", "a", []string{"Egress"}),
		pol("other", "x", []string{"Egress"}),
		pol("shop", "b", []string{"Ingress", "Egress"}),
	}, DefaultPrivate(nil, nil))
	if len(r.Policies) != 2 || r.Policies[0] != "a" || r.Policies[1] != "b" {
		t.Errorf("Policies = %v", r.Policies)
	}
}

func TestDefaultPrivateExtraCIDRs(t *testing.T) {
	p := DefaultPrivate([]string{"203.0.113.0/24", "bad"}, []string{"198.51.100.0/24"})
	w := model.Workload{Namespace: "n", Name: "w"}
	pols := []model.NetworkPolicy{pol("n", "p", []string{"Egress"}, model.EgressRule{To: block("203.0.113.0/25")})}
	if got := Evaluate(w, pols, p).Mode; got != Internal {
		t.Errorf("pod CIDR should be private, got %s", got)
	}
	if !slicesHas(p, netip.MustParsePrefix("198.51.100.0/24")) {
		t.Error("service CIDR missing")
	}
}

func slicesHas(ps []netip.Prefix, x netip.Prefix) bool {
	for _, p := range ps {
		if p == x {
			return true
		}
	}
	return false
}

func TestMatches(t *testing.T) {
	labels := map[string]string{"app": "api", "tier": "web"}
	ex := func(k, op string, v ...string) model.LabelSelector {
		return model.LabelSelector{MatchExpressions: []model.LabelExpr{{Key: k, Operator: op, Values: v}}}
	}
	tests := []struct {
		name string
		sel  model.LabelSelector
		want bool
	}{
		{"empty", model.LabelSelector{}, true},
		{"matchLabels hit", model.LabelSelector{MatchLabels: map[string]string{"app": "api"}}, true},
		{"matchLabels miss value", model.LabelSelector{MatchLabels: map[string]string{"app": "db"}}, false},
		{"matchLabels miss key", model.LabelSelector{MatchLabels: map[string]string{"x": "y"}}, false},
		{"In hit", ex("app", "In", "api", "db"), true},
		{"In miss", ex("app", "In", "db"), false},
		{"In absent", ex("x", "In", "db"), false},
		{"NotIn hit", ex("app", "NotIn", "db"), true},
		{"NotIn miss", ex("app", "NotIn", "api"), false},
		{"NotIn absent", ex("x", "NotIn", "api"), true},
		{"Exists hit", ex("app", "Exists"), true},
		{"Exists miss", ex("x", "Exists"), false},
		{"DoesNotExist hit", ex("x", "DoesNotExist"), true},
		{"DoesNotExist miss", ex("app", "DoesNotExist"), false},
		{"unknown op", ex("app", "Bogus"), false},
		{"labels and expr", model.LabelSelector{MatchLabels: map[string]string{"app": "api"},
			MatchExpressions: []model.LabelExpr{{Key: "tier", Operator: "In", Values: []string{"db"}}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Matches(tt.sel, labels); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if !Matches(model.LabelSelector{}, nil) {
		t.Error("empty selector must match nil labels")
	}
}
