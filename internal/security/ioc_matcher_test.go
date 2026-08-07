package security

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func mkIOC(t *testing.T, tenantID string, indicatorType domain.IOCIndicatorType, value string, severity domain.IncidentSeverity) *domain.IOC {
	t.Helper()
	i, err := domain.NewIOC(tenantID, indicatorType, value, severity)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	return i
}

func newTestIOCMatcher(iocs IOCLister, obs ObservationWindowReader, correlator IncidentCorrelator, now time.Time, interval time.Duration) *IOCMatcher {
	m := NewIOCMatcher(iocs, obs, correlator, NopIOCMatcherMetrics{}, quiet(), IOCMatcherConfig{Concurrency: 4, Interval: interval})
	m.now = func() time.Time { return now }
	return m
}

// TestIOCMatcher_MatchCreatesIncidentAtIOCSeverityWithNote is the create half
// of E8.2b's core payoff: an observation whose attribute value equals an
// enabled ioc's indicator_value raises exactly one open security incident,
// at the ioc's OWN severity (not the observation's ObservationSeverity), with
// a note naming the matched indicator and the observation.
func TestIOCMatcher_MatchCreatesIncidentAtIOCSeverityWithNote(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityCritical)
	iocs := newFakeIOCLister(ioc)

	obs := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "outbound_connection", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
		Attributes: map[string]string{"dest_ip": "203.0.113.5"},
	}
	reader := newFakeObservationReader(obs)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)

	m.RunOnce(context.Background())

	creates := correlator.callsOfKind("create")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1", len(creates))
	}
	inc := correlator.get(creates[0].incidentID)
	if inc.Severity != domain.IncidentSeverityCritical {
		t.Errorf("created incident severity = %q, want the ioc's own %q", inc.Severity, domain.IncidentSeverityCritical)
	}
	if inc.Source != domain.IncidentSourceSecurity {
		t.Errorf("created incident source = %q, want security", inc.Source)
	}
	if inc.AssetID == nil || *inc.AssetID != "asset-1" {
		t.Errorf("created incident asset_id = %v, want asset-1", inc.AssetID)
	}
	if inc.Description == "" {
		t.Error("created incident description must name the matched indicator/observation, got empty")
	}
}

// TestIOCMatcher_NonMatchingObservationRaisesNothing proves an observation
// whose attribute values never equal any enabled ioc's indicator_value
// raises no incident at all.
func TestIOCMatcher_NonMatchingObservationRaisesNothing(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh)
	iocs := newFakeIOCLister(ioc)

	obs := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "outbound_connection", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
		Attributes: map[string]string{"dest_ip": "198.51.100.9"},
	}
	reader := newFakeObservationReader(obs)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)

	m.RunOnce(context.Background())

	if got := len(correlator.calls); got != 0 {
		t.Fatalf("correlator calls for a non-matching observation = %d, want 0", got)
	}
}

// TestIOCMatcher_DisabledIOCDoesNotMatch proves a disabled ioc is never
// matched against, even though its indicator_value is otherwise identical to
// an observation's attribute value.
func TestIOCMatcher_DisabledIOCDoesNotMatch(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh)
	ioc.Enabled = false
	iocs := newFakeIOCLister(ioc)

	obs := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "outbound_connection", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
		Attributes: map[string]string{"dest_ip": "203.0.113.5"},
	}
	reader := newFakeObservationReader(obs)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)

	m.RunOnce(context.Background())

	if got := len(correlator.calls); got != 0 {
		t.Fatalf("correlator calls with only a disabled ioc = %d, want 0", got)
	}
}

// TestIOCMatcher_DomainNormalizationIsCaseInsensitive proves a
// differently-cased domain observation attribute matches a lower-cased
// domain-typed ioc — ADR-SOC-004's normalization applied at match time
// (domain.NormalizeIOCIndicatorValue).
func TestIOCMatcher_DomainNormalizationIsCaseInsensitive(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeDomain, "evil.example.com", domain.IncidentSeverityMedium)
	iocs := newFakeIOCLister(ioc)

	obs := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "dns_query", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
		Attributes: map[string]string{"queried_domain": "EVIL.EXAMPLE.COM"},
	}
	reader := newFakeObservationReader(obs)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)

	m.RunOnce(context.Background())

	if got := len(correlator.callsOfKind("create")); got != 1 {
		t.Fatalf("incident creations for a case-differing domain match = %d, want exactly 1", got)
	}
}

// TestIOCMatcher_IPIsCaseSensitiveExactTrim proves an ip-typed ioc is NOT
// case-normalized (there is no case to normalize for an IP, but this also
// pins that only whitespace is trimmed, not case-folded) — a value that
// differs by more than surrounding whitespace does not match.
func TestIOCMatcher_IPIsCaseSensitiveExactTrim(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh)
	iocs := newFakeIOCLister(ioc)

	obs := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "outbound_connection", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
		Attributes: map[string]string{"dest_ip": " 203.0.113.5 "}, // surrounding whitespace only
	}
	reader := newFakeObservationReader(obs)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)

	m.RunOnce(context.Background())

	if got := len(correlator.callsOfKind("create")); got != 1 {
		t.Fatalf("incident creations for a whitespace-padded ip match = %d, want exactly 1 (trim, not exact byte match)", got)
	}
}

// TestIOCMatcher_SecondMatchOnSameAssetLinksNoDuplicate mirrors
// TestSecurityDetector_SecondRuleSameAssetLinksNoDuplicate: two DIFFERENT
// observations on the SAME asset, each matching a DIFFERENT enabled ioc,
// must link to the SAME incident, never create a second one.
func TestIOCMatcher_SecondMatchOnSameAssetLinksNoDuplicate(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc1 := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh)
	ioc2 := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeDomain, "evil.example.com", domain.IncidentSeverityHigh)
	iocs := newFakeIOCLister(ioc1, ioc2)

	obs1 := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "outbound_connection", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-10 * time.Second),
		Attributes: map[string]string{"dest_ip": "203.0.113.5"},
	}
	obs2 := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "dns_query", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
		Attributes: map[string]string{"queried_domain": "evil.example.com"},
	}
	reader := newFakeObservationReader(obs1, obs2)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)

	m.RunOnce(context.Background())

	creates := correlator.callsOfKind("create")
	links := correlator.callsOfKind("link")
	if len(creates) != 1 {
		t.Fatalf("incident creations = %d, want exactly 1 (no duplicate)", len(creates))
	}
	if len(links) != 1 {
		t.Fatalf("incident links = %d, want exactly 1", len(links))
	}
	if links[0].incidentID != creates[0].incidentID {
		t.Fatalf("linked incident %q != created incident %q — second match created its own instead of linking",
			links[0].incidentID, creates[0].incidentID)
	}
	if len(correlator.incidents) != 1 {
		t.Fatalf("incidents in the correlator = %d, want exactly 1", len(correlator.incidents))
	}
}

// TestIOCMatcher_NoDuplicateAcrossRepeatedPassesWindowTiling is the no-spam
// proof design decision #2 exists for: a single observation, evaluated by
// TWO consecutive RunOnce passes whose windows TILE ((now-Interval, now]
// then (now, now+Interval]) rather than overlap, is correlated exactly once
// — the second pass's window excludes it, so it is never re-noted.
func TestIOCMatcher_NoDuplicateAcrossRepeatedPassesWindowTiling(t *testing.T) {
	interval := time.Minute
	pass1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ioc := mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh)
	iocs := newFakeIOCLister(ioc)

	obs := domain.SecurityObservation{
		TenantID: "tenant-a", AssetID: "asset-1", ObservationType: "outbound_connection", Source: "wazuh",
		Severity: domain.ObservationSeverityLow, ObservedAt: pass1.Add(-10 * time.Second), // inside pass1's (pass1-1m, pass1] window
		Attributes: map[string]string{"dest_ip": "203.0.113.5"},
	}
	reader := newFakeObservationReader(obs)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, pass1, interval)

	m.RunOnce(context.Background())
	if got := len(correlator.callsOfKind("create")); got != 1 {
		t.Fatalf("incident creations after pass 1 = %d, want exactly 1", got)
	}

	// Pass 2's clock advances by exactly Interval: its window is
	// (pass1, pass1+interval], which does NOT include obs (observed BEFORE
	// pass1). Re-running must not re-note it.
	m.now = func() time.Time { return pass1.Add(interval) }
	m.RunOnce(context.Background())

	if got := len(correlator.calls); got != 1 {
		t.Fatalf("correlator calls after a second, non-overlapping pass = %d, want still exactly 1 (no re-note)", got)
	}
}

// TestIOCMatcher_TenantIsolation_SharedIndicatorAndAssetID is the
// make-or-break, mutation-tested proof for ADR-TENANCY-012 at the matcher
// level: two tenants each register the SAME indicator_value AND happen to
// use the SAME asset_id string (an adversarial collision a globally unique
// id would not normally produce). Each tenant's observation must raise ONLY
// its own tenant's incident — an ioc in tenant A must never match tenant B's
// observation, even with an identical indicator value and asset id.
func TestIOCMatcher_TenantIsolation_SharedIndicatorAndAssetID(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	const sharedAsset = "asset-shared"
	const sharedValue = "203.0.113.5"

	var iocList []*domain.IOC
	var obsList []domain.SecurityObservation
	for i := 0; i < 5; i++ {
		tenantID := fmt.Sprintf("tenant-%02d", i)
		iocList = append(iocList, mkIOC(t, tenantID, domain.IOCIndicatorTypeIP, sharedValue, domain.IncidentSeverityHigh))
		obsList = append(obsList, domain.SecurityObservation{
			TenantID: tenantID, AssetID: sharedAsset, ObservationType: "outbound_connection", Source: "wazuh",
			Severity: domain.ObservationSeverityLow, ObservedAt: now.Add(-5 * time.Second),
			Attributes: map[string]string{"dest_ip": sharedValue},
		})
	}

	iocs := newFakeIOCLister(iocList...)
	reader := newFakeObservationReader(obsList...)
	correlator := newFakeCorrelator()
	m := newTestIOCMatcher(iocs, reader, correlator, now, time.Minute)
	m.cfg.Concurrency = 8

	m.RunOnce(context.Background())

	creates := correlator.callsOfKind("create")
	if len(creates) != 5 {
		t.Fatalf("incidents created = %d, want 5 (one per tenant sharing the same indicator value AND asset_id, zero cross-tenant reuse)", len(creates))
	}
	seenTenants := map[string]bool{}
	for _, c := range creates {
		inc := correlator.get(c.incidentID)
		if seenTenants[inc.TenantID] {
			t.Errorf("tenant %s got more than one incident", inc.TenantID)
		}
		seenTenants[inc.TenantID] = true
	}
	if len(seenTenants) != 5 {
		t.Fatalf("distinct tenants with an incident = %d, want 5", len(seenTenants))
	}
}

// TestMatchObservation_TypeAwareNormalizationAndDeterminism unit-tests the
// pure match decision directly (no worker plumbing): it proves the
// normalization is applied PER CANDIDATE INDICATOR TYPE (not once, globally)
// and that the result is deterministic when more than one enabled ioc could
// match.
func TestMatchObservation_TypeAwareNormalizationAndDeterminism(t *testing.T) {
	idx := map[domain.IOCIndicatorType]map[string]*domain.IOC{
		domain.IOCIndicatorTypeIP: {
			"203.0.113.5": mkIOC(t, "tenant-a", domain.IOCIndicatorTypeIP, "203.0.113.5", domain.IncidentSeverityHigh),
		},
		domain.IOCIndicatorTypeDomain: {
			"evil.example.com": mkIOC(t, "tenant-a", domain.IOCIndicatorTypeDomain, "evil.example.com", domain.IncidentSeverityMedium),
		},
	}

	obs := &domain.SecurityObservation{
		Attributes: map[string]string{"queried_domain": "EVIL.EXAMPLE.COM"},
	}
	got := matchObservation(idx, obs)
	if got == nil || got.IndicatorType != domain.IOCIndicatorTypeDomain {
		t.Fatalf("matchObservation() = %v, want the domain-typed ioc (case-insensitive match)", got)
	}

	obsNoMatch := &domain.SecurityObservation{
		Attributes: map[string]string{"queried_domain": "Evil.Example.Com.evil"},
	}
	if got := matchObservation(idx, obsNoMatch); got != nil {
		t.Fatalf("matchObservation() for a non-matching value = %v, want nil", got)
	}

	if got := matchObservation(idx, &domain.SecurityObservation{}); got != nil {
		t.Fatalf("matchObservation() with no attributes = %v, want nil", got)
	}
	if got := matchObservation(map[domain.IOCIndicatorType]map[string]*domain.IOC{}, obs); got != nil {
		t.Fatalf("matchObservation() with an empty index = %v, want nil", got)
	}
}
