package version

import (
	"fmt"
	"runtime"
)

// Version scheme (see .release/RELEASE-PROCESS.md):
//
//	0.1.0-beta     first public beta
//	0.1.0-beta.1   subsequent betas; the .N counter only ever increases
//
// Beta is deliberately below 1.0: the API, config schema, and plugin ABI are all
// still moving, so nothing here carries a compatibility promise yet.
//
// Build information set via ldflags at build time.
var (
	Version   = "0.1.0-beta"
	GitCommit = "unknown"
	BuildDate = "unknown"
	GoVersion = runtime.Version()
)

// Info contains version information.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// Get returns version information.
func Get() Info {
	return Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
		GoVersion: GoVersion,
	}
}

// String returns a formatted version string.
func (i Info) String() string {
	return fmt.Sprintf("loopworker %s (commit: %s, built: %s, go: %s)",
		i.Version, i.GitCommit, i.BuildDate, i.GoVersion)
}
