package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The tenant-data surface must be refused while a platform invariant is
// breached, and refused *before* authentication: when the boundary that makes
// tenant identity mean anything is gone, a perfectly valid credential is not a
// reason to serve tenant data (ADR-SECURITY-002).
func TestInvariantGate_RefusesWhileBreached(t *testing.T) {
	s := &Server{log: quietLog()}
	s.SetInvariantGate(func() error { return errors.New("row-level security is off") })

	var reached bool
	h := s.invariantGate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil))

	if reached {
		t.Error("the handler ran while the invariant was breached — the gate is open")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After on a 503; clients cannot back off sensibly")
	}
	// The refusal must not disclose which invariant broke: that names the exact
	// weakness to an unauthenticated caller.
	if body := rec.Body.String(); strings.Contains(body, "row-level security") {
		t.Errorf("the refusal body discloses the specific broken invariant to an "+
			"unauthenticated caller: %s", body)
	}
}

func TestInvariantGate_ServesWhileHealthy(t *testing.T) {
	s := &Server{log: quietLog()}
	s.SetInvariantGate(func() error { return nil })

	var reached bool
	h := s.invariantGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil))

	if !reached || rec.Code != http.StatusOK {
		t.Errorf("healthy invariant did not serve: reached=%v status=%d", reached, rec.Code)
	}
}

// An unwired gate must not block anything: the sentinel is additive wiring, and
// every existing construction path (tests, embedders) has no gate.
func TestInvariantGate_UnsetServes(t *testing.T) {
	s := &Server{log: quietLog()}

	var reached bool
	h := s.invariantGate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil))

	if !reached {
		t.Error("an unwired invariant gate blocked the request")
	}
}
