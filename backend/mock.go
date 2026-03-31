package backend

import (
	"context"
	"strings"
	"time"
)

// MockBackend simulates LLM token-by-token generation.
//
// Design: Configurable via the config map to support different testing scenarios:
//   - Default: generates tokens every 100ms to simulate real inference latency
//   - config["error"]="timeout": simulates a backend timeout after 3 seconds
//   - config["error"]="partial": generates some tokens then errors (simulates mid-stream failure)
//   - config["delay_ms"]="500": custom per-token delay in milliseconds
//
// This enables testing error handling, retry logic, and timeout scenarios
// without needing a real LLM backend.
type MockBackend struct{}

func (b *MockBackend) Name() string { return "mock" }

func (b *MockBackend) Stream(ctx context.Context, input string, config map[string]string, out chan<- Token) {
	defer close(out)

	// Check for error simulation modes
	if errMode := config["error"]; errMode != "" {
		switch errMode {
		case "timeout":
			// Simulate a slow/hung backend that exceeds timeout
			select {
			case <-time.After(30 * time.Second):
			case <-ctx.Done():
			}
			out <- Token{Error: "backend timeout: model did not respond within deadline", Done: true}
			return

		case "partial":
			// Emit a few tokens then fail — tests mid-stream error recovery
			for i, word := range []string{"Starting", "response", "but"} {
				select {
				case <-ctx.Done():
					return
				default:
				}
				suffix := " "
				if i == 2 {
					suffix = ""
				}
				out <- Token{Content: word + suffix}
				time.Sleep(100 * time.Millisecond)
			}
			out <- Token{Error: "backend error: internal model failure during inference", Done: true}
			return

		case "empty":
			// Simulate a model that returns nothing
			out <- Token{Done: true}
			return
		}
	}

	// Configurable per-token delay (default 100ms)
	delay := 100 * time.Millisecond
	if d := config["delay_ms"]; d != "" {
		if ms, err := time.ParseDuration(d + "ms"); err == nil {
			delay = ms
		}
	}

	// Generate a mock response
	response := "This is a mock response to: " + input + ". " +
		"Tokens generated one by one, simulating LLM inference."

	words := strings.Fields(response)
	for i, word := range words {
		select {
		case <-ctx.Done():
			out <- Token{Error: "request cancelled", Done: true}
			return
		default:
		}

		suffix := " "
		if i == len(words)-1 {
			suffix = ""
		}

		out <- Token{Content: word + suffix}
		time.Sleep(delay)
	}
	out <- Token{Done: true}
}
