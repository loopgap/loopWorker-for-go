package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/skill"
	"go.uber.org/zap"
	"loopworker/pkg/utils"
)

// Plugin is the interface that all sandboxed plugins must implement.
type Plugin interface {
	Name() string
	Version() string
	RequiredSkills() []string
	Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)
}

type SandboxConfig struct {
	MaxMemoryMB   int
	MaxCPUSeconds int
	MaxOutputMB   int
	MaxConcurrent int
	AllowedHosts  []string
}

type SandboxStats struct {
	PluginsLoaded    int
	ExecutionsTotal  int64
	ExecutionsFailed int64
	AvgExecTime      time.Duration
	MaxExecTime      time.Duration
	TotalExecTime    time.Duration
	MemoryUsed       int64
}

type Sandbox struct {
	config     SandboxConfig
	plugins    map[string]Plugin
	pluginSems map[string]chan struct{}
	stats      SandboxStats
	eventBus   *event.EventBus
	mu         sync.RWMutex
	runtime    wazero.Runtime
	logger     *zap.Logger
}

func NewSandbox(config SandboxConfig) *Sandbox {
	if config.MaxMemoryMB == 0 {
		config.MaxMemoryMB = 256
	}
	if config.MaxCPUSeconds == 0 {
		config.MaxCPUSeconds = 30
	}
	if config.MaxOutputMB == 0 {
		config.MaxOutputMB = 64
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 10
	}

	ctx := context.Background()
	rConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(uint32(config.MaxMemoryMB * 1024 * 1024 / 65536)) // 64KB per page

	r := wazero.NewRuntimeWithConfig(ctx, rConfig)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	_, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64 {
			mem := mod.Memory()
			urlBytes, ok := mem.Read(urlPtr, urlLen)
			if !ok {
				return 0
			}
			urlStr := string(urlBytes)

			bodyBytes, ok := mem.Read(bodyPtr, bodyLen)
			if !ok {
				return 0
			}

			parsedURL, err := url.Parse(urlStr)
			if err != nil {
				return 0
			}

			if len(config.AllowedHosts) > 0 {
				allowed := false
				for _, host := range config.AllowedHosts {
					if parsedURL.Host == host {
						allowed = true
						break
					}
				}
				if !allowed {
					return 0
				}
			}

			var req *http.Request
			if len(bodyBytes) > 0 {
				req, err = http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(bodyBytes))
				if err != nil {
					return 0
				}
				req.Header.Set("Content-Type", "application/json")
			} else {
				req, err = http.NewRequestWithContext(ctx, "GET", urlStr, nil)
				if err != nil {
					return 0
				}
			}

			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return 0
			}
			defer resp.Body.Close()

			respBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return 0
			}

			allocFunc := mod.ExportedFunction("alloc")
			if allocFunc == nil {
				return 0
			}

			results, err := allocFunc.Call(ctx, uint64(len(respBytes)))
			if err != nil || len(results) == 0 {
				return 0
			}
			resPtr := uint32(results[0])

			if len(respBytes) > 0 {
				if !mem.Write(resPtr, respBytes) {
					return 0
				}
			}

			return (uint64(resPtr) << 32) | uint64(len(respBytes))
		}).
		Export("host_http_request").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, msgPtr, msgLen uint32) {
			mem := mod.Memory()
			msgBytes, ok := mem.Read(msgPtr, msgLen)
			if !ok {
				return
			}
			logger.Debug("wasm log", zap.String("module", mod.Name()), zap.String("message", string(msgBytes)))
		}).
		Export("host_log").
		Instantiate(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to instantiate host module env: %w", err))
	}

	return &Sandbox{
		config:     config,
		plugins:    make(map[string]Plugin),
		pluginSems: make(map[string]chan struct{}),
		runtime:    r,
		logger:     logger.Named("sandbox"),
	}
}

// SetEventBus sets the event bus for publishing plugin execution events.
func (s *Sandbox) SetEventBus(bus *event.EventBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventBus = bus
}

func (s *Sandbox) LoadPlugin(name string, plugin Plugin) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.plugins[name]; exists {
		return fmt.Errorf("plugin %s already loaded", name)
	}

	s.plugins[name] = plugin
	s.pluginSems[name] = make(chan struct{}, s.config.MaxConcurrent)
	s.stats.PluginsLoaded++
	return nil
}

func (s *Sandbox) UnloadPlugin(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.plugins[name]; !exists {
		return fmt.Errorf("plugin %s not found", name)
	}

	delete(s.plugins, name)
	delete(s.pluginSems, name)
	s.stats.PluginsLoaded--
	return nil
}

func (s *Sandbox) Execute(ctx context.Context, pluginName string, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	s.mu.RLock()
	plugin, exists := s.plugins[pluginName]
	sem, semExists := s.pluginSems[pluginName]
	s.mu.RUnlock()

	if !exists || !semExists {
		return nil, fmt.Errorf("plugin %s not found", pluginName)
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(s.config.MaxCPUSeconds)*time.Second)
	defer cancel()

	type result struct {
		output []byte
		err    error
	}

	ch := make(chan result, 1)
	utils.GoSafe(timeoutCtx, func(innerCtx context.Context) {
		output, err := plugin.Execute(innerCtx, input, skillCtx)
		ch <- result{output, err}
	})

	start := time.Now()

	select {
	case <-timeoutCtx.Done():
		s.mu.Lock()
		s.stats.ExecutionsFailed++
		s.mu.Unlock()

		if s.eventBus != nil {
			if publishErr := s.eventBus.Publish(ctx, event.NewEvent(event.EventPluginExecuted, event.PluginExecutedPayload{
				PluginID:  pluginName,
				Duration:  time.Since(start),
				Success:   false,
				Error:     fmt.Sprintf("timeout after %d seconds", s.config.MaxCPUSeconds),
				InputSize: len(input),
			}, nil)); publishErr != nil {
				// 记录错误但不阻塞执行
				logger.Warn("failed to publish plugin executed event", zap.Error(publishErr))
			}
		}

		return nil, fmt.Errorf("execution timeout after %d seconds", s.config.MaxCPUSeconds)
	case res := <-ch:
		elapsed := time.Since(start)

		if res.err != nil {
			s.mu.Lock()
			s.stats.TotalExecTime += elapsed
			s.stats.ExecutionsTotal++
			s.stats.ExecutionsFailed++
			if elapsed > s.stats.MaxExecTime {
				s.stats.MaxExecTime = elapsed
			}
			s.mu.Unlock()

			if s.eventBus != nil {
				if publishErr := s.eventBus.Publish(ctx, event.NewEvent(event.EventPluginExecuted, event.PluginExecutedPayload{
					PluginID:  pluginName,
					Duration:  elapsed,
					Success:   false,
					Error:     res.err.Error(),
					InputSize: len(input),
				}, nil)); publishErr != nil {
					// 记录错误但不阻塞执行
					logger.Warn("failed to publish plugin executed event", zap.Error(publishErr))
				}
			}

			return nil, fmt.Errorf("plugin execution failed: %w", res.err)
		}

		if len(res.output) > s.config.MaxOutputMB*1024*1024 {
			s.mu.Lock()
			s.stats.TotalExecTime += elapsed
			s.stats.ExecutionsTotal++
			s.stats.ExecutionsFailed++
			if elapsed > s.stats.MaxExecTime {
				s.stats.MaxExecTime = elapsed
			}
			s.mu.Unlock()
			return nil, fmt.Errorf("output exceeds maximum size of %d MB", s.config.MaxOutputMB)
		}

		s.mu.Lock()
		s.stats.TotalExecTime += elapsed
		s.stats.ExecutionsTotal++
		if elapsed > s.stats.MaxExecTime {
			s.stats.MaxExecTime = elapsed
		}
		s.mu.Unlock()

		if s.eventBus != nil {
			if publishErr := s.eventBus.Publish(ctx, event.NewEvent(event.EventPluginExecuted, event.PluginExecutedPayload{
				PluginID:   pluginName,
				Duration:   elapsed,
				Success:    true,
				InputSize:  len(input),
				OutputSize: len(res.output),
			}, nil)); publishErr != nil {
				// 记录错误但不阻塞执行
				logger.Warn("failed to publish plugin executed event", zap.Error(publishErr))
			}
		}

		return res.output, nil
	}
}

func (s *Sandbox) ListPlugins() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.plugins))
	for name := range s.plugins {
		names = append(names, name)
	}
	return names
}

func (s *Sandbox) GetStats() SandboxStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := s.stats.ExecutionsTotal
	var avg time.Duration
	if total > 0 {
		avg = time.Duration(int64(s.stats.TotalExecTime) / total)
	}

	return SandboxStats{
		PluginsLoaded:    len(s.plugins),
		ExecutionsTotal:  total,
		ExecutionsFailed: s.stats.ExecutionsFailed,
		AvgExecTime:      avg,
		MaxExecTime:      s.stats.MaxExecTime,
		TotalExecTime:    s.stats.TotalExecTime,
	}
}

// Runtime returns the inner Wazero runtime, for advanced Wasm plugins.
func (s *Sandbox) Runtime() wazero.Runtime {
	return s.runtime
}

// WasmPlugin implements a true WebAssembly plugin using Wazero.
type WasmPlugin struct {
	name     string
	version  string
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
}

func NewWasmPlugin(ctx context.Context, runtime wazero.Runtime, name, version string, wasmBytes []byte) (*WasmPlugin, error) {
	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to compile wasm module: %w", err)
	}

	return &WasmPlugin{
		name:     name,
		version:  version,
		runtime:  runtime,
		compiled: compiled,
	}, nil
}

func (p *WasmPlugin) Name() string             { return p.name }
func (p *WasmPlugin) Version() string          { return p.version }
func (p *WasmPlugin) RequiredSkills() []string { return nil }

func (p *WasmPlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	stdin := bytes.NewReader(input)

	config := wazero.NewModuleConfig().
		WithStdin(stdin).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithName(p.name)

	mod, err := p.runtime.InstantiateModule(ctx, p.compiled, config)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate module: %w", err)
	}
	defer mod.Close(ctx)

	// In WASI, instantiation runs the _start function automatically
	// So whatever was written to stdout is our output.
	return stdout.Bytes(), nil
}

// GoPlugin is a native Go plugin with a stream-based handler.
type GoPlugin struct {
	name    string
	version string
	handler func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error
}

// NewGoPlugin creates a GoPlugin with the given handler.
// The handler receives input as a Reader, output as a Writer, and the skill context.
func NewGoPlugin(name, version string, handler func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error) *GoPlugin {
	return &GoPlugin{
		name:    name,
		version: version,
		handler: handler,
	}
}

func (p *GoPlugin) Name() string             { return p.name }
func (p *GoPlugin) Version() string          { return p.version }
func (p *GoPlugin) RequiredSkills() []string { return nil }

func (p *GoPlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	inputReader := io.NopCloser(bytes.NewReader(input))

	var output bytes.Buffer
	if err := p.handler(ctx, inputReader, &output, skillCtx); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// MockPlugin is a simple in-memory plugin for testing.
type MockPlugin struct {
	name    string
	version string
	handler func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)
	skills  []string
}

// NewMockPlugin creates a MockPlugin with the given handler function.
// The handler receives the skill context as its third argument.
func NewMockPlugin(name, version string, handler func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)) *MockPlugin {
	return &MockPlugin{
		name:    name,
		version: version,
		handler: handler,
	}
}

// NewMockPluginWithSkills creates a MockPlugin that declares required skills.
func NewMockPluginWithSkills(name, version string, skills []string, handler func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)) *MockPlugin {
	return &MockPlugin{
		name:    name,
		version: version,
		handler: handler,
		skills:  skills,
	}
}

func (p *MockPlugin) Name() string    { return p.name }
func (p *MockPlugin) Version() string { return p.version }
func (p *MockPlugin) RequiredSkills() []string {
	if p.skills == nil {
		return nil
	}
	return p.skills
}

func (p *MockPlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	return p.handler(ctx, input, skillCtx)
}
