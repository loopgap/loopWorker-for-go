package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
	"loopworker/pkg/skill"

	lwerrors "loopworker/pkg/errors"
)

const (
	// maxStderrBytes bounds plugin stderr so a chatty failing plugin cannot grow
	// host memory either.
	maxStderrBytes = 64 * 1024
	// shutdownGrace bounds how long closing a terminated module may take.
	shutdownGrace = 2 * time.Second
)

// WasmLimits is the per-plugin resource budget. Every plugin gets a runtime
// built from its own values, so a plugin can never be squeezed by, or squeeze,
// another plugin's budget.
type WasmLimits struct {
	// MemoryMB is the linear memory ceiling for this plugin (64 KiB pages).
	MemoryMB int `json:"memory_mb"`
	// MaxCPUSeconds is the wall-clock budget. Unlike a mere observation, the
	// deadline actually stops wasm execution.
	MaxCPUSeconds int `json:"max_cpu_seconds"`
	// MaxOutputMB caps stdout; crossing it aborts the plugin mid-write.
	MaxOutputMB int `json:"max_output_mb"`
	// AllowedHosts is the egress allowlist for env.host_http_request. An empty
	// list means the plugin can reach nothing.
	AllowedHosts []string `json:"allowed_hosts"`
}

func (l WasmLimits) withDefaults() WasmLimits {
	if l.MemoryMB == 0 {
		l.MemoryMB = DefaultMaxMemoryMB
	}
	if l.MaxCPUSeconds == 0 {
		l.MaxCPUSeconds = DefaultMaxCPUSeconds
	}
	if l.MaxOutputMB == 0 {
		l.MaxOutputMB = DefaultMaxOutputMB
	}
	return l
}

// WasmPluginConfig describes a wasm plugin: identity, artifact and budget.
type WasmPluginConfig struct {
	Name    string
	Version string
	Wasm    []byte
	Limits  WasmLimits
}

// WasmPlugin runs a .wasm artifact in a wazero runtime dedicated to it.
// Create one with NewWasmPlugin; the plugin owns that runtime and Close
// releases it.
type WasmPlugin struct {
	name      string
	version   string
	limits    WasmLimits
	runtime   wazero.Runtime
	compiled  wazero.CompiledModule
	seq       atomic.Uint64
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

// NewWasmPlugin compiles wasm in a runtime created for this plugin alone.
//
// An invalid artifact or an impossible limit is returned as an error — nothing
// in this package panics.
func NewWasmPlugin(ctx context.Context, cfg WasmPluginConfig) (*WasmPlugin, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("%w: plugin name is required", ErrManifestInvalid)
	}
	limits := cfg.Limits.withDefaults()

	pages, err := memoryPages(limits.MemoryMB)
	if err != nil {
		return nil, err
	}

	rt, err := newWasmRuntime(ctx, pages, wasmEnvConfig{
		AllowedHosts:     limits.AllowedHosts,
		MaxResponseBytes: int64(limits.MaxOutputMB) * 1024 * 1024,
	})
	if err != nil {
		return nil, err
	}

	compiled, err := rt.CompileModule(ctx, cfg.Wasm)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("compile wasm module %q: %w", cfg.Name, err)
	}

	return &WasmPlugin{
		name:     cfg.Name,
		version:  cfg.Version,
		limits:   limits,
		runtime:  rt,
		compiled: compiled,
	}, nil
}

// NewWasmPlugin compiles a wasm artifact into a plugin carrying this sandbox's
// limits. Each plugin gets its own runtime, so its memory and CPU budgets are
// genuinely per-plugin instead of inherited from a shared runtime.
//
// The returned plugin must be registered with LoadPlugin and is closed by
// UnloadPlugin.
func (s *Sandbox) NewWasmPlugin(ctx context.Context, name, version string, wasmBytes []byte) (*WasmPlugin, error) {
	if err := s.Err(); err != nil {
		return nil, err
	}
	return NewWasmPlugin(ctx, WasmPluginConfig{
		Name:    name,
		Version: version,
		Wasm:    wasmBytes,
		Limits: WasmLimits{
			MemoryMB:      s.config.MaxMemoryMB,
			MaxCPUSeconds: s.config.MaxCPUSeconds,
			MaxOutputMB:   s.config.MaxOutputMB,
			AllowedHosts:  s.config.AllowedHosts,
		},
	})
}

// NewWasmPluginWithLimits compiles a wasm artifact with a per-plugin budget.
//
// The direction of the override is the whole contract, so it is spelled out:
//
//   - A limit the plugin declares SMALLER than the sandbox's wins. Asking for
//     less is how a plugin limits itself, and the operator's number stays a
//     ceiling the plugin chose to go under.
//   - A limit the plugin declares LARGER than the sandbox's is clamped down to
//     the sandbox's. The sandbox is the operator's policy; a plugin asking for
//     more than the host allows must not get it, so the request is honoured
//     only as far as the host agrees. (It is clamped, not refused: the plugin
//     still runs, just with the budget the host is willing to give it. The
//     strict variant, LoadWasmDir, rejects instead - see its LoadOptions.)
//   - A zero or absent limit inherits the sandbox's.
//
// The egress allowlist follows the same rule by intersection: a manifest may
// narrow the host's list but never widen it.
func (s *Sandbox) NewWasmPluginWithLimits(ctx context.Context, name, version string, wasmBytes []byte, limits WasmLimits) (*WasmPlugin, error) {
	if err := s.Err(); err != nil {
		return nil, err
	}
	return NewWasmPlugin(ctx, WasmPluginConfig{
		Name:    name,
		Version: version,
		Wasm:    wasmBytes,
		Limits:  s.resolveWasmLimits(limits),
	})
}

// resolveWasmLimits overlays a plugin's declared limits on the sandbox's
// configuration. It is the single place where the two are combined, so every
// entry point gets the same answer.
//
// This is the lenient path: a manifest that asks for more than the sandbox
// allows is clamped down to the ceiling. loader.go's resolveLimits is the strict
// one and refuses instead. Both are intentional; see the comment there.
func (s *Sandbox) resolveWasmLimits(limits WasmLimits) WasmLimits {
	return WasmLimits{
		MemoryMB:      clampToCeiling(limits.MemoryMB, s.config.MaxMemoryMB),
		MaxCPUSeconds: clampToCeiling(limits.MaxCPUSeconds, s.config.MaxCPUSeconds),
		MaxOutputMB:   clampToCeiling(limits.MaxOutputMB, s.config.MaxOutputMB),
		AllowedHosts:  narrowHosts(limits.AllowedHosts, s.config.AllowedHosts),
	}
}

// clampToCeiling returns want when it is declared and at most ceiling, and
// ceiling otherwise. want <= 0 means "not declared".
func clampToCeiling(want, ceiling int) int {
	if want <= 0 || want > ceiling {
		return ceiling
	}
	return want
}

// narrowHosts intersects a plugin's requested hosts with the host's. An empty
// result means no egress at all: naming a host the operator never allowed must
// not grant a request.
func narrowHosts(want, ceiling []string) []string {
	if len(want) == 0 {
		return ceiling
	}
	var kept []string
	for _, host := range want {
		for _, allowed := range ceiling {
			if host == allowed {
				kept = append(kept, host)
				break
			}
		}
	}
	return kept
}

func (p *WasmPlugin) Name() string             { return p.name }
func (p *WasmPlugin) Version() string          { return p.version }
func (p *WasmPlugin) RequiredSkills() []string { return nil }

// Limits reports the budget this plugin's runtime was built with.
func (p *WasmPlugin) Limits() WasmLimits { return p.limits }

// Close releases the plugin's runtime. Sandbox.UnloadPlugin calls it once the
// plugin has drained.
func (p *WasmPlugin) Close(ctx context.Context) error {
	if !p.closed.CompareAndSwap(false, true) {
		return p.closeErr
	}
	p.closeOnce.Do(func() { p.closeErr = p.runtime.Close(ctx) })
	return p.closeErr
}

// Execute runs the plugin with its own CPU budget and output cap.
func (p *WasmPlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	return p.ExecuteWithOutputLimit(ctx, input, skillCtx, 0)
}

// ExecuteWithOutputLimit runs the plugin, capping stdout at maxOutputBytes when
// it is positive (otherwise p.limits.MaxOutputMB applies). Crossing the cap
// cancels the call, which terminates the module instead of letting it finish
// buffering.
//
// A WASI command plugin produces its output through stdout: instantiation runs
// its _start function.
func (p *WasmPlugin) ExecuteWithOutputLimit(ctx context.Context, input []byte, _ skill.SkillContext, maxOutputBytes int64) ([]byte, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("%w: plugin %q is closed", lwerrors.ErrPluginNotFound, p.name)
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = int64(p.limits.MaxOutputMB) * 1024 * 1024
	}

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if p.limits.MaxCPUSeconds > 0 {
		budgetCtx, budgetCancel := context.WithTimeout(callCtx, time.Duration(p.limits.MaxCPUSeconds)*time.Second)
		defer budgetCancel()
		callCtx = budgetCtx
	}

	stdout := newCappedWriter(maxOutputBytes, cancel)
	stderr := newCappedWriter(maxStderrBytes, nil)

	// Concurrent executions of one plugin must not collide on the module name,
	// which is why the name is unique per call rather than the plugin name.
	moduleName := fmt.Sprintf("%s-%d", p.name, p.seq.Add(1))

	mod, err := p.runtime.InstantiateModule(callCtx, p.compiled, wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(input)).
		WithStdout(stdout).
		WithStderr(stderr).
		WithName(moduleName))
	if mod != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), shutdownGrace)
		_ = mod.Close(closeCtx)
		closeCancel()
	}

	if stdout.didOverflow() {
		// Report the cap that actually bit, which is the caller's when it is
		// tighter than this plugin's own budget.
		return nil, fmt.Errorf("%w: max %d MB", lwerrors.ErrSandboxOversized, maxOutputBytes/(1<<20))
	}
	if err != nil {
		return nil, p.classify(callCtx, err)
	}

	return stdout.Bytes(), nil
}

// classify turns a wazero failure into the sandbox's error taxonomy: a killed
// loop is a timeout, a crash is a panic (never a timeout), and anything else is
// an ordinary execution error.
func (p *WasmPlugin) classify(callCtx context.Context, err error) error {
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case sys.ExitCodeDeadlineExceeded:
			return fmt.Errorf("%w: after %d seconds", lwerrors.ErrSandboxTimeout, p.limits.MaxCPUSeconds)
		case sys.ExitCodeContextCanceled:
			if cause := callCtx.Err(); cause != nil {
				return cause
			}
			return fmt.Errorf("%w: %v", lwerrors.ErrTaskCancelled, err)
		}
		// A non-zero exit or a trap is a deterministic crash: report it as one
		// so callers do not retry it and operators do not read it as a timeout.
		return &PluginPanicError{Plugin: p.name, Err: err}
	}
	return fmt.Errorf("execute wasm module %q: %w", p.name, err)
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
	return p.ExecuteWithOutputLimit(ctx, input, skillCtx, 0)
}

// ExecuteWithOutputLimit streams the handler's output through a capped writer:
// a handler that writes past maxOutputBytes gets a write error instead of an
// unbounded buffer, and its context is cancelled.
func (p *GoPlugin) ExecuteWithOutputLimit(ctx context.Context, input []byte, skillCtx skill.SkillContext, maxOutputBytes int64) ([]byte, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdout := newCappedWriter(maxOutputBytes, cancel)

	err := p.handler(callCtx, bytes.NewReader(input), stdout, skillCtx)
	if stdout.didOverflow() {
		return nil, fmt.Errorf("%w: max %d bytes", lwerrors.ErrSandboxOversized, maxOutputBytes)
	}
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
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

// Execute calls the handler. Because the whole output is returned as a slice,
// an oversized result can only be rejected after the fact by Sandbox.Execute.
func (p *MockPlugin) Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	return p.handler(ctx, input, skillCtx)
}
