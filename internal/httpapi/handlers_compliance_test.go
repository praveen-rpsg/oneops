package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/compliance"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

var errNF = domain.ErrNotFound

type fakeCompliance struct {
	ev       compliance.Evidence
	notFound bool
	lastKind string
}

func (f *fakeCompliance) Summary(_ context.Context, id string) (compliance.Summary, error) {
	f.lastKind = "summary"
	if f.notFound {
		return compliance.Summary{}, errNF
	}
	return compliance.Summary{GovernanceID: id, Compliant: true, ChecksPassed: 6, ChecksTotal: 6}, nil
}
func (f *fakeCompliance) Evidence(_ context.Context, id string) (compliance.Evidence, error) {
	f.lastKind = "evidence"
	if f.notFound {
		return compliance.Evidence{}, errNF
	}
	e := f.ev
	e.GovernanceID = id
	return e, nil
}
func (f *fakeCompliance) Checks(_ context.Context, _ string) ([]compliance.Check, error) {
	f.lastKind = "checks"
	return []compliance.Check{{ID: "audit-chain-verified", Passed: true}}, nil
}
func (f *fakeCompliance) Reports(_ context.Context, _ string, _ int) (compliance.ReportPage, error) {
	f.lastKind = "reports"
	return compliance.ReportPage{Items: []compliance.Summary{{GovernanceID: "c1", Compliant: true}}, NextCursor: "c2"}, nil
}

func newComplianceAPI(t *testing.T, wire bool) (http.Handler, *fakeCompliance) {
	t.Helper()
	fc := &fakeCompliance{ev: compliance.Evidence{Compliant: true, Checks: []compliance.Check{{ID: "x", Passed: true}}}}
	exports := 0
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if wire {
		s.SetCompliance(fc, func() { exports++ })
	}
	return s.Router(), fc
}

func TestComplianceRouting(t *testing.T) {
	h, fc := newComplianceAPI(t, true)

	if rec := do(h, http.MethodGet, "/v1/admin/compliance/c1", nil, nil); rec.Code != http.StatusOK || fc.lastKind != "summary" {
		t.Fatalf("summary: code=%d kind=%q", rec.Code, fc.lastKind)
	}
	if rec := do(h, http.MethodGet, "/v1/admin/compliance/c1/checks", nil, nil); rec.Code != http.StatusOK || fc.lastKind != "checks" {
		t.Fatalf("checks: code=%d kind=%q", rec.Code, fc.lastKind)
	}
	if rec := do(h, http.MethodGet, "/v1/admin/compliance/c1/evidence", nil, nil); rec.Code != http.StatusOK || fc.lastKind != "evidence" {
		t.Fatalf("evidence: code=%d kind=%q", rec.Code, fc.lastKind)
	}
	// Static /reports must not be captured by {governanceID}.
	if rec := do(h, http.MethodGet, "/v1/admin/compliance/reports", nil, nil); rec.Code != http.StatusOK || fc.lastKind != "reports" {
		t.Fatalf("reports: code=%d kind=%q", rec.Code, fc.lastKind)
	}
}

func TestComplianceEvidenceZIP(t *testing.T) {
	h, _ := newComplianceAPI(t, true)
	rec := do(h, http.MethodGet, "/v1/admin/compliance/c1/evidence?format=zip", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q", ct)
	}
	// The body is a valid ZIP containing an evidence JSON entry.
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("zip has %d files, want 1", len(zr.File))
	}
	f, _ := zr.File[0].Open()
	data, _ := io.ReadAll(f)
	var ev compliance.Evidence
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("zip entry not valid evidence JSON: %v", err)
	}
}

func TestComplianceNotFoundAndUnwired(t *testing.T) {
	h, fc := newComplianceAPI(t, true)
	fc.notFound = true
	if rec := do(h, http.MethodGet, "/v1/admin/compliance/nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("not found: status = %d", rec.Code)
	}

	hu, _ := newComplianceAPI(t, false)
	if rec := do(hu, http.MethodGet, "/v1/admin/compliance/c1", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("unwired: status = %d, want 500", rec.Code)
	}
}

func TestComplianceRBAC(t *testing.T) {
	fc := &fakeCompliance{}
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetCompliance(fc, nil)
	h := s.Router()
	editor := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-editor"})}
	admin := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
	if rec := do(h, http.MethodGet, "/v1/admin/compliance/c1", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor: status = %d, want 403", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/admin/compliance/c1", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", rec.Code)
	}
}
