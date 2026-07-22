package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/rpsg/oneops/internal/domain"
)

func (s *Server) parseListParams(r *http.Request) (domain.ListParams, error) {
	q := r.URL.Query()

	limit := s.cfg.DefaultPageSize
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return domain.ListParams{}, fmt.Errorf("limit must be a positive integer")
		}
		limit = n
	}
	if limit > s.cfg.MaxPageSize {
		limit = s.cfg.MaxPageSize
	}

	role := domain.Role(q.Get("role"))
	if role != "" && !role.Valid() {
		return domain.ListParams{}, fmt.Errorf("invalid role filter: %s", role)
	}
	lc := domain.Lifecycle(q.Get("lifecycle"))
	if lc != "" && !lc.Valid() {
		return domain.ListParams{}, fmt.Errorf("invalid lifecycle filter: %s", lc)
	}
	authority := domain.Authority(q.Get("authority"))
	if authority != "" && !authority.Valid() {
		return domain.ListParams{}, fmt.Errorf("invalid authority filter: %s", authority)
	}

	return domain.ListParams{
		Filter: domain.Filter{Role: role, Lifecycle: lc, Authority: authority, Query: q.Get("q")},
		Limit:  limit,
		Cursor: q.Get("cursor"),
	}, nil
}
