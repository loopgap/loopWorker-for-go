package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"loopworker/pkg/skill"
)

func TestSandboxLoadPlugin(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("hello"), nil
	})

	if err := s.LoadPlugin("test", plugin); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	plugins := s.ListPlugins()
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
}

func TestSandboxLoadDuplicatePlugin(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})

	_ = s.LoadPlugin("test", plugin)
	if err := s.LoadPlugin("test", plugin); err == nil {
		t.Error("expected error loading duplicate plugin")
	}
}

func TestSandboxUnloadPlugin(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})

	_ = s.LoadPlugin("test", plugin)
	if err := s.UnloadPlugin("test"); err != nil {
		t.Fatalf("unload plugin: %v", err)
	}

	plugins := s.ListPlugins()
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins after unload, got %d", len(plugins))
	}
}

func TestSandboxUnloadNonexistentPlugin(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	if err := s.UnloadPlugin("nonexistent"); err == nil {
		t.Error("expected error unloading nonexistent plugin")
	}
}

func TestSandboxExecute(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	plugin := NewMockPlugin("echo", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return input, nil
	})

	_ = s.LoadPlugin("echo", plugin)

	output, err := s.Execute(context.Background(), "echo", []byte("hello"), skill.SkillContext{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(output) != "hello" {
		t.Errorf("expected 'hello', got '%s'", string(output))
	}
}

func TestSandboxExecuteNonexistentPlugin(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	_, err := s.Execute(context.Background(), "nonexistent", nil, skill.SkillContext{})
	if err == nil {
		t.Error("expected error executing nonexistent plugin")
	}
}

func TestSandboxExecuteTimeout(t *testing.T) {
	s := NewSandbox(SandboxConfig{MaxCPUSeconds: 1})
	plugin := NewMockPlugin("slow", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return []byte("done"), nil
		}
	})

	_ = s.LoadPlugin("slow", plugin)

	_, err := s.Execute(context.Background(), "slow", nil, skill.SkillContext{})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestSandboxExecuteOutputLimit(t *testing.T) {
	s := NewSandbox(SandboxConfig{MaxOutputMB: 1})
	plugin := NewMockPlugin("big", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return make([]byte, 2*1024*1024), nil
	})

	_ = s.LoadPlugin("big", plugin)

	_, err := s.Execute(context.Background(), "big", nil, skill.SkillContext{})
	if err == nil {
		t.Error("expected output size error")
	}
}

func TestSandboxConcurrentAccess(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	plugin := NewMockPlugin("counter", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("ok"), nil
	})

	_ = s.LoadPlugin("counter", plugin)

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, _ = s.Execute(context.Background(), "counter", nil, skill.SkillContext{})
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestSandboxGetStats(t *testing.T) {
	s := NewSandbox(SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("ok"), nil
	})

	_ = s.LoadPlugin("test", plugin)
	_, _ = s.Execute(context.Background(), "test", nil, skill.SkillContext{})

	stats := s.GetStats()
	if stats.PluginsLoaded != 1 {
		t.Errorf("expected 1 plugin loaded, got %d", stats.PluginsLoaded)
	}
	if stats.ExecutionsTotal != 1 {
		t.Errorf("expected 1 execution, got %d", stats.ExecutionsTotal)
	}
}

func TestSandboxMaxConcurrent(t *testing.T) {
	s := NewSandbox(SandboxConfig{MaxConcurrent: 2})
	plugin := NewMockPlugin("slow", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		time.Sleep(100 * time.Millisecond)
		return []byte("ok"), nil
	})

	_ = s.LoadPlugin("slow", plugin)

	done := make(chan bool, 5)
	start := time.Now()
	for i := 0; i < 5; i++ {
		go func() {
			_, _ = s.Execute(context.Background(), "slow", nil, skill.SkillContext{})
			done <- true
		}()
	}

	for i := 0; i < 5; i++ {
		<-done
	}
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Error("expected concurrent execution to take longer")
	}
}

func TestSandboxHostFunctions(t *testing.T) {
	serverOutput := "hello from host server"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(serverOutput))
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}

	wasmBytes := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // Magic & Version
		0x01, 0x0e, 0x02, 0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7e, 0x60, 0x01, 0x7f, 0x01, 0x7f, // Type section
		0x02, 0x19, 0x01, 0x03, 0x65, 0x6e, 0x76, 0x11, 0x68, 0x6f, 0x73, 0x74, 0x5f, 0x68, 0x74, 0x74, 0x70, 0x5f, 0x72, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x00, 0x00, // Import section
		0x03, 0x03, 0x02, 0x01, 0x00, // Func section
		0x05, 0x03, 0x01, 0x00, 0x01, // Memory section
		0x07, 0x1e, 0x03, 0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, 0x02, 0x00, 0x05, 0x61, 0x6c, 0x6c, 0x6f, 0x63, 0x00, 0x01, 0x09, 0x74, 0x65, 0x73, 0x74, 0x5f, 0x68, 0x74, 0x74, 0x70, 0x00, 0x02, // Export section
		0x0a, 0x13, 0x02, 0x04, 0x00, 0x20, 0x00, 0x0b, 0x0c, 0x00, 0x20, 0x00, 0x20, 0x01, 0x20, 0x02, 0x20, 0x03, 0x10, 0x00, 0x0b, // Code section
	}

	s := NewSandbox(SandboxConfig{
		AllowedHosts: []string{u.Host},
	})

	ctx := context.Background()

	p, err := NewWasmPlugin(ctx, s.Runtime(), "test-host-funcs", "1.0", wasmBytes)
	if err != nil {
		t.Fatalf("new wasm plugin: %v", err)
	}

	modConfig := wazero.NewModuleConfig().WithName("test-host-funcs")
	mod, err := s.Runtime().InstantiateModule(ctx, p.compiled, modConfig)
	if err != nil {
		t.Fatalf("instantiate module: %v", err)
	}
	defer mod.Close(ctx)

	urlBytes := []byte(ts.URL)
	if !mod.Memory().Write(0, urlBytes) {
		t.Fatal("failed to write URL to memory")
	}

	testHttpFunc := mod.ExportedFunction("test_http")
	if testHttpFunc == nil {
		t.Fatal("test_http function not found")
	}

	results, err := testHttpFunc.Call(ctx, uint64(0), uint64(len(urlBytes)), uint64(0), uint64(0))
	if err != nil {
		t.Fatalf("call test_http: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected 1 result, got 0")
	}

	packed := results[0]
	if packed == 0 {
		t.Fatal("http request failed (returned 0)")
	}

	resPtr := uint32(packed >> 32)
	resLen := uint32(packed)

	if resPtr != uint32(len(serverOutput)) {
		t.Errorf("expected resPtr to be %d, got %d", len(serverOutput), resPtr)
	}

	respBytes, ok := mod.Memory().Read(resPtr, resLen)
	if !ok {
		t.Fatal("failed to read response from memory")
	}

	if string(respBytes) != serverOutput {
		t.Errorf("expected '%s', got '%s'", serverOutput, string(respBytes))
	}

	s2 := NewSandbox(SandboxConfig{
		AllowedHosts: []string{"google.com"},
	})
	p2, err := NewWasmPlugin(ctx, s2.Runtime(), "test-host-funcs-2", "1.0", wasmBytes)
	if err != nil {
		t.Fatalf("new wasm plugin 2: %v", err)
	}
	modConfig2 := wazero.NewModuleConfig().WithName("test-host-funcs-2")
	mod2, err := s2.Runtime().InstantiateModule(ctx, p2.compiled, modConfig2)
	if err != nil {
		t.Fatalf("instantiate module 2: %v", err)
	}
	defer mod2.Close(ctx)

	if !mod2.Memory().Write(0, urlBytes) {
		t.Fatal("failed to write URL to memory 2")
	}

	testHttpFunc2 := mod2.ExportedFunction("test_http")
	results2, err := testHttpFunc2.Call(ctx, uint64(0), uint64(len(urlBytes)), uint64(0), uint64(0))
	if err != nil {
		t.Fatalf("call test_http 2: %v", err)
	}
	if results2[0] != 0 {
		t.Errorf("expected SSRF block to return 0, got %v", results2[0])
	}
}
