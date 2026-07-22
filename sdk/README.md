# OneOps Go SDK

The official, supported Go client for the OneOps Governance Platform. It is a
thin, dependency-free transport layer over the platform's REST APIs (PRS-014/015/016).
It contains **no business logic** — all constitutional rules are enforced by the
server.

- **Zero third-party dependencies** (standard library only).
- **No OneOps internal imports** — safe to consume from any service.
- Typed methods, typed errors, idempotency, optimistic concurrency, bounded
  retries, context propagation, and optional observability hooks.

## Install

```
go get github.com/rpsg/oneops/sdk
```

## Quick start

```go
client, err := sdk.NewClient(sdk.Config{
    BaseURL: "https://oneops.internal:8080",
    Token:   os.Getenv("ONEOPS_TOKEN"),
})
if err != nil { log.Fatal(err) }

res, err := client.Governance.Ratify(ctx, "ONEOPS-CFG-0007", sdk.WriteOptions{
    OperationID:        "req-abc-123", // Idempotency-Key (required)
    ExpectedRowVersion: 3,             // If-Match (optional optimistic concurrency)
})
```

## Clients

| Client | Methods |
| --- | --- |
| `client.Governance` | `Ratify` `Approve` `Suspend` `Deprecate` `Withdraw` `Archive` `Delete` |
| `client.Query` | `Get` `History` `Audit` `Events` `Verification` |
| `client.Admin` | `Status` `Integrity` `RunIntegrity` `Metrics` `Configuration` `Report` |

## Errors

Every non-2xx response decodes to `*sdk.APIError` (from RFC 7807 problem+json).
Classify with predicates rather than status codes:

```go
switch {
case sdk.IsNotFound(err):     // 404
case sdk.IsConflict(err):     // 409 or 412 (version mismatch)
case sdk.IsValidation(err):   // 400 or 422
case sdk.IsUnauthorized(err): // 401
case sdk.IsForbidden(err):    // 403
case sdk.IsServerError(err):  // 5xx
case sdk.IsRetryable(err):    // network/timeout, 429, 5xx
}
```

## Transport behavior

- **Auth**: `Authorization: Bearer <token>` when `Token` is set.
- **Idempotency**: `WriteOptions.OperationID` → `Idempotency-Key`; the server
  derives the audit event id from it, so retries are recorded at most once.
- **Optimistic concurrency**: `WriteOptions.ExpectedRowVersion` → `If-Match` ETag.
- **Correlation**: an `X-Request-ID` is generated per request (echoed in errors).
- **Retries**: idempotent requests (GET, or any with `Idempotency-Key`) retry on
  network errors, `429`, and `5xx` with exponential backoff, up to `MaxRetries`.
- **Context**: every method takes a `context.Context`; cancellation/timeout is honored.

## Observability

Provide optional, framework-free `Hooks` for `OnRequest`, `OnResponse`, `OnRetry`
to bridge into your logging/tracing/metrics stack.

## Versioning

The SDK follows semantic versioning and targets the `/v1` API surface. See the
package documentation and `example_test.go` for runnable examples.
