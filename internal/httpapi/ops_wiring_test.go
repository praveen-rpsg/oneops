package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/observability"
)

func opsServer(t *testing.T, pprofEnabled bool, diag http.Handler) http.Handler {
	t.Helper()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: false, PProfEnabled: pprofEnabled,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if diag != nil {
		s.SetDiagnostics(diag)
	}
	return s.Router()
}

func TestPProf_DisabledByDefault(t *testing.T) {
	h := opsServer(t, false, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pprof reachable while disabled: status %d, want 404", rec.Code)
	}
}

func TestPProf_EnabledWhenConfigured(t *testing.T) {
	h := opsServer(t, true, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pprof not reachable while enabled: status %d, want 200", rec.Code)
	}
}

func TestDiagnostics_NotMountedUntilSet(t *testing.T) {
	h := opsServer(t, false, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/diagnostics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("diagnostics reachable while unset: status %d, want 404", rec.Code)
	}
}

func TestDiagnostics_ServedWhenSet(t *testing.T) {
	diag := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"oneops"}`))
	})
	h := opsServer(t, false, diag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/diagnostics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != `{"service":"oneops"}` {
		t.Fatalf("diagnostics body = %q", rec.Body.String())
	}
}
