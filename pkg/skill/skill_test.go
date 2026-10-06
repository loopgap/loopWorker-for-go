package skill

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"loopworker/pkg/event"
)

func TestNewSkillRegistry(t *testing.T) {
	r := NewSkillRegistry()
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	if len(r.List()) != 0 {
		t.Errorf("expected empty registry, got %d skills", len(r.List()))
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := NewSkillRegistry()

	def := SkillDefinition{
		Name:        "llm.chat",
		Version:     "1.0.0",
		Description: "LLM structured chat",
		InputTypes:  []string{"text"},
		OutputTypes: []string{"text"},
	}

	// Register without provider
	r.Register(def, nil)

	if !r.Has("llm.chat") {
		t.Error("expected llm.chat to be registered")
	}

	_, exists := r.Get("llm.chat")
	if exists {
		t.Error("expected no provider for llm.chat")
	}

	defs := r.List()
	if len(defs) != 1 {
		t.Errorf("expected 1 skill, got %d", len(defs))
	}
	if defs[0].Name != "llm.chat" {
		t.Errorf("expected skill name 'llm.chat', got '%s'", defs[0].Name)
	}
}

func TestRegisterWithProvider(t *testing.T) {
	r := NewSkillRegistry()

	def := SkillDefinition{Name: "echo", Version: "1.0.0"}
	provider := &mockSkillProvider{def: def}
	r.Register(def, provider)

	p, ok := r.Get("echo")
	if !ok {
		t.Fatal("expected provider to exist")
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}

	gotDef := p.Definition()
	if gotDef.Name != "echo" {
		t.Errorf("expected definition name 'echo', got '%s'", gotDef.Name)
	}
}

func TestCheckDependencies(t *testing.T) {
	r := NewSkillRegistry()
	r.Register(SkillDefinition{Name: "llm.chat", Version: "1.0.0"}, nil)
	r.Register(SkillDefinition{Name: "research.anomaly", Version: "1.0.0"}, nil)

	// All satisfied
	missing := r.CheckDependencies([]string{"llm.chat"})
	if len(missing) != 0 {
		t.Errorf("expected no missing skills, got %v", missing)
	}

	// Some missing
	missing = r.CheckDependencies([]string{"llm.chat", "data.transform"})
	if len(missing) != 1 {
		t.Errorf("expected 1 missing skill, got %d: %v", len(missing), missing)
	}
	if missing[0] != "data.transform" {
		t.Errorf("expected missing 'data.transform', got '%s'", missing[0])
	}

	// All missing
	missing = r.CheckDependencies([]string{"a", "b", "c"})
	if len(missing) != 3 {
		t.Errorf("expected 3 missing skills, got %d", len(missing))
	}

	// Empty required
	missing = r.CheckDependencies(nil)
	if len(missing) != 0 {
		t.Errorf("expected no missing skills for nil input, got %v", missing)
	}
}

func TestBuildContext(t *testing.T) {
	r := NewSkillRegistry()

	// All nil
	ctx := r.BuildContext(nil, nil, nil, nil)
	if ctx.Bus != nil {
		t.Error("expected nil Bus")
	}
	if ctx.Logger != nil {
		t.Error("expected nil Logger")
	}
	if ctx.Config != nil {
		t.Error("expected nil Config")
	}

	// With values
	bus := event.NewEventBus(nil)
	defer bus.Close()
	logger := NewDebugger()
	config := map[string]interface{}{"key": "value"}

	ctx = r.BuildContext("llm-client", bus, logger, config)
	if ctx.Bus != bus {
		t.Error("expected Bus to match")
	}
	if ctx.Logger != logger {
		t.Error("expected Logger to match")
	}
	if ctx.Config["key"] != "value" {
		t.Errorf("expected config key 'value', got '%v'", ctx.Config["key"])
	}
	if ctx.Config["llm"] != "llm-client" {
		t.Errorf("expected llm in config, got '%v'", ctx.Config["llm"])
	}
}

func TestBuildContextNilConfig(t *testing.T) {
	r := NewSkillRegistry()
	ctx := r.BuildContext("llm", nil, nil, nil)
	if ctx.Config == nil {
		t.Fatal("expected Config to be non-nil when llm is provided")
	}
	if _, ok := ctx.Config["llm"]; !ok {
		t.Error("expected 'llm' key in config")
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := NewSkillRegistry()
	r.Register(SkillDefinition{Name: "skill-a", Version: "1.0"}, nil)

	done := make(chan bool, 20)
	for i := 0; i < 10; i++ {
		go func() {
			r.Has("skill-a")
			r.List()
			r.CheckDependencies([]string{"skill-a"})
			done <- true
		}()
		go func() {
			r.Register(SkillDefinition{Name: "skill-b", Version: "1.0"}, nil)
			done <- true
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

// mockSkillProvider is a test double for SkillProvider.
type mockSkillProvider struct {
	def SkillDefinition
}

func (m *mockSkillProvider) Definition() SkillDefinition { return m.def }
func (m *mockSkillProvider) Execute(ctx context.Context, input []byte, config map[string]string) ([]byte, error) {
	return input, nil
}

// 并发压力测试

func TestConcurrentRegister(t *testing.T) {
	r := NewSkillRegistry()
	var wg sync.WaitGroup
	n := 100

	// 并发注册技能
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			def := SkillDefinition{
				Name:        fmt.Sprintf("skill-%d", idx),
				Version:     "1.0.0",
				Description: fmt.Sprintf("Skill %d", idx),
			}
			r.Register(def, nil)
		}(i)
	}
	wg.Wait()

	if len(r.List()) != n {
		t.Errorf("expected %d skills, got %d", n, len(r.List()))
	}
}

func TestConcurrentGet(t *testing.T) {
	r := NewSkillRegistry()

	// 预先注册技能（带provider）
	for i := 0; i < 10; i++ {
		def := SkillDefinition{
			Name:    fmt.Sprintf("skill-%d", i),
			Version: "1.0.0",
		}
		provider := &mockSkillProvider{def: def}
		r.Register(def, provider)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发获取技能
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			skillName := fmt.Sprintf("skill-%d", idx%10)
			provider, exists := r.Get(skillName)
			if !exists {
				t.Errorf("skill %s should exist", skillName)
			}
			if provider == nil {
				t.Errorf("skill %s provider should not be nil", skillName)
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrentHas(t *testing.T) {
	r := NewSkillRegistry()

	// 预先注册技能
	for i := 0; i < 10; i++ {
		def := SkillDefinition{
			Name:    fmt.Sprintf("skill-%d", i),
			Version: "1.0.0",
		}
		r.Register(def, nil)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发检查技能是否存在
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			skillName := fmt.Sprintf("skill-%d", idx%10)
			if !r.Has(skillName) {
				t.Errorf("skill %s should exist", skillName)
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrentList(t *testing.T) {
	r := NewSkillRegistry()

	// 预先注册技能
	for i := 0; i < 10; i++ {
		def := SkillDefinition{
			Name:    fmt.Sprintf("skill-%d", i),
			Version: "1.0.0",
		}
		r.Register(def, nil)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发列出技能
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			skills := r.List()
			if len(skills) != 10 {
				t.Errorf("expected 10 skills, got %d", len(skills))
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentCheckDependencies(t *testing.T) {
	r := NewSkillRegistry()

	// 预先注册技能
	for i := 0; i < 10; i++ {
		def := SkillDefinition{
			Name:    fmt.Sprintf("skill-%d", i),
			Version: "1.0.0",
		}
		r.Register(def, nil)
	}

	var wg sync.WaitGroup
	n := 100

	// 并发检查依赖
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			required := []string{fmt.Sprintf("skill-%d", idx%10)}
			missing := r.CheckDependencies(required)
			if len(missing) != 0 {
				t.Errorf("expected no missing skills, got %v", missing)
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrentMixedOperations(t *testing.T) {
	r := NewSkillRegistry()
	var wg sync.WaitGroup
	n := 100

	// 并发混合操作
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			skillName := fmt.Sprintf("skill-%d", idx%10)
			// 注册技能
			def := SkillDefinition{
				Name:    skillName,
				Version: "1.0.0",
			}
			r.Register(def, nil)
			// 检查是否存在
			r.Has(skillName)
			// 获取技能
			r.Get(skillName)
			// 列出技能
			r.List()
			// 检查依赖
			r.CheckDependencies([]string{skillName})
		}(i)
	}
	wg.Wait()
}

func TestConcurrentBuildContext(t *testing.T) {
	r := NewSkillRegistry()

	// 预先注册技能
	for i := 0; i < 10; i++ {
		def := SkillDefinition{
			Name:    fmt.Sprintf("skill-%d", i),
			Version: "1.0.0",
		}
		r.Register(def, nil)
	}

	bus := event.NewEventBus(nil)
	defer bus.Close()

	var wg sync.WaitGroup
	n := 100

	// 并发构建上下文
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx := r.BuildContext(nil, bus, nil, nil)
			// SkillContext是结构体，不会为nil
			// 验证Bus字段被正确设置
			if ctx.Bus != bus {
				t.Error("expected bus to be set")
			}
		}()
	}
	wg.Wait()
}

// ---- Debugger tests ----

func TestNewDebugger(t *testing.T) {
	d := NewDebugger()
	if d == nil {
		t.Fatal("expected non-nil debugger")
	}
	if d.minLevel != LevelDebug {
		t.Errorf("expected minLevel DEBUG, got %s", d.minLevel)
	}
}

func TestDebuggerLogLevel(t *testing.T) {
	d := NewDebugger()

	d.Log(LevelInfo, "comp", "hello", nil)
	logs := d.GetLogs(LevelDebug)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Level != LevelInfo {
		t.Errorf("expected INFO, got %s", logs[0].Level)
	}
	if logs[0].Component != "comp" {
		t.Errorf("expected comp, got %s", logs[0].Component)
	}
	if logs[0].Message != "hello" {
		t.Errorf("expected hello, got %s", logs[0].Message)
	}
}

func TestDebuggerLogFilteredByLevel(t *testing.T) {
	d := NewDebugger()
	d.SetMinLevel(LevelWarn)

	d.Log(LevelDebug, "c", "msg1", nil)
	d.Log(LevelInfo, "c", "msg2", nil)
	d.Log(LevelWarn, "c", "msg3", nil)
	d.Log(LevelError, "c", "msg4", nil)

	logs := d.GetLogs(LevelDebug)
	if len(logs) != 2 {
		t.Errorf("expected 2 logs (warn+error), got %d", len(logs))
	}
}

func TestDebuggerLogByComponent(t *testing.T) {
	d := NewDebugger()

	d.Log(LevelInfo, "comp-a", "msg1", nil)
	d.Log(LevelInfo, "comp-b", "msg2", nil)
	d.Log(LevelInfo, "comp-a", "msg3", nil)

	logs := d.GetLogsByComponent("comp-a")
	if len(logs) != 2 {
		t.Errorf("expected 2 logs for comp-a, got %d", len(logs))
	}
}

func TestDebuggerLogWithFields(t *testing.T) {
	d := NewDebugger()

	fields := map[string]interface{}{"key": "value", "count": 42}
	d.Log(LevelInfo, "c", "msg", fields)

	logs := d.GetLogs(LevelDebug)
	if len(logs) != 1 {
		t.Fatal("expected 1 log")
	}
	if logs[0].Fields["key"] != "value" {
		t.Errorf("expected field key=value, got %v", logs[0].Fields["key"])
	}
}

func TestDebuggerConvenienceMethods(t *testing.T) {
	d := NewDebugger()

	d.Debug("c", "d", nil)
	d.Info("c", "i", nil)
	d.Warn("c", "w", nil)
	d.Error("c", "e", nil)

	logs := d.GetLogs(LevelDebug)
	if len(logs) != 4 {
		t.Fatalf("expected 4 logs, got %d", len(logs))
	}

	expectedLevels := []LogLevel{LevelDebug, LevelInfo, LevelWarn, LevelError}
	for i, log := range logs {
		if log.Level != expectedLevels[i] {
			t.Errorf("log %d: expected %s, got %s", i, expectedLevels[i], log.Level)
		}
	}
}

func TestDebuggerCallerInfo(t *testing.T) {
	d := NewDebugger()

	d.Log(LevelInfo, "c", "msg", nil)
	logs := d.GetLogs(LevelDebug)
	if len(logs) != 1 {
		t.Fatal("expected 1 log")
	}
	// Caller info should contain the test file name
	if logs[0].Caller == "unknown" {
		t.Error("expected caller info, got 'unknown'")
	}
}

func TestDebuggerBreakpoints(t *testing.T) {
	d := NewDebugger()

	actionCalled := false
	d.AddBreakpoint("bp1", "mycomp", nil, func() { actionCalled = true })

	// Logging for the breakpoint's component should trigger it
	d.Log(LevelInfo, "mycomp", "triggered", nil)

	if !actionCalled {
		t.Error("expected breakpoint action to be called")
	}

	bps := d.GetBreakpoints()
	if len(bps) != 1 {
		t.Fatalf("expected 1 breakpoint, got %d", len(bps))
	}
	if bps[0].HitCount != 1 {
		t.Errorf("expected hit count 1, got %d", bps[0].HitCount)
	}
}

func TestDebuggerBreakpointCondition(t *testing.T) {
	d := NewDebugger()

	called := false
	d.AddBreakpoint("bp1", "c", func() bool { return false }, func() { called = true })

	d.Log(LevelInfo, "c", "msg", nil)

	if called {
		t.Error("breakpoint action should not be called when condition returns false")
	}
}

func TestDebuggerBreakpointDisabled(t *testing.T) {
	d := NewDebugger()

	called := false
	d.AddBreakpoint("bp1", "c", nil, func() { called = true })
	d.DisableBreakpoint("bp1")

	d.Log(LevelInfo, "c", "msg", nil)

	if called {
		t.Error("disabled breakpoint should not fire")
	}

	// Re-enable and try again
	d.EnableBreakpoint("bp1")
	d.Log(LevelInfo, "c", "msg2", nil)

	if !called {
		t.Error("re-enabled breakpoint should fire")
	}
}

func TestDebuggerRemoveBreakpoint(t *testing.T) {
	d := NewDebugger()

	called := false
	d.AddBreakpoint("bp1", "c", nil, func() { called = true })
	d.RemoveBreakpoint("bp1")

	d.Log(LevelInfo, "c", "msg", nil)

	if called {
		t.Error("removed breakpoint should not fire")
	}

	bps := d.GetBreakpoints()
	if len(bps) != 0 {
		t.Errorf("expected 0 breakpoints, got %d", len(bps))
	}
}

func TestDebuggerTakeSnapshot(t *testing.T) {
	d := NewDebugger()

	d.TakeSnapshot()
	snapshots := d.GetSnapshots()

	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}

	snap := snapshots[0]
	if snap.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if snap.Goroutines < 1 {
		t.Errorf("expected at least 1 goroutine, got %d", snap.Goroutines)
	}
}

func TestDebuggerWatchers(t *testing.T) {
	d := NewDebugger()

	d.AddWatcher("uptime", func() interface{} { return 42 })
	d.AddWatcher("name", func() interface{} { return "test" })

	watchers := d.GetWatchers()
	if len(watchers) != 2 {
		t.Fatalf("expected 2 watchers, got %d", len(watchers))
	}
	if watchers["uptime"] != 42 {
		t.Errorf("expected uptime 42, got %v", watchers["uptime"])
	}
	if watchers["name"] != "test" {
		t.Errorf("expected name test, got %v", watchers["name"])
	}
}

func TestDebuggerClear(t *testing.T) {
	d := NewDebugger()

	d.Log(LevelInfo, "c", "msg", nil)
	d.TakeSnapshot()

	d.Clear()

	logs := d.GetLogs(LevelDebug)
	if len(logs) != 0 {
		t.Errorf("expected 0 logs after clear, got %d", len(logs))
	}
	snapshots := d.GetSnapshots()
	if len(snapshots) != 0 {
		t.Errorf("expected 0 snapshots after clear, got %d", len(snapshots))
	}
}

func TestLogLevelString(t *testing.T) {
	tests := []struct {
		l    LogLevel
		want string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
		{LevelFatal, "FATAL"},
	}
	for _, tt := range tests {
		if got := tt.l.String(); got != tt.want {
			t.Errorf("LogLevel(%d).String() = %s, want %s", tt.l, got, tt.want)
		}
	}
}

func TestErrSkillNotFound(t *testing.T) {
	if ErrSkillNotFound.Error() != "required skill not registered" {
		t.Errorf("unexpected error message: %s", ErrSkillNotFound.Error())
	}
}
