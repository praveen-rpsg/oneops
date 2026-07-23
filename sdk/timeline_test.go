package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTimelineClient(t *testing.T) {
	var lastPath, lastQuery string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TimelinePage{
			Entries:    []TimelineEntry{{Component: "governance", Action: "operation_committed", Status: "committed"}},
			NextOffset: "1",
		})
	}))
	tc := c.Timeline()
	ctx := context.Background()

	if p, err := tc.EventTimeline(ctx, "evt_1", TimelineFilter{}); err != nil || len(p.Entries) != 1 || p.NextOffset != "1" {
		t.Fatalf("EventTimeline: %+v %v", p, err)
	}
	if lastPath != "/v1/admin/timeline/evt_1" {
		t.Fatalf("path = %q", lastPath)
	}
	if _, err := tc.GovernanceTimeline(ctx, "c1", TimelineFilter{Component: "webhook", Limit: 10}); err != nil {
		t.Fatalf("GovernanceTimeline: %v", err)
	}
	if lastPath != "/v1/admin/governance/c1/timeline" || !strings.Contains(lastQuery, "component=webhook") {
		t.Fatalf("gov path=%q query=%q", lastPath, lastQuery)
	}
	if _, err := tc.ReplayTimeline(ctx, "rpl_1", TimelineFilter{}); err != nil || lastPath != "/v1/admin/replay/rpl_1/timeline" {
		t.Fatalf("ReplayTimeline: path=%q err=%v", lastPath, err)
	}
	if _, err := tc.PolicyTimeline(ctx, "pex_1", TimelineFilter{}); err != nil || lastPath != "/v1/admin/policies/pex_1/timeline" {
		t.Fatalf("PolicyTimeline: path=%q err=%v", lastPath, err)
	}
}
