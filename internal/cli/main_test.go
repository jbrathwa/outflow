package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps every test away from the real user cache and the network.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "outflow-cli-cache")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("OUTFLOW_CACHE_DIR", dir)
	autoRefresh = false
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
