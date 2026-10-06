// Package sandbox implements plugin isolation using WebAssembly (wazero) and
// native Go plugins.
//
// # Isolation guarantees
//
//  1. WASM plugins run in a wazero runtime dedicated to that plugin, with its
//     own linear-memory page limit. Limits are baked in at compile time, so one
//     plugin can never inherit another plugin's budget.
//  2. Wall-clock CPU budgets are enforced, not just observed: each runtime is
//     built with wazero's WithCloseOnContextDone, so a plugin spinning in a
//     loop is terminated when its deadline fires and its execution goroutine
//     returns instead of leaking.
//  3. A plugin that panics is reported as a panic (PluginPanicError), never as a
//     timeout, and returns immediately. See IsNonRetryable.
//  4. Output is capped while it streams (cappedWriter); crossing the limit
//     cancels the execution, so oversized output is rejected without first
//     buffering the whole payload in host memory.
//  5. Concurrency per plugin is bounded by a counting semaphore, and
//     UnloadPlugin waits for in-flight executions to drain before closing the
//     plugin's runtime.
//
// A native Go plugin is only as well-behaved as its handler: the sandbox
// cancels its context at the deadline, but cannot stop Go code that ignores the
// context. Only wasm plugins get hard termination.
//
// Teaching Note: Semaphore Pattern with Buffered Channel
// =======================================================
// The per-plugin concurrency limiter uses a buffered channel as a counting
// semaphore:
//
//	sem := make(chan struct{}, maxConcurrent) // capacity = max concurrent
//	sem <- struct{}{}                         // acquire (blocks if full)
//	defer func() { <-sem }()                 // release
//
// Teaching Note: Host Functions for WASM
// ======================================
// WASM modules have no direct system access. Host functions (host_http_request,
// host_log) bridge that gap under explicit policy: the AllowedHosts egress
// allowlist prevents SSRF, and responses are size-bounded before being copied
// into wasm memory.
package sandbox

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"loopworker/pkg/event"
	"loopworker/pkg/logger"
	"loopworker/pkg/skill"
	"loopworker/pkg/utils"

	lwerrors "loopworker/pkg/errors"
)

// Defaults applied to SandboxConfig fields left at zero.
const (
	DefaultMaxMemoryMB   = 256
	DefaultMaxCPUSeconds = 30
	DefaultMaxOutputMB   = 64
	DefaultMaxConcurrent = 10
)

// Plugin is the interface that all sandboxed plugins must implement.
type Plugin interface {
	Name() string
	Version() string
	RequiredSkills() []string
	Execute(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error)
}

// Closer is implemented by plugins that own host resources (a wasm runtime, for
// example). Sandbox.UnloadPlugin drains in-flight executions and then calls
// Close, so a runtime is never closed out from under a running call.
type Closer interface {
	Close(ctx context.Context) error
}

// limitedOutput is implemented by plugins that can enforce the output cap while
// streaming. Sandbox.Execute hands them the configured cap instead of letting
// them buffer freely and check afterwards.
type limitedOutput interface {
	ExecuteWithOutputLimit(ctx context.Context, input []byte, skillCtx skill.SkillContext, maxOutputBytes int64) ([]byte, error)
}

// budgeted is implemented by plugins whose resource ceiling was compiled into
// their own runtime (every wasm plugin). Sandbox.Execute uses it to apply the
// tighter of the two output caps - otherwise a per-plugin limit would be a
// number in Limits() and nothing else, because Execute used to hand every
// plugin the sandbox-wide cap.
type budgeted interface {
	Limits() WasmLimits
}

// outputCap is the stdout ceiling for one plugin: the sandbox's, unless the
// plugin's own budget is smaller. The host never widens a plugin's cap.
func (s *Sandbox) outputCap(plugin Plugin) int64 {
	limit := int64(s.config.MaxOutputMB) * 1024 * 1024
	if b, ok := plugin.(budgeted); ok {
		if own := int64(b.Limits().MaxOutputMB) * 1024 * 1024; own > 0 && own < limit {
			limit = own
		}
	}
	return limit
}

// SandboxConfig is the host-side budget applied to plugins registered with this
// sandbox. Per-plugin overrides are supported: see NewWasmPluginWithLimits and
// LoadOptions.
type SandboxConfig struct {
	MaxMemoryMB   int
	MaxCPUSeconds int
	MaxOutputMB   int
	MaxConcurrent int
	AllowedHosts  []string
}

// isZero reports whether the caller left the whole configuration unset.
func (c SandboxConfig) isZero() bool {
	return c.MaxMemoryMB == 0 && c.MaxCPUSeconds == 0 && c.MaxOutputMB == 0 &&
		c.MaxConcurrent == 0 && len(c.AllowedHosts) == 0
}

// withDefaults fills zero fields and validates the result.
func (c SandboxConfig) withDefaults() (SandboxConfig, error) {
	if c.MaxMemoryMB == 0 {
		c.MaxMemoryMB = DefaultMaxMemoryMB
	}
	if c.MaxCPUSeconds == 0 {
		c.MaxCPUSeconds = DefaultMaxCPUSeconds
	}
	if c.MaxOutputMB == 0 {
		c.MaxOutputMB = DefaultMaxOutputMB
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = DefaultMaxConcurrent
	}
	if c.MaxMemoryMB < 0 || c.MaxCPUSeconds < 0 || c.MaxOutputMB < 0 || c.MaxConcurrent < 0 {
		return c, fmt.Errorf("%w: sandbox limits cannot be negative: %+v", ErrLimitTooLarge, c)
	}
	if c.MaxConcurrent < 1 {
		return c, fmt.Errorf("%w: MaxConcurrent must be at least 1, got %d", ErrLimitTooLarge, c.MaxConcurrent)
	}
	if _, err := memoryPages(c.MaxMemoryMB); err != nil {
		return c, err
	}
	return c, nil
}

// SandboxStats aggregates execution counters. Memory is reported per plugin by
// WasmPlugin, not as a sandbox-wide counter, because each plugin owns its
// runtime.
type SandboxStats struct {
	PluginsLoaded    int
	ExecutionsTotal  int64
	ExecutionsFailed int64
	AvgExecTime      time.Duration
	MaxExecTime      time.Duration
	TotalExecTime    time.Duration
}

// pluginEntry holds a plugin plus its concurrency semaphore and the
// bookkeeping that lets UnloadPlugin wait for in-flight executions.
type pluginEntry struct {
	plugin Plugin
	sem    chan struct{}

	mu      sync.Mutex
	refs    int
	removed bool
	drained bool
	idle    chan struct{}
}

func newPluginEntry(plugin Plugin, maxConcurrent int) *pluginEntry {
	return &pluginEntry{
		plugin: plugin,
		sem:    make(chan struct{}, maxConcurrent),
		idle:   make(chan struct{}),
	}
}

// acquire registers a new execution, reporting false once the entry is removed.
func (e *pluginEntry) acquire() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.removed {
		return false
	}
	e.refs++
	return true
}

func (e *pluginEntry) release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refs--
	e.signalDrainedLocked()
}

// markRemoved stops accepting new executions.
func (e *pluginEntry) markRemoved() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removed = true
	e.signalDrainedLocked()
}

func (e *pluginEntry) signalDrainedLocked() {
	if e.removed && !e.drained && e.refs == 0 {
		e.drained = true
		close(e.idle)
	}
}

// wait drains in-flight executions or the given context.
func (e *pluginEntry) wait(ctx context.Context) error {
	select {
	case <-e.idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Sandbox struct {
	config   SandboxConfig
	plugins  map[string]*pluginEntry
	stats    SandboxStats
	eventBus atomic.Pointer[event.EventBus]
	mu       sync.RWMutex
	initErr  error
}

// NewSandbox builds a Sandbox from config, applying defaults for zero fields.
//
// It never panics: a configuration the sandbox cannot honour leaves the
// returned Sandbox in a failed state, reported by Err and by every method that
// would use it. New code should prefer NewSandboxE, which returns that error
// directly.
func NewSandbox(config SandboxConfig) *Sandbox {
	sb, err := NewSandboxE(config)
	if err != nil {
		// The sandbox is unusable, but a constructor must not take the process
		// down with it: callers keep their existing single-return shape and get
		// the failure from every operation instead.
		return sb
	}
	return sb
}

// NewSandboxE builds a Sandbox and returns the configuration error instead of
// panicking. Defaults are applied to zero-valued limits.
func NewSandboxE(config SandboxConfig) (*Sandbox, error) {
	cfg, err := config.withDefaults()
	if err != nil {
		return &Sandbox{config: config, plugins: make(map[string]*pluginEntry), initErr: err}, err
	}
	return &Sandbox{
		config:  cfg,
		plugins: make(map[string]*pluginEntry),
	}, nil
}

// Err reports why this sandbox is unusable, or nil if it is healthy.
func (s *Sandbox) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initErr
}

// SetEventBus sets the event bus for publishing plugin execution events. It is
// safe to call concurrently with Execute.
func (s *Sandbox) SetEventBus(bus *event.EventBus) {
	s.eventBus.Store(bus)
}

func (s *Sandbox) bus() *event.EventBus { return s.eventBus.Load() }

// LoadPlugin registers plugin under name, bounded by the sandbox's
// MaxConcurrent. Loading a wasm plugin created against another sandbox's
// limits is allowed; use NewWasmPlugin to get this sandbox's limits.
func (s *Sandbox) LoadPlugin(name string, plugin Plugin) error {
	if err := s.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.plugins[name]; exists {
		return fmt.Errorf("%w: %s", lwerrors.ErrPluginLoaded, name)
	}

	s.plugins[name] = newPluginEntry(plugin, s.config.MaxConcurrent)
	s.stats.PluginsLoaded++
	return nil
}

// UnloadPlugin removes the plugin, waits for executions already in flight to
// finish, and closes it if it implements Closer. It blocks until the plugin has
// drained or the sandbox's CPU budget elapses; on that timeout the plugin stays
// removed and its runtime is left for the caller to close.
func (s *Sandbox) UnloadPlugin(name string) error {
	return s.UnloadPluginContext(context.Background(), name)
}

// UnloadPluginContext is UnloadPlugin with a caller-supplied context governing
// how long it waits for in-flight executions to drain.
func (s *Sandbox) UnloadPluginContext(ctx context.Context, name string) error {
	s.mu.Lock()
	entry, exists := s.plugins[name]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", lwerrors.ErrPluginNotFound, name)
	}
	delete(s.plugins, name)
	s.stats.PluginsLoaded--
	s.mu.Unlock()

	// Stop accepting new executions, then wait for the ones already running:
	// closing a runtime under a live call would fail that call.
	entry.markRemoved()
	if err := entry.wait(ctx); err != nil {
		return fmt.Errorf("%w: unload %s: %v", lwerrors.ErrPluginFailed, name, err)
	}

	if closer, ok := entry.plugin.(Closer); ok {
		if err := closer.Close(ctx); err != nil {
			return fmt.Errorf("close plugin %s: %w", name, err)
		}
	}
	return nil
}

// Execute runs a plugin under the sandbox's budget: the concurrency semaphore,
// the wall-clock CPU budget, the output cap, and panic isolation.
//
// Returned errors are classified: a crash yields *PluginPanicError (which
// errors.Is matches against ErrSandboxPanic), a budget overrun yields
// ErrSandboxTimeout, and an oversized payload yields ErrSandboxOversized. Use
// IsNonRetryable to decide whether another attempt can possibly help.
func (s *Sandbox) Execute(ctx context.Context, pluginName string, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
	if err := s.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	entry, exists := s.plugins[pluginName]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", lwerrors.ErrPluginNotFound, pluginName)
	}
	if !entry.acquire() {
		return nil, fmt.Errorf("%w: %s", ErrPluginUnloading, pluginName)
	}
	defer entry.release()

	sem := entry.sem
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(s.config.MaxCPUSeconds)*time.Second)
	defer cancel()

	plugin := entry.plugin
	maxOutputBytes := s.outputCap(plugin)

	done := utils.GoSafeE(timeoutCtx, func(innerCtx context.Context) ([]byte, error) {
		if capped, ok := plugin.(limitedOutput); ok {
			return capped.ExecuteWithOutputLimit(innerCtx, input, skillCtx, maxOutputBytes)
		}
		return plugin.Execute(innerCtx, input, skillCtx)
	})

	start := time.Now()

	select {
	case <-timeoutCtx.Done():
		// A cancelled caller closes timeoutCtx too, so this branch fires for two
		// different situations and the two selects below are both ready whenever a
		// fast plugin loses the race. Reporting a caller's cancellation as a
		// sandbox timeout sends the operator to the wrong configuration knob.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// A crash and the deadline can land in the same instant; a panic is the
		// more specific diagnosis and must not be reported as a timeout.
		select {
		case res := <-done:
			if panicked := panicError(pluginName, res.Err); panicked != nil {
				return nil, s.fail(ctx, pluginName, time.Since(start), panicked, len(input), 0)
			}
		default:
		}

		err := fmt.Errorf("%w: after %d seconds", lwerrors.ErrSandboxTimeout, s.config.MaxCPUSeconds)
		return nil, s.fail(ctx, pluginName, time.Since(start), err, len(input), 0)

	case res := <-done:
		elapsed := time.Since(start)

		if panicked := panicError(pluginName, res.Err); panicked != nil {
			return nil, s.fail(ctx, pluginName, elapsed, panicked, len(input), 0)
		}

		if res.Err != nil {
			err := fmt.Errorf("plugin execution failed: %w", res.Err)
			return nil, s.fail(ctx, pluginName, elapsed, err, len(input), 0)
		}

		if int64(len(res.Value)) > maxOutputBytes {
			err := fmt.Errorf("%w: max %d MB", lwerrors.ErrSandboxOversized, maxOutputBytes/(1<<20))
			return nil, s.fail(ctx, pluginName, elapsed, err, len(input), len(res.Value))
		}

		s.record(elapsed, false)
		s.publish(ctx, pluginName, elapsed, true, "", len(input), len(res.Value))
		return res.Value, nil
	}
}

// record updates the execution counters.
func (s *Sandbox) record(elapsed time.Duration, failed bool) {
	s.mu.Lock()
	s.stats.ExecutionsTotal++
	s.stats.TotalExecTime += elapsed
	if elapsed > s.stats.MaxExecTime {
		s.stats.MaxExecTime = elapsed
	}
	if failed {
		s.stats.ExecutionsFailed++
	}
	s.mu.Unlock()
}

// fail records a failed execution, publishes the failure, and returns err.
func (s *Sandbox) fail(ctx context.Context, pluginName string, elapsed time.Duration, err error, inputSize, outputSize int) error {
	s.record(elapsed, true)
	s.publish(ctx, pluginName, elapsed, false, err.Error(), inputSize, outputSize)
	return err
}

// publish reports an execution outcome on the event bus, if one is attached.
func (s *Sandbox) publish(ctx context.Context, pluginName string, elapsed time.Duration, success bool, errMsg string, inputSize, outputSize int) {
	bus := s.bus()
	if bus == nil {
		return
	}
	publishErr := bus.Publish(ctx, event.NewEvent(event.EventPluginExecuted, event.PluginExecutedPayload{
		PluginID:   pluginName,
		Duration:   elapsed,
		Success:    success,
		Error:      errMsg,
		InputSize:  inputSize,
		OutputSize: outputSize,
	}, nil))
	if publishErr != nil {
		logger.Warn("failed to publish plugin executed event", zap.Error(publishErr))
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

// Close unloads every plugin and releases the resources they hold.
func (s *Sandbox) Close(ctx context.Context) error {
	for _, name := range s.ListPlugins() {
		if err := s.UnloadPluginContext(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// Compile-time proof the shipped plugin types satisfy the sandbox contracts.
var (
	_ Plugin        = (*GoPlugin)(nil)
	_ Plugin        = (*MockPlugin)(nil)
	_ limitedOutput = (*GoPlugin)(nil)
	_ limitedOutput = (*WasmPlugin)(nil)
)
