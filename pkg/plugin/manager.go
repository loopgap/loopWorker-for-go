package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"loopworker/internal/core/sandbox"
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
}

// PluginManager manages plugin lifecycle: discovery, loading, unloading.
type PluginManager struct {
	sandbox      *sandbox.Sandbox
	eventBus     *event.EventBus
	skillRegistry *skill.SkillRegistry
	pluginsDir   string
	loaded       map[string]*PluginInfo
	mu           sync.RWMutex
}

// PluginManagerOption is a functional option for PluginManager.
type PluginManagerOption func(*PluginManager)

// WithSkillRegistry sets the skill registry for dependency checking.
func WithSkillRegistry(reg *skill.SkillRegistry) PluginManagerOption {
	return func(pm *PluginManager) {
		pm.skillRegistry = reg
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
		return fmt.Errorf("plugin %s already loaded", info.Name)
	}
	pm.mu.Unlock()

	handler := func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, fmt.Errorf("plugin execution not implemented for %s", info.Name)
	}

	mockPlugin := sandbox.NewMockPlugin(info.Name, info.Version, handler)
	if err := pm.sandbox.LoadPlugin(info.Name, mockPlugin); err != nil {
		return fmt.Errorf("load into sandbox: %w", err)
	}

	pm.mu.Lock()
	pm.loaded[info.Name] = &info
	pm.mu.Unlock()

	if pm.eventBus != nil {
		pluginEvent := event.NewEvent(event.EventPluginLoaded, event.PluginLoadedPayload{
			PluginID:   info.Name,
			PluginType: "wasm",
			Version:    info.Version,
		}, nil)
		_ = pm.eventBus.Publish(ctx, pluginEvent)
	}

	return nil
}

func (pm *PluginManager) UnloadPlugin(ctx context.Context, name string) error {
	pm.mu.Lock()
	info, exists := pm.loaded[name]
	if !exists {
		pm.mu.Unlock()
		return fmt.Errorf("plugin %s not loaded", name)
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

func (pm *PluginManager) LoadAllPlugins(ctx context.Context) error {
	dirs, err := pm.DiscoverPlugins()
	if err != nil {
		return fmt.Errorf("discover plugins: %w", err)
	}

	for _, dir := range dirs {
		if err := pm.LoadPlugin(ctx, dir); err != nil {
			return fmt.Errorf("load plugin from %s: %w", dir, err)
		}
	}

	return nil
}
