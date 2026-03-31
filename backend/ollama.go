package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OllamaBackend calls a local Ollama instance for streaming inference.
// Config keys:
//   - endpoint: Ollama API URL (default: http://localhost:11434)
//   - model: model name (default: llama3)
type OllamaBackend struct{}

func (b *OllamaBackend) Name() string { return "ollama" }

func (b *OllamaBackend) Stream(ctx context.Context, input string, config map[string]string, out chan<- Token) {
	defer close(out)

	endpoint := config["endpoint"]
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	model := config["model"]
	if model == "" {
		model = "llama3"
	}

	reqBody := map[string]interface{}{
		"model":  model,
		"prompt": input,
		"stream": true,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/generate", bytes.NewReader(bodyBytes))
	if err != nil {
		out <- Token{Error: fmt.Sprintf("failed to create request: %v", err), Done: true}
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		out <- Token{Error: fmt.Sprintf("ollama request failed: %v", err), Done: true}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		out <- Token{Error: fmt.Sprintf("ollama error %d: %s", resp.StatusCode, string(body)), Done: true}
		return
	}

	// Ollama streams newline-delimited JSON
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var chunk struct {
			Response string `json:"response"`
			Done     bool   `json:"done"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue
		}
		if chunk.Response != "" {
			out <- Token{Content: chunk.Response}
		}
		if chunk.Done {
			break
		}
	}
	out <- Token{Done: true}
}
