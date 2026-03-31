package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIBackend calls the OpenAI-compatible chat completions API with streaming.
// Config keys:
//   - api_key: OpenAI API key (required)
//   - api_base: base URL (default: https://api.openai.com/v1)
//   - model: model ID (default: gpt-3.5-turbo)
type OpenAIBackend struct{}

func (b *OpenAIBackend) Name() string { return "openai" }

func (b *OpenAIBackend) Stream(ctx context.Context, input string, config map[string]string, out chan<- Token) {
	defer close(out)

	apiKey := config["api_key"]
	if apiKey == "" {
		out <- Token{Error: "api_key not configured", Done: true}
		return
	}

	apiBase := config["api_base"]
	if apiBase == "" {
		apiBase = "https://api.openai.com/v1"
	}
	model := config["model"]
	if model == "" {
		model = "gpt-3.5-turbo"
	}

	// Build request body
	reqBody := map[string]interface{}{
		"model":  model,
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": input},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		out <- Token{Error: fmt.Sprintf("failed to create request: %v", err), Done: true}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		out <- Token{Error: fmt.Sprintf("request failed: %v", err), Done: true}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		out <- Token{Error: fmt.Sprintf("API error %d: %s", resp.StatusCode, string(body)), Done: true}
		return
	}

	// Parse SSE stream
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			out <- Token{Content: chunk.Choices[0].Delta.Content}
		}
	}
	out <- Token{Done: true}
}
