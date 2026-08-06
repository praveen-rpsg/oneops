package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/observability"
)

// The E7-UI.0 console introduced client-side routing (react-router: /noc,
// /artifacts/:id, /incidents — ADR-UI-001), but this Go router only ever
// registered "/". A deep link or a browser refresh on any client-side route
// therefore 404'd at this layer before react-router ever got a chance to run
// — reachable only by first navigating from "/" client-side, never by URL.
// These tests pin the fix: an unmatched, HTML-ish GET falls back to the
// console shell, while /v1 keeps its own JSON 404 untouched.
//
// routesFor (not routes) is used throughout so this suite does not depend on
// `make web` having been run: the unit-test job in .github/workflows/ci.yml
// never runs it, only the separate frontend job does (see routesFor's own
// comment in server.go).

func newSPAFallbackServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
		MetricsAddr: ":9090",
	}
	return NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
}

// fakeConsoleRoot is a minimal synthetic index.html, standing in for the real
// Vite-built shell: what matters here is that the SAME body is served for
// "/" and for a react-router deep link, not its actual content.
var fakeConsoleRoot = fstest.MapFS{
	"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><body>oneops console</body></html>")},
}

func TestSPAFallback_DeepLinkToNOCServesTheConsoleShell(t *testing.T) {
	s := newSPAFallbackServer(t)
	r := s.routesFor(fakeConsoleRoot, true)

	req := httptest.NewRequest(http.MethodGet, "/noc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /noc: status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("GET /noc: content-type = %q, want text/html", ct)
	}
	if w.Body.String() != "<!doctype html><html><body>oneops console</body></html>" {
		t.Fatalf("GET /noc did not serve the console shell body: %q", w.Body.String())
	}
}

func TestSPAFallback_DeepLinkToArtifactDetailServesTheConsoleShell(t *testing.T) {
	s := newSPAFallbackServer(t)
	r := s.routesFor(fakeConsoleRoot, true)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/01ANYTHING", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /artifacts/01ANYTHING: status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("GET /artifacts/01ANYTHING: content-type = %q, want text/html", ct)
	}
}

func TestSPAFallback_DoesNotSwallowUnknownV1Routes(t *testing.T) {
	s := newSPAFallbackServer(t)
	r := s.routesFor(fakeConsoleRoot, true)
	tok := mintToken(t, []string{"oneops-admin"})

	req := httptest.NewRequest(http.MethodGet, "/v1/definitely-not-a-route", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/definitely-not-a-route: status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("GET /v1/definitely-not-a-route: content-type = %q, want application/problem+json "+
			"(the SPA fallback must never answer for /v1)", ct)
	}
	if got := w.Body.String(); !strings.Contains(got, `"status":404`) {
		t.Fatalf("GET /v1/definitely-not-a-route: body = %q, want an RFC 7807 problem with status 404", got)
	}
}

func TestSPAFallback_NonGETOnAnUnmatchedPathStaysJSON(t *testing.T) {
	s := newSPAFallbackServer(t)
	r := s.routesFor(fakeConsoleRoot, true)

	req := httptest.NewRequest(http.MethodPost, "/noc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /noc: status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("POST /noc: content-type = %q, want application/problem+json (only GET/HEAD get the SPA shell)", ct)
	}
}

// When the console has not been built (this package's own default test
// state — see routesFor's comment), a deep link degrades to the ordinary
// JSON 404 rather than panicking on a nil handler or serving an empty body.
func TestSPAFallback_DegradesToJSON404WhenConsoleIsNotBuilt(t *testing.T) {
	s := newSPAFallbackServer(t)
	r := s.routesFor(nil, false)

	req := httptest.NewRequest(http.MethodGet, "/noc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /noc (console unbuilt): status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("GET /noc (console unbuilt): content-type = %q, want application/problem+json", ct)
	}
}
