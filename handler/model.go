package handler

import (
	"encoding/json"
	"llm-gateway/registry"
	"net/http"
)

// ModelHandler handles model registration, listing, update, and deletion.
type ModelHandler struct {
	Registry *registry.Registry
}

// Register handles POST /models — register a new model version.
func (h *ModelHandler) Register(w http.ResponseWriter, r *http.Request) {
	var input registry.RegisterInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if input.ModelName == "" || input.Version == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model_name and version are required"})
		return
	}
	if input.BackendType == "" {
		input.BackendType = "mock"
	}

	ver, err := h.Registry.Register(input)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "registered",
		"model":   input.ModelName,
		"version": ver.Version,
		"backend": ver.BackendType,
		"status":  ver.Status,
	})
}

// List handles GET /models — list all models and versions.
func (h *ModelHandler) List(w http.ResponseWriter, r *http.Request) {
	models := h.Registry.List()
	writeJSON(w, http.StatusOK, models)
}

// Update handles PUT /models/{name}/version/{v} — hot-update a model version.
func (h *ModelHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("v")

	if name == "" || version == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model name and version required"})
		return
	}

	var input registry.RegisterInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	ver, err := h.Registry.UpdateVersion(name, version, input)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "updated",
		"model":   name,
		"version": ver.Version,
		"backend": ver.BackendType,
		"status":  ver.Status,
	})
}

// Delete handles DELETE /models/{name}/version/{v}.
func (h *ModelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("v")

	if err := h.Registry.DeleteVersion(name, version); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted"})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
