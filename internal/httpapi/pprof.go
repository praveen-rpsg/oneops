package httpapi

import (
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/auth"
)

// mountPProf mounts the standard net/http/pprof endpoints under /debug/pprof,
// but only when profiling is explicitly enabled in configuration. Profiling is
// DISABLED by default (production-safe) and, when enabled, is protected by the
// same authentication and read permission as other privileged surfaces so raw
// runtime/goroutine/heap data is never exposed anonymously.
func (s *Server) mountPProf(r chi.Router) {
	if !s.cfg.PProfEnabled {
		return
	}
	s.log.Warn("pprof runtime profiling is ENABLED", "path", "/debug/pprof")
	r.Route("/debug/pprof", func(pr chi.Router) {
		pr.Use(s.authenticate, s.requirePermission(auth.PermRead))
		pr.HandleFunc("/", pprof.Index)
		pr.HandleFunc("/cmdline", pprof.Cmdline)
		pr.HandleFunc("/profile", pprof.Profile)
		pr.HandleFunc("/symbol", pprof.Symbol)
		pr.HandleFunc("/trace", pprof.Trace)
		// Named runtime profiles (goroutine, heap, allocs, block, mutex, …).
		pr.Handle("/*", http.HandlerFunc(pprof.Index))
	})
}
