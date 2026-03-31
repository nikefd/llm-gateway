// Package middleware provides HTTP middleware and observability handlers.
//
// Design choice: Custom Prometheus text format output instead of using the
// prometheus/client_golang library. This keeps dependencies minimal for an
// interview project. In production, you'd use promauto/prometheus.NewCounterVec
// for proper histogram buckets and label cardinality management.
package middleware

import (
	"fmt"
	"llm-gateway/handler"
	"net/http"
	"sync/atomic"
)

// MetricsHandler exposes /metrics in Prometheus text format.
// Exports per-model: total requests, errors, and average latency.
type MetricsHandler struct {
	Infer *handler.InferHandler
}

func (m *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	// Gather metrics from InferHandler's sync.Maps
	m.Infer.TotalRequests.Range(func(key, value interface{}) bool {
		model := key.(string)
		total := value.(*atomic.Int64).Load()

		var errors int64
		if v, ok := m.Infer.TotalErrors.Load(model); ok {
			errors = v.(*atomic.Int64).Load()
		}

		var totalLatency int64
		if v, ok := m.Infer.TotalLatencyMs.Load(model); ok {
			totalLatency = v.(*atomic.Int64).Load()
		}

		var avgLatency float64
		if total > 0 {
			avgLatency = float64(totalLatency) / float64(total)
		}

		failRate := float64(0)
		if total > 0 {
			failRate = float64(errors) / float64(total)
		}

		fmt.Fprintf(w, "# HELP llm_gateway_requests_total Total inference requests per model\n")
		fmt.Fprintf(w, "llm_gateway_requests_total{model=%q} %d\n", model, total)
		fmt.Fprintf(w, "# HELP llm_gateway_errors_total Total inference errors per model\n")
		fmt.Fprintf(w, "llm_gateway_errors_total{model=%q} %d\n", model, errors)
		fmt.Fprintf(w, "# HELP llm_gateway_latency_avg_ms Average inference latency in ms\n")
		fmt.Fprintf(w, "llm_gateway_latency_avg_ms{model=%q} %.2f\n", model, avgLatency)
		fmt.Fprintf(w, "# HELP llm_gateway_failure_rate Failure rate (errors/total)\n")
		fmt.Fprintf(w, "llm_gateway_failure_rate{model=%q} %.4f\n", model, failRate)
		return true
	})
}
