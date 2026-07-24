package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

// The built console. Serving it from the control plane keeps the browser and the
// API on one origin, so no CORS layer is needed. Mirrors the openapi.yaml embed.
//
//go:embed all:webdist
var webAssets embed.FS

// webFS returns the console's asset root, or false when the console has not been
// built. The Go build must never depend on `pnpm build` having run.
func webFS() (fs.FS, bool) {
	sub, err := fs.Sub(webAssets, "webdist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// serveConsoleIndex serves the console shell at "/".
//
// There is deliberately no catch-all fallback: the console is a single screen
// today, so an unknown path is a genuine 404 and must stay JSON for API clients
// and probes. A fallback is added when client-side routing arrives, scoped to
// the prefixes the router actually owns.
func serveConsoleIndex(root fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	}
}

// serveConsoleAssets serves content-hashed bundles, which are safe to cache
// permanently because the filename changes whenever the content does.
func serveConsoleAssets(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}
