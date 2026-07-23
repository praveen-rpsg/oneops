package sdk

import (
	"context"
	"net/http"
)

// AdminClient exposes the administration APIs (admin permission required). Every
// method is read-only except RunIntegrity, which triggers one verification sweep.
type AdminClient struct {
	c *Client
}

// Status returns the platform status document.
func (a *AdminClient) Status(ctx context.Context) (*AdminStatus, error) {
	var out AdminStatus
	if err := a.c.do(ctx, http.MethodGet, "/v1/admin/status", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Integrity returns the integrity/scheduler summary.
func (a *AdminClient) Integrity(ctx context.Context) (*AdminIntegrity, error) {
	var out AdminIntegrity
	if err := a.c.do(ctx, http.MethodGet, "/v1/admin/integrity", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RunIntegrity triggers exactly one verification sweep via the server's scheduler.
func (a *AdminClient) RunIntegrity(ctx context.Context) (*IntegrityRun, error) {
	var out IntegrityRun
	if err := a.c.do(ctx, http.MethodPost, "/v1/admin/integrity/run", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Metrics returns the summarized operational counters.
func (a *AdminClient) Metrics(ctx context.Context) (*MetricsSummary, error) {
	var out MetricsSummary
	if err := a.c.do(ctx, http.MethodGet, "/v1/admin/metrics", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Configuration returns the redacted runtime configuration and enabled modules.
func (a *AdminClient) Configuration(ctx context.Context) (*AdminConfig, error) {
	var out AdminConfig
	if err := a.c.do(ctx, http.MethodGet, "/v1/admin/config", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Report returns the combined operational report (diagnostics + metrics).
func (a *AdminClient) Report(ctx context.Context) (*Report, error) {
	var out Report
	if err := a.c.do(ctx, http.MethodGet, "/v1/admin/report", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
