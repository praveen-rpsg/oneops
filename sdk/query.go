package sdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// QueryClient exposes the read-only governance and audit APIs.
type QueryClient struct {
	c *Client
}

// PageOptions controls history/list pagination and filtering.
type PageOptions struct {
	// Limit bounds the page size (server caps it).
	Limit int
	// Cursor continues from a previous page's NextCursor.
	Cursor string
	// Operation filters to a single §8 operation (empty = all).
	Operation string
}

// EventsOptions controls audit-event pagination, ordering, and filtering.
type EventsOptions struct {
	Limit     int
	Cursor    string
	Operation string
	// Order is "asc" (default) or "desc".
	Order string
}

// Get returns the current governance state of a configuration object.
func (q *QueryClient) Get(ctx context.Context, id string) (*ObjectState, error) {
	var out ObjectState
	if err := q.c.do(ctx, http.MethodGet, "/v1/governance/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// History returns a chronological page of governance operations.
func (q *QueryClient) History(ctx context.Context, id string, opts PageOptions) (*HistoryPage, error) {
	qs := url.Values{}
	if opts.Limit > 0 {
		qs.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		qs.Set("cursor", opts.Cursor)
	}
	if opts.Operation != "" {
		qs.Set("operation", opts.Operation)
	}
	var out HistoryPage
	if err := q.c.do(ctx, http.MethodGet, "/v1/governance/"+url.PathEscape(id)+"/history"+encode(qs), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Audit returns the audit chain summary and verification status.
func (q *QueryClient) Audit(ctx context.Context, id string) (*AuditChain, error) {
	var out AuditChain
	if err := q.c.do(ctx, http.MethodGet, "/v1/governance/"+url.PathEscape(id)+"/audit", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Events returns a page of audit event metadata.
func (q *QueryClient) Events(ctx context.Context, id string, opts EventsOptions) (*EventsPage, error) {
	qs := url.Values{}
	if opts.Limit > 0 {
		qs.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		qs.Set("cursor", opts.Cursor)
	}
	if opts.Operation != "" {
		qs.Set("operation", opts.Operation)
	}
	if opts.Order != "" {
		qs.Set("order", opts.Order)
	}
	var out EventsPage
	if err := q.c.do(ctx, http.MethodGet, "/v1/governance/"+url.PathEscape(id)+"/audit/events"+encode(qs), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Verification returns per-object integrity and the latest scheduler result.
func (q *QueryClient) Verification(ctx context.Context, id string) (*Verification, error) {
	var out Verification
	if err := q.c.do(ctx, http.MethodGet, "/v1/governance/"+url.PathEscape(id)+"/verification", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func encode(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}
