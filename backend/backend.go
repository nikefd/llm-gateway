// Package backend defines the unified inference interface and implementations.
//
// Design: Strategy pattern — each backend_type maps to a Backend implementation.
// All backends share the same Stream() signature, making them interchangeable.
// This allows hot-swapping backends without changing the inference handler.
package backend

import (
	"context"
	"fmt"
)

// Token represents a single streamed output token/chunk.
type Token struct {
	Content  string `json:"content"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
}

// Backend is the unified inference interface.
// All backend types (mock, openai, ollama, qwen) implement this.
type Backend interface {
	// Stream sends tokens to the channel. It must close the channel when done.
	// The caller owns the context for cancellation.
	Stream(ctx context.Context, input string, config map[string]string, out chan<- Token)

	// Name returns the backend type identifier.
	Name() string
}

// registry of backend factories
var backends = map[string]func() Backend{
	"mock":   func() Backend { return &MockBackend{} },
	"openai": func() Backend { return &OpenAIBackend{} },
	"ollama": func() Backend { return &OllamaBackend{} },
	"qwen":   func() Backend { return &QwenBackend{} },
}

// Get returns a Backend instance for the given type.
func Get(backendType string) (Backend, error) {
	factory, ok := backends[backendType]
	if !ok {
		return nil, fmt.Errorf("unknown backend type: %s", backendType)
	}
	return factory(), nil
}
