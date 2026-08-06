package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/rpsg/oneops/internal/domain"
)

// tenantUserDirectory is the narrow read GET /admin/tenant-users needs — the
// same "one method, not the whole repository" shape nocEscalationReader
// establishes for GET /admin/noc/overview. *postgres.MembershipStore
// (tenant-scoped appPool) satisfies it via ListActiveDirectory.
type tenantUserDirectory interface {
	ListActiveDirectory(ctx context.Context, limit int, after string) ([]*domain.TenantUserSummary, error)
}

// SetTenantUserDirectory wires the tenant-scoped user-directory projection.
// Until it is called, GET /admin/tenant-users answers 501, the same
// convention every other administration surface in this package uses for an
// unwired dependency.
func (s *Server) SetTenantUserDirectory(d tenantUserDirectory) { s.tenantUsers = d }

// tenantUserDTO is deliberately narrower than userDTO (handlers_users.go):
// it carries only what an assignee/roster picker needs. No status, no
// row_version, no timestamps — this is a read-only projection of ACTIVE
// members, not the User aggregate, and nothing here is ever written back.
type tenantUserDTO struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

func toTenantUserDTO(u *domain.TenantUserSummary) tenantUserDTO {
	return tenantUserDTO{UserID: u.UserID, Email: u.Email, DisplayName: u.DisplayName}
}

// listTenantUsers serves GET /admin/tenant-users (E-ACT.0): a bounded,
// keyset-paginated directory of the CALLER'S OWN tenant's active members,
// for populating an assignee/roster picker without the cross-tenant
// requirePlatformAdmin gate GET /admin/users sits behind (ADR-ACT-003).
//
// Row-level security alone confines the result to the caller's tenant — see
// MembershipStore.ListActiveDirectory's own doc comment — so this handler
// carries no tenant predicate of its own, exactly like getAssetGraph and
// nocOverview.
func (s *Server) listTenantUsers(w http.ResponseWriter, r *http.Request) {
	if s.tenantUsers == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented",
			"the tenant user directory is not configured")
		return
	}

	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.mapError(w, r, domain.NewValidationError("limit", "must be a positive integer"))
			return
		}
		limit = n
	}

	items, err := s.tenantUsers.ListActiveDirectory(
		r.Context(), limit, strings.TrimSpace(r.URL.Query().Get("after")))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]tenantUserDTO, 0, len(items))
	for _, u := range items {
		out = append(out, toTenantUserDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
