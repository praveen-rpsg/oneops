package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
	"github.com/rpsg/oneops/internal/observability"
)

type fakeGov struct {
	calls   int
	lastCmd governance.Command
	res     governance.Result
	err     error
}

func (f *fakeGov) Execute(_ context.Context, cmd governance.Command) (governance.Result, error) {
	f.calls++
	f.lastCmd = cmd
	if f.err != nil {
		return governance.Result{}, f.err
	}
	r := f.res
	r.Operation, r.CfgID, r.Actor = cmd.Operation, cmd.CfgID, cmd.Actor
	return r, nil
}

func newGovAPI(t *testing.T, authEnabled bool, gov governanceExecutor) http.Handler {
	t.Helper()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: authEnabled, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetGovernance(gov)
	return s.Router()
}

func idemHdr(key string) map[string]string { return map[string]string{"Idempotency-Key": key} }

func TestGovernance_RatifySuccess(t *testing.T) {
	gov := &fakeGov{res: governance.Result{
		NewLifecycle: domain.LifecycleRatified,
		NewRetention: domain.RetentionCurrentBaseline,
		NewAuthority: domain.AuthorityActive,
		RowVersion:   2,
	}}
	h := newGovAPI(t, false, gov)
	rec := do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, idemHdr("op-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp governanceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if resp.Operation != "ratification" || resp.State == nil || resp.State.Lifecycle != "ratified" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Audit.OperationID != "op-1" || !resp.Audit.Recorded || resp.Audit.ChainID != "c1" {
		t.Fatalf("audit metadata = %+v", resp.Audit)
	}
	if rec.Header().Get("ETag") != `"2"` {
		t.Errorf("ETag = %q, want \"2\"", rec.Header().Get("ETag"))
	}
	// Transport → engine propagation.
	if gov.lastCmd.OperationID != "op-1" || gov.lastCmd.CfgID != "c1" || gov.lastCmd.Actor != "system" {
		t.Fatalf("command = %+v", gov.lastCmd)
	}
	// Correlation id reused (observability).
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID correlation header")
	}
}

func TestGovernance_MissingIdempotencyKey(t *testing.T) {
	h := newGovAPI(t, false, &fakeGov{})
	rec := do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGovernance_OptimisticConcurrencyPropagatedAndMapped(t *testing.T) {
	// If-Match propagates to ExpectedRowVersion.
	gov := &fakeGov{res: governance.Result{RowVersion: 3}}
	h := newGovAPI(t, false, gov)
	do(h, http.MethodPost, "/v1/governance/c1/suspend", nil,
		map[string]string{"Idempotency-Key": "op-2", "If-Match": `"5"`})
	if gov.lastCmd.ExpectedRowVersion != 5 {
		t.Fatalf("ExpectedRowVersion = %d, want 5", gov.lastCmd.ExpectedRowVersion)
	}
	// A version mismatch maps to 412 (existing semantics preserved).
	gov2 := &fakeGov{err: domain.ErrVersionMismatch}
	rec := do(newGovAPI(t, false, gov2), http.MethodPost, "/v1/governance/c1/suspend", nil,
		map[string]string{"Idempotency-Key": "op-2", "If-Match": `"5"`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", rec.Code)
	}
}

func TestGovernance_StatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", domain.ErrNotFound, http.StatusNotFound},
		{"transition", &governance.TransitionError{Operation: domain.OpRatification, From: domain.LifecycleRatified, Reason: "x"}, http.StatusConflict},
		{"dependents", governance.ErrHasDependents, http.StatusConflict},
		{"unsupported", governance.ErrUnsupportedOperation, http.StatusUnprocessableEntity},
		{"validation", domain.NewValidationError("target_retention", "bad"), http.StatusUnprocessableEntity},
		{"internal", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newGovAPI(t, false, &fakeGov{err: c.err})
			rec := do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, idemHdr("op-x"))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
				t.Errorf("content-type = %q, want problem+json", ct)
			}
		})
	}
}

func TestGovernance_Authorization(t *testing.T) {
	gov := &fakeGov{res: governance.Result{RowVersion: 1}}
	h := newGovAPI(t, true, gov)

	// No token -> 401.
	if rec := do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, idemHdr("k")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	// Reader token -> 403 (write required).
	reader := map[string]string{"Idempotency-Key": "k", "Authorization": "Bearer " + mintToken(t, []string{"oneops-reader"})}
	if rec := do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, reader); rec.Code != http.StatusForbidden {
		t.Fatalf("reader: status = %d, want 403", rec.Code)
	}
	// Editor token -> 200 on write op.
	editor := map[string]string{"Idempotency-Key": "k", "Authorization": "Bearer " + mintToken(t, []string{"oneops-editor"})}
	if rec := do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, editor); rec.Code != http.StatusOK {
		t.Fatalf("editor: status = %d, want 200", rec.Code)
	}
	// Editor cannot delete (delete permission required) -> 403.
	if rec := do(h, http.MethodDelete, "/v1/governance/c1", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor delete: status = %d, want 403", rec.Code)
	}
	// Admin can delete -> 200.
	admin := map[string]string{"Idempotency-Key": "k", "Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
	if rec := do(h, http.MethodDelete, "/v1/governance/c1", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin delete: status = %d, want 200", rec.Code)
	}
}

func TestGovernance_ArchiveRequiresTargetRetention(t *testing.T) {
	gov := &fakeGov{res: governance.Result{RowVersion: 1}}
	h := newGovAPI(t, false, gov)

	// Missing target_retention -> 400, engine not called.
	rec := do(h, http.MethodPost, "/v1/governance/c1/archive", nil, idemHdr("op-a"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if gov.calls != 0 {
		t.Fatalf("engine called %d times for invalid archive, want 0", gov.calls)
	}
	// With target_retention -> propagated to the command.
	body := map[string]string{"target_retention": "audit_record"}
	rec = do(h, http.MethodPost, "/v1/governance/c1/archive", body, idemHdr("op-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gov.lastCmd.TargetRetention != domain.RetentionAuditRecord {
		t.Fatalf("TargetRetention = %q, want audit_record", gov.lastCmd.TargetRetention)
	}
}

func TestGovernance_DeleteResponse(t *testing.T) {
	gov := &fakeGov{res: governance.Result{Removed: true}}
	h := newGovAPI(t, false, gov)
	rec := do(h, http.MethodDelete, "/v1/governance/c1", nil, idemHdr("op-d"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp governanceResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Removed || resp.State != nil {
		t.Fatalf("delete response = %+v", resp)
	}
	if gov.lastCmd.Operation != domain.OpDeletion {
		t.Fatalf("operation = %q, want deletion", gov.lastCmd.Operation)
	}
	if rec.Header().Get("ETag") != "" {
		t.Error("deletion must not set an ETag")
	}
}

func TestGovernance_MetricsInstrumented(t *testing.T) {
	h := newGovAPI(t, false, &fakeGov{res: governance.Result{RowVersion: 1}})
	do(h, http.MethodPost, "/v1/governance/c1/ratify", nil, idemHdr("op-m"))

	m := do(h, http.MethodGet, "/metrics", nil, nil)
	if m.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", m.Code)
	}
	if !strings.Contains(m.Body.String(), "/v1/governance/{id}/ratify") {
		t.Fatal("governance route not present in HTTP metrics (instrumentation not reused)")
	}
}

func TestGovernance_OpenAPIDocumented(t *testing.T) {
	h := newGovAPI(t, false, &fakeGov{})
	rec := do(h, http.MethodGet, "/openapi.yaml", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/v1/governance/{id}/ratify", "ratifyGovernance", "deleteGovernance",
		"GovernanceResponse", "GovernanceArchiveRequest",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("openapi.yaml missing %q", want)
		}
	}
}

func TestGovernance_NotWiredReturns500(t *testing.T) {
	// Server without SetGovernance: routes exist but report unavailable.
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	rec := do(s.Router(), http.MethodPost, "/v1/governance/c1/ratify", nil, idemHdr("op"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// M4 WP-2 — the extend endpoint (§8 Extension). Transport concerns only: the
// successor is required and is propagated verbatim to the engine, which owns
// every constitutional decision.
func TestGovernance_ExtendRequiresSuccessor(t *testing.T) {
	gov := &fakeGov{res: governance.Result{RowVersion: 1}}
	h := newGovAPI(t, false, gov)

	// Missing successor_id -> 400, engine not called.
	rec := do(h, http.MethodPost, "/v1/governance/c1/extend", nil, idemHdr("op-e"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if gov.calls != 0 {
		t.Fatalf("engine called %d times for invalid extend, want 0", gov.calls)
	}

	// With successor_id -> propagated to the command as an Extension operation.
	rec = do(h, http.MethodPost, "/v1/governance/c1/extend",
		map[string]string{"successor_id": "c2"}, idemHdr("op-e"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gov.lastCmd.Operation != domain.OpExtension {
		t.Fatalf("Operation = %q, want %q", gov.lastCmd.Operation, domain.OpExtension)
	}
	if gov.lastCmd.SuccessorID != "c2" {
		t.Fatalf("SuccessorID = %q, want c2", gov.lastCmd.SuccessorID)
	}
}

// The response surfaces the successor so a client can confirm what was recorded.
func TestGovernance_ExtendResponseCarriesSuccessor(t *testing.T) {
	gov := &fakeGov{res: governance.Result{RowVersion: 4, SuccessorID: "c2"}}
	h := newGovAPI(t, false, gov)

	rec := do(h, http.MethodPost, "/v1/governance/c1/extend",
		map[string]string{"successor_id": "c2"}, idemHdr("op-e"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp governanceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.SuccessorID != "c2" {
		t.Fatalf("successor_id = %q, want c2", resp.SuccessorID)
	}
}

// M4 WP-1 step 4 — the replace endpoint (§8 Replacement). Transport only: the
// successor is required and the operation reaches the engine unchanged. Every
// constitutional decision, including the four-part Replacement Test, is the
// engine's.
func TestGovernance_ReplaceRequiresSuccessor(t *testing.T) {
	gov := &fakeGov{res: governance.Result{RowVersion: 1}}
	h := newGovAPI(t, false, gov)

	rec := do(h, http.MethodPost, "/v1/governance/c1/replace", nil, idemHdr("op-r"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if gov.calls != 0 {
		t.Fatalf("engine called %d times for invalid replace, want 0", gov.calls)
	}

	rec = do(h, http.MethodPost, "/v1/governance/c1/replace",
		map[string]string{"successor_id": "c2"}, idemHdr("op-r"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gov.lastCmd.Operation != domain.OpReplacement {
		t.Fatalf("Operation = %q, want %q", gov.lastCmd.Operation, domain.OpReplacement)
	}
	if gov.lastCmd.SuccessorID != "c2" {
		t.Fatalf("SuccessorID = %q, want c2", gov.lastCmd.SuccessorID)
	}
}

// A failed Replacement Test is a precondition failure and must surface as 409,
// reusing the existing TransitionError mapping — no new error contract.
func TestGovernance_ReplaceFailedTestMapsTo409(t *testing.T) {
	gov := &fakeGov{err: &governance.TransitionError{
		Operation: domain.OpReplacement,
		From:      domain.LifecycleRatified,
		Reason:    "replacement test failed on superseded_active_dependency: [c9]",
	}}
	h := newGovAPI(t, false, gov)

	rec := do(h, http.MethodPost, "/v1/governance/c1/replace",
		map[string]string{"successor_id": "c2"}, idemHdr("op-r2"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "replacement test failed") {
		t.Errorf("body does not name the failing clause: %s", rec.Body.String())
	}
}
