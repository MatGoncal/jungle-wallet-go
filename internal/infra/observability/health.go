package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
)

// Checker probes a dependency for readiness.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// HealthHandler serves liveness and readiness probes.
type HealthHandler struct {
	cfg      config.Config
	checkers []Checker
	mu       sync.RWMutex
}

func NewHealthHandler(cfg config.Config, checkers []Checker) *HealthHandler {
	return &HealthHandler{cfg: cfg, checkers: checkers}
}

func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive"})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.ReadyCheckTimeout)
	defer cancel()

	type depResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}

	results := make([]depResult, 0, len(h.checkers))
	ready := true

	h.mu.RLock()
	checkers := append([]Checker(nil), h.checkers...)
	h.mu.RUnlock()

	for _, c := range checkers {
		res := depResult{Name: c.Name(), Status: "up"}
		if err := c.Check(ctx); err != nil {
			ready = false
			res.Status = "down"
			res.Error = err.Error()
		}
		results = append(results, res)
	}

	body := map[string]any{
		"status":       "ready",
		"checkedAt":    time.Now().UTC().Format(time.RFC3339Nano),
		"dependencies": results,
	}
	status := http.StatusOK
	if !ready {
		body["status"] = "not_ready"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
