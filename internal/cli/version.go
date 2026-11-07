package cli

import (
	"runtime/debug"
	"strings"
)

// Build information, set at release time with
// -ldflags "-X github.com/jbrathwa/outflow/internal/cli.Version=..." (see .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// moduleVersion is the version recorded by `go install module@vX.Y.Z`,
// used when the binary was not stamped by a release build.
func moduleVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return ""
}

// versionString renders "outflow <version> (<commit>, <date>)", leaving out
// whatever is unknown.
func versionString(version, commit, date, module string) string {
	if version == "dev" && module != "" && module != "(devel)" {
		version = module
	}
	s := "outflow " + strings.TrimPrefix(version, "v")
	var extra []string
	for _, v := range []string{commit, date} {
		if v != "" {
			extra = append(extra, v)
		}
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}
