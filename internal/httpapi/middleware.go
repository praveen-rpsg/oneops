package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/domain"
)

type responseRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
		r.ResponseWriter.WriteHeader(code)
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func genRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = genRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
	})
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestIDFrom(r.Context()),
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "err", rec, "path", r.URL.Path)
				writeProblem(w, r, http.StatusInternalServerError, "internal error", "unexpected error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.AuthEnabled {
			ctx := withClaims(r.Context(), &auth.Claims{Subject: "system", Roles: []string{"oneops-admin"}})
			next.ServeHTTP(w, r.WithContext(s.resolveTenant(ctx, w, r, "")))
			return
		}
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) {
			writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		claims, err := s.verifier.Verify(strings.TrimPrefix(h, prefix))
		if err != nil {
			s.log.Warn("auth rejected", "err", err.Error(), "request_id", RequestIDFrom(r.Context()))
			writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}
		ctx := s.resolveTenant(withClaims(r.Context(), claims), w, r, claims.Tenant)
		if ctx == nil {
			return // resolveTenant has already written the rejection
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveTenant turns the tenant asserted by a token into a registered tenant
// and attaches it to the context. It returns nil when the request has been
// rejected, in which case the response is already written.
//
// This is the single place a claim becomes a tenant. Everything downstream
// reads the resolved tenant from the context rather than re-deriving it, so
// there is exactly one code path that decides which isolation boundary a
// request belongs to.
//
// An unrecognised tenant is rejected rather than trusted. Treating the claim as
// authoritative would let any token mint a silo that could never be enumerated,
// suspended, or revoked — which is why the registry is consulted here and not
// merely recorded.
func (s *Server) resolveTenant(
	ctx context.Context, w http.ResponseWriter, r *http.Request, external string,
) context.Context {
	// A token that asserts no tenant resolves to the system tenant. This keeps
	// single-tenant deployments and unauthenticated local runs working without
	// configuration, and is why introducing tenancy does not break them.
	if external == "" {
		return domain.WithTenant(ctx, &domain.Tenant{
			TenantID: domain.SystemTenantID, Slug: "system",
			Name: "System", Status: domain.TenantActive,
		})
	}
	// Without a registry the platform cannot validate a claim, so it must not
	// pretend to. Falling back to the system tenant keeps behaviour identical
	// to the pre-tenancy platform instead of inventing an unverified boundary.
	if s.tenants == nil {
		return domain.WithTenant(ctx, &domain.Tenant{
			TenantID: domain.SystemTenantID, Slug: "system",
			Name: "System", Status: domain.TenantActive,
		})
	}

	t, err := s.tenants.GetByExternalID(r.Context(), external)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.Warn("auth rejected: unknown tenant",
			"request_id", RequestIDFrom(r.Context()))
		// Deliberately does not echo the claim back: the response would
		// otherwise confirm which tenant identifiers exist.
		writeProblem(w, r, http.StatusForbidden, "forbidden", "tenant not recognised")
		return nil
	}
	if err != nil {
		s.log.Error("tenant resolution failed", "err", err.Error(),
			"request_id", RequestIDFrom(r.Context()))
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "tenant resolution failed")
		return nil
	}
	if !t.Active() {
		s.log.Warn("auth rejected: suspended tenant",
			"tenant_id", t.TenantID, "request_id", RequestIDFrom(r.Context()))
		writeProblem(w, r, http.StatusForbidden, "forbidden", "tenant is suspended")
		return nil
	}
	return domain.WithTenant(ctx, t)
}

func (s *Server) requirePermission(perm auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFrom(r.Context())
			if !ok {
				writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "no identity")
				return
			}
			if !auth.HasPermission(claims.Roles, perm) {
				writeProblem(w, r, http.StatusForbidden, "forbidden", "missing permission: "+string(perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
