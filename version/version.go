package version

import (
	"fmt"
	"runtime"
)

// Build information set via ldflags at build time.
var (
	Version   = "dev"
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
