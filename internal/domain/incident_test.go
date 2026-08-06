package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewIncident_BuildsAnOpenIncident(t *testing.T) {
	inc, err := NewIncident(" tn-1 ", " db down ", "primary is unreachable", IncidentSeverityCritical, nil, nil)
	if err != nil {
		t.Fatalf("NewIncident: %v", err)
	}
	if inc.TenantID != "tn-1" || inc.Title != "db down" {
		t.Errorf("identifiers/title not trimmed: %+v", inc)
	}
	if inc.Status != IncidentOpen {
		t.Errorf("status = %q, want open", inc.Status)
	}
	if !inc.Open() {
		t.Error("a new incident must be open")
	}
	if inc.IncidentID == "" {
		t.Error("incident_id must be minted")
	}
}

func TestNewIncident_RequiresTenantTitleAndSeverity(t *testing.T) {
	for _, c := range []struct {
		name          string
		tenant, title string
		severity      IncidentSeverity
	}{
		{"no tenant", "", "title", IncidentSeverityHigh},
		{"blank tenant", "   ", "title", IncidentSeverityHigh},
		{"no title", "tn-1", "", IncidentSeverityHigh},
		{"blank title", "tn-1", "   ", IncidentSeverityHigh},
		{"invalid severity", "tn-1", "title", IncidentSeverity("catastrophic")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewIncident(c.tenant, c.title, "", c.severity, nil, nil); err == nil {
				t.Error("an incomplete incident was constructed")
			}
		})
	}
}

func TestNewIncident_RejectsAnOverlongTitle(t *testing.T) {
	if _, err := NewIncident("tn-1", strings.Repeat("x", MaxIncidentTitleLength+1), "", IncidentSeverityLow, nil, nil); err == nil {
		t.Error("a title over the length bound was accepted")
	}
}

func TestNewIncident_RejectsAnOverlongDescription(t *testing.T) {
	if _, err := NewIncident("tn-1", "title", strings.Repeat("x", MaxIncidentDescriptionLength+1), IncidentSeverityLow, nil, nil); err == nil {
		t.Error("a description over the length bound was accepted")
	}
}

func TestIncidentSeverity_Valid(t *testing.T) {
	for _, s := range []IncidentSeverity{IncidentSeverityCritical, IncidentSeverityHigh, IncidentSeverityMedium, IncidentSeverityLow} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if IncidentSeverity("unknown").Valid() {
		t.Error(`"unknown" must not be a valid severity — an incident always states impact`)
	}
}

func TestIncidentStatus_Valid(t *testing.T) {
	for _, s := range []IncidentStatus{
		IncidentOpen, IncidentAcknowledged, IncidentInvestigating,
		IncidentResolved, IncidentClosed, IncidentReopened,
	} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if IncidentStatus("bogus").Valid() {
		t.Error(`"bogus" should not be valid`)
	}
}

// The whole legal lifecycle: every documented forward edge, plus the
// resolved -> reopened -> investigating loop, succeeds.
func TestIncidentStatus_CanTransitionTo_LegalEdges(t *testing.T) {
	cases := []struct{ from, to IncidentStatus }{
		{IncidentOpen, IncidentAcknowledged},
		{IncidentAcknowledged, IncidentInvestigating},
		{IncidentInvestigating, IncidentResolved},
		{IncidentResolved, IncidentClosed},
		{IncidentResolved, IncidentReopened},
		{IncidentReopened, IncidentInvestigating},
	}
	for _, c := range cases {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be legal", c.from, c.to)
		}
	}
}

// THIS MUST BITE: every move not explicitly listed above is refused,
// including skipping states, moving backward, and self-transitions.
func TestIncidentStatus_CanTransitionTo_RejectsIllegalEdges(t *testing.T) {
	cases := []struct{ from, to IncidentStatus }{
		{IncidentOpen, IncidentInvestigating},         // skip acknowledged
		{IncidentOpen, IncidentResolved},              // skip two states
		{IncidentOpen, IncidentClosed},                // skip the whole pipeline
		{IncidentOpen, IncidentOpen},                  // self-transition (no-op)
		{IncidentAcknowledged, IncidentOpen},          // backward
		{IncidentAcknowledged, IncidentResolved},      // skip investigating
		{IncidentInvestigating, IncidentAcknowledged}, // backward
		{IncidentInvestigating, IncidentClosed},       // skip resolved
		{IncidentResolved, IncidentInvestigating},     // must go through reopened
		{IncidentResolved, IncidentOpen},
		{IncidentClosed, IncidentOpen},     // terminal
		{IncidentClosed, IncidentReopened}, // terminal
		{IncidentClosed, IncidentClosed},
		{IncidentReopened, IncidentResolved}, // must re-earn it via investigating
		{IncidentReopened, IncidentClosed},
		{IncidentReopened, IncidentOpen},
	}
	for _, c := range cases {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be illegal", c.from, c.to)
		}
	}
}

func TestNewIncidentTransitionError_UnwrapsToErrInvalidTransition(t *testing.T) {
	err := NewIncidentTransitionError(IncidentOpen, IncidentClosed)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Error("must unwrap to ErrInvalidTransition")
	}
	var target *IncidentTransitionError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed")
	}
	if target.From != IncidentOpen || target.To != IncidentClosed {
		t.Errorf("From/To = %s/%s, want open/closed", target.From, target.To)
	}
}

func TestIncidentEventKind_Valid(t *testing.T) {
	for _, k := range []IncidentEventKind{IncidentEventCreated, IncidentEventStatusTransitioned, IncidentEventAssigned} {
		if !k.Valid() {
			t.Errorf("%q should be valid", k)
		}
	}
	if IncidentEventKind("commented").Valid() {
		t.Error(`"commented" has no write path in E5.1 and must not validate as a storable kind yet`)
	}
}

// AssetID/AssigneeUserID follow the same tri-state-friendly "not blank when
// supplied" rule Asset.OwnerTeamID/OwnerUserID carry.
func TestIncident_Validate_RejectsBlankOptionalReferences(t *testing.T) {
	blank := "   "
	inc, err := NewIncident("tn-1", "title", "", IncidentSeverityLow, nil, nil)
	if err != nil {
		t.Fatalf("NewIncident: %v", err)
	}
	inc.AssetID = &blank
	if err := inc.Validate(); err == nil {
		t.Error("a blank asset_id should be rejected")
	}
	inc.AssetID = nil
	inc.AssigneeUserID = &blank
	if err := inc.Validate(); err == nil {
		t.Error("a blank assignee_user_id should be rejected")
	}
}

// ---------------------------------------------------------------- E9.1 trends

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestIncidentTrendsQuery_Validate_RejectsBadBucket(t *testing.T) {
	q := IncidentTrendsQuery{
		From:   mustParseRFC3339(t, "2026-08-01T00:00:00Z"),
		To:     mustParseRFC3339(t, "2026-08-02T00:00:00Z"),
		Bucket: IncidentTrendBucket("week"),
	}
	if err := q.Validate(); err == nil {
		t.Fatal("expected a validation error for an unsupported bucket")
	}
}

func TestIncidentTrendsQuery_Validate_RejectsToNotAfterFrom(t *testing.T) {
	from := mustParseRFC3339(t, "2026-08-02T00:00:00Z")
	for _, to := range []time.Time{from, from.Add(-time.Hour)} {
		q := IncidentTrendsQuery{From: from, To: to, Bucket: IncidentTrendBucketHour}
		if err := q.Validate(); err == nil {
			t.Errorf("to=%v: expected a validation error when to is not after from", to)
		}
	}
}

func TestIncidentTrendsQuery_Validate_RejectsMissingFields(t *testing.T) {
	valid := IncidentTrendsQuery{
		From: mustParseRFC3339(t, "2026-08-01T00:00:00Z"), To: mustParseRFC3339(t, "2026-08-02T00:00:00Z"),
		Bucket: IncidentTrendBucketHour,
	}
	noFrom := valid
	noFrom.From = time.Time{}
	if err := noFrom.Validate(); err == nil {
		t.Error("expected a validation error for a zero-value from")
	}
	noTo := valid
	noTo.To = time.Time{}
	if err := noTo.Validate(); err == nil {
		t.Error("expected a validation error for a zero-value to")
	}
}

// TestIncidentTrendsQuery_Validate_EnforcesTheBucketCap is the CAP proof: a
// window/bucket combination producing more than MaxIncidentTrendBuckets
// buckets is refused, and the boundary itself (exactly the cap) is accepted.
func TestIncidentTrendsQuery_Validate_EnforcesTheBucketCap(t *testing.T) {
	from := mustParseRFC3339(t, "2026-01-01T00:00:00Z")

	atCap := IncidentTrendsQuery{From: from, To: from.Add(MaxIncidentTrendBuckets * time.Hour), Bucket: IncidentTrendBucketHour}
	if err := atCap.Validate(); err != nil {
		t.Errorf("exactly %d hourly buckets should be accepted: %v", MaxIncidentTrendBuckets, err)
	}

	overCap := IncidentTrendsQuery{From: from, To: from.Add((MaxIncidentTrendBuckets + 1) * time.Hour), Bucket: IncidentTrendBucketHour}
	if err := overCap.Validate(); err == nil {
		t.Errorf("%d hourly buckets should be rejected as over the %d cap", MaxIncidentTrendBuckets+1, MaxIncidentTrendBuckets)
	}
}

// TestIncidentTrendsQuery_BucketCount_MatchesBucketStartsLength proves the
// two are never allowed to drift — the handler's zero-fill sizing and the
// cap enforcement above must agree on the exact same number.
func TestIncidentTrendsQuery_BucketCount_MatchesBucketStartsLength(t *testing.T) {
	q := IncidentTrendsQuery{
		From: mustParseRFC3339(t, "2026-08-01T00:30:00Z"), To: mustParseRFC3339(t, "2026-08-01T05:00:00Z"),
		Bucket: IncidentTrendBucketHour,
	}
	starts := q.BucketStarts()
	if len(starts) != q.BucketCount() {
		t.Fatalf("BucketStarts len = %d, BucketCount = %d, want equal", len(starts), q.BucketCount())
	}
	// From is not itself hour-aligned (00:30) — the first bucket is anchored
	// to From's own truncated boundary (00:00), matching Postgres'
	// date_trunc semantics exactly, not to the exact instant supplied.
	if want := mustParseRFC3339(t, "2026-08-01T00:00:00Z"); !starts[0].Equal(want) {
		t.Errorf("first bucket = %v, want %v (From truncated to the hour)", starts[0], want)
	}
	// To (05:00) is itself hour-aligned, so the window [00:00, 05:00) is
	// exactly 5 contiguous hourly buckets: 00,01,02,03,04.
	if len(starts) != 5 {
		t.Fatalf("buckets = %d, want 5: %v", len(starts), starts)
	}
	for i, want := range []string{
		"2026-08-01T00:00:00Z", "2026-08-01T01:00:00Z", "2026-08-01T02:00:00Z",
		"2026-08-01T03:00:00Z", "2026-08-01T04:00:00Z",
	} {
		if !starts[i].Equal(mustParseRFC3339(t, want)) {
			t.Errorf("bucket[%d] = %v, want %v", i, starts[i], want)
		}
	}
}

// TestIncidentTrendsQuery_BucketStarts_DayGranularity proves day buckets
// align to UTC midnight, exactly what
// `date_trunc('day', ts AT TIME ZONE 'UTC')` produces on the store side.
func TestIncidentTrendsQuery_BucketStarts_DayGranularity(t *testing.T) {
	q := IncidentTrendsQuery{
		From: mustParseRFC3339(t, "2026-08-01T13:45:00Z"), To: mustParseRFC3339(t, "2026-08-04T00:00:00Z"),
		Bucket: IncidentTrendBucketDay,
	}
	starts := q.BucketStarts()
	want := []string{"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z", "2026-08-03T00:00:00Z"}
	if len(starts) != len(want) {
		t.Fatalf("buckets = %d, want %d: %v", len(starts), len(want), starts)
	}
	for i, w := range want {
		if !starts[i].Equal(mustParseRFC3339(t, w)) {
			t.Errorf("bucket[%d] = %v, want %v", i, starts[i], w)
		}
	}
}

// TestIncidentTrendsQuery_BucketCount_ZeroForInvalidQuery proves the
// zero-value/invalid-query case degrades to an empty shape rather than a
// negative or panicking count.
func TestIncidentTrendsQuery_BucketCount_ZeroForInvalidQuery(t *testing.T) {
	var zero IncidentTrendsQuery
	if n := zero.BucketCount(); n != 0 {
		t.Errorf("zero-value query BucketCount = %d, want 0", n)
	}
	if starts := zero.BucketStarts(); starts != nil {
		t.Errorf("zero-value query BucketStarts = %v, want nil", starts)
	}
}
