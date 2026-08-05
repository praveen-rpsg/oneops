package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/observability"
)

// The published contract and the served surface must be the same set of
// operations. A spec that promises an endpoint the router does not serve is a
// lie to every client generated from it; a route the spec omits is an
// unversioned, undocumented surface that no consumer can plan around.
//
// The subject set is derived by walking the live chi router, not by listing
// routes. Adding a /v1 route therefore fails this test until the spec is
// updated — the sweep cannot go stale by someone forgetting to extend a list.
//
// Scope is /v1 only. /healthz, /readyz, /metrics, /openapi.yaml, /docs, /,
// /noc, /auth/config, /internal/diagnostics and /debug/pprof are operational
// or discovery surfaces, deliberately outside the versioned API contract.

// routesFromRouter walks the fully-wired router and returns "METHOD /v1/path"
// for every versioned operation it serves.
func routesFromRouter(t *testing.T) map[string]bool {
	t.Helper()

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
		// A separate metrics listener keeps /metrics off the public router, which
		// is what production requires anyway (config.validateProduction).
		MetricsAddr: ":9090",
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })

	got := map[string]bool{}
	err := chi.Walk(s.routes(), func(method, route string, _ http.Handler,
		_ ...func(http.Handler) http.Handler) error {
		route = normaliseRoute(route)
		if strings.HasPrefix(route, "/v1/") {
			got[method+" "+route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the router: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("walked the router and found no /v1 routes — the walk is vacuous " +
			"and would pass against an empty API")
	}
	return got
}

// normaliseRoute makes a chi route comparable with an OpenAPI path. chi emits a
// trailing slash for routes mounted through a sub-router; OpenAPI does not.
func normaliseRoute(route string) string {
	if len(route) > 1 && strings.HasSuffix(route, "/") {
		route = strings.TrimSuffix(route, "/")
	}
	return route
}

// operationsFromSpec reads the embedded contract and returns "METHOD /v1/path"
// for every operation under `paths:`.
//
// The spec is parsed by indentation rather than through a YAML library: the
// shape needed here is two levels deep and fixed, and pulling in a YAML
// dependency to read it would cost more than it pays for (Article X).
func operationsFromSpec(t *testing.T) map[string]bool {
	t.Helper()

	methods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true, "delete": true,
	}

	got := map[string]bool{}
	var inPaths bool
	var path string

	sc := bufio.NewScanner(bytes.NewReader(openapiSpec))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		// A top-level key ends the paths block.
		if !strings.HasPrefix(line, " ") {
			inPaths = strings.HasPrefix(line, "paths:")
			path = ""
			continue
		}
		if !inPaths {
			continue
		}

		switch {
		case strings.HasPrefix(line, "  /") && !strings.HasPrefix(line, "   "):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") && path != "":
			key := strings.TrimSuffix(strings.TrimSpace(line), ":")
			if methods[key] {
				got[strings.ToUpper(key)+" "+path] = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the embedded spec: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("parsed the embedded spec and found no operations — the parser is " +
			"vacuous and would pass against an empty contract")
	}
	return got
}

func TestOpenAPIContract_CoversEveryServedRoute(t *testing.T) {
	router := routesFromRouter(t)
	spec := operationsFromSpec(t)

	var missing []string
	for op := range router {
		if !spec[op] {
			missing = append(missing, op)
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("%d served /v1 operation(s) are absent from internal/httpapi/openapi.yaml.\n"+
			"An undocumented endpoint is an unversioned surface no consumer can plan "+
			"around. Add it to the contract, or take the route out.\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func TestOpenAPIContract_PromisesNothingItDoesNotServe(t *testing.T) {
	router := routesFromRouter(t)
	spec := operationsFromSpec(t)

	var phantom []string
	for op := range spec {
		if !strings.HasPrefix(strings.SplitN(op, " ", 2)[1], "/v1/") {
			continue
		}
		if !router[op] {
			phantom = append(phantom, op)
		}
	}
	sort.Strings(phantom)

	if len(phantom) > 0 {
		t.Errorf("%d operation(s) in internal/httpapi/openapi.yaml are not served by "+
			"the router.\nEvery client generated from this contract would call them "+
			"and get 404. Remove them, or implement them.\n  %s",
			len(phantom), strings.Join(phantom, "\n  "))
	}
}
