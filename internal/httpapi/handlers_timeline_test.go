package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/timeline"
)

type fakeTimeline struct {
	lastFilter timeline.Filter
	lastKind   string
	page       timeline.Page
	notFound   bool
}

func (f *fakeTimeline) rec(kind string, fl timeline.Filter) (timeline.Page, error) {
	f.lastKind, f.lastFilter = kind, fl
	if f.notFound {
		return timeline.Page{}, timeline.ErrNotFound
	}
	return f.page, nil
}
func (f *fakeTimeline) ByEvent(_ context.Context, _ string, fl timeline.Filter) (timeline.Page, error) {
	return f.rec("event", fl)
}
func (f *fakeTimeline) ByGovernance(_ context.Context, _ string, fl timeline.Filter) (timeline.Page, error) {
	return f.rec("governance", fl)
}
func (f *fakeTimeline) ByReplay(_ context.Context, _ string, fl timeline.Filter) (timeline.Page, error) {
	return f.rec("replay", fl)
}
func (f *fakeTimeline) ByPolicyExecution(_ context.Context, _ string, fl timeline.Filter) (timeline.Page, error) {
	return f.rec("policy", fl)
}

func newTimelineAPI(t *testing.T, wire bool) (http.Handler, *fakeTimeline) {
	t.Helper()
	tl := &fakeTimeline{page: timeline.Page{Entries: []timeline.Entry{
		{Component: "governance", Action: "operation_committed", Status: "committed", Correlation: map[string]string{"event_id": "evt_1"}},
	}, NextOffset: "1"}}
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if wire {
		s.SetTimeline(tl)
	}
	return s.Router(), tl
}

func TestTimelineRouting(t *testing.T) {
	h, tl := newTimelineAPI(t, true)
	cases := []struct {
		path string
		kind string
	}{
		{"/v1/admin/timeline/evt_1", "event"},
		{"/v1/admin/governance/c1/timeline", "governance"},
		{"/v1/admin/replay/rpl_1/timeline", "replay"},
		{"/v1/admin/policies/pex_1/timeline", "policy"},
	}
	for _, c := range cases {
		rec := do(h, http.MethodGet, c.path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", c.path, rec.Code)
		}
		if tl.lastKind != c.kind {
			t.Fatalf("%s routed to %q, want %q", c.path, tl.lastKind, c.kind)
		}
		var page timeline.Page
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		if len(page.Entries) != 1 || page.NextOffset != "1" {
			t.Fatalf("%s body = %+v", c.path, page)
		}
	}
}

func TestTimelineFilterParsing(t *testing.T) {
	h, tl := newTimelineAPI(t, true)
	from := "2026-07-26T00:00:00Z"
	do(h, http.MethodGet, "/v1/admin/timeline/evt_1?component=webhook&status=delivered&from="+from+"&offset=5&limit=10", nil, nil)
	f := tl.lastFilter
	if f.Component != "webhook" || f.Status != "delivered" || f.Offset != 5 || f.Limit != 10 {
		t.Fatalf("filter = %+v", f)
	}
	if want, _ := time.Parse(time.RFC3339, from); !f.From.Equal(want) {
		t.Fatalf("from = %v, want %v", f.From, want)
	}
}

func TestTimelineNotFound(t *testing.T) {
	h, tl := newTimelineAPI(t, true)
	tl.notFound = true
	if rec := do(h, http.MethodGet, "/v1/admin/replay/nope/timeline", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTimelineRBACAndUnwired(t *testing.T) {
	// Unwired -> 500.
	h, _ := newTimelineAPI(t, false)
	if rec := do(h, http.MethodGet, "/v1/admin/timeline/evt_1", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("unwired: status = %d, want 500", rec.Code)
	}

	// RBAC: admin required.
	tl := &fakeTimeline{}
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetTimeline(tl)
	admin := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
	editor := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-editor"})}
	if rec := do(s.Router(), http.MethodGet, "/v1/admin/timeline/evt_1", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor: status = %d, want 403", rec.Code)
	}
	if rec := do(s.Router(), http.MethodGet, "/v1/admin/timeline/evt_1", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", rec.Code)
	}
}
