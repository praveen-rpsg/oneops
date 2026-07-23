package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// getDependencies serves GET /v1/configurations/{cfgId}/dependencies.
func (s *Server) getDependencies(w http.ResponseWriter, r *http.Request) {
	s.serveTraversal(w, r, domain.DirectionDependencies)
}

// getDependents serves GET /v1/configurations/{cfgId}/dependents.
func (s *Server) getDependents(w http.ResponseWriter, r *http.Request) {
	s.serveTraversal(w, r, domain.DirectionDependents)
}

func (s *Server) serveTraversal(w http.ResponseWriter, r *http.Request, dir domain.Direction) {
	start := time.Now()
	if s.graph == nil || s.graphRepo == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "graph service unavailable")
		return
	}
	cfgID := chi.URLParam(r, "cfgId")
	// cfgId must reference an existing configuration object.
	if _, err := s.repo.Get(r.Context(), cfgID); err != nil {
		mapError(w, r, err)
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

	nodes, err := s.traverse(r, dir, cfgID, recursive, maxDepth)
	if err != nil {
		mapError(w, r, err)
		return
	}

	s.log.Info("graph_traversal",
		"cfg_id", cfgID,
		"direction", string(dir),
		"recursive", recursive,
		"node_count", len(nodes),
		"duration_ms", time.Since(start).Milliseconds(),
		"request_id", RequestIDFrom(r.Context()),
	)
	writeJSON(w, http.StatusOK, newTraversalResponse(cfgID, dir, recursive, nodes))
}

func (s *Server) traverse(r *http.Request, dir domain.Direction, cfgID string, recursive bool, maxDepth int) ([]domain.TraversalNode, error) {
	if recursive {
		var (
			res *domain.TraversalResult
			err error
		)
		if dir == domain.DirectionDependents {
			res, err = s.graph.WalkDependents(r.Context(), cfgID)
		} else {
			res, err = s.graph.WalkDependencies(r.Context(), cfgID)
		}
		if err != nil {
			return nil, err
		}
		nodes := res.Nodes
		if maxDepth > 0 {
			nodes = filterByDepth(nodes, maxDepth)
		}
		return nodes, nil
	}

	// Non-recursive: direct (one-hop) neighbours, at depth 1.
	var (
		ids []string
		err error
	)
	if dir == domain.DirectionDependents {
		ids, err = s.graphRepo.Dependents(r.Context(), cfgID)
	} else {
		ids, err = s.graphRepo.Dependencies(r.Context(), cfgID)
	}
	if err != nil {
		return nil, err
	}
	nodes := make([]domain.TraversalNode, len(ids))
	for i, id := range ids {
		nodes[i] = domain.TraversalNode{CfgID: id, Depth: 1}
	}
	return nodes, nil
}

// getCycles serves GET /v1/configurations/{cfgId}/cycles.
func (s *Server) getCycles(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if s.graph == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "graph service unavailable")
		return
	}
	cfgID := chi.URLParam(r, "cfgId")
	if _, err := s.repo.Get(r.Context(), cfgID); err != nil {
		mapError(w, r, err)
		return
	}
	cycles, err := s.graph.DetectCycles(r.Context(), cfgID)
	if err != nil {
		mapError(w, r, err)
		return
	}
	s.log.Info("graph_cycles",
		"cfg_id", cfgID,
		"cycle_count", len(cycles),
		"duration_ms", time.Since(start).Milliseconds(),
		"request_id", RequestIDFrom(r.Context()),
	)
	writeJSON(w, http.StatusOK, newCyclesResponse(cfgID, cycles))
}

func parseBoolParam(v string) (bool, error) {
	if v == "" {
		return false, nil
	}
	return strconv.ParseBool(v)
}

func parseDepthParam(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, errors.New("max_depth must be positive")
	}
	return n, nil
}

func filterByDepth(nodes []domain.TraversalNode, maxDepth int) []domain.TraversalNode {
	out := make([]domain.TraversalNode, 0, len(nodes))
	for _, n := range nodes {
		if n.Depth <= maxDepth {
			out = append(out, n)
		}
	}
	return out
}
