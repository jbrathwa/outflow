package analyze

import (
	"net/netip"
	"slices"

	"github.com/jbrathwa/outflow/internal/config"
)

// Status is a destination's or workload's standing against the residency rule.
type Status string

const (
	Inside        Status = "inside"
	Outside       Status = "outside"
	OutsideDenied Status = "outside (denied)" // outside, but the firewall blocked it
	Unknown       Status = "unknown"
	NoTraffic     Status = "no traffic"
	Neutral       Status = "" // no rule configured
)

// Classify rates one destination of workloadID against r. A nil rule yields Neutral.
// The second result is the matching exception's reason, if any.
func Classify(d Destination, r *config.Residency, workloadID string) (Status, string) {
	if r == nil {
		return Neutral, ""
	}
	ip, _ := netip.ParseAddr(d.IP)
	for _, e := range r.Exceptions {
		if e.Matches(d.Domain, ip, workloadID) {
			return Inside, e.Reason
		}
	}
	l := d.Location
	if !l.Known() {
		return Unknown, ""
	}
	if slices.Contains(r.Allow.Countries, l.Country) ||
		(l.Provider != "" && l.Region != "" && slices.Contains(r.Allow.CloudRegions, l.Provider+":"+l.Region)) {
		return Inside, ""
	}
	switch {
	case d.Action == "deny":
		return OutsideDenied, ""
	case d.Evidence == DNSOnly:
		return Unknown, "" // looked up only: no proof traffic left
	}
	return Outside, ""
}

// counts reports whether a destination counts toward workload status and totals.
// Denied traffic never left, so it never counts, whatever its location.
func counts(d Destination, r *config.Residency) bool {
	if d.Action == "deny" {
		return false
	}
	switch d.Status {
	case Outside, Inside:
		return true
	case Unknown:
		return r.UnknownLocation != "ignore"
	}
	return false
}

// rank orders statuses worst first.
func rank(s Status) int {
	switch s {
	case Outside:
		return 0
	case Unknown:
		return 1
	case Inside:
		return 2
	}
	return 3
}
