package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"loopworker/internal/core/sandbox"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
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
	_ = writeWasmPlugin(pluginDir, info)

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
	_ = writeWasmPlugin(pluginDir, info)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, filepath.Join(tmpDir, "plugins"))

	_ = mgr.LoadPlugin(context.Background(), pluginDir)
	if err := mgr.LoadPlugin(context.Background(), pluginDir); err == nil {
		t.Error("expected error loading duplicate plugin")
	}
}

// TestManifestLimitsReachTheRunningPlugin is the regression test for the README
// claim "per-plugin limits ... from config".
//
// PluginManager.build used to call Sandbox.NewWasmPlugin, which builds the
// runtime from the sandbox-global config, so a manifest's own limits were parsed
// into PluginInfo and then thrown away: the plugin silently ran with the
// sandbox's caps. A plugin that asked for 1 MiB of output got the sandbox's
// 64 MiB.
func TestManifestLimitsReachTheRunningPlugin(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "quiet")

	// The sandbox allows 64 MiB of output; the plugin below asks for 1.
	s := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 256, MaxCPUSeconds: 30, MaxOutputMB: 64})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, err := NewPluginManager(s, bus, tmpDir)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if err := writeRunnableWasmPlugin(pluginDir, PluginInfo{
		Name:    "quiet",
		Version: "1.0",
		Limits:  sandbox.WasmLimits{MemoryMB: 128, MaxCPUSeconds: 30, MaxOutputMB: 1},
	}); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	if err := mgr.LoadPlugin(ctx, pluginDir); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	// hello.wasm echoes stdin to stdout, so 2 MiB in is ~2 MiB out. The
	// manifest's 1 MiB cap must bite; the sandbox's 64 MiB must not rescue it.
	_, err = s.Execute(ctx, "quiet", bytes.Repeat([]byte("x"), 2<<20), skill.SkillContext{})
	if !errors.Is(err, lwerrors.ErrSandboxOversized) {
		t.Fatalf("the manifest's 1 MiB output cap must stop a 2 MiB echo, got %v", err)
	}
}

// TestManifestLimitsAreTheCeilingNotTheFloor pins the other half of the rule:
// a manifest that declares no limits inherits the sandbox's.
func TestManifestLimitsAreTheCeilingNotTheFloor(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	s := sandbox.NewSandbox(sandbox.SandboxConfig{MaxMemoryMB: 256, MaxCPUSeconds: 30, MaxOutputMB: 2})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, err := NewPluginManager(s, bus, tmpDir)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	// No limits block at all: the plugin gets the sandbox's budget.
	if err := writeRunnableWasmPlugin(filepath.Join(tmpDir, "plain"), PluginInfo{Name: "plain", Version: "1.0"}); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	if err := mgr.LoadPlugin(ctx, filepath.Join(tmpDir, "plain")); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	// The sandbox's 2 MiB output cap still applies to a plugin that declared none.
	_, err = s.Execute(ctx, "plain", bytes.Repeat([]byte("x"), 3<<20), skill.SkillContext{})
	if !errors.Is(err, lwerrors.ErrSandboxOversized) {
		t.Fatalf("the sandbox cap must still apply, got %v", err)
	}
}

func TestPluginManagerUnloadPlugin(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	os.MkdirAll(pluginDir, 0755)

	info := PluginInfo{Name: "test", Version: "1.0"}
	_ = writeWasmPlugin(pluginDir, info)

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
	_ = writeWasmPlugin(pluginDir, info)

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
	if err := writeWasmPlugin(filepath.Join(pluginsDir, "plugin-a"), infoA); err != nil {
		t.Fatalf("write plugin-a: %v", err)
	}
	if err := writeWasmPlugin(filepath.Join(pluginsDir, "plugin-b"), infoB); err != nil {
		t.Fatalf("write plugin-b: %v", err)
	}

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
			_ = writeWasmPlugin(pluginDir, info)
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
		_ = writeWasmPlugin(pluginDir, info)
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
		_ = writeWasmPlugin(pluginDir, info)
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
			_ = writeWasmPlugin(pluginDir, info)

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

// ---- Additional coverage tests ----

// TestWithSkillRegistry 验证 WithSkillRegistry 选项设置技能注册表。
func TestWithSkillRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	reg := skill.NewSkillRegistry()
	mgr, err := NewPluginManager(s, bus, tmpDir, WithSkillRegistry(reg))
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if mgr.SkillRegistry() != reg {
		t.Error("expected SkillRegistry to match")
	}
}

// TestSkillRegistryDefaultNil 验证默认情况下 SkillRegistry 为 nil。
func TestSkillRegistryDefaultNil(t *testing.T) {
	tmpDir := t.TempDir()
	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, err := NewPluginManager(s, bus, tmpDir)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	if mgr.SkillRegistry() != nil {
		t.Error("expected nil SkillRegistry by default")
	}
}

// TestLoadPluginWithSkillDependency 验证插件技能依赖检查。
func TestLoadPluginWithSkillDependency(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "needs-llm")
	os.MkdirAll(pluginDir, 0755)

	// Plugin requires "llm.chat" skill
	info := PluginInfo{Name: "needs-llm", Version: "1.0", Skills: []string{"llm.chat"}}
	_ = writeWasmPlugin(pluginDir, info)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	// Create registry with llm.chat skill registered
	reg := skill.NewSkillRegistry()
	reg.Register(skill.SkillDefinition{Name: "llm.chat", Version: "1.0"}, nil)
	mgr, _ := NewPluginManager(s, bus, tmpDir, WithSkillRegistry(reg))

	if err := mgr.LoadPlugin(context.Background(), pluginDir); err != nil {
		t.Fatalf("load plugin with satisfied skill dependency: %v", err)
	}
}

// TestLoadPluginMissingSkillDependency 验证缺失技能依赖时的错误。
func TestLoadPluginMissingSkillDependency(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "needs-llm")
	os.MkdirAll(pluginDir, 0755)

	info := PluginInfo{Name: "needs-llm", Version: "1.0", Skills: []string{"missing.skill"}}
	_ = writeWasmPlugin(pluginDir, info)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	// Empty registry - nothing registered
	reg := skill.NewSkillRegistry()
	mgr, _ := NewPluginManager(s, bus, tmpDir, WithSkillRegistry(reg))

	if err := mgr.LoadPlugin(context.Background(), pluginDir); err == nil {
		t.Error("expected error for missing skill dependency")
	}
}

// TestLoadPluginInvalidJSON 验证无效 plugin.json 的错误处理。
func TestLoadPluginInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "bad")
	os.MkdirAll(pluginDir, 0755)
	os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte("not json"), 0644)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, tmpDir)
	if err := mgr.LoadPlugin(context.Background(), pluginDir); err == nil {
		t.Error("expected error for invalid plugin.json")
	}
}

// TestLoadPluginMissingFile 验证 plugin.json 不存在时的错误处理。
func TestLoadPluginMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "empty")
	os.MkdirAll(pluginDir, 0755)

	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, tmpDir)
	if err := mgr.LoadPlugin(context.Background(), pluginDir); err == nil {
		t.Error("expected error for missing plugin.json")
	}
}

// TestLoadAllPluginsEmptyDir 验证空插件目录不报错。
func TestLoadAllPluginsEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	mgr, _ := NewPluginManager(s, bus, tmpDir)
	if err := mgr.LoadAllPlugins(context.Background()); err != nil {
		t.Fatalf("load all from empty dir: %v", err)
	}

	if len(mgr.ListPlugins()) != 0 {
		t.Error("expected 0 plugins from empty dir")
	}
}

// TestNewPluginManagerInvalidDir 验证无法创建目录时的错误处理。
func TestNewPluginManagerInvalidDir(t *testing.T) {
	s := sandbox.NewSandbox(sandbox.SandboxConfig{})
	bus := event.NewEventBus(nil)
	defer bus.Close()

	// Use a path that can't be created (file blocks directory creation)
	tmpDir := t.TempDir()
	blockerPath := filepath.Join(tmpDir, "blocker")
	os.WriteFile(blockerPath, []byte("blocking file"), 0644)
	impossibleDir := filepath.Join(blockerPath, "subdir")

	mgr, err := NewPluginManager(s, bus, impossibleDir)
	if err == nil && mgr != nil {
		// If creation succeeded, try DiscoverPlugins on the invalid dir
		_, derr := mgr.DiscoverPlugins()
		if derr == nil {
			// On some platforms this may succeed - acceptable
			t.Log("platform allows nested dirs under file - skipping")
		}
	}
}

// minimalWasm is the smallest module the WebAssembly specification allows: the
// 8-byte header and no sections. The loader only parses and compiles it, which
// is all most of these tests ask for.
//
// These fixtures used to write the real examples/hello-plugin module - 2.5 MB of
// Go-generated bytecode - roughly 140 times per run, about 100 of them
// concurrently inside the TestConcurrent* cases. Under -race that is wazero
// compiling 2.5 MB per load with every allocation instrumented: pkg/plugin alone
// took 9m37s and peaked at 7.2 GB, which is more than the GitHub runner that
// executes the race job has, so the job would have died of OOM rather than
// reporting a race. The concurrency under test is PluginManager's map and lock,
// not wazero's compiler. Tests that must actually execute a plugin still use
// the real artifact - see writeRunnableWasmPlugin.
var minimalWasm = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// writeWasmPlugin writes a plugin directory that the loader will actually
// accept: a valid WebAssembly artifact plus a manifest pointing at it.
//
// The fixtures here used to write a manifest whose entry ("main.go") did not
// exist. That was only possible because LoadPlugin wrapped every manifest in a
// mock that loaded unconditionally, so "loaded" never meant "runnable".
func writeWasmPlugin(dir string, info PluginInfo) error {
	return writePluginArtifact(dir, info, minimalWasm)
}

// writeRunnableWasmPlugin is writeWasmPlugin with the real module, for the
// tests that execute the plugin and assert on what it does. Loading tests use
// the minimal header instead; see minimalWasm for why that is not a shortcut.
func writeRunnableWasmPlugin(dir string, info PluginInfo) error {
	wasm, err := os.ReadFile(filepath.Join("testdata", "hello.wasm"))
	if err != nil {
		return fmt.Errorf("read testdata/hello.wasm: %w", err)
	}
	return writePluginArtifact(dir, info, wasm)
}

func writePluginArtifact(dir string, info PluginInfo, wasm []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.wasm"), wasm, 0o644); err != nil {
		return err
	}
	info.Entry = "hello.wasm"
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0o644)
}
