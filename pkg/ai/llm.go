package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JSONSchema struct {
	Name   string                 `json:"name"`
	Strict bool                   `json:"strict"`
	Schema map[string]interface{} `json:"schema"`
}

type ResponseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

type ChatCompletionRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Temperature    float32         `json:"temperature,omitempty"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type LLMClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func NewLLMClient(baseURL, apiKey string) *LLMClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &LLMClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *LLMClient) GenerateStructured(ctx context.Context, model string, systemPrompt, userPrompt string, schemaJSON string) (string, error) {
	if model == "" {
		model = "gpt-4o"
	}

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	reqBody := ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.2, // 低温使结构化输出更稳定
	}

	if schemaJSON != "" {
		var schemaMap map[string]interface{}
		if err := json.Unmarshal([]byte(schemaJSON), &schemaMap); err != nil {
			return "", fmt.Errorf("invalid response schema JSON: %w", err)
		}

		reqBody.ResponseFormat = &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &JSONSchema{
				Name:   "structured_output",
				Strict: true,
				Schema: schemaMap,
			},
		}
	}

	url := c.BaseURL + "/chat/completions"
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm api request failed with status %d: %s", resp.StatusCode, string(respData))
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(respData, &chatResp); err != nil {
		return "", err
	}

	if chatResp.Error != nil && chatResp.Error.Message != "" {
		return "", fmt.Errorf("llm api error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("llm api returned no choices")
	}

	return chatResp.Choices[0].Message.Content, nil
}

type ContextBuilder struct {
	MaxChars int
}

func NewContextBuilder(maxChars int) *ContextBuilder {
	if maxChars <= 0 {
		maxChars = 16000
	}
	return &ContextBuilder{MaxChars: maxChars}
}

func (cb *ContextBuilder) BuildUserPrompt(upstreamResults map[string]string, mainPrompt string) string {
	var sb strings.Builder
	if len(upstreamResults) > 0 {
		sb.WriteString("Upstream task outputs:\n")
		for name, result := range upstreamResults {
			sb.WriteString(fmt.Sprintf("- Task [%s]: ", name))
			if len(result) > cb.MaxChars/2 {
				sb.WriteString(result[:cb.MaxChars/2])
				sb.WriteString("\n... [Truncated due to context limits] ...\n")
			} else {
				sb.WriteString(result)
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Instructions:\n")
	sb.WriteString(mainPrompt)

	result := sb.String()
	if len(result) > cb.MaxChars {
		half := cb.MaxChars / 2
		return result[:half] + "\n... [Context Truncated] ...\n" + result[len(result)-half:]
	}
	return result
}
