package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"loopworker/pkg/event"
	"loopworker/pkg/skill"

	lwerrors "loopworker/pkg/errors"
)

func mustSandbox(t *testing.T, config SandboxConfig) *Sandbox {
	t.Helper()

	sb, err := NewSandboxE(config)
	if err != nil {
		t.Fatalf("NewSandboxE: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sb.Close(ctx)
	})
	return sb
}

func TestSandboxLoadPlugin(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("hello"), nil
	})

	if err := s.LoadPlugin("test", plugin); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	if plugins := s.ListPlugins(); len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
}

func TestSandboxLoadDuplicatePlugin(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})

	_ = s.LoadPlugin("test", plugin)
	if err := s.LoadPlugin("test", plugin); !errors.Is(err, lwerrors.ErrPluginLoaded) {
		t.Errorf("expected ErrPluginLoaded, got %v", err)
	}
}

func TestSandboxUnloadPlugin(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})

	_ = s.LoadPlugin("test", plugin)
	if err := s.UnloadPlugin("test"); err != nil {
		t.Fatalf("unload plugin: %v", err)
	}
	if plugins := s.ListPlugins(); len(plugins) != 0 {
		t.Errorf("expected 0 plugins after unload, got %d", len(plugins))
	}
}

func TestSandboxUnloadNonexistentPlugin(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	if err := s.UnloadPlugin("nonexistent"); !errors.Is(err, lwerrors.ErrPluginNotFound) {
		t.Errorf("expected ErrPluginNotFound, got %v", err)
	}
}

func TestSandboxExecute(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
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
	s := mustSandbox(t, SandboxConfig{})
	if _, err := s.Execute(context.Background(), "nonexistent", nil, skill.SkillContext{}); !errors.Is(err, lwerrors.ErrPluginNotFound) {
		t.Errorf("expected ErrPluginNotFound, got %v", err)
	}
}

func TestSandboxExecuteTimeout(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{MaxCPUSeconds: 1})
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
	if !errors.Is(err, lwerrors.ErrSandboxTimeout) {
		t.Errorf("expected ErrSandboxTimeout, got %v", err)
	}
}

func TestSandboxExecuteOutputLimit(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{MaxOutputMB: 1})
	plugin := NewMockPlugin("big", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return make([]byte, 2*1024*1024), nil
	})
	_ = s.LoadPlugin("big", plugin)

	_, err := s.Execute(context.Background(), "big", nil, skill.SkillContext{})
	if !errors.Is(err, lwerrors.ErrSandboxOversized) {
		t.Errorf("expected ErrSandboxOversized, got %v", err)
	}
}

func TestSandboxConcurrentAccess(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	plugin := NewMockPlugin("counter", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("ok"), nil
	})
	_ = s.LoadPlugin("counter", plugin)

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			if _, err := s.Execute(context.Background(), "counter", nil, skill.SkillContext{}); err != nil {
				t.Errorf("concurrent execute: %v", err)
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestSandboxGetStats(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
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
	s := mustSandbox(t, SandboxConfig{MaxConcurrent: 2})
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
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("expected the concurrency limit to serialise work, took %v", elapsed)
	}
}

// TestSandboxHostFunctions exercises env.host_http_request end to end: the
// allowlist must let an approved host through and refuse everything else.
func TestSandboxHostFunctions(t *testing.T) {
	const serverOutput = "hello from host server"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, serverOutput)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}

	allowed := mustSandbox(t, SandboxConfig{AllowedHosts: []string{u.Host}})
	plugin, err := allowed.NewWasmPlugin(context.Background(), "fetch-allowed", "1.0", fetchWasm(ts.URL))
	if err != nil {
		t.Fatalf("new wasm plugin: %v", err)
	}
	if err := allowed.LoadPlugin("fetch-allowed", plugin); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := allowed.Execute(context.Background(), "fetch-allowed", nil, skill.SkillContext{}); err != nil {
		t.Errorf("allowed host must succeed, got %v", err)
	}

	blocked := mustSandbox(t, SandboxConfig{AllowedHosts: []string{"only.this.host"}})
	blockedPlugin, err := blocked.NewWasmPlugin(context.Background(), "fetch-blocked", "1.0", fetchWasm(ts.URL))
	if err != nil {
		t.Fatalf("new wasm plugin: %v", err)
	}
	if err := blocked.LoadPlugin("fetch-blocked", blockedPlugin); err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = blocked.Execute(context.Background(), "fetch-blocked", nil, skill.SkillContext{})
	if err == nil {
		t.Fatal("a host outside AllowedHosts must not be reachable")
	}
}

// ---- Configuration ----

func TestSandboxDefaultConfig(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	if s.config.MaxMemoryMB != DefaultMaxMemoryMB || s.config.MaxCPUSeconds != DefaultMaxCPUSeconds ||
		s.config.MaxOutputMB != DefaultMaxOutputMB || s.config.MaxConcurrent != DefaultMaxConcurrent {
		t.Errorf("expected defaults to be applied, got %+v", s.config)
	}
}

func TestSandboxCustomConfig(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{
		MaxMemoryMB:   512,
		MaxCPUSeconds: 60,
		MaxOutputMB:   128,
		MaxConcurrent: 5,
	})
	if s.config.MaxMemoryMB != 512 || s.config.MaxCPUSeconds != 60 {
		t.Errorf("expected custom values, got %+v", s.config)
	}
}

func TestSandboxSetEventBus(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	if s.bus() != nil {
		t.Error("expected nil eventBus initially")
	}

	bus := event.NewEventBus(nil)
	defer bus.Close()
	s.SetEventBus(bus)

	if s.bus() != bus {
		t.Error("expected eventBus to be set")
	}
}

// ---- Plugin types ----

func TestGoPluginInterface(t *testing.T) {
	handler := func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error {
		data, _ := io.ReadAll(input)
		output.Write(data)
		return nil
	}

	p := NewGoPlugin("echo-go", "1.0", handler)
	if p.Name() != "echo-go" || p.Version() != "1.0" {
		t.Errorf("unexpected identity %q %q", p.Name(), p.Version())
	}
	if p.RequiredSkills() != nil {
		t.Errorf("expected nil RequiredSkills, got %v", p.RequiredSkills())
	}

	output, err := p.Execute(context.Background(), []byte("hello"), skill.SkillContext{})
	if err != nil {
		t.Fatalf("GoPlugin.Execute failed: %v", err)
	}
	if string(output) != "hello" {
		t.Errorf("expected 'hello', got '%s'", string(output))
	}
}

func TestGoPluginError(t *testing.T) {
	handler := func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error {
		return fmt.Errorf("handler error")
	}

	p := NewGoPlugin("fail", "1.0", handler)
	if _, err := p.Execute(context.Background(), []byte("test"), skill.SkillContext{}); err == nil {
		t.Error("expected error from handler")
	}
}

func TestMockPluginWithSkills(t *testing.T) {
	p := NewMockPluginWithSkills("skilled", "1.0", []string{"llm.chat", "research.anomaly"},
		func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
			return input, nil
		})

	skills := p.RequiredSkills()
	if len(skills) != 2 || skills[0] != "llm.chat" {
		t.Errorf("expected ['llm.chat', 'research.anomaly'], got %v", skills)
	}
}

func TestMockPluginWithoutSkills(t *testing.T) {
	p := NewMockPlugin("plain", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})
	if p.RequiredSkills() != nil {
		t.Error("expected nil RequiredSkills for basic MockPlugin")
	}
}

func TestMockPluginNameVersion(t *testing.T) {
	p := NewMockPlugin("mock", "1.5", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	})
	if p.Name() != "mock" || p.Version() != "1.5" {
		t.Errorf("unexpected identity %q %q", p.Name(), p.Version())
	}
}

func TestGoPluginNameVersion(t *testing.T) {
	p := NewGoPlugin("my-go", "2.0", func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error {
		return nil
	})
	if p.Name() != "my-go" || p.Version() != "2.0" {
		t.Errorf("unexpected identity %q %q", p.Name(), p.Version())
	}
}

// ---- WasmPlugin ----

func TestWasmPluginInterface(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	p, err := sb.NewWasmPlugin(ctx, "test-wasm", "2.0", minMemoryWasm(1))
	if err != nil {
		t.Fatalf("NewWasmPlugin failed: %v", err)
	}
	if p.Name() != "test-wasm" || p.Version() != "2.0" {
		t.Errorf("unexpected identity %q %q", p.Name(), p.Version())
	}
	if p.RequiredSkills() != nil {
		t.Error("expected nil RequiredSkills")
	}
	if p.Limits().MemoryMB != DefaultMaxMemoryMB {
		t.Errorf("expected sandbox defaults as the plugin budget, got %+v", p.Limits())
	}
}

func TestWasmPluginInvalidBytes(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	_, err := sb.NewWasmPlugin(ctx, "bad", "1.0", []byte("not wasm"))
	if err == nil {
		t.Fatal("expected error for invalid WASM bytes")
	}
	var panicErr *PluginPanicError
	if errors.As(err, &panicErr) {
		t.Errorf("a bad artifact is not a plugin panic: %v", err)
	}
}

func TestWasmPluginExecute(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	p, err := sb.NewWasmPlugin(ctx, "quiet", "1.0", minMemoryWasm(1))
	if err != nil {
		t.Fatalf("NewWasmPlugin: %v", err)
	}
	if err := sb.LoadPlugin("quiet", p); err != nil {
		t.Fatalf("load: %v", err)
	}

	output, err := sb.Execute(ctx, "quiet", nil, skill.SkillContext{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(output) != 0 {
		t.Errorf("expected no output, got %q", output)
	}
}

func TestWasmPluginNameRequired(t *testing.T) {
	if _, err := NewWasmPlugin(context.Background(), WasmPluginConfig{Wasm: minMemoryWasm(1)}); !errors.Is(err, ErrManifestInvalid) {
		t.Errorf("expected ErrManifestInvalid for an unnamed plugin, got %v", err)
	}
}

func TestSandboxExecuteContextCancelled(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("ok"), nil
	})
	_ = s.LoadPlugin("test", plugin)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Execute(ctx, "test", nil, skill.SkillContext{}); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSandboxExecuteTimeoutPublishesEvent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventPluginExecuted, 10)

	s := mustSandbox(t, SandboxConfig{MaxCPUSeconds: 1})
	s.SetEventBus(bus)

	plugin := NewMockPlugin("slow", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	_ = s.LoadPlugin("slow", plugin)

	if _, err := s.Execute(context.Background(), "slow", nil, skill.SkillContext{}); !errors.Is(err, lwerrors.ErrSandboxTimeout) {
		t.Errorf("expected timeout, got %v", err)
	}

	select {
	case evt := <-sub.Chan():
		payload := evt.Payload().(event.PluginExecutedPayload)
		if payload.Success {
			t.Error("expected success=false in timeout event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for timeout event")
	}
}

// ---- Event publishing ----

func TestSandboxExecuteSuccessEvent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventPluginExecuted, 10)
	defer bus.Unsubscribe(sub)

	s := mustSandbox(t, SandboxConfig{})
	s.SetEventBus(bus)

	plugin := NewMockPlugin("test", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("result"), nil
	})
	if err := s.LoadPlugin("test", plugin); err != nil {
		t.Fatalf("load: %v", err)
	}

	output, err := s.Execute(context.Background(), "test", []byte("input"), skill.SkillContext{})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if string(output) != "result" {
		t.Errorf("expected 'result', got '%s'", string(output))
	}

	select {
	case evt := <-sub.Chan():
		payload := evt.Payload().(event.PluginExecutedPayload)
		if !payload.Success {
			t.Error("expected success=true in event")
		}
		if payload.PluginID != "test" {
			t.Errorf("expected plugin 'test', got %s", payload.PluginID)
		}
		if payload.InputSize != 5 || payload.OutputSize != 6 {
			t.Errorf("expected sizes 5/6, got %d/%d", payload.InputSize, payload.OutputSize)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for plugin executed event")
	}
}

func TestSandboxExecuteErrorEvent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(event.EventPluginExecuted, 10)
	defer bus.Unsubscribe(sub)

	s := mustSandbox(t, SandboxConfig{})
	s.SetEventBus(bus)

	plugin := NewMockPlugin("fail", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, fmt.Errorf("plugin error")
	})
	_ = s.LoadPlugin("fail", plugin)

	if _, err := s.Execute(context.Background(), "fail", []byte("input"), skill.SkillContext{}); err == nil {
		t.Error("expected error from failing plugin")
	}

	select {
	case evt := <-sub.Chan():
		payload := evt.Payload().(event.PluginExecutedPayload)
		if payload.Success || payload.Error == "" {
			t.Errorf("expected a failed event carrying an error, got %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error event")
	}
}

func TestSandboxGetStatsAfterErrors(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{})
	plugin := NewMockPlugin("fail", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		time.Sleep(time.Millisecond)
		return nil, fmt.Errorf("fail")
	})
	_ = s.LoadPlugin("fail", plugin)

	_, _ = s.Execute(context.Background(), "fail", nil, skill.SkillContext{})

	stats := s.GetStats()
	if stats.ExecutionsFailed != 1 || stats.ExecutionsTotal != 1 {
		t.Errorf("expected 1/1 executions failed/total, got %d/%d", stats.ExecutionsFailed, stats.ExecutionsTotal)
	}
	if stats.AvgExecTime < time.Millisecond || stats.MaxExecTime < time.Millisecond || stats.TotalExecTime < time.Millisecond {
		t.Errorf("expected timings >= 1ms, got %+v", stats)
	}
}
