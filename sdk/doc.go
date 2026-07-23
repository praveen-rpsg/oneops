// Package sdk is the official OneOps Governance Platform Go client.
//
// It is a thin, dependency-free transport layer over the platform's REST APIs
// (PRS-014/015/016): typed methods construct requests, attach the platform's
// headers (bearer auth, Idempotency-Key, If-Match, X-Request-ID), apply a bounded
// retry policy for idempotent requests, and decode responses or RFC 7807
// problem+json errors into typed Go values. It contains no business logic —
// constitutional rules remain enforced exclusively by the server.
//
// The SDK depends only on the standard library and imports no OneOps internal
// package, so it is safe to consume from any external service.
package sdk
