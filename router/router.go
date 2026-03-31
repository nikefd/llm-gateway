package router

import (
	"llm-gateway/handler"
	"llm-gateway/middleware"
	"llm-gateway/registry"
	"net/http"
)

// New creates the HTTP router with all endpoints.
//
// Route design follows RESTful conventions:
//   - /models: resource-oriented CRUD for model management
//   - /infer: action endpoint for inference (POST only)
//   - /metrics: Prometheus-compatible scrape target
//   - /admin: web-based management panel for human operators
//   - /health: lightweight liveness probe for load balancers / k8s
func New(reg *registry.Registry) http.Handler {
	mux := http.NewServeMux()

	modelH := &handler.ModelHandler{Registry: reg}
	inferH := &handler.InferHandler{Registry: reg}
	metricsH := &middleware.MetricsHandler{Infer: inferH}
	adminH := &handler.AdminHandler{Registry: reg}

	// Model management — RESTful CRUD
	mux.HandleFunc("POST /models", modelH.Register)
	mux.HandleFunc("GET /models", modelH.List)
	mux.HandleFunc("PUT /models/{name}/version/{v}", modelH.Update)
	mux.HandleFunc("DELETE /models/{name}/version/{v}", modelH.Delete)

	// Streaming inference — SSE-based token streaming
	mux.HandleFunc("POST /infer", inferH.Infer)

	// Observability
	mux.Handle("GET /metrics", metricsH)

	// Admin panel — simple web UI for model/version/status overview
	mux.Handle("GET /admin", adminH)

	// Health check — for load balancers and orchestrators
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	return mux
}
