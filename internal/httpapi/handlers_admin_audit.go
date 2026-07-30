package httpapi

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// SetAdminAudit wires the constitutional administrative-audit read boundary.
func (s *Server) SetAdminAudit(r domain.AdminAuditReader) { s.adminAudit = r }

type adminAuditDTO struct {
	ChainID         string    `json:"chain_id"`
	Seq             int64     `json:"seq"`
	EventID         string    `json:"event_id"`
	Operation       string    `json:"operation"`
	Actor           string    `json:"actor"`
	SubjectOrgID    string    `json:"subject_org_id,omitempty"`
	SubjectTenantID string    `json:"subject_tenant_id,omitempty"`
	SubjectUserID   string    `json:"subject_user_id,omitempty"`
	Payload         string    `json:"payload_canonical"`
	ThisHash        string    `json:"this_hash"`
	OccurredAt      time.Time `json:"occurred_at"`
}

func toAdminAuditDTO(r *domain.AdminAuditRecord) adminAuditDTO {
	return adminAuditDTO{
		ChainID: r.ChainID, Seq: r.Seq, EventID: r.EventID,
		Operation: string(r.Operation), Actor: r.Actor,
		SubjectOrgID: r.SubjectOrgID, SubjectTenantID: r.SubjectTenantID,
		SubjectUserID: r.SubjectUserID,
		// The bytes the chain hash was computed over, so a caller can verify the
		// record rather than trust this rendering of it.
		Payload:    base64.StdEncoding.EncodeToString(r.Payload),
		ThisHash:   base64.StdEncoding.EncodeToString(r.ThisHash),
		OccurredAt: r.OccurredAt,
	}
}

// listAdminAudit answers §6.1's question — who did what, to whom, when.
//
// It is the only read path to administrative history. Every route reaching it
// is registered with requirePlatformAdmin, and the store behind it is built
// from the privileged pool precisely so the request-path database role holds no
// read access of its own (ADR-AUDIT-007 §6.5).
func (s *Server) listAdminAudit(w http.ResponseWriter, r *http.Request) {
	if s.adminAudit == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented",
			"administrative audit query is not configured")
		return
	}

	q := r.URL.Query()
	f := domain.AdminAuditFilter{
		Actor:           q.Get("actor"),
		Operation:       domain.AdminOperation(q.Get("operation")),
		SubjectOrgID:    q.Get("subject_org_id"),
		SubjectTenantID: q.Get("subject_tenant_id"),
		SubjectUserID:   q.Get("subject_user_id"),
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit",
				"limit must be an integer")
			return
		}
		f.Limit = n
	}
	for _, b := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(b.name); v != "" {
			ts, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeProblem(w, r, http.StatusUnprocessableEntity, "invalid "+b.name,
					b.name+" must be an RFC 3339 timestamp")
				return
			}
			*b.dst = ts
		}
	}
	cursor, err := domain.DecodeAdminAuditCursor(q.Get("after"))
	if err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid cursor",
			"after must be a cursor returned by this endpoint")
		return
	}
	f.After = cursor

	// Validate at the transport edge, not only in the store. The store
	// validates defensively too, but a caller must be refused by the surface it
	// called rather than by whichever implementation happens to be wired
	// behind it — otherwise the refusal is a property of the store, not of the
	// API contract.
	if err := f.Validate(); err != nil {
		s.mapError(w, r, err)
		return
	}

	records, next, err := s.adminAudit.QueryAdminAudit(r.Context(), f)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	items := make([]adminAuditDTO, 0, len(records))
	for _, rec := range records {
		items = append(items, toAdminAuditDTO(rec))
	}
	body := map[string]any{"items": items}
	if next != nil {
		body["next_cursor"] = next.Encode()
	}
	writeJSON(w, http.StatusOK, body)
}
