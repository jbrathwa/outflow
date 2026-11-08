// Package locdata downloads location data and keeps it in an on-disk cache.
package locdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbrathwa/outflow/internal/location"
)

const day = 24 * time.Hour

// DefaultDir returns $OUTFLOW_CACHE_DIR, else os.UserCacheDir()/outflow.
func DefaultDir() (string, error) {
	if d := os.Getenv("OUTFLOW_CACHE_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "outflow"), nil
}

// Cache is the on-disk store. Now nil means time.Now.
type Cache struct {
	Dir string
	Now func() time.Time
}

// Meta is the pointer file written for each cached source.
type Meta struct {
	Source  string    `json:"source"`
	Version string    `json:"version"`
	Fetched time.Time `json:"fetched"`
	Dir     string    `json:"dir"` // folder name under Cache.Dir
}

// LoadIssue reports a cached source that could not be used.
type LoadIssue struct {
	Source string
	Err    error
}

func (c Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Cache) pointer(source string) string {
	return filepath.Join(c.Dir, source+".json")
}

// plainName reports whether s is a single path element.
func plainName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`) && filepath.Base(s) == s
}

// Meta returns the pointer for a source; false if missing or unreadable.
func (c Cache) Meta(source string) (Meta, bool) {
	if !plainName(source) {
		return Meta{}, false
	}
	b, err := os.ReadFile(c.pointer(source))
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if json.Unmarshal(b, &m) != nil {
		return Meta{}, false
	}
	return m, true
}

// Save writes a dataset into a new folder, then atomically switches the
// pointer to it and removes older folders for the source.
func (c Cache) Save(source string, d Dataset) error {
	if !plainName(source) {
		return fmt.Errorf("invalid source name %q", source)
	}
	if len(d.Files) == 0 {
		return fmt.Errorf("%s: refusing to cache an empty dataset", source)
	}
	for name := range d.Files {
		if !plainName(name) {
			return fmt.Errorf("%s: invalid file name %q", source, name)
		}
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(c.Dir, source+"-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	for name, data := range d.Files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	meta := Meta{Source: source, Version: d.Version, Fetched: c.now().UTC(), Dir: filepath.Base(dir)}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, source+".json.tmp-")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.pointer(source)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	keep = true

	// Best-effort cleanup of old folders. Recent ones may belong to a
	// concurrent run that has not switched its pointer yet.
	entries, _ := os.ReadDir(c.Dir)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), source+"-") || e.Name() == meta.Dir {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.RemoveAll(filepath.Join(c.Dir, e.Name()))
		}
	}
	return nil
}

// Load builds the database with one part per source, in srcs order, from the
// cache where possible and from the built-in sample otherwise. A cached source
// that fails to build is reported as a LoadIssue and replaced by its sample.
func (c Cache) Load(srcs []Source) (*location.DB, []LoadIssue, error) {
	now := c.now()
	var issues []LoadIssue
	parts := make([]location.Part, 0, len(srcs))
	for _, s := range srcs {
		sample, ok := samplePart(s.Name)
		if !ok {
			return nil, issues, fmt.Errorf("%s: no embedded data", s.Name)
		}
		m, found := c.Meta(s.Name)
		if !found {
			parts = append(parts, sample)
			continue
		}
		if !plainName(m.Dir) {
			issues = append(issues, LoadIssue{s.Name, fmt.Errorf("invalid cache folder %q", m.Dir)})
			parts = append(parts, sample)
			continue
		}
		dir := filepath.Join(c.Dir, m.Dir)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			if err == nil {
				err = errors.New("not a directory")
			}
			issues = append(issues, LoadIssue{s.Name, fmt.Errorf("cached data unreadable: %w", err)})
			parts = append(parts, sample)
			continue
		}
		age := now.Sub(m.Fetched)
		part := location.Part{
			Info: location.SourceInfo{
				Name:    s.Name,
				Version: m.Version,
				Fetched: m.Fetched,
				AgeDays: int(age / day),
				Stale:   age > s.MaxAge,
			},
			FS: os.DirFS(dir),
		}
		if _, err := location.Build([]location.Part{part}); err != nil {
			issues = append(issues, LoadIssue{s.Name, err})
			parts = append(parts, sample)
			continue
		}
		parts = append(parts, part)
	}
	db, err := location.Build(parts)
	if err != nil {
		return nil, issues, err
	}
	return db, issues, nil
}

func samplePart(name string) (location.Part, bool) {
	fsys, ok := location.SampleFS(name)
	if !ok {
		return location.Part{}, false
	}
	return location.Part{
		Info: location.SourceInfo{Name: name, Version: location.SampleVersion(), Sample: true},
		FS:   fsys,
	}, true
}

// SampleOnly returns the database built from the embedded sample data.
func SampleOnly() (*location.DB, error) { return location.Embedded() }
