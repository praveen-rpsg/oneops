package sdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// User is a person known to the platform.
//
// Users are global: a person exists before, and independently of, any
// membership, so this is not scoped to a tenant and administering it is a
// platform operation.
type User struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// Status is one of invited, active, suspended, deactivated.
	Status string `json:"status"`
	// RowVersion is the optimistic-concurrency guard. Patch requires the value
	// last read for this user.
	RowVersion int64     `json:"row_version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Organization is a scope of Identity to which Policy attaches, realised as an
// isolation boundary by exactly one Tenant.
type Organization struct {
	OrgID string `json:"org_id"`
	// TenantID is the tenant realising this organisation's boundary. It points
	// AT the boundary rather than naming the row's owner, and is minted by the
	// platform alongside OrgID.
	TenantID string `json:"tenant_id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	// Status is one of active, suspended. Suspending an organisation cascades
	// to its tenant.
	Status     string    `json:"status"`
	RowVersion int64     `json:"row_version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CreateUserInput is the body for Users().Create.
//
// There is no user_id: the identifier is minted server-side so a client cannot
// choose one.
type CreateUserInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

// PatchUserInput changes exactly one thing per call.
//
// DisplayName and Status are pointers so an absent field is distinguishable
// from an empty one — clearing a display name and not touching it are different
// requests, and a non-pointer string cannot express the difference. The server
// rejects a request that sets both, or neither; that rule is enforced there and
// is deliberately not repeated here.
type PatchUserInput struct {
	// RowVersion is required and carries no omitempty: the field must reach the
	// server even when it is zero, so the server reports the missing guard
	// rather than a request that merely looks absent.
	RowVersion  int64   `json:"row_version"`
	DisplayName *string `json:"display_name,omitempty"`
	Status      *string `json:"status,omitempty"`
}

// CreateOrganizationInput is the body for Organizations().Create.
//
// Neither identifier is accepted: org_id and tenant_id are both minted
// server-side, so a caller cannot name the system tenant.
type CreateOrganizationInput struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// PatchOrganizationInput suspends or reactivates an organisation.
//
// Status is the only mutation the registry exposes. Renaming is not a modelled
// operation, so the SDK offers no way to attempt one.
type PatchOrganizationInput struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
}

// ListUsersInput filters and pages the user registry. The zero value lists the
// first page with the server's default size.
type ListUsersInput struct {
	// Email resolves a single address case-insensitively, returning a page of
	// zero or one. An address that matches nothing is an empty page, not an
	// error: the collection exists whether or not anything in it matched.
	Email string
	// Limit is clamped by the server; zero means the server's default.
	Limit int
	// After is the keyset cursor — the last UserID of the previous page.
	After string
}

// ListOrganizationsInput filters and pages the organisation registry.
type ListOrganizationsInput struct {
	// Slug resolves a single organisation, returning a page of zero or one.
	Slug  string
	Limit int
	// After is the keyset cursor — the last OrgID of the previous page.
	After string
}

// withQuery appends the encoded parameters to path, omitting empty ones.
//
// url.Values does the encoding rather than string concatenation, and that is
// load-bearing for the identity surface specifically: an email address may
// contain `+`, which is a space when a query string is decoded, so a
// hand-assembled query silently looks up the wrong address. `@` and non-ASCII
// local parts have the same problem.
func withQuery(path string, params map[string]string) string {
	q := url.Values{}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// limitParam renders a page size, treating anything non-positive as unset so
// the server applies its own default.
func limitParam(limit int) string {
	if limit <= 0 {
		return ""
	}
	return strconv.Itoa(limit)
}

// UsersClient administers the global user registry (platform admin required).
type UsersClient struct {
	c *Client
}

// Users returns the user administration client.
func (c *Client) Users() *UsersClient { return &UsersClient{c: c} }

// List returns a page of users, or resolves one by address when Email is set.
func (uc *UsersClient) List(ctx context.Context, in ListUsersInput) ([]User, error) {
	path := withQuery("/v1/admin/users", map[string]string{
		"email": in.Email,
		"limit": limitParam(in.Limit),
		"after": in.After,
	})
	var out struct {
		Items []User `json:"items"`
	}
	if err := uc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Get returns one user by platform identifier.
func (uc *UsersClient) Get(ctx context.Context, id string) (*User, error) {
	var out User
	if err := uc.c.do(ctx, http.MethodGet, "/v1/admin/users/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Create registers a user. The user starts invited and holds no access until a
// membership is granted.
func (uc *UsersClient) Create(ctx context.Context, in CreateUserInput) (*User, error) {
	var out User
	if err := uc.c.do(ctx, http.MethodPost, "/v1/admin/users", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Patch renames a user or performs a lifecycle transition.
//
// A refused transition is a conflict rather than a validation failure: the
// target state is well-formed, it is the move that is not allowed. Use
// IsConflict to tell that apart from a stale RowVersion, which is also a
// conflict and is the signal to re-read and decide again.
func (uc *UsersClient) Patch(ctx context.Context, id string, in PatchUserInput) (*User, error) {
	var out User
	if err := uc.c.do(ctx, http.MethodPatch,
		"/v1/admin/users/"+url.PathEscape(id), nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OrganizationsClient administers the global organisation registry (platform
// admin required).
type OrganizationsClient struct {
	c *Client
}

// Organizations returns the organisation administration client.
func (c *Client) Organizations() *OrganizationsClient { return &OrganizationsClient{c: c} }

// List returns a page of organisations, or resolves one by slug when Slug is set.
func (oc *OrganizationsClient) List(ctx context.Context, in ListOrganizationsInput) ([]Organization, error) {
	path := withQuery("/v1/admin/organizations", map[string]string{
		"slug":  in.Slug,
		"limit": limitParam(in.Limit),
		"after": in.After,
	})
	var out struct {
		Items []Organization `json:"items"`
	}
	if err := oc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Get returns one organisation by platform identifier.
func (oc *OrganizationsClient) Get(ctx context.Context, id string) (*Organization, error) {
	var out Organization
	if err := oc.c.do(ctx, http.MethodGet,
		"/v1/admin/organizations/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Create registers an organisation and the tenant realising it. Both rows are
// written in one transaction server-side: there is no state in which one exists
// without the other.
func (oc *OrganizationsClient) Create(ctx context.Context, in CreateOrganizationInput) (*Organization, error) {
	var out Organization
	if err := oc.c.do(ctx, http.MethodPost, "/v1/admin/organizations", nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Patch suspends or reactivates an organisation.
//
// Suspension cascades to the realising tenant, so this disables every request
// made in that boundary — not only administration of the organisation record.
func (oc *OrganizationsClient) Patch(ctx context.Context, id string, in PatchOrganizationInput) (*Organization, error) {
	var out Organization
	if err := oc.c.do(ctx, http.MethodPatch,
		"/v1/admin/organizations/"+url.PathEscape(id), nil, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
