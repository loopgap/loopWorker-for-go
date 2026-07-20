package skill

import (
	"context"
	"errors"
	"sync"
	"loopworker/pkg/debugger"
	"loopworker/pkg/event"
)

// SkillDefinition describes a capability that a plugin can declare and consume.
type SkillDefinition struct {
	Name        string            // e.g. "llm.chat"
	Version     string            // e.g. "1.0.0"
	Description string
	InputTypes  []string          // e.g. ["text", "json"]
	OutputTypes []string          // e.g. ["text", "json"]
	Config      map[string]string // default config for this skill
}

// SkillProvider is the concrete implementation of a skill.
type SkillProvider interface {
	Definition() SkillDefinition
	Execute(ctx context.Context, input []byte, config map[string]string) ([]byte, error)
}

// SkillContext is the read-only runtime context injected into plugins.
// All fields may be nil, indicating the capability is not configured.
type SkillContext struct {
	LLM      interface{}
	Bus      *event.EventBus
	Logger   *debugger.Debugger
	Config   map[string]interface{}
}

// SkillRegistry manages all registered skills and builds execution contexts.
type SkillRegistry struct {
	definitions map[string]SkillDefinition
	providers   map[string]SkillProvider
	mu          sync.RWMutex
}

// NewSkillRegistry creates an empty SkillRegistry.
func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{
		definitions: make(map[string]SkillDefinition),
		providers:   make(map[string]SkillProvider),
	}
}

// Register adds a skill definition and optional provider to the registry.
func (r *SkillRegistry) Register(def SkillDefinition, provider SkillProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.definitions[def.Name] = def
	if provider != nil {
		r.providers[def.Name] = provider
	}
}

// Get returns the provider for a skill name, if registered.
func (r *SkillRegistry) Get(name string) (SkillProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Has returns true if the skill definition is registered.
func (r *SkillRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.definitions[name]
	return ok
}

// List returns all registered skill definitions.
func (r *SkillRegistry) List() []SkillDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]SkillDefinition, 0, len(r.definitions))
	for _, def := range r.definitions {
		result = append(result, def)
	}
	return result
}

// CheckDependencies returns the subset of required skill names that are not registered.
// An empty slice means all dependencies are satisfied.
func (r *SkillRegistry) CheckDependencies(required []string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var missing []string
	for _, name := range required {
		if _, ok := r.definitions[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

// BuildContext constructs a SkillContext from the given components.
// Any parameter may be nil, indicating the capability is not available.
func (r *SkillRegistry) BuildContext(
	llmClient interface{},
	bus *event.EventBus,
	logger *debugger.Debugger,
	config map[string]interface{},
) SkillContext {
	ctx := SkillContext{
		Bus:    bus,
		Logger: logger,
		Config: config,
	}
	if llmClient != nil {
		if ctx.Config == nil {
			ctx.Config = make(map[string]interface{})
		}
		ctx.Config["llm"] = llmClient
	}
	return ctx
}

// ErrSkillNotFound is returned when a plugin requires a skill that is not registered.
var ErrSkillNotFound = errors.New("required skill not registered")
