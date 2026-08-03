package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// maxAssetImportBatch bounds one import call — the same "no unbounded
// memory/query" rule every bulk op in this schema honors
// (docs/PLATFORM-BUILD-PLAN.md §3). A larger inventory is imported in
// several calls.
const maxAssetImportBatch = 1000

// SetAssets wires the CMDB asset repository. Until it is called the endpoints
// report 501 rather than 404, so a deployment that has not enabled the CMDB
// says so instead of looking like a routing mistake.
func (s *Server) SetAssets(repo domain.AssetRepository) { s.assets = repo }

type assetDTO struct {
	AssetID     string         `json:"asset_id"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Attributes  map[string]any `json:"attributes"`
	Status      string         `json:"status"`
	Environment string         `json:"environment"`
	Criticality string         `json:"criticality"`
	OwnerTeamID *string        `json:"owner_team_id,omitempty"`
	OwnerUserID *string        `json:"owner_user_id,omitempty"`
	Source      string         `json:"source"`
	ExternalRef *string        `json:"external_ref,omitempty"`
	// LastSeen is read-only (E1.5): no request DTO in this file accepts it.
	// Set by the store on Create/Update/Upsert — see domain.Asset.LastSeen's
	// doc comment. Absent (nil) when this CI has never been confirmed by any
	// source since the column existed.
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	RowVersion int64      `json:"row_version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// toAssetDTO deliberately omits tenant_id, the same choice toTeamDTO makes:
// the caller is already inside exactly one tenant, so echoing the boundary
// back tells them nothing they can act on.
func toAssetDTO(a *domain.Asset) assetDTO {
	return assetDTO{
		AssetID: a.AssetID, Type: a.Type, Name: a.Name, Attributes: a.Attributes,
		Status: string(a.Status), Environment: string(a.Environment), Criticality: string(a.Criticality),
		OwnerTeamID: a.OwnerTeamID, OwnerUserID: a.OwnerUserID,
		Source: a.Source, ExternalRef: a.ExternalRef, LastSeen: a.LastSeen,
		RowVersion: a.RowVersion,
		CreatedAt:  a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

type assetRelationshipDTO struct {
	RelationshipID string    `json:"relationship_id"`
	FromAssetID    string    `json:"from_asset_id"`
	ToAssetID      string    `json:"to_asset_id"`
	Type           string    `json:"type"`
	RowVersion     int64     `json:"row_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toAssetRelationshipDTO(r *domain.AssetRelationship) assetRelationshipDTO {
	return assetRelationshipDTO{
		RelationshipID: r.RelationshipID, FromAssetID: r.FromAssetID, ToAssetID: r.ToAssetID,
		Type: string(r.Type), RowVersion: r.RowVersion,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// assetChangeEntryDTO is one row of an asset's append-only change history
// (E1.3). OldValue/NewValue are omitted when nil rather than rendered as
// JSON null — AssetChangeCreated carries neither, and a field cleared to
// empty is represented by an empty string, not an absent key.
type assetChangeEntryDTO struct {
	ChangeID   string    `json:"change_id"`
	AssetID    string    `json:"asset_id"`
	Kind       string    `json:"kind"`
	Field      string    `json:"field,omitempty"`
	OldValue   *string   `json:"old_value,omitempty"`
	NewValue   *string   `json:"new_value,omitempty"`
	Actor      string    `json:"actor"`
	RowVersion int64     `json:"row_version"`
	OccurredAt time.Time `json:"occurred_at"`
}

func toAssetChangeEntryDTO(e *domain.AssetChangeEntry) assetChangeEntryDTO {
	return assetChangeEntryDTO{
		ChangeID: e.ChangeID, AssetID: e.AssetID, Kind: string(e.Kind), Field: e.Field,
		OldValue: e.OldValue, NewValue: e.NewValue, Actor: e.Actor,
		RowVersion: e.RowVersion, OccurredAt: e.OccurredAt,
	}
}

// createAssetRequest carries no asset_id: the identifier is minted
// server-side so a caller cannot choose one (the same rule createTeamRequest
// follows, and the reason `asset.asset_pkey` needs no tenant-key-scope
// justification beyond "no create route accepts one").
//
// Environment/Criticality default to "unknown" when omitted or blank
// (domain.Asset.ApplyClassification). OwnerTeamID/OwnerUserID are optional;
// when set, s.assets.Create re-verifies the id against the caller's tenant
// before the asset is written (ADR-ASSET-001 §6, extended to ownership).
// Status is optional and defaults to "active" (domain.NewAsset's own
// default, E1.3 decision); a caller pre-registering a CI not yet in service
// may set it to "planned" — any other value is refused, because maintenance
// and retired describe something that happened to a CI already in service
// and are reachable only by a later transition (PATCH .../status).
type createAssetRequest struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Attributes  map[string]any `json:"attributes"`
	Status      string         `json:"status"`
	Environment string         `json:"environment"`
	Criticality string         `json:"criticality"`
	OwnerTeamID *string        `json:"owner_team_id"`
	OwnerUserID *string        `json:"owner_user_id"`
	// Source/ExternalRef are the E1.4 provenance fields. Source defaults to
	// "manual" when omitted or blank; ExternalRef is the natural key an
	// external source uses for this CI, or absent for a manual entry.
	// Setting ExternalRef here does not make this call idempotent — that is
	// what POST .../import (AssetRepository.Upsert) is for; a second
	// createAsset call always creates a second row.
	Source      string  `json:"source"`
	ExternalRef *string `json:"external_ref"`
}

// patchAssetRequest changes one or more non-lifecycle fields in a single
// call, OR performs exactly one lifecycle transition — never both in the
// same request (E1.3). The two go through different repository methods on
// purpose, mirroring patchUser: SetStatus is the single place a transition
// is checked, and a call that did both would consume two row versions and
// leave the caller unable to say which half failed.
//
// OwnerTeamID/OwnerUserID follow domain.AssetPatch's tri-state rule: absent
// (nil) leaves ownership unchanged; present as "" clears it; present as a
// non-empty string sets it, re-verified against the caller's tenant first.
type patchAssetRequest struct {
	RowVersion  int64          `json:"row_version"`
	Name        *string        `json:"name"`
	Attributes  map[string]any `json:"attributes"`
	Status      *string        `json:"status"`
	Environment *string        `json:"environment"`
	Criticality *string        `json:"criticality"`
	OwnerTeamID *string        `json:"owner_team_id"`
	OwnerUserID *string        `json:"owner_user_id"`
}

// nonStatusPatchFieldsSet reports whether req carries any field other than
// status — used to refuse a request that tries to combine a lifecycle
// transition with an ordinary field change.
func (req patchAssetRequest) nonStatusPatchFieldsSet() bool {
	return req.Name != nil || req.Attributes != nil || req.Environment != nil ||
		req.Criticality != nil || req.OwnerTeamID != nil || req.OwnerUserID != nil
}

type createAssetRelationshipRequest struct {
	FromAssetID string `json:"from_asset_id"`
	ToAssetID   string `json:"to_asset_id"`
	Type        string `json:"type"`
}

// listAssets returns a page of the caller's assets. By default (no status
// query param) a retired asset is excluded — soft-retire (E1.3) removes it
// from the ordinary view, not from existence — but it remains individually
// fetchable via getAsset, and ?status=retired lists exactly the retired
// ones.
func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}
	status := domain.AssetStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !status.Valid() {
		s.mapError(w, r, domain.NewValidationError("status", "must be one of: planned, active, maintenance, retired"))
		return
	}

	items, err := s.assets.List(r.Context(), limit, r.URL.Query().Get("after"), status)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]assetDTO, 0, len(items))
	for _, a := range items {
		out = append(out, toAssetDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createAsset registers a Configuration Item.
func (s *Server) createAsset(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	var req createAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule createTeam enforces, for the same reason: a request that could
	// name its own tenant_id could plant an asset inside another boundary.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	a, err := domain.NewAsset(tenant.TenantID, req.Type, req.Name, req.Attributes)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if err := a.ApplyClassification(req.Environment, req.Criticality, req.OwnerTeamID, req.OwnerUserID); err != nil {
		s.mapError(w, r, err)
		return
	}
	if err := a.ApplySource(req.Source, req.ExternalRef); err != nil {
		s.mapError(w, r, err)
		return
	}
	if status := domain.AssetStatus(strings.TrimSpace(req.Status)); status != "" {
		if !status.ValidInitialStatus() {
			s.mapError(w, r, domain.NewValidationError("status",
				"must be one of: planned, active at creation; maintenance and retired are reached by transition"))
			return
		}
		a.Status = status
	}
	created, err := s.assets.Create(r.Context(), a)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found",
			"owner_team_id or owner_user_id does not name a team or user visible to this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAssetDTO(created))
}

// getAsset returns one asset by identifier.
func (s *Server) getAsset(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	a, err := s.assets.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such asset")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAssetDTO(a))
}

// patchAsset changes name/attributes/environment/criticality/ownership, OR
// performs a lifecycle transition — never both (see patchAssetRequest).
func (s *Server) patchAsset(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version",
			"must be the row_version last read for this asset"))
		return
	}
	switch {
	case req.Status == nil && !req.nonStatusPatchFieldsSet():
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of name, attributes, status, environment, criticality, owner_team_id or owner_user_id"))
		return
	case req.Status != nil && req.nonStatusPatchFieldsSet():
		s.mapError(w, r, domain.NewValidationError("body",
			"a status transition must be supplied on its own; changing both would consume "+
				"two row versions and leave the outcome of one of them unreported"))
		return
	}

	var (
		updated *domain.Asset
		err     error
	)
	if req.Status != nil {
		status := domain.AssetStatus(strings.TrimSpace(*req.Status))
		if !status.Valid() {
			s.mapError(w, r, domain.NewValidationError("status", "must be one of: planned, active, maintenance, retired"))
			return
		}
		updated, err = s.assets.SetStatus(r.Context(), id, req.RowVersion, status)
	} else {
		patch := domain.AssetPatch{Attributes: req.Attributes, OwnerTeamID: req.OwnerTeamID, OwnerUserID: req.OwnerUserID}
		if req.Name != nil {
			trimmed := strings.TrimSpace(*req.Name)
			patch.Name = &trimmed
		}
		if req.Environment != nil {
			env := domain.AssetEnvironment(strings.TrimSpace(*req.Environment))
			if !env.Valid() {
				s.mapError(w, r, domain.NewValidationError("environment", "must be one of: production, staging, development, unknown"))
				return
			}
			patch.Environment = &env
		}
		if req.Criticality != nil {
			crit := domain.AssetCriticality(strings.TrimSpace(*req.Criticality))
			if !crit.Valid() {
				s.mapError(w, r, domain.NewValidationError("criticality", "must be one of: critical, high, medium, low, unknown"))
				return
			}
			patch.Criticality = &crit
		}
		updated, err = s.assets.Update(r.Context(), id, req.RowVersion, patch)
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found",
			"no such asset, or owner_team_id/owner_user_id does not name a team or user visible to this tenant")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "asset was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAssetDTO(updated))
}

// deleteAsset removes an asset. Its relationships are removed with it.
func (s *Server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	err := s.assets.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such asset")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createAssetRelationship adds a directed, typed edge to the CMDB graph.
func (s *Server) createAssetRelationship(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	var req createAssetRelationshipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	rel, err := domain.NewAssetRelationship(tenant.TenantID, req.FromAssetID, req.ToAssetID, domain.RelationshipType(req.Type))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.assets.CreateRelationship(r.Context(), rel)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "from_asset_id or to_asset_id does not name an asset this tenant can see")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAssetRelationshipDTO(created))
}

// deleteAssetRelationship removes a relationship by identifier.
func (s *Server) deleteAssetRelationship(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	err := s.assets.DeleteRelationship(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such relationship")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listAssetRelationships returns the direct (one-hop) edges naming assetID,
// in both directions.
func (s *Server) listAssetRelationships(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.assets.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
		return
	}

	from, err := s.assets.RelationshipsFrom(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	to, err := s.assets.RelationshipsTo(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	fromOut := make([]assetRelationshipDTO, 0, len(from))
	for _, rel := range from {
		fromOut = append(fromOut, toAssetRelationshipDTO(rel))
	}
	toOut := make([]assetRelationshipDTO, 0, len(to))
	for _, rel := range to {
		toOut = append(toOut, toAssetRelationshipDTO(rel))
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": fromOut, "to": toOut})
}

// getAssetDependencies serves GET /v1/admin/assets/{id}/dependencies.
func (s *Server) getAssetDependencies(w http.ResponseWriter, r *http.Request) {
	s.serveAssetTraversal(w, r, domain.DirectionDependencies)
}

// getAssetDependents serves GET /v1/admin/assets/{id}/dependents.
func (s *Server) getAssetDependents(w http.ResponseWriter, r *http.Request) {
	s.serveAssetTraversal(w, r, domain.DirectionDependents)
}

// serveAssetTraversal answers a dependencies/dependents query over the CMDB
// graph. It is the asset-graph mirror of Server.serveTraversal
// (handlers_graph.go): same shape, same recursive/max_depth parameters, over
// s.assetGraph/s.assetGraphRepo instead of s.graph/s.graphRepo — the CMDB's
// own graph.Service, built from AssetGraphRepo (ADR-ASSET-001 §4).
func (s *Server) serveAssetTraversal(w http.ResponseWriter, r *http.Request, dir domain.Direction) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	if s.assetGraph == nil || s.assetGraphRepo == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "asset graph service unavailable")
		return
	}
	assetID := chi.URLParam(r, "id")
	if _, err := s.assets.Get(r.Context(), assetID); err != nil {
		s.mapError(w, r, err)
		return
	}
	recursive, err := parseBoolParam(r.URL.Query().Get("recursive"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid recursive: must be true or false")
		return
	}
	maxDepth, err := parseDepthParam(r.URL.Query().Get("max_depth"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid max_depth: must be a positive integer")
		return
	}

	var nodes []domain.TraversalNode
	if recursive {
		var res *domain.TraversalResult
		if dir == domain.DirectionDependents {
			res, err = s.assetGraph.WalkDependents(r.Context(), assetID)
		} else {
			res, err = s.assetGraph.WalkDependencies(r.Context(), assetID)
		}
		if err != nil {
			s.mapError(w, r, err)
			return
		}
		nodes = res.Nodes
		if maxDepth > 0 {
			nodes = filterByDepth(nodes, maxDepth)
		}
	} else {
		var ids []string
		if dir == domain.DirectionDependents {
			ids, err = s.assetGraphRepo.Dependents(r.Context(), assetID)
		} else {
			ids, err = s.assetGraphRepo.Dependencies(r.Context(), assetID)
		}
		if err != nil {
			s.mapError(w, r, err)
			return
		}
		nodes = make([]domain.TraversalNode, len(ids))
		for i, nid := range ids {
			nodes[i] = domain.TraversalNode{CfgID: nid, Depth: 1}
		}
	}

	writeJSON(w, http.StatusOK, newAssetTraversalResponse(assetID, dir, recursive, nodes))
}

// assetTypeBusinessService is the seeded Asset.Type value a service-map
// query applies to (E1.2). Asset.Type is open-but-validated, not a closed
// enum (ADR-ASSET-001 §1), so this is a convention this endpoint enforces,
// not a database constraint.
const assetTypeBusinessService = "business_service"

// getAssetServiceMap serves GET /v1/admin/assets/{id}/service-map: the
// supporting Configuration Items composing a business_service, computed as a
// PROJECTION over the existing CMDB graph — no new stored structure. It
// walks only the composition edge types, depends_on and runs_on
// (connected_to is a network link and member_of is grouping, neither
// composes a service), reusing the same recursive-CTE traversal the
// /dependencies endpoint uses via domain.TypedGraphTraversal
// (ADR-ASSET-001 §4).
func (s *Server) getAssetServiceMap(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	asset, err := s.assets.Get(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if asset.Type != assetTypeBusinessService {
		s.mapError(w, r, domain.NewValidationError("id",
			"asset is not a business_service; service-map composes only business_service assets"))
		return
	}

	typed, ok := s.assetGraphRepo.(domain.TypedGraphTraversal)
	if !ok {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "asset graph service unavailable")
		return
	}
	nodes, err := typed.RecursiveDependenciesOfTypes(r.Context(), id,
		[]string{string(domain.RelationshipDependsOn), string(domain.RelationshipRunsOn)})
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newServiceMapResponse(id, nodes))
}

// getAssetHistory serves GET /v1/admin/assets/{id}/history: the append-only
// record of every change to a CI (E1.3) — field, old/new value, actor, and
// the resulting row_version, in chronological order.
//
// The asset must currently exist (checked via Get, the same 404 shape every
// other per-asset endpoint here uses). asset_change_history itself carries
// no such requirement — a row survives a hard Delete of its asset by design
// (see the migration) — so a fully deleted asset's history remains in the
// database, addressable at the storage layer, but is not exposed through
// this asset-scoped route once the asset itself is gone.
func (s *Server) getAssetHistory(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.assets.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
		return
	}

	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}

	items, err := s.assets.History(r.Context(), id, limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]assetChangeEntryDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toAssetChangeEntryDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// importAssetRow is one row of a bulk import (E1.4): the same shape as
// createAssetRequest, matched by (tenant, source, external_ref). See
// AssetRepository.Upsert's doc comment for the exact match/replace/history
// semantics: ExternalRef nil always creates; a non-nil ExternalRef that
// matches an existing (tenant, source, external_ref) triple updates it,
// replacing every field this row carries.
type importAssetRow struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Attributes  map[string]any `json:"attributes"`
	Status      string         `json:"status"`
	Environment string         `json:"environment"`
	Criticality string         `json:"criticality"`
	OwnerTeamID *string        `json:"owner_team_id"`
	OwnerUserID *string        `json:"owner_user_id"`
	Source      string         `json:"source"`
	ExternalRef *string        `json:"external_ref"`
}

type importAssetsRequest struct {
	Items []importAssetRow `json:"items"`
}

// assetImportResultDTO is one row's outcome, always one per input row and at
// that row's input index, so a caller can correlate a failure back to the
// request it sent without re-transmitting the payload.
type assetImportResultDTO struct {
	Index   int    `json:"index"`
	Outcome string `json:"outcome"`
	AssetID string `json:"asset_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type importAssetsResponse struct {
	Results []assetImportResultDTO `json:"results"`
}

// importAssets bulk-upserts Configuration Items by (tenant, source,
// external_ref) — the idempotent multi-source ingestion path (E1.4).
// Re-importing the same payload creates no new rows and writes no spurious
// change history: see AssetStore.Upsert's doc comment. Each row is built,
// validated and upserted independently and reported at its own index — one
// bad row (a validation failure, an owner reference the tenant cannot see)
// does not abort the rows around it, unlike bulkCreateArtifacts's single
// all-or-nothing transaction, because a multi-source import of hundreds of
// CIs must not be held hostage by one malformed record.
func (s *Server) importAssets(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	var req importAssetsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if len(req.Items) == 0 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", "items must not be empty")
		return
	}
	if len(req.Items) > maxAssetImportBatch {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed",
			fmt.Sprintf("items must not exceed %d per import", maxAssetImportBatch))
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	results := make([]assetImportResultDTO, len(req.Items))
	for i, row := range req.Items {
		results[i] = s.importAssetRow(r.Context(), tenant.TenantID, i, row)
	}
	writeJSON(w, http.StatusOK, importAssetsResponse{Results: results})
}

// importAssetRow builds, validates and upserts a single import row, never
// returning transport-level errors of its own — every outcome, including a
// validation or owner-reference failure, is reported in the result at the
// row's index so one row's failure cannot take the whole call down with a
// single error response.
func (s *Server) importAssetRow(ctx context.Context, tenantID string, index int, row importAssetRow) assetImportResultDTO {
	a, err := domain.NewAsset(tenantID, row.Type, row.Name, row.Attributes)
	if err == nil {
		err = a.ApplyClassification(row.Environment, row.Criticality, row.OwnerTeamID, row.OwnerUserID)
	}
	if err == nil {
		err = a.ApplySource(row.Source, row.ExternalRef)
	}
	if err == nil {
		if status := domain.AssetStatus(strings.TrimSpace(row.Status)); status != "" {
			if !status.ValidInitialStatus() {
				err = domain.NewValidationError("status",
					"must be one of: planned, active at creation; maintenance and retired are reached by transition")
			} else {
				a.Status = status
			}
		}
	}
	if err != nil {
		return assetImportResultDTO{Index: index, Outcome: string(domain.AssetImportFailed), Reason: err.Error()}
	}

	result, created, err := s.assets.Upsert(ctx, a)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, domain.ErrNotFound) {
			reason = "owner_team_id or owner_user_id does not name a team or user visible to this tenant"
		}
		return assetImportResultDTO{Index: index, Outcome: string(domain.AssetImportFailed), Reason: reason}
	}
	outcome := domain.AssetImportUpdated
	if created {
		outcome = domain.AssetImportCreated
	}
	return assetImportResultDTO{Index: index, Outcome: string(outcome), AssetID: result.AssetID}
}

// exportAssets serves a paginated full export of the caller's tenant's CMDB
// — every field, every status (including retired) — for backup or migration
// (E1.4). Unlike listAssets, retirement does not filter this view: an export
// exists to reconstruct the CMDB exactly as it stands, not as an operator
// browses it. Relationship export is deferred (E1.4b's scope note).
func (s *Server) exportAssets(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}
	items, err := s.assets.Export(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]assetDTO, 0, len(items))
	for _, a := range items {
		out = append(out, toAssetDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// assetDuplicateMemberDTO is one Configuration Item inside a duplicate group.
type assetDuplicateMemberDTO struct {
	AssetID     string  `json:"asset_id"`
	Type        string  `json:"type"`
	Name        string  `json:"name"`
	Source      string  `json:"source"`
	ExternalRef *string `json:"external_ref,omitempty"`
	Status      string  `json:"status"`
}

type assetDuplicateGroupDTO struct {
	Reason  string                    `json:"reason"`
	Key     string                    `json:"key"`
	Members []assetDuplicateMemberDTO `json:"members"`
}

// duplicateAssets serves GET /admin/assets/duplicates: a read-only report of
// Configuration Items likely describing the same real-world thing (E1.4).
// Nothing here merges or mutates anything — see domain.AssetRepository.
// Duplicates' doc comment. Controlled merge/resolution is E1.4b, a
// separately gated later increment.
func (s *Server) duplicateAssets(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	groups, err := s.assets.Duplicates(r.Context())
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]assetDuplicateGroupDTO, 0, len(groups))
	for _, g := range groups {
		members := make([]assetDuplicateMemberDTO, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, assetDuplicateMemberDTO{
				AssetID: m.AssetID, Type: m.Type, Name: m.Name, Source: m.Source,
				ExternalRef: m.ExternalRef, Status: string(m.Status),
			})
		}
		out = append(out, assetDuplicateGroupDTO{Reason: string(g.Reason), Key: g.Key, Members: members})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// assetHealthSampleDTO is one Configuration Item inside a bounded
// health-report category sample (E1.5).
type assetHealthSampleDTO struct {
	AssetID string `json:"asset_id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
}

// assetHealthCategoryDTO is one CMDB data-quality/freshness dimension: count
// is the TRUE, uncapped total; samples is bounded (see domain.
// AssetHealthCategory's doc comment) — a caller with more candidates than the
// sample re-runs the report after acting on the first batch, or (for stale)
// narrows stale_after.
type assetHealthCategoryDTO struct {
	Count   int                    `json:"count"`
	Samples []assetHealthSampleDTO `json:"samples"`
}

func toAssetHealthCategoryDTO(c domain.AssetHealthCategory) assetHealthCategoryDTO {
	samples := make([]assetHealthSampleDTO, 0, len(c.Samples))
	for _, m := range c.Samples {
		samples = append(samples, assetHealthSampleDTO{AssetID: m.AssetID, Type: m.Type, Name: m.Name})
	}
	return assetHealthCategoryDTO{Count: c.Count, Samples: samples}
}

// assetHealthReportDTO mirrors domain.AssetHealthReport (E1.5): where the
// CMDB is rotting, never whether it matches reality — true config-drift-vs-
// discovered-state needs E2's monitoring/discovery and is explicitly out of
// scope here.
type assetHealthReportDTO struct {
	StaleAfter               string                 `json:"stale_after"`
	Stale                    assetHealthCategoryDTO `json:"stale"`
	OrphanedAssets           assetHealthCategoryDTO `json:"orphaned_assets"`
	OrphanedBusinessServices assetHealthCategoryDTO `json:"orphaned_business_services"`
	Incomplete               assetHealthCategoryDTO `json:"incomplete"`
}

// assetHealth serves GET /admin/assets/health: the CMDB data-quality/
// freshness report (E1.5) — see domain.AssetHealthReport's doc comment for
// exactly what each category means. Read-only: nothing here mutates
// anything. stale_after is an optional Go duration (e.g. "720h", "30m");
// omitted, it defaults to domain.DefaultAssetStaleAfter (30 days).
func (s *Server) assetHealth(w http.ResponseWriter, r *http.Request) {
	if s.assets == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "asset administration is not configured")
		return
	}
	staleAfter, err := domain.ParseAssetStaleAfter(r.URL.Query().Get("stale_after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	report, err := s.assets.Health(r.Context(), staleAfter)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, assetHealthReportDTO{
		StaleAfter:               report.StaleAfter.String(),
		Stale:                    toAssetHealthCategoryDTO(report.Stale),
		OrphanedAssets:           toAssetHealthCategoryDTO(report.OrphanedAssets),
		OrphanedBusinessServices: toAssetHealthCategoryDTO(report.OrphanedBusinessServices),
		Incomplete:               toAssetHealthCategoryDTO(report.Incomplete),
	})
}
