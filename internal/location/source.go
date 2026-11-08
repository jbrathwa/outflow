package location

import "time"

// SourceNames lists the data sources a database is built from, in order.
var SourceNames = []string{"azure", "aws", "gcp", "cdn", "geo"}

// SourceInfo describes one data source: which version is loaded and how old it is.
type SourceInfo struct {
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Fetched time.Time `json:"fetched,omitzero"`
	AgeDays int       `json:"ageDays"`
	Stale   bool      `json:"stale"`
	Sample  bool      `json:"sample"`
}

var sourceLabels = map[string]string{
	"azure": "Azure service tags",
	"aws":   "AWS IP ranges",
	"gcp":   "GCP IP ranges",
	"cdn":   "CDN ranges",
	"geo":   "DB-IP country database",
}

// SourceLabel returns the human-readable name of a source, or name itself if unknown.
func SourceLabel(name string) string {
	if l, ok := sourceLabels[name]; ok {
		return l
	}
	return name
}

// Sources returns the sources the database was built from, in the order given.
func (db *DB) Sources() []SourceInfo { return db.sources }
