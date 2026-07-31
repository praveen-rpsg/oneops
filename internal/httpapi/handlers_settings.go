package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetSettings wires the setting repository. Until it is called the endpoints
// report 501 rather than 404, the same convention SetTeams uses.
func (s *Server) SetSettings(repo domain.SettingRepository) { s.settings = repo }

// SetApprovals wires the multi-approver quorum read repository (ADR-GOV-005).
// Until it is called, GET /governance/{id}/approvals reports 501 rather than
// 404, the same convention SetSettings uses.
func (s *Server) SetApprovals(repo domain.ApprovalRepository) { s.approvals = repo }

// requiredApprovals resolves the calling tenant's configured Approval quorum
// threshold (Setting governance_required_approvals; ADR-GOV-005). Absent a
// wired setting repository it returns the definition's own default (1) —
// exactly the same "not configured yet" fallback listSettings/putSetting
// would otherwise force every caller to special-case.
func (s *Server) requiredApprovals(ctx context.Context) (int, error) {
	if s.settings == nil {
		return domain.EffectiveRequiredApprovals(nil), nil
	}
	overrides, err := s.settings.GetAll(ctx)
	if err != nil {
		return 0, err
	}
	return domain.EffectiveRequiredApprovals(overrides), nil
}

type settingDTO struct {
	Key         string    `json:"key"`
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Value       any       `json:"value"`
	Default     any       `json:"default"`
	Overridden  bool      `json:"overridden"`
	RowVersion  int64     `json:"row_version"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// toSettingDTO renders an effective setting's canonical stored string as the
// typed JSON value its definition promises — a bool renders as true/false, an
// int as a number — rather than as the string every value is stored as.
func toSettingDTO(es domain.EffectiveSetting) settingDTO {
	def := domain.SettingDefinitions[es.Key]
	return settingDTO{
		Key: es.Key, Type: string(es.Type), Description: es.Description,
		Value: def.DecodeValue(es.Value), Default: def.DecodeValue(es.Default),
		Overridden: es.Overridden, RowVersion: es.RowVersion, UpdatedAt: es.UpdatedAt,
	}
}

// putSettingRequest carries no key: it is taken from the URL, and no
// tenant_id: the tenant is resolved from the bound connection, never from the
// caller (the same rule createTeam enforces). Value is untyped because its
// legal shape depends on which key it is — a bool for one definition, an
// integer for another — and that is exactly what EncodeValue below checks.
type putSettingRequest struct {
	Value      any   `json:"value"`
	RowVersion int64 `json:"row_version"`
}

// listSettings serves GET /admin/settings: the tenant's effective settings —
// every defined key, resolved to its stored override or its default.
func (s *Server) listSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "setting administration is not configured")
		return
	}
	overrides, err := s.settings.GetAll(r.Context())
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	effective := domain.ResolveSettings(overrides)
	out := make([]settingDTO, 0, len(effective))
	for _, es := range effective {
		out = append(out, toSettingDTO(es))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// putSetting serves PUT /admin/settings/{key}: create or update the caller's
// tenant's override of one defined setting.
//
// An unknown key or a value that fails its definition's validation is
// rejected with 422 before the store is ever reached — the allowlist is
// enforced here and again in domain.Setting.Validate, so a defect in one does
// not silently widen what the other accepts.
func (s *Server) putSetting(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "setting administration is not configured")
		return
	}
	key := chi.URLParam(r, "key")
	def, ok := domain.GetDefinition(key)
	if !ok {
		s.mapError(w, r, domain.NewValidationError("key", "\""+key+"\" is not a defined setting"))
		return
	}

	var req putSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if req.RowVersion < 0 {
		s.mapError(w, r, domain.NewValidationError("row_version",
			"must be 0 to create, or the row_version last read to update"))
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	value, err := def.EncodeValue(req.Value)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	st, err := domain.NewSetting(tenant.TenantID, key, value, req.RowVersion)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	updated, err := s.settings.Upsert(r.Context(), st)
	switch {
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict",
			"setting was created or modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingDTO(domain.EffectiveSetting{
		Key: key, Type: def.Type, Description: def.Description,
		Value: updated.Value, Default: def.Default,
		Overridden: true, RowVersion: updated.RowVersion, UpdatedAt: updated.UpdatedAt,
	}))
}
