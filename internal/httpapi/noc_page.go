package httpapi

import (
	_ "embed"
	"net/http"
)

// The NOC live screen (E7.2): a single self-contained, vanilla HTML/CSS/JS
// page that polls GET /v1/admin/noc/overview (E7.1) and renders it. No build
// step, no framework, no external network asset — see ADR-NOC-002 for why
// this is a second embed rather than a route inside the React console
// (webdist/).
//
//go:embed nocweb/noc.html
var nocPageHTML []byte

// serveNOCPage serves the NOC screen at GET /noc: an operational/UI route
// outside the /v1 contract surface, exactly like "/" (the console) and
// "/docs" — see contract_test.go's own scope comment. It is a static asset
// with no server-side templating: every value it renders is filled in by the
// page's own JavaScript from the JSON the browser fetches at runtime.
func serveNOCPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(nocPageHTML)
}
