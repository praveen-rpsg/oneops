package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// GovernanceClient exposes the constitutional write operations. Every operation
// requires an OperationID (idempotency key) and returns the atomic result.
type GovernanceClient struct {
	c *Client
}

// WriteOptions carries the idempotency key and optional optimistic-concurrency
// guard for a write operation.
type WriteOptions struct {
	// OperationID is the required idempotency key (sent as Idempotency-Key). The
	// server derives the audit event id from it, so retries are recorded at most once.
	OperationID string
	// ExpectedRowVersion, when > 0, enforces optimistic concurrency via If-Match.
	ExpectedRowVersion int64
}

func (o WriteOptions) headers() (map[string]string, error) {
	if o.OperationID == "" {
		return nil, errors.New("oneops: WriteOptions.OperationID is required")
	}
	h := map[string]string{"Idempotency-Key": o.OperationID}
	if o.ExpectedRowVersion > 0 {
		h["If-Match"] = etag(o.ExpectedRowVersion)
	}
	return h, nil
}

// Ratify ratifies a configuration object.
func (g *GovernanceClient) Ratify(ctx context.Context, id string, opts WriteOptions) (*GovernanceResult, error) {
	return g.post(ctx, id, "ratify", nil, opts)
}

// Approve approves a configuration object.
func (g *GovernanceClient) Approve(ctx context.Context, id string, opts WriteOptions) (*GovernanceResult, error) {
	return g.post(ctx, id, "approve", nil, opts)
}

// Suspend suspends a configuration object.
func (g *GovernanceClient) Suspend(ctx context.Context, id string, opts WriteOptions) (*GovernanceResult, error) {
	return g.post(ctx, id, "suspend", nil, opts)
}

// Deprecate deprecates a configuration object.
func (g *GovernanceClient) Deprecate(ctx context.Context, id string, opts WriteOptions) (*GovernanceResult, error) {
	return g.post(ctx, id, "deprecate", nil, opts)
}

// Withdraw withdraws a configuration object.
func (g *GovernanceClient) Withdraw(ctx context.Context, id string, opts WriteOptions) (*GovernanceResult, error) {
	return g.post(ctx, id, "withdraw", nil, opts)
}

// Archive archives a configuration object into the given archival retention class.
func (g *GovernanceClient) Archive(ctx context.Context, id, targetRetention string, opts WriteOptions) (*GovernanceResult, error) {
	if targetRetention == "" {
		return nil, errors.New("oneops: Archive requires a target retention class")
	}
	body := map[string]string{"target_retention": targetRetention}
	return g.post(ctx, id, "archive", body, opts)
}

// Extend records that successorID extends the configuration object id
// (Configuration State Model §8 Extension). The base object's dimensions are
// unchanged — in particular its authority is not demoted. Extension is not
// replacement.
func (g *GovernanceClient) Extend(ctx context.Context, id, successorID string, opts WriteOptions) (*GovernanceResult, error) {
	if successorID == "" {
		return nil, errors.New("oneops: Extend requires a successor id")
	}
	body := map[string]string{"successor_id": successorID}
	return g.post(ctx, id, "extend", body, opts)
}

// Replace supersedes the configuration object id with successorID
// (Configuration State Model §8 Replacement). The server gates this on the
// four-part Replacement Test (§9.1): the successor must own every
// responsibility of the base, no Active artifact may cite it, no Active
// configuration may depend on it, and its removal must leave no operational
// gap. On success the base becomes authority=historical,
// retention=historical_record.
//
// A failed test is returned as an *APIError with Status 409 whose Detail names
// the clause that failed. The client evaluates nothing — the server decides.
func (g *GovernanceClient) Replace(ctx context.Context, id, successorID string, opts WriteOptions) (*GovernanceResult, error) {
	if successorID == "" {
		return nil, errors.New("oneops: Replace requires a successor id")
	}
	body := map[string]string{"successor_id": successorID}
	return g.post(ctx, id, "replace", body, opts)
}

// Delete deletes a working-material configuration object (engine enforces the rules).
func (g *GovernanceClient) Delete(ctx context.Context, id string, opts WriteOptions) (*GovernanceResult, error) {
	h, err := opts.headers()
	if err != nil {
		return nil, err
	}
	var out GovernanceResult
	if err := g.c.do(ctx, http.MethodDelete, "/v1/governance/"+url.PathEscape(id), h, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (g *GovernanceClient) post(ctx context.Context, id, action string, body any, opts WriteOptions) (*GovernanceResult, error) {
	h, err := opts.headers()
	if err != nil {
		return nil, err
	}
	var out GovernanceResult
	path := "/v1/governance/" + url.PathEscape(id) + "/" + action
	if err := g.c.do(ctx, http.MethodPost, path, h, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
