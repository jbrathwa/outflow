package location

import (
	"strings"

	"github.com/jbrathwa/outflow/internal/model"
)

var saasDomains = []string{
	"stripe.com", "segment.io", "slack.com", "sentry.io",
	"datadoghq.com", "zendesk.com", "salesforce.com", "twilio.com",
}

// IsSaaS reports whether domain is, or is a subdomain of, a known SaaS domain.
func IsSaaS(domain string) bool {
	d := model.NormalizeDomain(domain)
	for _, s := range saasDomains {
		if d == s || strings.HasSuffix(d, "."+s) {
			return true
		}
	}
	return false
}
