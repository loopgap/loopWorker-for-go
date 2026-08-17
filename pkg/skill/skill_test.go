package skill

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"loopworker/pkg/debugger"
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
	logger := debugger.NewDebugger()
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
