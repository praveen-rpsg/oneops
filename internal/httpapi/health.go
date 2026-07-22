package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/rpsg/oneops/pkg/version"
)

// Health serves liveness and readiness. Readiness delegates to a checker that
// verifies downstream dependencies (the database).
type Health struct {
	started time.Time
	ready   func(context.Context) error
}

func newHealth(ready func(context.Context) error) *Health {
	return &Health{started: time.Now(), ready: ready}
}

// Live reports process liveness.
func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"commit":  version.Commit,
		"uptime":  time.Since(h.started).String(),
	})
}

// Ready reports readiness to serve traffic.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unready",
			"error":  err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
