package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ComplianceCheck is one compliance-rule result.
type ComplianceCheck struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
	Detail      string `json:"detail"`
}

// ComplianceSummary is the compact compliance status for a governance object.
type ComplianceSummary struct {
	GovernanceID string    `json:"governance_id"`
	Lifecycle    string    `json:"lifecycle"`
	Verified     bool      `json:"verified"`
	Compliant    bool      `json:"compliant"`
	ChecksPassed int       `json:"checks_passed"`
	ChecksTotal  int       `json:"checks_total"`
	GeneratedAt  time.Time `json:"generated_at"`
}

// ComplianceReportPage is a paginated list of compliance summaries.
type ComplianceReportPage struct {
	Items      []ComplianceSummary `json:"items"`
	NextCursor string              `json:"next_cursor"`
}

// Evidence is the immutable evidence bundle (opaque timeline/histories preserved
// as raw JSON so the SDK stays decoupled from internal timeline types).
type Evidence struct {
	GovernanceID   string            `json:"governance_id"`
	GeneratedAt    time.Time         `json:"generated_at"`
	Governance     json.RawMessage   `json:"governance"`
	Integrity      json.RawMessage   `json:"integrity"`
	Timeline       json.RawMessage   `json:"timeline"`
	Webhooks       json.RawMessage   `json:"webhooks"`
	Replays        json.RawMessage   `json:"replays"`
	Policies       json.RawMessage   `json:"policies"`
	CorrelationIDs []string          `json:"correlation_ids"`
	Checks         []ComplianceCheck `json:"checks"`
	Compliant      bool              `json:"compliant"`
}

// ComplianceClient serves the read-only compliance & evidence engine (admin).
type ComplianceClient struct {
	c *Client
}

// Compliance returns the compliance client.
func (c *Client) Compliance() *ComplianceClient { return &ComplianceClient{c: c} }

// Summary returns the compact compliance status for a governance object.
func (cc *ComplianceClient) Summary(ctx context.Context, govID string) (*ComplianceSummary, error) {
	var out ComplianceSummary
	if err := cc.c.do(ctx, http.MethodGet, "/v1/admin/compliance/"+url.PathEscape(govID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Evidence returns the full evidence bundle for a governance object.
func (cc *ComplianceClient) Evidence(ctx context.Context, govID string) (*Evidence, error) {
	var out Evidence
	if err := cc.c.do(ctx, http.MethodGet, "/v1/admin/compliance/"+url.PathEscape(govID)+"/evidence", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Checks returns only the compliance-check results.
func (cc *ComplianceClient) Checks(ctx context.Context, govID string) ([]ComplianceCheck, error) {
	var out struct {
		Items []ComplianceCheck `json:"items"`
	}
	if err := cc.c.do(ctx, http.MethodGet, "/v1/admin/compliance/"+url.PathEscape(govID)+"/checks", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Reports lists compliance summaries across governance objects.
func (cc *ComplianceClient) Reports(ctx context.Context, cursor string, limit int) (*ComplianceReportPage, error) {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/admin/compliance/reports"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out ComplianceReportPage
	if err := cc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
