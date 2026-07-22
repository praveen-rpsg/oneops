package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

type fakeAuditRead struct {
	headSeq    int64
	headHash   []byte
	headFound  bool
	events     []domain.AuditEvent
	err        error
	lastCursor int64
	lastDesc   bool
	lastLimit  int
	lastOp     string
}

func (f *fakeAuditRead) HeadOf(context.Context, string) (int64, []byte, bool, error) {
	return f.headSeq, f.headHash, f.headFound, nil
}

func (f *fakeAuditRead) ListEvents(_ context.Context, _ string, cursor int64, desc bool, limit int, op string) ([]domain.AuditEvent, error) {
	f.lastCursor, f.lastDesc, f.lastLimit, f.lastOp = cursor, desc, limit, op
	if f.err != nil {
		return nil, f.err
	}
	evs := f.events
	if len(evs) > limit {
		evs = evs[:limit]
	}
	return evs, nil
}

type fakeChainVerifier struct {
	res domain.VerifyResult
	err error
}

func (f fakeChainVerifier) VerifyChain(_ context.Context, chainID string) (domain.VerifyResult, error) {
	if f.err != nil {
		return domain.VerifyResult{}, f.err
	}
	r := f.res
	r.ChainID = chainID
	return r, nil
}

func (f fakeChainVerifier) VerifyRange(_ context.Context, chainID string, _, _ int64) (domain.VerifyResult, error) {
	return domain.VerifyResult{ChainID: chainID}, nil
}

func seedObject(repo *fakeRepo, id string) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.items[id] = &domain.ConfigObject{
		CfgID: id, Artifact: "a.md", Version: "1.0.0", Role: domain.RoleReference,
		Lifecycle: domain.LifecycleRatified, RetentionClass: domain.RetentionCurrentBaseline,
		Authority: domain.AuthorityActive, RetentionPolicy: "permanent", RowVersion: 4,
		RatifiedBy: "user:alice",
	}
}

func evt(seq int64, op domain.ConfigurationOperation, payload string) domain.AuditEvent {
	return domain.AuditEvent{
		ChainID: "c1", Seq: seq, EventID: "evt_" + string(op), OperationID: "op-" + string(op),
		Operation: op, Actor: "user:alice", PayloadCanonical: []byte(payload),
		PrevHash: make([]byte, 32), ThisHash: make([]byte, 32), OccurredAt: time.Now().UTC(),
	}
}

func newReadAPI(t *testing.T, authEnabled bool, wireQuery bool) (http.Handler, *fakeRepo, *fakeAuditRead, *fakeChainVerifier, *SchedulerView) {
	t.Helper()
	repo := newFakeRepo()
	read := &fakeAuditRead{}
	ver := &fakeChainVerifier{}
	sched := &SchedulerView{Enabled: true, HasRun: true, LastHealthy: true}
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: authEnabled, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo, newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if wireQuery {
		s.SetGovernanceQuery(read, ver, func() SchedulerView { return *sched })
	}
	return s.Router(), repo, read, ver, sched
}

func TestReadState(t *testing.T) {
	h, repo, _, _, _ := newReadAPI(t, false, true)
	seedObject(repo, "c1")

	rec := do(h, http.MethodGet, "/v1/governance/c1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp governanceStateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.CfgID != "c1" || resp.Lifecycle != "ratified" || resp.Authority != "active" ||
		resp.RowVersion != 4 || resp.RatifiedBy != "user:alice" {
		t.Fatalf("state = %+v", resp)
	}
	if rec.Header().Get("ETag") != `"4"` {
		t.Errorf("ETag = %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing correlation id (observability reuse)")
	}
	// Unknown id -> 404.
	if r2 := do(h, http.MethodGet, "/v1/governance/nope", nil, nil); r2.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404", r2.Code)
	}
}

func TestReadHistoryPaginationAndState(t *testing.T) {
	h, repo, read, _, _ := newReadAPI(t, false, true)
	seedObject(repo, "c1")
	read.events = []domain.AuditEvent{
		evt(1, domain.OpRatification, `{"removed":false,"new_lifecycle":"ratified","new_retention":"current_baseline","new_authority":"active","row_version":2}`),
		evt(2, domain.OpDeprecation, `{"removed":false,"new_lifecycle":"deprecated","new_retention":"current_baseline","new_authority":"active","row_version":3}`),
	}

	// limit=1 -> one item, next_cursor = seq of last returned.
	rec := do(h, http.MethodGet, "/v1/governance/c1/history?limit=1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp historyResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 1 || resp.NextCursor != "1" {
		t.Fatalf("history page = %+v", resp)
	}
	if resp.Items[0].Operation != "ratification" || resp.Items[0].ResultingState == nil ||
		resp.Items[0].ResultingState.Lifecycle != "ratified" {
		t.Fatalf("history item = %+v", resp.Items[0])
	}
	if read.lastLimit != 1 || read.lastDesc {
		t.Fatalf("history is chronological asc: lastLimit=%d desc=%v", read.lastLimit, read.lastDesc)
	}
}

func TestReadHistoryFilter(t *testing.T) {
	h, repo, read, _, _ := newReadAPI(t, false, true)
	seedObject(repo, "c1")

	do(h, http.MethodGet, "/v1/governance/c1/history?operation=deprecation", nil, nil)
	if read.lastOp != "deprecation" {
		t.Fatalf("operation filter = %q, want deprecation", read.lastOp)
	}
	// Unknown operation -> 400.
	if rec := do(h, http.MethodGet, "/v1/governance/c1/history?operation=bogus", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad operation: status = %d, want 400", rec.Code)
	}
}

func TestReadAuditChain(t *testing.T) {
	h, repo, read, ver, _ := newReadAPI(t, false, true)
	seedObject(repo, "c1")
	read.headSeq, read.headHash, read.headFound = 5, make([]byte, 32), true
	ver.res = domain.VerifyResult{OK: true, Checked: 5}

	rec := do(h, http.MethodGet, "/v1/governance/c1/audit", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp auditChainResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ChainID != "c1" || !resp.Verified || resp.HeadSeq != 5 || len(resp.HeadHash) != 64 {
		t.Fatalf("audit chain = %+v", resp)
	}
}

func TestReadAuditEventsOrdering(t *testing.T) {
	h, repo, read, _, _ := newReadAPI(t, false, true)
	seedObject(repo, "c1")
	read.events = []domain.AuditEvent{evt(2, domain.OpRatification, "{}"), evt(1, domain.OpApproval, "{}")}

	rec := do(h, http.MethodGet, "/v1/governance/c1/audit/events?order=desc", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp auditEventsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Order != "desc" || len(resp.Items) != 2 || len(resp.Items[0].ThisHash) != 64 {
		t.Fatalf("events = %+v", resp)
	}
	if !read.lastDesc || read.lastCursor != math.MaxInt64 {
		t.Fatalf("desc paging cursor = %d desc=%v", read.lastCursor, read.lastDesc)
	}
}

func TestReadVerification(t *testing.T) {
	h, repo, _, ver, sched := newReadAPI(t, false, true)
	seedObject(repo, "c1")
	ver.res = domain.VerifyResult{OK: true, Checked: 3}
	sched.Failures = 0

	rec := do(h, http.MethodGet, "/v1/governance/c1/verification", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp verificationResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.IntegrityOK || resp.Scheduler == nil || !resp.Scheduler.Enabled || !resp.Scheduler.LastHealthy {
		t.Fatalf("verification = %+v scheduler=%+v", resp, resp.Scheduler)
	}
}

func TestReadAuthorization(t *testing.T) {
	h, repo, _, _, _ := newReadAPI(t, true, true)
	seedObject(repo, "c1")
	// No token -> 401.
	if rec := do(h, http.MethodGet, "/v1/governance/c1", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	// Reader token -> 200 (read permission suffices).
	reader := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-reader"})}
	if rec := do(h, http.MethodGet, "/v1/governance/c1", nil, reader); rec.Code != http.StatusOK {
		t.Fatalf("reader: status = %d, want 200", rec.Code)
	}
}

func TestReadMetricsAndOpenAPI(t *testing.T) {
	h, repo, _, _, _ := newReadAPI(t, false, true)
	seedObject(repo, "c1")
	do(h, http.MethodGet, "/v1/governance/c1/history", nil, nil)

	m := do(h, http.MethodGet, "/metrics", nil, nil)
	if !strings.Contains(m.Body.String(), "/v1/governance/{id}/history") {
		t.Fatal("history route not present in HTTP metrics (instrumentation not reused)")
	}
	spec := do(h, http.MethodGet, "/openapi.yaml", nil, nil).Body.String()
	for _, want := range []string{
		"getGovernanceState", "getGovernanceHistory", "getAuditChain",
		"getAuditEvents", "getVerification", "AuditChainResponse", "VerificationResponse",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("openapi.yaml missing %q", want)
		}
	}
}

func TestReadUnwiredReturns500(t *testing.T) {
	h, repo, _, _, _ := newReadAPI(t, false, false) // query deps not wired
	seedObject(repo, "c1")
	if rec := do(h, http.MethodGet, "/v1/governance/c1/history", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
