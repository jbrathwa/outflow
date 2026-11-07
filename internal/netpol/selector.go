package netpol

import (
	"slices"

	"github.com/jbrathwa/outflow/internal/model"
)

// Matches reports whether labels satisfy sel. An empty selector matches everything.
// Unknown operators never match.
func Matches(sel model.LabelSelector, labels map[string]string) bool {
	for k, v := range sel.MatchLabels {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	for _, e := range sel.MatchExpressions {
		got, ok := labels[e.Key]
		switch e.Operator {
		case "In":
			if !ok || !slices.Contains(e.Values, got) {
				return false
			}
		case "NotIn":
			if ok && slices.Contains(e.Values, got) {
				return false
			}
		case "Exists":
			if !ok {
				return false
			}
		case "DoesNotExist":
			if ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}
