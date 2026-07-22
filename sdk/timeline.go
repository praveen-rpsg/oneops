package sdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// TimelineEntry is one point on the execution timeline.
type TimelineEntry struct {
	Timestamp   time.Time         `json:"timestamp"`
	Component   string            `json:"component"`
	Action      string            `json:"action"`
	Status      string            `json:"status"`
	DurationMS  int64             `json:"duration_ms"`
	Correlation map[string]string `json:"correlation"`
	Metadata    map[string]string `json:"metadata"`
}

// TimelinePage is a filtered, ordered, paginated timeline.
type TimelinePage struct {
	Entries    []TimelineEntry `json:"entries"`
	NextOffset string          `json:"next_offset"`
}

// TimelineFilter narrows and paginates a timeline query. Zero fields are omitted.
type TimelineFilter struct {
	From      time.Time
	To        time.Time
	Component string
	Status    string
	Offset    int
	Limit     int
}

func (f TimelineFilter) query() url.Values {
	q := url.Values{}
	if !f.From.IsZero() {
		q.Set("from", f.From.UTC().Format(time.RFC3339))
	}
	if !f.To.IsZero() {
		q.Set("to", f.To.UTC().Format(time.RFC3339))
	}
	if f.Component != "" {
		q.Set("component", f.Component)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.Offset > 0 {
		q.Set("offset", strconv.Itoa(f.Offset))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	return q
}

// TimelineClient serves the read-only execution timeline (admin permission).
type TimelineClient struct {
	c *Client
}

// Timeline returns the timeline client.
func (c *Client) Timeline() *TimelineClient { return &TimelineClient{c: c} }

func (tc *TimelineClient) get(ctx context.Context, path string, f TimelineFilter) (*TimelinePage, error) {
	if q := f.query(); len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out TimelinePage
	if err := tc.c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EventTimeline composes the timeline correlated by an audit event id.
func (tc *TimelineClient) EventTimeline(ctx context.Context, eventID string, f TimelineFilter) (*TimelinePage, error) {
	return tc.get(ctx, "/v1/admin/timeline/"+url.PathEscape(eventID), f)
}

// GovernanceTimeline composes the timeline for a configuration object.
func (tc *TimelineClient) GovernanceTimeline(ctx context.Context, id string, f TimelineFilter) (*TimelinePage, error) {
	return tc.get(ctx, "/v1/admin/governance/"+url.PathEscape(id)+"/timeline", f)
}

// ReplayTimeline composes the timeline for a replay job.
func (tc *TimelineClient) ReplayTimeline(ctx context.Context, jobID string, f TimelineFilter) (*TimelinePage, error) {
	return tc.get(ctx, "/v1/admin/replay/"+url.PathEscape(jobID)+"/timeline", f)
}

// PolicyTimeline composes the timeline for a policy execution.
func (tc *TimelineClient) PolicyTimeline(ctx context.Context, executionID string, f TimelineFilter) (*TimelinePage, error) {
	return tc.get(ctx, "/v1/admin/policies/"+url.PathEscape(executionID)+"/timeline", f)
}
