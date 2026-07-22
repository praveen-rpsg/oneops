package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecovererCatchesPanic(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("recovered status = %d, want 500", rec.Code)
	}
}

func TestListStoreErrorMapsTo500(t *testing.T) {
	h, repo := newTestAPI(false)
	repo.failList = true
	rec := do(h, http.MethodGet, "/v1/artifacts", nil, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("list error status = %d, want 500", rec.Code)
	}
}

func TestDocsEndpoint(t *testing.T) {
	h, _ := newTestAPI(false)
	rec := do(h, http.MethodGet, "/docs", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("docs = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("docs content-type = %q", ct)
	}
}

func TestBadJSONBodies(t *testing.T) {
	h, _ := newTestAPI(false)

	// A JSON string is not a createRequest object => 400.
	if rec := do(h, http.MethodPost, "/v1/artifacts", "not-an-object", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("create bad body = %d, want 400", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/artifacts/bulk", "nope", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bulk bad body = %d, want 400", rec.Code)
	}

	created := decodeCO(t, do(h, http.MethodPost, "/v1/artifacts", validCreate(), nil))
	// Non-numeric If-Match => 400.
	if rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, patchRequest{}, map[string]string{"If-Match": `"abc"`}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad If-Match = %d, want 400", rec.Code)
	}
	// Bad JSON patch body => 400.
	if rec := do(h, http.MethodPatch, "/v1/artifacts/"+created.CfgID, "x", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad patch body = %d, want 400", rec.Code)
	}
}

func TestProblemContentType(t *testing.T) {
	h, _ := newTestAPI(false)
	rec := do(h, http.MethodGet, "/v1/artifacts/missing", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("problem content-type = %q", ct)
	}
}
