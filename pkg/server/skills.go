package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"loopworker/internal/config"
	"loopworker/internal/core/executor"
	"loopworker/pkg/event"
	"loopworker/pkg/skill"
)

// newSkillRegistry registers the built-in skills with real providers. A skill
// without configuration fails with the exact setting to change, instead of
// silently doing nothing.
func newSkillRegistry(cfg *config.Config) *skill.SkillRegistry {
	registry := skill.NewSkillRegistry()
	registry.Register(skill.SkillDefinition{
		Name:        "llm.chat",
		Version:     "1.0.0",
		Description: "LLM structured chat completion",
		InputTypes:  []string{"text", "json"},
		OutputTypes: []string{"text", "json"},
	}, &llmChatSkill{cfg: cfg})
	registry.Register(skill.SkillDefinition{
		Name:        "research.anomaly",
		Version:     "1.0.0",
		Description: "Anomaly detection on numeric series (|z| > threshold)",
		InputTypes:  []string{"json", "[]float64"},
		OutputTypes: []string{"json"},
	}, &anomalySkill{})
	return registry
}

func llmClientFor(cfg *config.Config) *executor.LLMClient {
	if cfg.LLM.APIKey == "" && cfg.LLM.BaseURL == "" {
		return nil
	}
	return executor.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey)
}

func skillContext(cfg *config.Config, registry *skill.SkillRegistry, bus *event.EventBus) skill.SkillContext {
	settings := map[string]interface{}{
		"llm.model":      cfg.LLM.Model,
		"llm.base_url":   cfg.LLM.BaseURL,
		"llm.configured": strconv.FormatBool(cfg.LLM.APIKey != ""),
	}
	return skill.SkillContext{
		LLM:    llmClientFor(cfg),
		Bus:    bus,
		Logger: skill.NewDebugger(),
		Config: settings,
	}
}

// llmChatSkill executes the llm.chat skill against the configured endpoint.
type llmChatSkill struct {
	cfg *config.Config
}

func (l *llmChatSkill) Definition() skill.SkillDefinition {
	return skill.SkillDefinition{
		Name:        "llm.chat",
		Version:     "1.0.0",
		Description: "LLM structured chat completion",
		InputTypes:  []string{"text", "json"},
		OutputTypes: []string{"text", "json"},
	}
}

type llmChatRequest struct {
	SystemPrompt string `json:"system_prompt"`
	Prompt       string `json:"prompt"`
	UserPrompt   string `json:"user_prompt"`
	Model        string `json:"model"`
	Schema       string `json:"response_schema"`
}

func (l *llmChatSkill) Execute(ctx context.Context, input []byte, _ map[string]string) ([]byte, error) {
	if l.cfg.LLM.APIKey == "" {
		return nil, fmt.Errorf("skill llm.chat is not configured: set llm.api_key in the config file, or LOOPWORKER_LLM_API_KEY, then restart")
	}

	req := llmChatRequest{SystemPrompt: "You are a helpful assistant in a workflow execution. Answer in structured JSON."}
	trimmed := strings.TrimSpace(string(input))
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
			return nil, fmt.Errorf("llm.chat input is not a valid JSON object: %w", err)
		}
	} else {
		req.Prompt = trimmed
	}
	prompt := req.Prompt
	if prompt == "" {
		prompt = req.UserPrompt
	}
	if prompt == "" {
		return nil, fmt.Errorf("llm.chat input must contain a prompt (text, or JSON with a \"prompt\" field)")
	}
	if req.SystemPrompt == "" {
		req.SystemPrompt = "You are a helpful assistant in a workflow execution. Answer in structured JSON."
	}
	model := req.Model
	if model == "" {
		model = l.cfg.LLM.Model
	}

	client := executor.NewLLMClient(l.cfg.LLM.BaseURL, l.cfg.LLM.APIKey)
	content, err := client.GenerateStructured(ctx, model, req.SystemPrompt, prompt, req.Schema)
	if err != nil {
		return nil, fmt.Errorf("llm.chat call to %s failed: %w", l.cfg.LLM.BaseURL, err)
	}
	out, err := json.Marshal(map[string]string{"content": content, "model": model})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// anomalySkill executes the research.anomaly skill.
type anomalySkill struct{}

func (a *anomalySkill) Definition() skill.SkillDefinition {
	return skill.SkillDefinition{
		Name:        "research.anomaly",
		Version:     "1.0.0",
		Description: "Anomaly detection on numeric series (|z| > threshold)",
		InputTypes:  []string{"json", "[]float64"},
		OutputTypes: []string{"json"},
	}
}

type anomalyInput struct {
	Values    []float64 `json:"values"`
	Threshold float64   `json:"threshold"`
}

func (a *anomalySkill) Execute(_ context.Context, input []byte, _ map[string]string) ([]byte, error) {
	var payload anomalyInput
	trimmed := strings.TrimSpace(string(input))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &payload.Values); err != nil {
			return nil, fmt.Errorf("research.anomaly input must be a JSON array of numbers: %w", err)
		}
	} else if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("research.anomaly input must be {\"values\":[...]} or [1,2,3]: %w", err)
	}
	if len(payload.Values) < 2 {
		return nil, fmt.Errorf("research.anomaly needs at least 2 values, got %d", len(payload.Values))
	}
	threshold := payload.Threshold
	if threshold <= 0 {
		threshold = 3
	}

	mean := 0.0
	for _, v := range payload.Values {
		mean += v
	}
	mean /= float64(len(payload.Values))

	variance := 0.0
	for _, v := range payload.Values {
		variance += (v - mean) * (v - mean)
	}
	variance /= float64(len(payload.Values))
	stddev := math.Sqrt(variance)

	type anomaly struct {
		Index int     `json:"index"`
		Value float64 `json:"value"`
		Z     float64 `json:"z"`
	}
	anomalies := []anomaly{}
	if stddev > 0 {
		for i, v := range payload.Values {
			z := (v - mean) / stddev
			if math.Abs(z) > threshold {
				anomalies = append(anomalies, anomaly{Index: i, Value: v, Z: z})
			}
		}
	}

	out, err := json.Marshal(map[string]interface{}{
		"count":     len(payload.Values),
		"mean":      mean,
		"stddev":    stddev,
		"threshold": threshold,
		"anomalies": anomalies,
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
