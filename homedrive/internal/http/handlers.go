package http

import (
	"net/http"
)

// handleStatus serves GET /status with the current agent state.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncCounter("homedrive_http_requests_total_status")

	info, err := s.deps.StatusProvider.Status(r.Context())
	if err != nil {
		s.log.Error("failed to get status", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to retrieve status")
		return
	}

	s.writeJSON(w, http.StatusOK, info)
}

// handlePause serves POST /pause to pause the watcher and workers.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncCounter("homedrive_http_requests_total_pause")

	if err := s.deps.Pausable.Pause(r.Context()); err != nil {
		s.log.Error("failed to pause", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to pause")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleResume serves POST /resume to resume the watcher and workers.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncCounter("homedrive_http_requests_total_resume")

	if err := s.deps.Pausable.Resume(r.Context()); err != nil {
		s.log.Error("failed to resume", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to resume")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// handleResync serves POST /resync to trigger an immediate bisync.
// Returns 202 Accepted because the resync runs asynchronously.
func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncCounter("homedrive_http_requests_total_resync")

	if err := s.deps.Resyncable.ForceResync(r.Context()); err != nil {
		s.log.Error("failed to trigger resync", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to trigger resync")
		return
	}

	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "resync_triggered"})
}

// handleReload serves POST /reload to hot-reload configuration.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncCounter("homedrive_http_requests_total_reload")

	if err := s.deps.Reloadable.Reload(r.Context()); err != nil {
		s.log.Error("failed to reload config", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to reload config")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// handleHealthz serves GET /healthz, returning 200 when all components are
// healthy and 503 when any component is unhealthy.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.metrics.IncCounter("homedrive_http_requests_total_healthz")

	result, err := s.deps.HealthChecker.Healthz(r.Context())
	if err != nil {
		s.log.Error("health check failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "health check error")
		return
	}

	status := http.StatusOK
	if !result.Healthy {
		status = http.StatusServiceUnavailable
	}

	s.writeJSON(w, status, result)
}

// handleMetrics serves GET /metrics in Prometheus text exposition format.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := s.metrics.WriteTo(w); err != nil {
		s.log.Error("failed to write metrics", "error", err)
	}
}
