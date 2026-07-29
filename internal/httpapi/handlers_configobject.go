package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

func etag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }

func parseETag(s string) (int64, error) {
	return strconv.ParseInt(strings.Trim(strings.TrimSpace(s), `"`), 10, 64)
}

func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		if status, body, found, err := s.idem.Lookup(r.Context(), key); err == nil && found {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
	}

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	obj := req.toDomain()
	if err := obj.ValidateInception(); err != nil {
		s.mapError(w, r, err)
		return
	}
	if err := obj.Validate(); err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.repo.Create(r.Context(), obj)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	body, _ := json.Marshal(fromDomain(created))
	if key != "" {
		if err := s.idem.Save(r.Context(), key, r.Method, r.URL.Path, http.StatusCreated, body); err != nil {
			s.log.Warn("idempotency save failed", "err", err.Error())
		}
	}
	w.Header().Set("ETag", etag(created.RowVersion))
	w.Header().Set("Location", "/v1/artifacts/"+created.CfgID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	obj, err := s.repo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	tag := etag(obj.RowVersion)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, http.StatusOK, fromDomain(obj))
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	params, err := s.parseListParams(r)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	page, err := s.repo.List(r.Context(), params)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	items := make([]configObjectResponse, 0, len(page.Items))
	for _, o := range page.Items {
		items = append(items, fromDomain(o))
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, NextCursor: page.NextCursor})
}

func (s *Server) patchArtifact(w http.ResponseWriter, r *http.Request) {
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeProblem(w, r, http.StatusPreconditionRequired, "precondition required",
			"If-Match header with the current row version is required")
		return
	}
	expected, err := parseETag(ifMatch)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid If-Match value")
		return
	}
	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	patch, verr := req.toPatch()
	if verr != nil {
		s.mapError(w, r, verr)
		return
	}
	// §9.3 enforcement on the patch path (CP-1.1 completed the invariants; this
	// is the caller that was missing). The merge is validated BEFORE it is
	// persisted. Patch can no longer touch a constitutional dimension, so the
	// dimensions are carried through unchanged and the invariants hold by
	// construction; what this catches is a field-level violation such as
	// clearing retention_policy.
	id := chi.URLParam(r, "id")
	current, err := s.repo.Get(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	merged := *current
	if patch.RatifiedBy != nil {
		merged.RatifiedBy = *patch.RatifiedBy
	}
	if patch.ReviewCycle != nil {
		merged.ReviewCycle = *patch.ReviewCycle
	}
	if patch.RetentionPolicy != nil {
		merged.RetentionPolicy = *patch.RetentionPolicy
	}
	if err := merged.Validate(); err != nil {
		s.mapError(w, r, err)
		return
	}

	updated, err := s.repo.Update(r.Context(), id, expected, patch)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(updated.RowVersion))
	writeJSON(w, http.StatusOK, fromDomain(updated))
}

// deleteArtifact destroys a Configuration Object, which is a §8 constitutional
// operation and is therefore executed by the Governance Engine — the same path
// as DELETE /v1/governance/{id} (ADR-GOV-002).
//
// It previously issued a bare `DELETE FROM configuration_object` through the
// repository, enforcing only the protected-role rule and nothing else. Proven
// live: a ratified, current_baseline object that the engine refuses to delete
// (409) was destroyed through this route (204), with the dependents check
// skipped, its dependency edges silently cascaded away, and **no audit event
// written at all**. A governed object could be erased with no record of who did
// it or when.
//
// Destruction now goes through the one door that enforces §8: role protection,
// working-material-only, the dependents check, and the atomic audit append.
func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	s.execGovernance(w, r, domain.OpDeletion)
}

func (s *Server) bulkCreateArtifacts(w http.ResponseWriter, r *http.Request) {
	var req bulkCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	if len(req.Items) == 0 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", "items must not be empty")
		return
	}
	objs := make([]*domain.ConfigObject, 0, len(req.Items))
	for i, it := range req.Items {
		o := it.toDomain()
		if err := o.ValidateInception(); err != nil {
			s.mapError(w, r, err)
			return
		}
		if err := o.Validate(); err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed",
				fmt.Sprintf("items[%d]: %s", i, err.Error()))
			return
		}
		objs = append(objs, o)
	}
	created, err := s.repo.BulkCreate(r.Context(), objs)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	items := make([]configObjectResponse, 0, len(created))
	for _, o := range created {
		items = append(items, fromDomain(o))
	}
	writeJSON(w, http.StatusCreated, listResponse{Items: items})
}
