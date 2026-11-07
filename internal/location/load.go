package location

import (
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

//go:embed data/*
var embeddedData embed.FS

// Embedded loads the seed data compiled into the binary.
func Embedded() (*DB, error) {
	sub, err := fs.Sub(embeddedData, "data")
	if err != nil {
		return nil, err
	}
	return load(sub)
}

// LoadDir loads cloud.csv, cdn.csv, geo.csv and VERSION from dir.
func LoadDir(dir string) (*DB, error) {
	return load(os.DirFS(dir))
}

func load(fsys fs.FS) (*DB, error) {
	db := &DB{}
	if err := readCSV(fsys, "cloud.csv", 5, db.addCloud); err != nil {
		return nil, err
	}
	if err := readCSV(fsys, "cdn.csv", 2, db.addCDN); err != nil {
		return nil, err
	}
	if err := readCSV(fsys, "geo.csv", 3, db.addGeo); err != nil {
		return nil, err
	}
	v, err := fs.ReadFile(fsys, "VERSION")
	if err != nil {
		return nil, fmt.Errorf("VERSION: %w", err)
	}
	db.version = strings.TrimSpace(string(v))
	db.cdn.finish()
	db.cloud.finish()
	sort.Slice(db.geo, func(i, j int) bool { return db.geo[i].start.Less(db.geo[j].start) })
	return db, nil
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
