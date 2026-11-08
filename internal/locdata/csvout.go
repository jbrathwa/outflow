package locdata

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

type cloudRow struct {
	Prefix                    netip.Prefix
	Provider, Region, Service string
	Global                    bool
}

type cdnRow struct {
	Prefix netip.Prefix
	Name   string
}

// cloudCSV renders rows in loader format, sorted by prefix string.
func cloudCSV(rows []cloudRow) []byte {
	sorted := append([]cloudRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Prefix.String() < sorted[j].Prefix.String()
	})
	var b strings.Builder
	b.WriteString("prefix,provider,region,service,global\n")
	for _, r := range sorted {
		b.WriteString(r.Prefix.String() + "," + r.Provider + "," + r.Region + "," + r.Service + "," + strconv.FormatBool(r.Global) + "\n")
	}
	return []byte(b.String())
}

// cdnCSV renders rows in loader format, sorted by prefix string.
func cdnCSV(rows []cdnRow) []byte {
	sorted := append([]cdnRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Prefix.String() < sorted[j].Prefix.String()
	})
	var b strings.Builder
	b.WriteString("prefix,name\n")
	for _, r := range sorted {
		b.WriteString(r.Prefix.String() + "," + r.Name + "\n")
	}
	return []byte(b.String())
}
