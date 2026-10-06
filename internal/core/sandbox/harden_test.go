package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"loopworker/pkg/event"
	"loopworker/pkg/skill"
	"loopworker/pkg/utils"

	lwerrors "loopworker/pkg/errors"
)

// Item 1: a panicking plugin must be reported as a panic, immediately, and must
// never be mistaken for a timeout (which self-heal would dutifully retry).
func TestExecutePanickingPluginIsClassifiedAsPanic(t *testing.T) {
	s := mustSandbox(t, SandboxConfig{MaxCPUSeconds: 30})

	crash := NewMockPlugin("crasher", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		panic("plugin exploded")
	})
	if err := s.LoadPlugin("crasher", crash); err != nil {
		t.Fatalf("load: %v", err)
	}

	start := time.Now()
	_, err := s.Execute(context.Background(), "crasher", []byte("in"), skill.SkillContext{})
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("caller blocked for %v; a deterministic panic must return at once, not burn the 30s CPU budget", elapsed)
	}
	if !errors.Is(err, lwerrors.ErrSandboxPanic) {
		t.Fatalf("expected ErrSandboxPanic, got %v", err)
	}
	if errors.Is(err, lwerrors.ErrSandboxTimeout) {
		t.Errorf("panic was misreported as a timeout: %v", err)
	}

	var panicErr *PluginPanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("expected *PluginPanicError, got %#v", err)
	}
	if panicErr.Plugin != "crasher" {
		t.Errorf("expected the plugin name in the error, got %q", panicErr.Plugin)
	}
	if !strings.Contains(err.Error(), "plugin exploded") {
		t.Errorf("expected the panic value in the message, got %v", err)
	}
	var pe *utils.PanicError
	if !errors.As(err, &pe) || !strings.Contains(pe.Stack, "harden_test.go") {
		t.Errorf("expected the panic stack to be preserved, got %#v", pe)
	}
	if !IsNonRetryable(err) {
		t.Error("a plugin panic must be classified non-retryable")
	}
}

// Item 1 (continued): the operator-facing event must say "panicked", not
// "timeout".
func TestPanickingPluginPublishesPanicEvent(t *testing.T) {
	bus := event.NewEventBus(nil)
	defer bus.Close()
	sub := bus.Subscribe(event.EventPluginExecuted, 10)
	defer bus.Unsubscribe(sub)

	s := mustSandbox(t, SandboxConfig{MaxCPUSeconds: 30})
	s.SetEventBus(bus)
	_ = s.LoadPlugin("crasher", NewMockPlugin("crasher", "1.0",
		func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
			panic("boom")
		}))

	_, _ = s.Execute(context.Background(), "crasher", nil, skill.SkillContext{})

	select {
	case evt := <-sub.Chan():
		payload := evt.Payload().(event.PluginExecutedPayload)
		if payload.Success {
			t.Fatal("expected success=false")
		}
		if !strings.Contains(payload.Error, "panicked") {
			t.Errorf("event must name the crash, got %q", payload.Error)
		}
		if strings.Contains(payload.Error, "timeout") {
			t.Errorf("event must not claim a timeout, got %q", payload.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event published")
	}
}

// Item 2: the constructor must not panic on a configuration it cannot honour.
func TestNewSandboxNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewSandbox panicked: %v", r)
		}
	}()

	badConfigs := []SandboxConfig{
		{MaxMemoryMB: 1 << 20}, // beyond the 4 GiB wasm address space
		{MaxConcurrent: -1},    // nonsensical
		{MaxCPUSeconds: -5},    // nonsensical
		{MaxOutputMB: -1},      // nonsensical
	}

	for _, cfg := range badConfigs {
		sb := NewSandbox(cfg)
		if sb == nil {
			t.Fatalf("NewSandbox returned nil for %+v", cfg)
		}
		if sb.Err() == nil {
			t.Errorf("expected Err() to be set for %+v", cfg)
		}

		plugin := NewMockPlugin("x", "1.0", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
			return nil, nil
		})
		if err := sb.LoadPlugin("x", plugin); err == nil {
			t.Errorf("expected LoadPlugin to fail for %+v", cfg)
		}
		if _, err := sb.Execute(context.Background(), "x", nil, skill.SkillContext{}); err == nil {
			t.Errorf("expected Execute to fail for %+v", cfg)
		}
		if _, err := sb.NewWasmPlugin(context.Background(), "w", "1", minMemoryWasm(1)); err == nil {
			t.Errorf("expected NewWasmPlugin to fail for %+v", cfg)
		}
		if err := sb.Close(context.Background()); err != nil {
			t.Errorf("Close of a failed sandbox: %v", err)
		}
	}

	for _, cfg := range badConfigs {
		sb, err := NewSandboxE(cfg)
		if err == nil {
			t.Errorf("expected NewSandboxE to return an error for %+v", cfg)
			_ = sb.Close(context.Background())
		}
		if errors.Is(err, ErrManifestInvalid) {
			t.Errorf("expected a limit error, got %v", err)
		}
	}

	if _, err := NewSandboxE(SandboxConfig{MaxMemoryMB: 1 << 20}); !errors.Is(err, ErrLimitTooLarge) {
		t.Errorf("expected ErrLimitTooLarge, got %v", err)
	}
}

// Item 2 (continued): a plugin whose host environment cannot be built returns
// an error rather than taking the process down.
func TestNewWasmPluginReportsErrorsInsteadOfPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewWasmPlugin panicked: %v", r)
		}
	}()

	sb := mustSandbox(t, SandboxConfig{})

	if _, err := NewWasmPlugin(context.Background(), WasmPluginConfig{
		Name:   "greedy",
		Wasm:   minMemoryWasm(1),
		Limits: WasmLimits{MemoryMB: 1 << 20},
	}); !errors.Is(err, ErrLimitTooLarge) {
		t.Errorf("expected ErrLimitTooLarge for an impossible memory budget, got %v", err)
	}

	if _, err := sb.NewWasmPlugin(context.Background(), "junk", "1", []byte{0xde, 0xad, 0xbe, 0xef}); err == nil {
		t.Error("expected an error for a non-wasm artifact")
	}
}

// Item 3: each plugin gets its own runtime, so two plugins built from the same
// artifact can carry different memory budgets.
func TestPerPluginMemoryLimits(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	// growWasm(64) grows 64 pages (4 MiB) from its 1-page minimum and traps if
	// the runtime refuses the growth.
	tight, err := sb.NewWasmPluginWithLimits(ctx, "tight", "1", growWasm(64), WasmLimits{MemoryMB: 2})
	if err != nil {
		t.Fatalf("tight plugin: %v", err)
	}
	roomy, err := sb.NewWasmPluginWithLimits(ctx, "roomy", "1", growWasm(64), WasmLimits{MemoryMB: 16})
	if err != nil {
		t.Fatalf("roomy plugin: %v", err)
	}
	if got := tight.Limits().MemoryMB; got != 2 {
		t.Errorf("expected the tight plugin to keep its own 2 MiB budget, got %d", got)
	}

	for name, p := range map[string]*WasmPlugin{"tight": tight, "roomy": roomy} {
		if err := sb.LoadPlugin(name, p); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}

	if _, err := sb.Execute(ctx, "roomy", nil, skill.SkillContext{}); err != nil {
		t.Errorf("a 16 MiB plugin must be able to grow 4 MiB, got %v", err)
	}
	if _, err := sb.Execute(ctx, "tight", nil, skill.SkillContext{}); err == nil {
		t.Error("a 2 MiB plugin must NOT be able to grow 4 MiB: the memory limit is not per-plugin")
	}

	// A module that cannot fit inside its own budget is rejected at build time.
	if _, err := sb.NewWasmPluginWithLimits(ctx, "impossible", "1", minMemoryWasm(200), WasmLimits{MemoryMB: 1}); err == nil {
		t.Error("expected an artifact needing 12 MiB to be refused under a 1 MiB budget")
	}
}

// TestNewWasmPluginWithLimitsSandboxIsTheCeiling pins the direction of the
// manifest-vs-host rule.
//
// The manifest wins where it declares LESS: a plugin that asks for a smaller
// budget than the host allows is held to what it asked for. Where it declares
// MORE the host wins, because a plugin asking for more than the operator
// configured must never be handed it - the sandbox is the policy, the manifest
// is a request inside it. That also applies to egress: a manifest may narrow the
// allowlist, never widen it.
func TestNewWasmPluginWithLimitsSandboxIsTheCeiling(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{
		MaxMemoryMB:   16,
		MaxCPUSeconds: 5,
		MaxOutputMB:   4,
		AllowedHosts:  []string{"allowed.example"},
	})

	greedy, err := sb.NewWasmPluginWithLimits(ctx, "greedy", "1", growWasm(2), WasmLimits{
		MemoryMB:      4096,
		MaxCPUSeconds: 3600,
		MaxOutputMB:   512,
		AllowedHosts:  []string{"allowed.example", "smuggled.example"},
	})
	if err != nil {
		t.Fatalf("a plugin asking for more than the host allows must be capped, not refused: %v", err)
	}
	got := greedy.Limits()
	if got.MemoryMB != 16 || got.MaxCPUSeconds != 5 || got.MaxOutputMB != 4 {
		t.Errorf("the host ceiling must win, got %+v", got)
	}
	if len(got.AllowedHosts) != 1 || got.AllowedHosts[0] != "allowed.example" {
		t.Errorf("a manifest may not widen the egress allowlist, got %v", got.AllowedHosts)
	}

	// Declaring less still wins: that is the whole point of per-plugin limits.
	quiet, err := sb.NewWasmPluginWithLimits(ctx, "quiet", "1", minMemoryWasm(1), WasmLimits{
		MemoryMB:      4,
		MaxCPUSeconds: 2,
		MaxOutputMB:   1,
	})
	if err != nil {
		t.Fatalf("build quiet plugin: %v", err)
	}
	if q := quiet.Limits(); q.MemoryMB != 4 || q.MaxCPUSeconds != 2 || q.MaxOutputMB != 1 {
		t.Errorf("a manifest's smaller limits must be honoured, got %+v", q)
	}
}

// TestExecuteAppliesTighterPerPluginOutputCap proves a per-plugin output limit
// survives Sandbox.Execute.
//
// Execute used to hand every plugin the sandbox-wide cap, so a manifest's
// max_output_mb only ever showed up in Limits() - the run itself was capped by
// the host's number. The module writes a bounded 2 MiB, which sits between the
// two caps, so the outcome says which one was applied.
func TestExecuteAppliesTighterPerPluginOutputCap(t *testing.T) {
	ctx := context.Background()
	payload := []byte(strings.Repeat("x", 2<<20))

	cases := []struct {
		name         string
		sandboxMB    int
		pluginMB     int
		wantOverflow bool
	}{
		{name: "plugin cap tighter than sandbox", sandboxMB: 64, pluginMB: 1, wantOverflow: true},
		{name: "sandbox cap tighter than plugin", sandboxMB: 1, pluginMB: 4, wantOverflow: true},
		{name: "both above the payload", sandboxMB: 64, pluginMB: 4, wantOverflow: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := mustSandbox(t, SandboxConfig{MaxOutputMB: tc.sandboxMB, MaxCPUSeconds: 30})

			p, err := sb.NewWasmPluginWithLimits(ctx, "talker", "1", chatterWasm(payload), WasmLimits{MaxOutputMB: tc.pluginMB})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if err := sb.LoadPlugin("talker", p); err != nil {
				t.Fatalf("load: %v", err)
			}

			out, err := sb.Execute(ctx, "talker", nil, skill.SkillContext{})
			if tc.wantOverflow {
				if !errors.Is(err, lwerrors.ErrSandboxOversized) {
					t.Fatalf("expected ErrSandboxOversized, got %v", err)
				}
				wantMB := tc.pluginMB
				if tc.sandboxMB < tc.pluginMB {
					wantMB = tc.sandboxMB
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("%d MB", wantMB)) {
					t.Errorf("the error must report the effective %d MB cap, got %v", wantMB, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("2 MiB under a %d MB cap must succeed, got %v", tc.pluginMB, err)
			}
			if len(out) != len(payload) {
				t.Errorf("expected the full %d byte payload, got %d", len(payload), len(out))
			}
		})
	}
}

// Item 4: MaxCPUSeconds must actually stop a spinning plugin and retire its
// goroutine, not merely stop waiting for it.
func TestSpinningPluginIsTerminatedAndGoroutineGone(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxCPUSeconds: 1})

	p, err := sb.NewWasmPlugin(ctx, "spinner", "1", spinWasm())
	if err != nil {
		t.Fatalf("build plugin: %v", err)
	}
	if err := sb.LoadPlugin("spinner", p); err != nil {
		t.Fatalf("load: %v", err)
	}

	settleGoroutines(t)
	before := runtime.NumGoroutine()

	start := time.Now()
	_, err = sb.Execute(ctx, "spinner", nil, skill.SkillContext{})
	elapsed := time.Since(start)

	if !errors.Is(err, lwerrors.ErrSandboxTimeout) {
		t.Fatalf("expected ErrSandboxTimeout, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("expected termination near the 1s budget, took %v", elapsed)
	}

	waitForGoroutines(t, before, 3*time.Second)

	// The plugin enforces its own budget, so WasmPlugin.Execute itself returns a
	// timeout rather than relying on the sandbox's backstop.
	directDone := make(chan error, 1)
	go func() {
		_, err := p.Execute(ctx, nil, skill.SkillContext{})
		directDone <- err
	}()
	select {
	case err := <-directDone:
		if !errors.Is(err, lwerrors.ErrSandboxTimeout) {
			t.Errorf("expected the plugin to self-terminate with a timeout, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WasmPlugin.Execute did not return: the CPU budget is not enforced in the plugin")
	}

	// Close must return promptly even though the module was killed mid-loop.
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Close(closeCtx); err != nil {
		t.Errorf("Close after a kill: %v", err)
	}
}

// Item 4 (continued): a hostile artifact from a real toolchain is killed too.
func TestSpinningRealWasmPluginIsKilled(t *testing.T) {
	artifact := realWasmArtifact(t, "go-wasi-spin.wasm")
	if artifact == nil {
		t.Skip("testdata/go-wasi-spin.wasm is missing; build it with GOOS=wasip1 GOARCH=wasm")
	}

	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxCPUSeconds: 2, MaxMemoryMB: 512})

	p, err := sb.NewWasmPlugin(ctx, "go-spin", "1", artifact)
	if err != nil {
		t.Fatalf("compile real artifact: %v", err)
	}
	if err := sb.LoadPlugin("go-spin", p); err != nil {
		t.Fatalf("load: %v", err)
	}

	settleGoroutines(t)
	before := runtime.NumGoroutine()

	start := time.Now()
	_, err = p.ExecuteWithOutputLimit(ctx, nil, skill.SkillContext{}, 0)
	t.Logf("real wasm run returned after %v: %v", time.Since(start), err)

	waitForGoroutines(t, before, 5*time.Second)
}

// Item 5: a native plugin that streams past the cap must be stopped at the cap,
// not after it has written everything.
func TestGoPluginOutputCapFailsFast(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxOutputMB: 1})

	const chunk = 64 * 1024
	var written int
	var errAtWrite int

	p := NewGoPlugin("streamer", "1.0", func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error {
		buf := make([]byte, chunk)
		for i := 0; i < 4096; i++ { // 256 MiB if nothing stopped it
			if _, err := output.Write(buf); err != nil {
				errAtWrite = i
				return err
			}
			written++
		}
		return nil
	})
	if err := sb.LoadPlugin("streamer", p); err != nil {
		t.Fatalf("load: %v", err)
	}

	_, err := sb.Execute(ctx, "streamer", nil, skill.SkillContext{})
	if !errors.Is(err, lwerrors.ErrSandboxOversized) {
		t.Fatalf("expected ErrSandboxOversized, got %v", err)
	}
	// A 1 MiB cap and 64 KiB chunks means the handler must stop around chunk 16.
	if errAtWrite == 0 {
		t.Errorf("the handler was never told to stop: it wrote %d chunks", written)
	}
	if written > 24 {
		t.Errorf("output was buffered far past the cap before failing: %d chunks (%d MiB) written",
			written, written*chunk/(1<<20))
	}
}

// Item 5 (continued): a wasm plugin that floods stdout is terminated at the cap
// instead of buffering its whole output.
func TestWasmPluginOutputCapTerminatesModule(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxOutputMB: 1, MaxCPUSeconds: 60})

	p, err := sb.NewWasmPlugin(ctx, "noisy", "1", noisyWasm([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := sb.LoadPlugin("noisy", p); err != nil {
		t.Fatalf("load: %v", err)
	}

	start := time.Now()
	_, err = sb.Execute(ctx, "noisy", nil, skill.SkillContext{})
	elapsed := time.Since(start)

	if !errors.Is(err, lwerrors.ErrSandboxOversized) {
		t.Fatalf("expected ErrSandboxOversized, got %v", err)
	}
	if elapsed > 20*time.Second {
		t.Errorf("oversized output must fail fast, took %v of a 60s budget", elapsed)
	}
	if stats := sb.GetStats(); stats.ExecutionsFailed != 1 {
		t.Errorf("expected the capped run to be counted as failed, got %+v", stats)
	}
}

// Item 6: per-plugin concurrency must not collide on wazero module names.
func TestConcurrentExecutionsOfOneWasmPlugin(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxConcurrent: 8, MaxCPUSeconds: 20})

	p, err := sb.NewWasmPlugin(ctx, "shared-name", "1", minMemoryWasm(1))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := sb.LoadPlugin("shared-name", p); err != nil {
		t.Fatalf("load: %v", err)
	}

	const calls = 32
	var wg sync.WaitGroup
	errs := make([]error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = sb.Execute(ctx, "shared-name", nil, skill.SkillContext{})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d failed: %v", i, err)
		}
	}
	if stats := sb.GetStats(); stats.ExecutionsTotal != calls {
		t.Errorf("expected %d executions recorded, got %+v", calls, stats)
	}
}

func TestConcurrentNativePluginExecutions(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxConcurrent: 4})

	_ = sb.LoadPlugin("echo", NewGoPlugin("echo", "1", func(ctx context.Context, input io.Reader, output io.Writer, skillCtx skill.SkillContext) error {
		_, err := io.Copy(output, input)
		return err
	}))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := sb.Execute(ctx, "echo", []byte("payload"), skill.SkillContext{})
			if err != nil || string(out) != "payload" {
				t.Errorf("execute: out=%q err=%v", out, err)
			}
		}()
	}
	wg.Wait()
}

// Item 6 (continued): unloading must wait for in-flight executions instead of
// pulling the semaphore out from under them.
func TestUnloadWaitsForInFlightExecutions(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	started := make(chan struct{})
	release := make(chan struct{})
	_ = sb.LoadPlugin("busy", NewMockPlugin("busy", "1", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		close(started)
		<-release
		return []byte("done"), nil
	}))

	execDone := make(chan error, 1)
	go func() {
		_, err := sb.Execute(ctx, "busy", nil, skill.SkillContext{})
		execDone <- err
	}()

	<-started

	unloadDone := make(chan error, 1)
	go func() { unloadDone <- sb.UnloadPlugin("busy") }()

	select {
	case err := <-unloadDone:
		t.Fatalf("unload completed while an execution was in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-execDone:
		if err != nil {
			t.Fatalf("in-flight execution failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight execution never finished")
	}

	select {
	case err := <-unloadDone:
		if err != nil {
			t.Fatalf("unload: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unload did not complete after the execution drained")
	}

	if _, err := sb.Execute(ctx, "busy", nil, skill.SkillContext{}); !errors.Is(err, lwerrors.ErrPluginNotFound) {
		t.Errorf("expected ErrPluginNotFound after unload, got %v", err)
	}
}

// A wasm plugin's runtime must be released by unload, and a closed plugin must
// refuse to run rather than resurrecting itself.
func TestUnloadClosesWasmPluginRuntime(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{})

	p, err := sb.NewWasmPlugin(ctx, "closable", "1", minMemoryWasm(1))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := sb.LoadPlugin("closable", p); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := sb.UnloadPlugin("closable"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if _, err := p.Execute(ctx, nil, skill.SkillContext{}); err == nil {
		t.Error("expected a closed plugin to refuse execution")
	}
	if err := p.Close(ctx); err != nil {
		t.Errorf("second Close must be idempotent, got %v", err)
	}
}

// Item 7: SetEventBus must be safe to call while executions are running.
func TestEventBusSwapIsRaceFree(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxConcurrent: 4})

	_ = sb.LoadPlugin("echo", NewMockPlugin("echo", "1", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return []byte("ok"), nil
	}))

	buses := []*event.EventBus{event.NewEventBus(nil), event.NewEventBus(nil)}
	defer func() {
		for _, b := range buses {
			b.Close()
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				sb.SetEventBus(buses[j%len(buses)])
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if _, err := sb.Execute(ctx, "echo", nil, skill.SkillContext{}); err != nil {
					t.Errorf("execute: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Item 7 (continued): statistics must stay consistent under the same load.
func TestStatsAreSynchronized(t *testing.T) {
	ctx := context.Background()
	sb := mustSandbox(t, SandboxConfig{MaxConcurrent: 8})

	_ = sb.LoadPlugin("quick", NewMockPlugin("quick", "1", func(ctx context.Context, input []byte, skillCtx skill.SkillContext) ([]byte, error) {
		return nil, nil
	}))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sb.Execute(ctx, "quick", nil, skill.SkillContext{})
			_ = sb.GetStats()
			_ = sb.ListPlugins()
		}()
	}
	wg.Wait()

	if stats := sb.GetStats(); stats.ExecutionsTotal != 20 {
		t.Errorf("expected 20 recorded executions, got %+v", stats)
	}
}

// settleGoroutines waits for transient goroutines (event bus workers, wazero
// watchers) to drain so a leak check starts from a stable baseline.
func settleGoroutines(t *testing.T) {
	t.Helper()

	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
}

func waitForGoroutines(t *testing.T, baseline int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var current int
	for time.Now().Before(deadline) {
		runtime.GC()
		current = runtime.NumGoroutine()
		if current <= baseline {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("goroutines leaked: baseline %d, still %d after %v", baseline, current, timeout)
}
