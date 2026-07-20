package skill

import (
	"context"
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
