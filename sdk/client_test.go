package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{BaseURL: srv.URL, Token: "tok", RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClient_Validation(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("expected error for empty BaseURL")
	}
	c, err := NewClient(Config{BaseURL: "https://x/"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.cfg.BaseURL != "https://x" || c.Governance == nil || c.Query == nil || c.Admin == nil {
		t.Fatalf("client not initialized: %+v", c.cfg)
	}
}

func TestRequestGeneration(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotIdem, gotIfMatch, gotReqID, gotUA string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotIdem = r.Header.Get("Idempotency-Key")
		gotIfMatch = r.Header.Get("If-Match")
		gotReqID = r.Header.Get("X-Request-ID")
		gotUA = r.Header.Get("User-Agent")
		writeResult(w)
	})
	c := testClient(t, h)

	_, err := c.Governance.Ratify(context.Background(), "c1", WriteOptions{OperationID: "op-1", ExpectedRowVersion: 3})
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/governance/c1/ratify" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" || gotIdem != "op-1" || gotIfMatch != `"3"` {
		t.Errorf("headers: auth=%q idem=%q ifmatch=%q", gotAuth, gotIdem, gotIfMatch)
	}
	if gotReqID == "" || gotUA != defaultUserAgent {
		t.Errorf("reqID=%q ua=%q", gotReqID, gotUA)
	}
}

func TestOperationIDRequired(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeResult(w) }))
	if _, err := c.Governance.Ratify(context.Background(), "c1", WriteOptions{}); err == nil {
		t.Fatal("expected error for missing OperationID")
	}
}

func TestRetryOnServerErrorThenSuccess(t *testing.T) {
	var calls int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeResult(w)
	})
	c := testClient(t, h)

	// Idempotent (has Idempotency-Key) -> retried through the 503s.
	res, err := c.Governance.Ratify(context.Background(), "c1", WriteOptions{OperationID: "op-1"})
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if res.CfgID != "c1" {
		t.Fatalf("result = %+v", res)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("server called %d times, want 3 (2 retries)", got)
	}
}

func TestNoRetryExhaustionReturnsAPIError(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	c := testClient(t, h)
	_, err := c.Query.Get(context.Background(), "c1")
	if !IsServerError(err) || !IsRetryable(err) {
		t.Fatalf("err = %v, want retryable server error", err)
	}
}

func TestErrorDecoding(t *testing.T) {
	cases := []struct {
		status int
		check  func(error) bool
	}{
		{http.StatusNotFound, IsNotFound},
		{http.StatusConflict, IsConflict},
		{http.StatusPreconditionFailed, IsConflict},
		{http.StatusUnprocessableEntity, IsValidation},
		{http.StatusBadRequest, IsValidation},
		{http.StatusUnauthorized, IsUnauthorized},
		{http.StatusForbidden, IsForbidden},
	}
	for _, tc := range cases {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(tc.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"title": "x", "detail": "d", "instance": "req-1"})
		})
		c := testClient(t, h)
		_, err := c.Query.Get(context.Background(), "c1")
		if !tc.check(err) {
			t.Errorf("status %d: predicate failed for %v", tc.status, err)
		}
		if e, ok := asAPIError(err); !ok || e.RequestID != "req-1" || e.Detail != "d" {
			t.Errorf("status %d: APIError not decoded: %v", tc.status, err)
		}
	}
}

func TestContextCancellation(t *testing.T) {
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	c := testClient(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Query.Get(ctx, "c1"); err == nil {
		t.Fatal("expected a context error")
	}
}

func TestHooksInvoked(t *testing.T) {
	var reqs, resps int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeResult(w) }))
	c.cfg.Hooks = Hooks{
		OnRequest:  func(context.Context, string, string) { atomic.AddInt32(&reqs, 1) },
		OnResponse: func(context.Context, string, string, int, time.Duration) { atomic.AddInt32(&resps, 1) },
	}
	if _, err := c.Query.Get(context.Background(), "c1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reqs != 1 || resps != 1 {
		t.Fatalf("hooks: reqs=%d resps=%d", reqs, resps)
	}
}

func writeResult(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(GovernanceResult{
		Operation: "ratification", CfgID: "c1", Actor: "system", RowVersion: 2,
		State: &GovernanceState{Lifecycle: "ratified"}, Audit: AuditMeta{OperationID: "op-1", ChainID: "c1", Recorded: true},
	})
}
