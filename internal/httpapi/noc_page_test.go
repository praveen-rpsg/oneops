package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeNOCPage_ServesTheEmbeddedScreen proves the E7.2 screen is actually
// wired into the router: a plain GET returns the embedded asset (not a 404
// falling through to the console or the JSON service descriptor), with the
// content type a browser needs to render it, and the structural anchors
// noc.html's own <script> populates from GET /v1/admin/noc/overview — so a
// future edit that renames/removes one of those ids fails here, not silently
// in a browser nobody is watching.
func TestServeNOCPage_ServesTheEmbeddedScreen(t *testing.T) {
	s, _ := newTestServer(false)
	router := s.Router()

	req := httptest.NewRequest("GET", "/noc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("GET /noc = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}

	body := rec.Body.String()
	for _, anchor := range []string{
		`id="incidents-card"`,
		`id="incidents-open-total"`,
		`id="incidents-by-status"`,
		`id="incidents-by-severity"`,
		`id="incidents-grouped"`,
		`id="alerts-card"`,
		`id="alerts-firing-total"`,
		`id="alerts-by-severity"`,
		`id="assets-card"`,
		`id="assets-health"`,
		`id="oncall-card"`,
		`id="oncall-list"`,
		`id="escalations-card"`,
		`id="escalations-active"`,
		`id="last-updated"`,
		`id="refresh-btn"`,
		`id="error-banner"`,
		`/v1/admin/noc/overview`,
	} {
		if !strings.Contains(body, anchor) {
			t.Errorf("GET /noc body missing expected anchor %q", anchor)
		}
	}

	// No external network dependency: CSP/offline-safe per ADR-NOC-002.
	for _, forbidden := range []string{"cdn.", "http://", "https://"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("GET /noc body contains external reference %q; the page must be self-contained", forbidden)
		}
	}
}

// TestServeNOCPage_IsNotUnderV1 documents (and pins) that /noc is an
// operational UI route outside the /v1 contract surface, exactly like "/"
// and "/docs" — contract_test.go's own scope comment names it explicitly, so
// TestOpenAPIContract_PromisesNothingItDoesNotServe never sees it walked as a
// /v1 route (routesFromRouter filters by the "/v1/" prefix), and no openapi
// entry is required for it.
func TestServeNOCPage_IsNotUnderV1(t *testing.T) {
	got := routesFromRouter(t)
	if got["GET /noc"] {
		t.Fatal("GET /noc must not appear as a /v1 route (routesFromRouter only " +
			"walks /v1/*, so this would mean /noc was mounted under /v1)")
	}
	// The E7.1 projection endpoint this screen polls IS a /v1 route, and must
	// remain one — this pins the two routes apart, not just /noc's absence.
	if !got["GET /v1/admin/noc/overview"] {
		t.Fatal("GET /v1/admin/noc/overview missing from the /v1 surface")
	}
}
