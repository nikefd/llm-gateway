package main

import (
	"fmt"
	"llm-gateway/registry"
	"llm-gateway/router"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	reg := registry.New()

	// Background goroutine: auto-unload idle models (进阶B: 热重启与资源回收)
	// Models unused for 30 minutes are automatically unloaded.
	// They will be lazily reloaded on next /infer request.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			unloaded := reg.IdleUnload(30 * time.Minute)
			for _, name := range unloaded {
				log.Printf("[IDLE-UNLOAD] %s unloaded due to inactivity", name)
			}
		}
	}()

	handler := router.New(reg)

	fmt.Printf("🚀 LLM Gateway running on :%s\n", port)
	fmt.Println("Endpoints:")
	fmt.Println("  POST   /models                          - Register model")
	fmt.Println("  GET    /models                          - List models")
	fmt.Println("  PUT    /models/{name}/version/{v}       - Hot-update version")
	fmt.Println("  DELETE /models/{name}/version/{v}       - Delete version")
	fmt.Println("  POST   /infer                           - Streaming inference")
	fmt.Println("  GET    /metrics                         - Prometheus metrics")
	fmt.Println("  GET    /admin                           - Management panel")
	fmt.Println("  GET    /health                          - Health check")

	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
