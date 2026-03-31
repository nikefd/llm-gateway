package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"llm-gateway/backend"
	"llm-gateway/registry"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// InferHandler handles streaming inference requests.
type InferHandler struct {
	Registry *registry.Registry

	// Metrics counters (exported for prometheus middleware)
	TotalRequests   sync.Map // model -> *atomic.Int64
	TotalErrors     sync.Map // model -> *atomic.Int64
	TotalLatencyMs  sync.Map // model -> *atomic.Int64
}

// InferRequest is the POST /infer request body.
type InferRequest struct {
	Model   string `json:"model"`
	Version string `json:"version,omitempty"` // optional: if empty, use weighted routing
	Input   string `json:"input"`
}

// Infer handles POST /infer — streaming inference via SSE.
//
// Design choice: SSE (Server-Sent Events) over WebSocket/gRPC because:
// 1. SSE is the de facto standard for LLM streaming (OpenAI, Anthropic all use it)
// 2. Works with standard HTTP — no upgrade needed, proxy-friendly
// 3. Auto-reconnect built into browser EventSource API
// 4. Simpler than WebSocket for unidirectional streaming
// 5. curl-friendly for debugging
func (h *InferHandler) Infer(w http.ResponseWriter, r *http.Request) {
	traceID := uuid.New().String()[:8]
	start := time.Now()

	var req InferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sseError(w, traceID, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Model == "" || req.Input == "" {
		h.sseError(w, traceID, "model and input are required", http.StatusBadRequest)
		return
	}

	// Select version (supports weighted routing)
	ver, shadows, err := h.Registry.SelectVersion(req.Model, req.Version)
	if err != nil {
		h.sseError(w, traceID, err.Error(), http.StatusNotFound)
		return
	}

	// If version was unloaded, trigger lazy reload
	if ver.Status == registry.StatusUnloaded {
		ver, err = h.Registry.ReloadVersion(req.Model, ver.Version)
		if err != nil {
			h.sseError(w, traceID, "failed to reload model: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	// Concurrency control — try to acquire a slot.
	// Uses atomic CAS (compare-and-swap) for lock-free concurrency limiting.
	// This is more efficient than a semaphore channel for high-throughput scenarios.
	if !ver.Acquire() {
		h.incrErrors(req.Model)
		h.sseError(w, traceID, fmt.Sprintf("model %s/%s at capacity (%d concurrent)",
			req.Model, ver.Version, ver.MaxConcurrent), http.StatusTooManyRequests)
		return
	}
	defer ver.Release()

	h.incrRequests(req.Model)

	// CRITICAL: Snapshot backend type and config under lock BEFORE starting inference.
	// This guarantees hot-update safety: if PUT /models/{name}/version/{v} fires
	// while we're streaming, we continue with the config we started with.
	// The update will only affect NEW requests after this point.
	backendType, config := ver.SnapshotConfig()

	// Get the backend driver using the snapshotted type
	be, err := backend.Get(backendType)
	if err != nil {
		h.incrErrors(req.Model)
		h.sseError(w, traceID, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Trace-ID", traceID)
	w.Header().Set("X-Model-Version", ver.Version)
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sseError(w, traceID, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Use request context for cancellation (client disconnect)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Start shadow versions in background (灰度发布 — shadow mode)
	if len(shadows) > 0 {
		h.runShadows(ctx, shadows, req, traceID)
	}

	// Stream tokens from the primary backend
	tokenCh := make(chan backend.Token, 32)
	go be.Stream(ctx, req.Input, config, tokenCh)

	for token := range tokenCh {
		if token.Error != "" {
			h.incrErrors(req.Model)
			data, _ := json.Marshal(map[string]string{
				"error":    token.Error,
				"trace_id": traceID,
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			continue
		}
		if token.Done {
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}

		data, _ := json.Marshal(map[string]string{
			"content":  token.Content,
			"trace_id": traceID,
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Record latency
	elapsed := time.Since(start).Milliseconds()
	h.addLatency(req.Model, elapsed)

	log.Printf("[%s] model=%s version=%s backend=%s latency=%dms",
		traceID, req.Model, ver.Version, ver.BackendType, elapsed)
}

// runShadows executes shadow versions in background and logs comparison.
// This implements the 灰度发布 (canary/shadow) pattern:
// - Primary version response goes to the user
// - Shadow versions run concurrently, results are logged for comparison
func (h *InferHandler) runShadows(ctx context.Context, shadows []*registry.ModelVersion, req InferRequest, traceID string) {
	for _, sv := range shadows {
		sv := sv
		if !sv.Acquire() {
			continue
		}
		go func() {
			defer sv.Release()
			be, err := backend.Get(sv.BackendType)
			if err != nil {
				return
			}

			shadowCh := make(chan backend.Token, 64)
			start := time.Now()
			go be.Stream(ctx, req.Input, sv.Config, shadowCh)

			var tokenCount int
			var fullResponse string
			for t := range shadowCh {
				if t.Done || t.Error != "" {
					break
				}
				tokenCount++
				fullResponse += t.Content
			}

			log.Printf("[SHADOW %s] model=%s shadow_version=%s tokens=%d latency=%dms response_len=%d",
				traceID, req.Model, sv.Version, tokenCount,
				time.Since(start).Milliseconds(), len(fullResponse))
		}()
	}
}

func (h *InferHandler) sseError(w http.ResponseWriter, traceID, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":    msg,
		"trace_id": traceID,
	})
}

// Metrics helpers
func (h *InferHandler) incrRequests(model string) {
	v, _ := h.TotalRequests.LoadOrStore(model, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

func (h *InferHandler) incrErrors(model string) {
	v, _ := h.TotalErrors.LoadOrStore(model, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

func (h *InferHandler) addLatency(model string, ms int64) {
	v, _ := h.TotalLatencyMs.LoadOrStore(model, &atomic.Int64{})
	v.(*atomic.Int64).Add(ms)
}
