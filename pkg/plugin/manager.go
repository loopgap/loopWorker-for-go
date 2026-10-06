package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"loopworker/internal/core/sandbox"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

// PluginInfo holds metadata loaded from a plugin's plugin.json file.
type PluginInfo struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Author      string            `json:"author"`
	Entry       string            `json:"entry"`
	Config      map[string]string `json:"config"`
	Skills      []string          `json:"skills,omitempty"` // skills this plugin requires
	// Limits is the plugin's own resource request. build() resolves it against
	// the sandbox's configuration before the runtime is created - see build.
	Limits sandbox.WasmLimits `json:"limits"`

	// SHA256 is the digest the publisher claims for the entry artifact. It is
	// only enforced when the manager was built with WithVerifyChecksum(true);
	// otherwise it is parsed and unused, which used to be true always.
	SHA256 string `json:"sha256"`

	// Dir is the directory the plugin was loaded from. It is deliberately not
	// part of the manifest: a directory name and a plugin name are independent
	// (an operator can drop plugin "hello" into a folder called "smoke"), and
	// the loader registers the plugin under Name. Anything that has to map a
	// directory back to a plugin must read this field rather than assume the
	// two are equal - assuming it cost a day of "plugin not found" dead letters.
	Dir string `json:"-"`
}

// PluginManager manages plugin lifecycle: discovery, loading, unloading.
type PluginManager struct {
	sandbox       *sandbox.Sandbox
	eventBus      *event.EventBus
	skillRegistry *skill.SkillRegistry
	pluginsDir    string
	verifyDigest  bool
	loaded        map[string]*PluginInfo
	mu            sync.RWMutex
}

// PluginManagerOption is a functional option for PluginManager.
type PluginManagerOption func(*PluginManager)

// WithSkillRegistry sets the skill registry for dependency checking.
func WithSkillRegistry(reg *skill.SkillRegistry) PluginManagerOption {
	return func(pm *PluginManager) {
		pm.skillRegistry = reg
	}
}

// WithVerifyChecksum makes LoadPlugin refuse a plugin whose manifest digest does
// not describe the artifact, and refuse one that declares no digest at all.
//
// It is off by default (plugins.verify_checksum) because turning it on stops
// plugins that run today: most manifests in the wild carry no sha256. It is
// per plugin, not per process - a refused plugin fails like any other bad
// manifest, and the rest of the directory still loads.
func WithVerifyChecksum(on bool) PluginManagerOption {
	return func(pm *PluginManager) {
		pm.verifyDigest = on
	}
}

// NewPluginManager creates a PluginManager. Options can override defaults.
func NewPluginManager(sb *sandbox.Sandbox, bus *event.EventBus, pluginsDir string, opts ...PluginManagerOption) (*PluginManager, error) {
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		return nil, fmt.Errorf("create plugins dir: %w", err)
	}

	pm := &PluginManager{
		sandbox:    sb,
		eventBus:   bus,
		pluginsDir: pluginsDir,
		loaded:     make(map[string]*PluginInfo),
	}

	for _, opt := range opts {
		opt(pm)
	}

	return pm, nil
}

// SkillRegistry returns the skill registry attached to this manager, if any.
func (pm *PluginManager) SkillRegistry() *skill.SkillRegistry {
	return pm.skillRegistry
}

func (pm *PluginManager) LoadPlugin(ctx context.Context, pluginDir string) error {
	infoPath := filepath.Join(pluginDir, "plugin.json")
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return fmt.Errorf("read plugin.json: %w", err)
	}

	var info PluginInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("parse plugin.json: %w", err)
	}

	// Check skill dependencies before loading
	if pm.skillRegistry != nil && len(info.Skills) > 0 {
		missing := pm.skillRegistry.CheckDependencies(info.Skills)
		if len(missing) > 0 {
			return fmt.Errorf("plugin %s requires missing skills: %v", info.Name, missing)
		}
	}

	pm.mu.Lock()
	if _, exists := pm.loaded[info.Name]; exists {
		pm.mu.Unlock()
		return fmt.Errorf("%w: %s", lwerrors.ErrPluginLoaded, info.Name)
	}
	pm.mu.Unlock()

	plugin, kind, err := pm.build(ctx, pluginDir, &info)
	if err != nil {
		return err
	}
	if err := pm.sandbox.LoadPlugin(info.Name, plugin); err != nil {
		return fmt.Errorf("load into sandbox: %w", err)
	}

	pm.mu.Lock()
	info.Dir = pluginDir
	pm.loaded[info.Name] = &info
	pm.mu.Unlock()

	if pm.eventBus != nil {
		pluginEvent := event.NewEvent(event.EventPluginLoaded, event.PluginLoadedPayload{
			PluginID:   info.Name,
			PluginType: kind,
			Version:    info.Version,
		}, nil)
		_ = pm.eventBus.Publish(ctx, pluginEvent)
	}

	return nil
}

// build turns a plugin manifest into something the sandbox can actually run,
// and reports what it built so the load event can stop claiming "wasm" for
// everything.
//
// Before this existed, LoadPlugin wrapped every manifest in a mock whose handler
// failed unconditionally: loading succeeded, the plugin was listed as loaded, and
// the plugin.loaded event said "wasm" — but the first execution returned
// ErrPluginInvalid. The manifest's entry field was parsed and then ignored.
func (pm *PluginManager) build(ctx context.Context, pluginDir string, info *PluginInfo) (sandbox.Plugin, string, error) {
	entry := strings.TrimSpace(info.Entry)
	if entry == "" {
		return nil, "", fmt.Errorf("%w: %s declares no entry (want a .wasm artifact)",
			lwerrors.ErrPluginInvalid, info.Name)
	}

	// plugin.json is untrusted input, so the entry must not escape its own
	// directory: "../../somewhere/evil.wasm" is not a plugin.
	path := filepath.Join(pluginDir, filepath.FromSlash(entry))
	rel, err := filepath.Rel(pluginDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", fmt.Errorf("%w: %s entry %q resolves outside its plugin directory",
			lwerrors.ErrPluginInvalid, info.Name, entry)
	}

	if !strings.EqualFold(filepath.Ext(path), ".wasm") {
		return nil, "", fmt.Errorf("%w: %s entry %q is not a .wasm artifact; this build executes WebAssembly plugins only",
			lwerrors.ErrPluginInvalid, info.Name, entry)
	}

	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", entry, err)
	}

	// Before the module is compiled, not after: a mismatched artifact must
	// never reach the runtime, and the refusal has to be an ordinary load
	// failure so the per-plugin isolation in LoadAllPlugins/DiscoverAndLoad
	// reports it and keeps the server up.
	if pm.verifyDigest {
		if err := sandbox.VerifyArtifactDigest(info.Name, info.SHA256, wasm, true); err != nil {
			return nil, "", fmt.Errorf("plugin %q failed checksum verification: %w\n"+
				"  cause: plugins.verify_checksum is on and %s does not match the digest %s declares\n"+
				"  fix:   republish the artifact and the manifest together (sha256sum %s),\n"+
				"         or set plugins.verify_checksum=false to load plugins without this check\n"+
				"  docs:  docs/USAGE.md#wasm-plugin",
				info.Name, err, entry, filepath.Join(pluginDir, "plugin.json"), entry)
		}
	}

	// The manifest's limits are this plugin's own request, so they go through
	// NewWasmPluginWithLimits: a smaller declared limit wins, a larger one is
	// clamped to the sandbox's, and an absent one inherits it. Calling
	// NewWasmPlugin here (as this used to) applied the sandbox-global config to
	// every plugin, which is why a manifest could declare any limit it liked and
	// the plugin silently ran under the sandbox's numbers instead.
	wp, err := pm.sandbox.NewWasmPluginWithLimits(ctx, info.Name, info.Version, wasm, info.Limits)
	if err != nil {
		return nil, "", fmt.Errorf("compile %s: %w", entry, err)
	}
	return wp, "wasm", nil
}

func (pm *PluginManager) UnloadPlugin(ctx context.Context, name string) error {
	pm.mu.Lock()
	info, exists := pm.loaded[name]
	if !exists {
		pm.mu.Unlock()
		return fmt.Errorf("%w: %s", lwerrors.ErrPluginNotFound, name)
	}
	delete(pm.loaded, name)
	pm.mu.Unlock()

	if err := pm.sandbox.UnloadPlugin(name); err != nil {
		return fmt.Errorf("unload from sandbox: %w", err)
	}

	if pm.eventBus != nil {
		pluginEvent := event.NewEvent(event.EventPluginUnloaded, event.PluginLoadedPayload{
			PluginID:   info.Name,
			PluginType: "wasm",
			Version:    info.Version,
		}, nil)
		_ = pm.eventBus.Publish(ctx, pluginEvent)
	}

	return nil
}

func (pm *PluginManager) GetPlugin(name string) (*PluginInfo, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	info, exists := pm.loaded[name]
	return info, exists
}

func (pm *PluginManager) ListPlugins() []*PluginInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	plugins := make([]*PluginInfo, 0, len(pm.loaded))
	for _, info := range pm.loaded {
		plugins = append(plugins, info)
	}
	return plugins
}

// NameForDir returns the name a plugin loaded from dir is registered under, and
// whether that directory produced a loaded plugin. Callers that hold a directory
// (discovery, the startup report, doctor) must use this instead of the directory
// base name: the registered name comes from the manifest, and the two differ
// whenever an operator names a folder differently from the plugin inside it.
func (pm *PluginManager) NameForDir(dir string) (string, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for name, info := range pm.loaded {
		if info.Dir == dir {
			return name, true
		}
	}
	return "", false
}

func (pm *PluginManager) DiscoverPlugins() ([]string, error) {
	entries, err := os.ReadDir(pm.pluginsDir)
	if err != nil {
		return nil, fmt.Errorf("read plugins dir: %w", err)
	}

	var pluginDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginJSON := filepath.Join(pm.pluginsDir, entry.Name(), "plugin.json")
		if _, err := os.Stat(pluginJSON); err == nil {
			pluginDirs = append(pluginDirs, filepath.Join(pm.pluginsDir, entry.Name()))
		}
	}

	return pluginDirs, nil
}

// LoadAllPlugins loads every discovered plugin.
//
// A plugin that cannot be loaded is reported but does not stop the others: one
// bad manifest in the plugins directory must not take the server down with it.
// The returned error names every failure so the caller can log all of them.
func (pm *PluginManager) LoadAllPlugins(ctx context.Context) error {
	dirs, err := pm.DiscoverPlugins()
	if err != nil {
		return fmt.Errorf("discover plugins: %w", err)
	}

	var failed []string
	for _, dir := range dirs {
		if err := pm.LoadPlugin(ctx, dir); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", filepath.Base(dir), err))
		}
	}

	switch len(failed) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("1 of %d plugins failed to load — %s", len(dirs), failed[0])
	default:
		return fmt.Errorf("%d of %d plugins failed to load — %s",
			len(failed), len(dirs), strings.Join(failed, "; "))
	}
}
