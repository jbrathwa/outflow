package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jbrathwa/outflow/internal/model"
)

// snapshotFlagError names the first offline flag that snapshot cannot be combined with.
func (c *common) snapshotFlagError() error {
	for _, f := range []struct {
		name string
		on   bool
	}{
		{"--demo", c.demo}, {"--demo-degraded", c.demoDegraded}, {"--snapshot", c.snapshot != ""},
		{"--demo-rule", c.demoRule}, {"--dump-demo", c.dumpDemo},
	} {
		if f.on {
			return fmt.Errorf("snapshot reads the live cluster; %s cannot be used", f.name)
		}
	}
	return nil
}

// writeSnapshot writes s as indented JSON to path ("-" = stdout) and a summary to stderr.
// Files are created (or tightened) to mode 0600: snapshots hold cluster inventory.
func writeSnapshot(stdout, stderr io.Writer, path string, s model.Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	name := path
	if path == "-" {
		name = "snapshot to stdout"
		if _, err := stdout.Write(b); err != nil {
			return fmt.Errorf("write snapshot: %w", err)
		}
	} else if err := writeFile0600(path, b); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	fmt.Fprintf(stderr, "wrote %s (%d workloads, %d lookups, %d firewall events)\n",
		name, len(s.Workloads), len(s.DNSLookups), len(s.EgressEvents))
	return nil
}

func writeFile0600(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// A file that already existed keeps its mode on open; tighten a wider regular file.
	if st, err := f.Stat(); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&^0o600 != 0 {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return err
		}
	}
	_, werr := f.Write(b)
	return errors.Join(werr, f.Close())
}
