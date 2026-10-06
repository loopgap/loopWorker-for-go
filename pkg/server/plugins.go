package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loopworker/internal/config"
	"loopworker/pkg/plugin"
)

// PluginFailure records one plugin directory that could not be loaded.
type PluginFailure struct {
	Dir    string
	Reason string
}

// PluginRename records a directory whose name differs from the plugin name inside
// it. It is reported at startup because the mismatch is the single most
// confusing thing an operator can hit: tasks are addressed to the plugin name,
// but the folder on disk is what they navigated to.
type PluginRename struct {
	Dir     string `json:"dir"`
	DirName string `json:"dir_name"`
	Name    string `json:"name"`
}

// PluginRun is the outcome of plugin discovery and loading for this process.
// It is reported by the startup log, the health endpoint and the doctor dump, so
// "why is my task failing" is answerable without reading code.
type PluginRun struct {
	Dir        string          `json:"dir"`
	Discovered []string        `json:"discovered"`
	Loaded     []string        `json:"loaded"`
	Failed     []PluginFailure `json:"failed,omitempty"`
	Renamed    []PluginRename  `json:"renamed,omitempty"`
	AutoLoad   bool            `json:"auto_load"`
	// VerifyChecksum is plugins.verify_checksum as actually applied to this
	// process. Reported so that turning the switch on leaves a startup line
	// saying it is on - a control that announces nothing is indistinguishable
	// from a config key nobody reads.
	VerifyChecksum bool `json:"verify_checksum"`
}

// DiscoverAndLoad scans the configured plugin directory and loads every plugin
// found there when plugins.auto_load is true. Each plugin is loaded in
// isolation: one bad directory is reported in PluginRun.Failed and the rest
// still load, because a broken plugin is an operator problem with one directory
// rather than a reason to refuse to serve.
func DiscoverAndLoad(ctx context.Context, pm *plugin.PluginManager, cfg *config.Config) (*PluginRun, error) {
	run := &PluginRun{
		Dir:            cfg.Plugins.Dir,
		AutoLoad:       cfg.Plugins.AutoLoad,
		VerifyChecksum: cfg.Plugins.VerifyChecksum,
	}

	dirs, err := pm.DiscoverPlugins()
	if err != nil {
		return run, fmt.Errorf("scan plugin directory %s: %w (create it, or set plugins.dir to an existing directory)", cfg.Plugins.Dir, err)
	}
	run.Discovered = dirs
	if !cfg.Plugins.AutoLoad || len(dirs) == 0 {
		return run, nil
	}

	for _, dir := range dirs {
		if err := pm.LoadPlugin(ctx, dir); err != nil {
			run.Failed = append(run.Failed, PluginFailure{Dir: dir, Reason: err.Error()})
			continue
		}
		// The registered name comes from the manifest, not from the directory
		// name. Asking the manager is the only reliable way to learn it: a
		// folder called "smoke" holding plugin "hello" used to register workers
		// against "smoke" and dead-letter every task with "plugin not found".
		name, ok := pm.NameForDir(dir)
		if !ok {
			run.Failed = append(run.Failed, PluginFailure{
				Dir:    dir,
				Reason: "loaded but the manager reports no name for this directory (a bug in the plugin loader)",
			})
			continue
		}
		run.Loaded = append(run.Loaded, name)
		if base := filepath.Base(dir); base != name {
			run.Renamed = append(run.Renamed, PluginRename{Dir: dir, DirName: base, Name: name})
		}
	}

	if len(run.Failed) > 0 {
		var b strings.Builder
		for _, f := range run.Failed {
			fmt.Fprintf(&b, "\n  - %s: %s", f.Dir, f.Reason)
		}
		return run, fmt.Errorf("plugin auto-load failed for %d of %d plugin director(s)%s"+
			"\n  next steps: fix plugin.json in the named directory, or remove it, or set plugins.auto_load=false to start without plugins",
			len(run.Failed), len(dirs), b.String())
	}
	return run, nil
}

// primaryPlugin is the plugin id workers are registered against.
func (r *PluginRun) primaryPlugin() string {
	if r == nil {
		return noPluginID
	}
	if len(r.Loaded) > 0 {
		return r.Loaded[0]
	}
	if len(r.Discovered) > 0 {
		return filepath.Base(r.Discovered[0])
	}
	return noPluginID
}

const noPluginID = "no-plugin-installed"

// Summary describes the plugin state in one line for operators.
func (r *PluginRun) Summary() string {
	if r == nil {
		return "plugin state unavailable"
	}
	switch {
	case len(r.Discovered) == 0:
		return fmt.Sprintf("no plugin found in %s - install one by copying a directory containing plugin.json there, or tasks will fail with %q",
			r.Dir, noPluginID)
	case !r.AutoLoad:
		return fmt.Sprintf("%d plugin(s) discovered in %s, auto_load is off so none are running", len(r.Discovered), r.Dir)
	case len(r.Failed) > 0:
		return fmt.Sprintf("%d of %d plugin(s) loaded from %s, %d failed - run `loopworker doctor` for the per-directory reason",
			len(r.Loaded), len(r.Discovered), r.Dir, len(r.Failed))
	default:
		summary := fmt.Sprintf("%d plugin(s) loaded from %s: %s",
			len(r.Loaded), r.Dir, strings.Join(r.Loaded, ", "))
		// Name the directory -> name mapping. Tasks are addressed by plugin
		// name, so an operator who looks in a folder called "smoke" and then
		// submits a task saying "smoke" gets "plugin not found" with nothing in
		// the log to explain it.
		for _, rn := range r.Renamed {
			summary += fmt.Sprintf(" (directory %q provides plugin %q - use the plugin name in tasks)", rn.DirName, rn.Name)
		}
		if r.VerifyChecksum {
			summary += " (verify_checksum on: each artifact was checked against the sha256 in its manifest)"
		} else {
			summary += " (verify_checksum off: manifests are not checked against the artifacts)"
		}
		return summary
	}
}

// pluginDirListing is used by the self-check to describe what is on disk.
func pluginDirListing(dir string) (entries int, readable bool, err error) {
	items, readErr := os.ReadDir(dir)
	if readErr != nil {
		return 0, false, readErr
	}
	return len(items), true, nil
}
