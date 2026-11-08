package location

import (
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

//go:embed data
var embeddedData embed.FS

// Part is one source's data: any of cloud.csv, cdn.csv and geo.csv in FS.
// Files that are missing are skipped.
type Part struct {
	Info SourceInfo
	FS   fs.FS
}

// SampleFS returns the built-in sample data for a source.
func SampleFS(source string) (fs.FS, bool) {
	if !slices.Contains(SourceNames, source) {
		return nil, false
	}
	sub, err := fs.Sub(embeddedData, "data/"+source)
	if err != nil {
		return nil, false
	}
	return sub, true
}

// SampleVersion returns the version of the built-in sample data.
func SampleVersion() string {
	v, err := embeddedData.ReadFile("data/VERSION")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(v))
}

// Embedded loads the sample data compiled into the binary.
func Embedded() (*DB, error) {
	parts := make([]Part, 0, len(SourceNames))
	for _, name := range SourceNames {
		sub, ok := SampleFS(name)
		if !ok {
			return nil, fmt.Errorf("%s: no embedded data", name)
		}
		parts = append(parts, Part{
			Info: SourceInfo{Name: name, Version: SampleVersion(), Sample: true},
			FS:   sub,
		})
	}
	return Build(parts)
}

// LoadDir loads cloud.csv, cdn.csv, geo.csv and VERSION from dir.
func LoadDir(dir string) (*DB, error) {
	fsys := os.DirFS(dir)
	db := &DB{}
	for _, f := range csvFiles {
		if err := readCSV(fsys, f.name, f.fields, f.add(db)); err != nil {
			return nil, err
		}
	}
	v, err := fs.ReadFile(fsys, "VERSION")
	if err != nil {
		return nil, fmt.Errorf("VERSION: %w", err)
	}
	db.version = strings.TrimSpace(string(v))
	db.finish()
	return db, nil
}

// Build merges the parts into one database. Missing files are skipped; errors
// name the part and file they came from.
func Build(parts []Part) (*DB, error) {
	db := &DB{}
	for _, p := range parts {
		for _, f := range csvFiles {
			err := readCSV(p.FS, f.name, f.fields, f.add(db))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%s: %w", p.Info.Name, err)
			}
		}
		db.sources = append(db.sources, p.Info)
	}
	db.finish()
	return db, nil
}

var csvFiles = []struct {
	name   string
	fields int
	add    func(*DB) func([]string) error
}{
	{"cloud.csv", 5, func(db *DB) func([]string) error { return db.addCloud }},
	{"cdn.csv", 2, func(db *DB) func([]string) error { return db.addCDN }},
	{"geo.csv", 3, func(db *DB) func([]string) error { return db.addGeo }},
}

func (db *DB) finish() {
	db.cdn.finish()
	db.cloud.finish()
	sort.Slice(db.geo, func(i, j int) bool { return db.geo[i].start.Less(db.geo[j].start) })
}

// readCSV skips the header row and calls add for each record.
func readCSV(fsys fs.FS, name string, fields int, add func([]string) error) error {
	f, err := fsys.Open(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = fields
	r.TrimLeadingSpace = true
	r.Comment = '#'
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if first {
			first = false
			continue
		}
		if err := add(rec); err != nil {
			line, _ := r.FieldPos(0)
			return fmt.Errorf("%s:%d: %w", name, line, err)
		}
	}
}

func (db *DB) addCloud(rec []string) error {
	p, err := netip.ParsePrefix(rec[0])
	if err != nil {
		return err
	}
	g := false
	if rec[4] != "" {
		if g, err = strconv.ParseBool(rec[4]); err != nil {
			return fmt.Errorf("global: %w", err)
		}
	}
	db.cloud.add(p, cloudEntry{provider: rec[1], region: rec[2], service: rec[3], global: g})
	return nil
}

func (db *DB) addCDN(rec []string) error {
	p, err := netip.ParsePrefix(rec[0])
	if err != nil {
		return err
	}
	db.cdn.add(p, rec[1])
	return nil
}

func (db *DB) addGeo(rec []string) error {
	s, err := netip.ParseAddr(rec[0])
	if err != nil {
		return err
	}
	e, err := netip.ParseAddr(rec[1])
	if err != nil {
		return err
	}
	db.geo = append(db.geo, geoRange{start: s.Unmap(), end: e.Unmap(), country: rec[2]})
	return nil
}
