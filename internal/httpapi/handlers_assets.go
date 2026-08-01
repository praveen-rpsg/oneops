package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetAssets wires the CMDB asset repository. Until it is called the endpoints
// report 501 rather than 404, so a deployment that has not enabled the CMDB
// says so instead of looking like a routing mistake.
func (s *Server) SetAssets(repo domain.AssetRepository) { s.assets = repo }

type assetDTO struct {
	AssetID    string         `json:"asset_id"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
	Status     string         `json:"status"`
	RowVersion int64          `json:"row_version"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// toAssetDTO deliberately omits tenant_id, the same choice toTeamDTO makes:
// the caller is already inside exactly one tenant, so echoing the boundary
// back tells them nothing they can act on.
func toAssetDTO(a *domain.Asset) assetDTO {
	return assetDTO{
		AssetID: a.AssetID, Type: a.Type, Name: a.Name, Attributes: a.Attributes,
		Status: string(a.Status), RowVersion: a.RowVersion,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
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

// createAssetRequest carries no asset_id: the identifier is minted
// server-side so a caller cannot choose one (the same rule createTeamRequest
// follows, and the reason `asset.asset_pkey` needs no tenant-key-scope
// justification beyond "no create route accepts one").
type createAssetRequest struct {
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

// patchAssetRequest changes one or more of name/attributes/status in a single
// call. Unlike patchTeamRequest, these do not go through different repository
// methods — Asset has no authorisation-adjacent status split to protect — so
// there is no reason to forbid combining them.
type patchAssetRequest struct {
	RowVersion int64          `json:"row_version"`
	Name       *string        `json:"name"`
	Attributes map[string]any `json:"attributes"`
	Status     *string        `json:"status"`
}

type createAssetRelationshipRequest struct {
	FromAssetID string `json:"from_asset_id"`
	ToAssetID   string `json:"to_asset_id"`
	Type        string `json:"type"`
}

// listAssets returns a page of the caller's assets.
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

	items, err := s.assets.List(r.Context(), limit, r.URL.Query().Get("after"))
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
	created, err := s.assets.Create(r.Context(), a)
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

// patchAsset changes name, attributes and/or status under optimistic locking.
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
	if req.Name == nil && req.Attributes == nil && req.Status == nil {
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of name, attributes or status"))
		return
	}

	var name *string
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		name = &trimmed
	}
	var status *domain.AssetStatus
	if req.Status != nil {
		st := domain.AssetStatus(strings.TrimSpace(*req.Status))
		if !st.Valid() {
			s.mapError(w, r, domain.NewValidationError("status", "must be one of: active, retired"))
			return
		}
		status = &st
	}

	updated, err := s.assets.Update(r.Context(), id, req.RowVersion, name, req.Attributes, status)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such asset")
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
