package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type errDoer struct{ err error }

func (d errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

func TestRunHTTPCheck_SuccessRecordsUpLatencyAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	result := runHTTPCheck(context.Background(), http.DefaultClient, srv.URL)
	if !result.Up {
		t.Fatal("Up = false, want true for a server that responded")
	}
	if !result.HasStatusCode || result.StatusCode != http.StatusTeapot {
		t.Errorf("StatusCode = %d (has=%v), want %d", result.StatusCode, result.HasStatusCode, http.StatusTeapot)
	}
	if result.LatencyMs < 0 {
		t.Errorf("LatencyMs = %v, want >= 0", result.LatencyMs)
	}
}

func TestRunHTTPCheck_TransportErrorRecordsDownWithNoStatusCode(t *testing.T) {
	result := runHTTPCheck(context.Background(), errDoer{err: errors.New("connection refused")}, "http://example.test/")
	if result.Up {
		t.Fatal("Up = true, want false for a transport error")
	}
	if result.HasStatusCode {
		t.Errorf("HasStatusCode = true, want false — no response was ever received")
	}
}

func TestRunHTTPCheck_ContextDeadlineRecordsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	result := runHTTPCheck(ctx, http.DefaultClient, srv.URL)
	elapsed := time.Since(start)

	if result.Up {
		t.Fatal("Up = true, want false for a check that exceeded its deadline")
	}
	if result.HasStatusCode {
		t.Errorf("HasStatusCode = true, want false")
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("runHTTPCheck took %v, want it to return promptly once the context deadline fires (a hanging target must not stall the caller)", elapsed)
	}
}

func TestRunHTTPCheck_MalformedTargetRecordsDown(t *testing.T) {
	result := runHTTPCheck(context.Background(), http.DefaultClient, "://not-a-url")
	if result.Up {
		t.Fatal("Up = true, want false for a malformed target")
	}
}
