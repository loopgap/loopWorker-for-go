package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"loopworker/internal/core/sandbox"
	"loopworker/pkg/event"
)

func TestPluginManagerLoadPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	info := PluginInfo{
		Name:    "test",
		Version: "1.0",
		Entry:   "main.go",
	}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, err := NewPluginManager(s, bus, filepath.Join(tmpDir, "plugins"))
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if err := mgr.LoadPlugin(context.Background(), pluginDir); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	plugins := mgr.ListPlugins()
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
}

func TestPluginManagerLoadDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	info := PluginInfo{Name: "test", Version: "1.0"}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, filepath.Join(tmpDir, "plugins"))

	_ = mgr.LoadPlugin(context.Background(), pluginDir)
	if err := mgr.LoadPlugin(context.Background(), pluginDir); err == nil {
		t.Error("expected error loading duplicate plugin")
	}
}

func TestPluginManagerUnloadPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	info := PluginInfo{Name: "test", Version: "1.0"}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, filepath.Join(tmpDir, "plugins"))

	_ = mgr.LoadPlugin(context.Background(), pluginDir)
	if err := mgr.UnloadPlugin(context.Background(), "test"); err != nil {
		t.Fatalf("unload plugin: %v", err)
	}

	plugins := mgr.ListPlugins()
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins after unload, got %d", len(plugins))
	}
}

func TestPluginManagerUnloadNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, filepath.Join(tmpDir, "plugins"))
	if err := mgr.UnloadPlugin(context.Background(), "nonexistent"); err == nil {
		t.Error("expected error unloading nonexistent plugin")
	}
}

func TestPluginManagerGetPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	info := PluginInfo{Name: "test", Version: "1.0", Description: "test plugin"}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, filepath.Join(tmpDir, "plugins"))
	_ = mgr.LoadPlugin(context.Background(), pluginDir)

	loaded, exists := mgr.GetPlugin("test")
	if !exists {
		t.Error("plugin not found")
	}
	if loaded.Description != "test plugin" {
		t.Errorf("expected description 'test plugin', got '%s'", loaded.Description)
	}
}

func TestPluginManagerDiscoverPlugins(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(filepath.Join(pluginsDir, "plugin-a"), 0755)
	os.MkdirAll(filepath.Join(pluginsDir, "plugin-b"), 0755)
	os.MkdirAll(filepath.Join(pluginsDir, "not-a-plugin"), 0755)

	info := PluginInfo{Name: "test", Version: "1.0"}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(pluginsDir, "plugin-a", "plugin.json"), data, 0644)
	os.WriteFile(filepath.Join(pluginsDir, "plugin-b", "plugin.json"), data, 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, pluginsDir)
	dirs, err := mgr.DiscoverPlugins()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(dirs) != 2 {
		t.Errorf("expected 2 plugin dirs, got %d", len(dirs))
	}
}

func TestPluginManagerLoadAllPlugins(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(filepath.Join(pluginsDir, "plugin-a"), 0755)
	os.MkdirAll(filepath.Join(pluginsDir, "plugin-b"), 0755)

	infoA := PluginInfo{Name: "plugin-a", Version: "1.0"}
	infoB := PluginInfo{Name: "plugin-b", Version: "2.0"}
	dataA, _ := json.Marshal(infoA)
	dataB, _ := json.Marshal(infoB)
	os.WriteFile(filepath.Join(pluginsDir, "plugin-a", "plugin.json"), dataA, 0644)
	os.WriteFile(filepath.Join(pluginsDir, "plugin-b", "plugin.json"), dataB, 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, pluginsDir)
	if err := mgr.LoadAllPlugins(context.Background()); err != nil {
		t.Fatalf("load all: %v", err)
	}

	plugins := mgr.ListPlugins()
	if len(plugins) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(plugins))
	}
}

// 并发压力测试

func TestConcurrentLoadPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginsDir, 0755)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, pluginsDir)
	var wg sync.WaitGroup
	n := 50

	// 并发加载插件
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			pluginDir := filepath.Join(tmpDir, fmt.Sprintf("plugin-%d", idx))
			os.MkdirAll(pluginDir, 0755)
			info := PluginInfo{Name: fmt.Sprintf("plugin-%d", idx), Version: "1.0"}
			data, _ := json.Marshal(info)
			os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)
			_ = mgr.LoadPlugin(context.Background(), pluginDir)
		}(i)
	}
	wg.Wait()

	plugins := mgr.ListPlugins()
	if len(plugins) != n {
		t.Errorf("expected %d plugins, got %d", n, len(plugins))
	}
}

func TestConcurrentUnloadPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginsDir, 0755)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, pluginsDir)

	// 预先加载插件
	for i := 0; i < 10; i++ {
		pluginDir := filepath.Join(tmpDir, fmt.Sprintf("plugin-%d", i))
		os.MkdirAll(pluginDir, 0755)
		info := PluginInfo{Name: fmt.Sprintf("plugin-%d", i), Version: "1.0"}
		data, _ := json.Marshal(info)
		os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)
		_ = mgr.LoadPlugin(context.Background(), pluginDir)
	}

	var wg sync.WaitGroup

	// 并发卸载插件
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			defer wg.Done()
			pluginName := fmt.Sprintf("plugin-%d", idx)
			_ = mgr.UnloadPlugin(context.Background(), pluginName)
		}(i)
	}
	wg.Wait()

	plugins := mgr.ListPlugins()
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins after unload, got %d", len(plugins))
	}
}

func TestConcurrentListPlugins(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginsDir, 0755)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, pluginsDir)

	// 预先加载插件
	for i := 0; i < 10; i++ {
		pluginDir := filepath.Join(tmpDir, fmt.Sprintf("plugin-%d", i))
		os.MkdirAll(pluginDir, 0755)
		info := PluginInfo{Name: fmt.Sprintf("plugin-%d", i), Version: "1.0"}
		data, _ := json.Marshal(info)
		os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)
		_ = mgr.LoadPlugin(context.Background(), pluginDir)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发读取插件列表
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			plugins := mgr.ListPlugins()
			if len(plugins) != 10 {
				t.Errorf("expected 10 plugins, got %d", len(plugins))
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentMixedOperations(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginsDir, 0755)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, pluginsDir)
	var wg sync.WaitGroup
	n := 50

	// 并发混合操作
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			pluginName := fmt.Sprintf("plugin-%d", idx%10)
			pluginDir := filepath.Join(tmpDir, pluginName)
			os.MkdirAll(pluginDir, 0755)
			info := PluginInfo{Name: pluginName, Version: "1.0"}
			data, _ := json.Marshal(info)
			os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0644)

			// 加载插件
			_ = mgr.LoadPlugin(context.Background(), pluginDir)
			// 列出插件
			_ = mgr.ListPlugins()
			// 卸载插件
			_ = mgr.UnloadPlugin(context.Background(), pluginName)
		}(i)
	}
	wg.Wait()
}
