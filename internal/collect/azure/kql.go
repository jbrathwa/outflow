package azure

import (
	"strings"
	"time"
)

// kqlTime renders t as a KQL datetime literal in RFC3339 UTC.
func kqlTime(t time.Time) string {
	return "datetime(" + t.UTC().Format(time.RFC3339) + ")"
}

var kqlEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// kqlStrings renders vals as a comma-separated list of double-quoted KQL string literals.
func kqlStrings(vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = `"` + kqlEscaper.Replace(v) + `"`
	}
	return strings.Join(q, ", ")
}
