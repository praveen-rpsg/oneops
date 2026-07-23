package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PolicyCondition selects events a policy reacts to (empty slices mean "any").
type PolicyCondition struct {
	Operations []string          `json:"operations,omitempty"`
	Resources  []string          `json:"resources,omitempty"`
	Actors     []string          `json:"actors,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// PolicyActionSpec names an action type and its opaque configuration.
type PolicyActionSpec struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Policy is an automation reacting to committed governance events.
type Policy struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Enabled    bool             `json:"enabled"`
	Condition  PolicyCondition  `json:"condition"`
	Action     PolicyActionSpec `json:"action"`
	MaxRetries int              `json:"max_retries"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

// PolicyExecution is one asynchronous policy run.
type PolicyExecution struct {
	ID         string    `json:"id"`
	PolicyID   string    `json:"policy_id"`
	EventID    string    `json:"event_id"`
	Operation  string    `json:"operation"`
	CfgID      string    `json:"cfg_id"`
	Status     string    `json:"status"`
	RetryCount int       `json:"retry_count"`
	Error      string    `json:"error"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreatePolicyInput is the body for CreatePolicy.
type CreatePolicyInput struct {
	Name       string           `json:"name"`
	Enabled    *bool            `json:"enabled,omitempty"`
	Condition  PolicyCondition  `json:"condition"`
	Action     PolicyActionSpec `json:"action"`
	MaxRetries int              `json:"max_retries,omitempty"`
}

// UpdatePolicyInput is the body for UpdatePolicy; nil fields are unchanged.
type UpdatePolicyInput struct {
	Name       *string           `json:"name,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Condition  *PolicyCondition  `json:"condition,omitempty"`
	Action     *PolicyActionSpec `json:"action,omitempty"`
	MaxRetries *int              `json:"max_retries,omitempty"`
}

// PoliciesClient administers policy automation (admin permission required).
type PoliciesClient struct {
	c *Client
}

// Policies returns the policy administration client.
func (c *Client) Policies() *PoliciesClient { return &PoliciesClient{c: c} }

// List returns all policies.
func (pc *PoliciesClient) List(ctx context.Context) ([]Policy, error) {
	var out struct {
		Items []Policy `json:"items"`
	}
	if err := pc.c.do(ctx, http.MethodGet, "/v1/admin/policies", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreatePolicy registers a policy.
func (pc *PoliciesClient) CreatePolicy(ctx context.Context, in CreatePolicyInput) (*Policy, error) {
	var out Policy
	if err := pc.c.do(ctx, http.MethodPost, "/v1/admin/policies", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePolicy patches a policy.
func (pc *PoliciesClient) UpdatePolicy(ctx context.Context, id string, in UpdatePolicyInput) (*Policy, error) {
	var out Policy
	if err := pc.c.do(ctx, http.MethodPatch, "/v1/admin/policies/"+url.PathEscape(id), nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeletePolicy removes a policy (its execution history is retained).
func (pc *PoliciesClient) DeletePolicy(ctx context.Context, id string) error {
	return pc.c.do(ctx, http.MethodDelete, "/v1/admin/policies/"+url.PathEscape(id), nil, nil, nil)
}

// Executions returns a policy's recent executions.
func (pc *PoliciesClient) Executions(ctx context.Context, id string, limit int) ([]PolicyExecution, error) {
	path := "/v1/admin/policies/" + url.PathEscape(id) + "/executions"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out struct {
		Items []PolicyExecution `json:"items"`
	}
	if err := pc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// TestPolicy runs one synthetic execution of the policy and returns its status.
func (pc *PoliciesClient) TestPolicy(ctx context.Context, id string) (string, error) {
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := pc.c.do(ctx, http.MethodPost, "/v1/admin/policies/"+url.PathEscape(id)+"/test", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}
