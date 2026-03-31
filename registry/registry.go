// Package registry implements a thread-safe, in-memory model version store.
//
// Concurrency design:
//   - Registry.mu (RWMutex): protects the models map (add/remove models)
//   - Model.mu (RWMutex): protects the versions map within a model
//   - ModelVersion.mu (RWMutex): protects mutable fields (config, status)
//   - ModelVersion.ActiveCount (atomic.Int32): lock-free concurrent request counting
//
// This layered locking allows high concurrency: multiple models can be read
// simultaneously, and inference requests only need the version-level read lock
// (briefly, to snapshot config) — they don't hold any lock during streaming.
//
// Hot-update safety: callers use SnapshotConfig() to copy config before streaming.
// UpdateVersion replaces the config map reference; in-flight requests keep their copy.
package registry

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ModelStatus represents the lifecycle state of a model version.
type ModelStatus string

const (
	StatusLoading   ModelStatus = "loading"
	StatusReady     ModelStatus = "ready"
	StatusUpdating  ModelStatus = "updating"
	StatusDeleted   ModelStatus = "deleted"
	StatusUnloaded  ModelStatus = "unloaded" // auto-unloaded due to idle
)

// ModelVersion holds metadata and runtime state for a single version.
type ModelVersion struct {
	Version     string      `json:"version"`
	BackendType string      `json:"backend_type"` // mock, openai, ollama, qwen
	Status      ModelStatus `json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	LastUsedAt  time.Time   `json:"last_used_at"`

	// Config holds backend-specific configuration (e.g. API key, model ID).
	Config map[string]string `json:"config,omitempty"`

	// Traffic weight for weighted routing (0-100).
	Weight int `json:"weight"`

	// Shadow mode: if true, this version runs alongside but response is discarded.
	Shadow bool `json:"shadow"`

	// Concurrency control
	MaxConcurrent int32        `json:"max_concurrent"` // 0 = unlimited
	ActiveCount   atomic.Int32 `json:"-"`

	mu sync.RWMutex
}

// Acquire tries to increment the active count. Returns false if at capacity.
func (v *ModelVersion) Acquire() bool {
	if v.MaxConcurrent <= 0 {
		v.ActiveCount.Add(1)
		v.mu.Lock()
		v.LastUsedAt = time.Now()
		v.mu.Unlock()
		return true
	}
	for {
		cur := v.ActiveCount.Load()
		if cur >= v.MaxConcurrent {
			return false
		}
		if v.ActiveCount.CompareAndSwap(cur, cur+1) {
			v.mu.Lock()
			v.LastUsedAt = time.Now()
			v.mu.Unlock()
			return true
		}
	}
}

// Release decrements the active count.
func (v *ModelVersion) Release() {
	v.ActiveCount.Add(-1)
}

// SnapshotConfig returns a copy of backend type and config under lock.
// This is the key to hot-update safety: callers snapshot before starting work,
// so concurrent UpdateVersion calls don't corrupt in-flight requests.
func (v *ModelVersion) SnapshotConfig() (string, map[string]string) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	configCopy := make(map[string]string, len(v.Config))
	for k, val := range v.Config {
		configCopy[k] = val
	}
	return v.BackendType, configCopy
}

// GetActive returns the current active connection count.
func (v *ModelVersion) GetActive() int32 {
	return v.ActiveCount.Load()
}

// Model groups all versions under one name.
type Model struct {
	Name     string                   `json:"name"`
	Versions map[string]*ModelVersion `json:"versions"`
	mu       sync.RWMutex
}

// Registry is the global thread-safe model store.
type Registry struct {
	models map[string]*Model
	mu     sync.RWMutex
}

// New creates a new Registry.
func New() *Registry {
	return &Registry{
		models: make(map[string]*Model),
	}
}

// RegisterInput is the payload for registering a new model version.
type RegisterInput struct {
	ModelName    string            `json:"model_name"`
	Version      string            `json:"version"`
	BackendType  string            `json:"backend_type"`
	Config       map[string]string `json:"config,omitempty"`
	Weight       int               `json:"weight,omitempty"`
	Shadow       bool              `json:"shadow,omitempty"`
	MaxConcurrent int32            `json:"max_concurrent,omitempty"`
}

// Register adds a new model version. If the model doesn't exist, it's created.
func (r *Registry) Register(input RegisterInput) (*ModelVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	m, exists := r.models[input.ModelName]
	if !exists {
		m = &Model{
			Name:     input.ModelName,
			Versions: make(map[string]*ModelVersion),
		}
		r.models[input.ModelName] = m
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, vExists := m.Versions[input.Version]; vExists {
		return nil, fmt.Errorf("model %s version %s already exists", input.ModelName, input.Version)
	}

	now := time.Now()
	weight := input.Weight
	if weight == 0 {
		weight = 100
	}

	ver := &ModelVersion{
		Version:       input.Version,
		BackendType:   input.BackendType,
		Status:        StatusReady,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastUsedAt:    now,
		Config:        input.Config,
		Weight:        weight,
		Shadow:        input.Shadow,
		MaxConcurrent: input.MaxConcurrent,
	}

	m.Versions[input.Version] = ver
	return ver, nil
}

// GetVersion retrieves a specific model version.
func (r *Registry) GetVersion(modelName, version string) (*ModelVersion, error) {
	r.mu.RLock()
	m, exists := r.models[modelName]
	r.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("model %s not found", modelName)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	v, exists := m.Versions[version]
	if !exists {
		return nil, fmt.Errorf("model %s version %s not found", modelName, version)
	}
	return v, nil
}

// SelectVersion picks a version using weighted routing.
// If a specific version is requested, it returns that one.
// Otherwise, it selects among ready, non-shadow versions by weight.
func (r *Registry) SelectVersion(modelName, version string) (*ModelVersion, []*ModelVersion, error) {
	if version != "" {
		v, err := r.GetVersion(modelName, version)
		if err != nil {
			return nil, nil, err
		}
		if v.Status != StatusReady && v.Status != StatusUpdating {
			return nil, nil, fmt.Errorf("model %s version %s is not available (status: %s)", modelName, version, v.Status)
		}
		// Also collect shadow versions
		shadows := r.getShadowVersions(modelName, version)
		return v, shadows, nil
	}

	// Weighted selection among ready non-shadow versions
	r.mu.RLock()
	m, exists := r.models[modelName]
	r.mu.RUnlock()
	if !exists {
		return nil, nil, fmt.Errorf("model %s not found", modelName)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var candidates []*ModelVersion
	var shadows []*ModelVersion
	totalWeight := 0
	for _, v := range m.Versions {
		if v.Status == StatusReady || v.Status == StatusUpdating {
			if v.Shadow {
				shadows = append(shadows, v)
			} else {
				candidates = append(candidates, v)
				totalWeight += v.Weight
			}
		}
	}

	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("no available versions for model %s", modelName)
	}

	// Simple weighted selection using time-based seed
	if len(candidates) == 1 {
		return candidates[0], shadows, nil
	}

	pick := int(time.Now().UnixNano() % int64(totalWeight))
	cumulative := 0
	for _, v := range candidates {
		cumulative += v.Weight
		if pick < cumulative {
			return v, shadows, nil
		}
	}
	return candidates[0], shadows, nil
}

func (r *Registry) getShadowVersions(modelName, excludeVersion string) []*ModelVersion {
	r.mu.RLock()
	m, exists := r.models[modelName]
	r.mu.RUnlock()
	if !exists {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var shadows []*ModelVersion
	for _, v := range m.Versions {
		if v.Shadow && v.Version != excludeVersion && (v.Status == StatusReady || v.Status == StatusUpdating) {
			shadows = append(shadows, v)
		}
	}
	return shadows
}

// UpdateVersion hot-updates a model version's backend config.
// The key design: set status to "updating", swap config, set back to "ready".
// Existing connections using the old config continue unaffected because they
// already hold a reference to the backend instance.
func (r *Registry) UpdateVersion(modelName, version string, input RegisterInput) (*ModelVersion, error) {
	v, err := r.GetVersion(modelName, version)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.Status == StatusDeleted {
		return nil, fmt.Errorf("cannot update deleted version")
	}

	// Mark as updating — new requests can still use it, but we signal
	// that a transition is happening.
	v.Status = StatusUpdating

	// Apply updates
	if input.BackendType != "" {
		v.BackendType = input.BackendType
	}
	if input.Config != nil {
		v.Config = input.Config
	}
	if input.Weight > 0 {
		v.Weight = input.Weight
	}
	if input.MaxConcurrent >= 0 {
		v.MaxConcurrent = input.MaxConcurrent
	}
	v.Shadow = input.Shadow
	v.UpdatedAt = time.Now()
	v.Status = StatusReady

	return v, nil
}

// DeleteVersion marks a version as deleted. Active connections continue.
func (r *Registry) DeleteVersion(modelName, version string) error {
	v, err := r.GetVersion(modelName, version)
	if err != nil {
		return err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.Status = StatusDeleted
	return nil
}

// ModelInfo is the serializable view of a model.
type ModelInfo struct {
	Name     string        `json:"name"`
	Versions []VersionInfo `json:"versions"`
}

// VersionInfo is the serializable view of a version.
type VersionInfo struct {
	Version       string      `json:"version"`
	BackendType   string      `json:"backend_type"`
	Status        ModelStatus `json:"status"`
	Weight        int         `json:"weight"`
	Shadow        bool        `json:"shadow"`
	MaxConcurrent int32       `json:"max_concurrent"`
	ActiveCount   int32       `json:"active_count"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	LastUsedAt    time.Time   `json:"last_used_at"`
}

// List returns all models and their versions.
func (r *Registry) List() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []ModelInfo
	for _, m := range r.models {
		m.mu.RLock()
		info := ModelInfo{Name: m.Name}
		for _, v := range m.Versions {
			info.Versions = append(info.Versions, VersionInfo{
				Version:       v.Version,
				BackendType:   v.BackendType,
				Status:        v.Status,
				Weight:        v.Weight,
				Shadow:        v.Shadow,
				MaxConcurrent: v.MaxConcurrent,
				ActiveCount:   v.GetActive(),
				CreatedAt:     v.CreatedAt,
				UpdatedAt:     v.UpdatedAt,
				LastUsedAt:    v.LastUsedAt,
			})
		}
		m.mu.RUnlock()
		result = append(result, info)
	}
	return result
}

// IdleUnload checks all versions and unloads those idle for longer than maxIdle.
// Returns names of unloaded versions.
func (r *Registry) IdleUnload(maxIdle time.Duration) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var unloaded []string
	now := time.Now()

	for _, m := range r.models {
		m.mu.RLock()
		for _, v := range m.Versions {
			v.mu.Lock()
			if v.Status == StatusReady && v.GetActive() == 0 && now.Sub(v.LastUsedAt) > maxIdle {
				v.Status = StatusUnloaded
				unloaded = append(unloaded, fmt.Sprintf("%s/%s", m.Name, v.Version))
			}
			v.mu.Unlock()
		}
		m.mu.RUnlock()
	}
	return unloaded
}

// ReloadVersion transitions an unloaded version back to ready (lazy reload).
func (r *Registry) ReloadVersion(modelName, version string) (*ModelVersion, error) {
	v, err := r.GetVersion(modelName, version)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.Status == StatusUnloaded {
		v.Status = StatusLoading
		// Simulate reload delay — in production this would load model weights etc.
		v.Status = StatusReady
		v.UpdatedAt = time.Now()
	}
	return v, nil
}
