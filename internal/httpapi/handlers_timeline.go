package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/timeline"
)

// timelineService is the read-only timeline read model. *timeline.Service
// satisfies it. It composes existing persisted data only.
type timelineService interface {
	ByEvent(ctx context.Context, eventID string, f timeline.Filter) (timeline.Page, error)
	ByGovernance(ctx context.Context, cfgID string, f timeline.Filter) (timeline.Page, error)
	ByReplay(ctx context.Context, jobID string, f timeline.Filter) (timeline.Page, error)
	ByPolicyExecution(ctx context.Context, execID string, f timeline.Filter) (timeline.Page, error)
}

// SetTimeline wires the read-only execution-timeline endpoints.
func (s *Server) SetTimeline(svc timelineService) { s.timeline = svc }

func (s *Server) timelineReady(w http.ResponseWriter, r *http.Request) bool {
	if s.timeline == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "timeline unavailable")
		return false
	}
	return true
}

// parseTimelineFilter reads the common time-range/component/status/pagination
// query parameters.
func (s *Server) parseTimelineFilter(r *http.Request) timeline.Filter {
	q := r.URL.Query()
	f := timeline.Filter{
		Component: q.Get("component"),
		Status:    q.Get("status"),
		Limit:     s.pageLimit(r),
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.Offset = n
		}
	}
	return f
}

func (s *Server) writeTimeline(w http.ResponseWriter, r *http.Request, page timeline.Page, err error) {
	if err != nil {
		if errors.Is(err, timeline.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "not found", "no such timeline root")
			return
		}
		mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getEventTimeline(w http.ResponseWriter, r *http.Request) {
	if !s.timelineReady(w, r) {
		return
	}
	page, err := s.timeline.ByEvent(r.Context(), chi.URLParam(r, "eventID"), s.parseTimelineFilter(r))
	s.writeTimeline(w, r, page, err)
}

func (s *Server) getGovernanceTimeline(w http.ResponseWriter, r *http.Request) {
	if !s.timelineReady(w, r) {
		return
	}
	page, err := s.timeline.ByGovernance(r.Context(), chi.URLParam(r, "id"), s.parseTimelineFilter(r))
	s.writeTimeline(w, r, page, err)
}

func (s *Server) getReplayTimeline(w http.ResponseWriter, r *http.Request) {
	if !s.timelineReady(w, r) {
		return
	}
	page, err := s.timeline.ByReplay(r.Context(), chi.URLParam(r, "jobID"), s.parseTimelineFilter(r))
	s.writeTimeline(w, r, page, err)
}

func (s *Server) getPolicyTimeline(w http.ResponseWriter, r *http.Request) {
	if !s.timelineReady(w, r) {
		return
	}
	page, err := s.timeline.ByPolicyExecution(r.Context(), chi.URLParam(r, "id"), s.parseTimelineFilter(r))
	s.writeTimeline(w, r, page, err)
}
