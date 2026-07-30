package httpapi

import (
	"context"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/domain"
)

type ctxKey int

const (
	claimsKey ctxKey = iota
	requestIDKey
)

// withClaims attaches the verified claims and, from them, the actor identity
// that persistence uses to attribute administrative audit records.
//
// The actor is set here, at the single point a token becomes claims, for the
// same reason the tenant is: one place decides, everything downstream reads.
// It is the token subject, matching what the governance path already records
// (handlers_governance.go passes claims.Subject as the command's actor), so
// both audit trails name a principal the same way.
func withClaims(ctx context.Context, c *auth.Claims) context.Context {
	if c != nil && c.Subject != "" {
		ctx = domain.WithActor(ctx, c.Subject)
	}
	return withClaimsOnly(ctx, c)
}

func withClaimsOnly(ctx context.Context, c *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

// ClaimsFrom returns the authenticated claims, if present.
func ClaimsFrom(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*auth.Claims)
	return c, ok
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the correlation/request id, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
