// Package httpapi is the Configuration Registry HTTP transport: routing,
// middleware, handlers, and the published OpenAPI contract.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/graph"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/pkg/version"
)

// idempotencyStore persists idempotent responses. *postgres.IdempotencyStore
// satisfies it; tests supply a fake.
type idempotencyStore interface {
	Lookup(ctx context.Context, key string) (status int, body []byte, found bool, err error)
	Save(ctx context.Context, key, method, path string, status int, body []byte) error
}

// Server wires the registry dependencies into an HTTP handler.
type Server struct {
	cfg       *config.Config
	log       *slog.Logger
	repo      domain.ConfigObjectRepository
	idem      idempotencyStore
	verifier  *auth.Verifier
	metrics   *observability.Metrics
	health    *Health
	graph     *graph.Service        // M2.3 graph transport; nil until SetGraph
	graphRepo domain.GraphTraversal // direct (one-hop) lookups
}

// SetGraph wires the dependency-graph traversal layer (M2.3). It is additive:
// the registry endpoints operate without it. The GraphService is constructed
// here from the traversal repository, unchanged from M2.2.
func (s *Server) SetGraph(gt domain.GraphTraversal) {
	s.graphRepo = gt
	s.graph = graph.NewService(gt)
}

// NewServer constructs a Server. ready checks downstream readiness (DB ping).
func NewServer(
	cfg *config.Config,
	log *slog.Logger,
	repo domain.ConfigObjectRepository,
	idem idempotencyStore,
	verifier *auth.Verifier,
	metrics *observability.Metrics,
	ready func(context.Context) error,
) *Server {
	return &Server{
		cfg:      cfg,
		log:      log,
		repo:     repo,
		idem:     idem,
		verifier: verifier,
		metrics:  metrics,
		health:   newHealth(ready),
	}
}

// Router builds the fully-wired HTTP handler.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.requestID, s.logging, s.metrics.Middleware)

	r.Get("/healthz", s.health.Live)
	r.Get("/readyz", s.health.Ready)
	r.Handle("/metrics", s.metrics.Handler())
	r.Get("/openapi.yaml", serveOpenAPI)
	r.Get("/docs", serveDocs)
	r.Get("/", s.serveRoot)

	r.Route("/v1", func(rt chi.Router) {
		rt.Use(s.authenticate)
		rt.With(s.requirePermission(auth.PermRead)).Get("/artifacts", s.listArtifacts)
		rt.With(s.requirePermission(auth.PermRead)).Get("/artifacts/{id}", s.getArtifact)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/artifacts", s.createArtifact)
		rt.With(s.requirePermission(auth.PermWrite)).Post("/artifacts/bulk", s.bulkCreateArtifacts)
		rt.With(s.requirePermission(auth.PermWrite)).Patch("/artifacts/{id}", s.patchArtifact)
		rt.With(s.requirePermission(auth.PermDelete)).Delete("/artifacts/{id}", s.deleteArtifact)

		// M2.3 Dependency Graph — read-only, same authorization as config reads.
		rt.With(s.requirePermission(auth.PermRead)).Get("/configurations/{cfgId}/dependencies", s.getDependencies)
		rt.With(s.requirePermission(auth.PermRead)).Get("/configurations/{cfgId}/dependents", s.getDependents)
		rt.With(s.requirePermission(auth.PermRead)).Get("/configurations/{cfgId}/cycles", s.getCycles)
	})

	return otelhttp.NewHandler(r, "oneops-controlplane")
}

// HTTPServer builds an *http.Server with production timeouts.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.HTTPAddr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such resource")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "oneops-controlplane",
		"version": version.Version,
		"openapi": "/openapi.yaml",
		"docs":    "/docs",
	})
}
