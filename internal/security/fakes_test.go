package security

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeStore is an in-memory security.Store, mirroring alerting's fakeStore
// shape: EnabledRules pages the fixture set, RecordTransition performs the
// same fenced, single-row CAS the real store's UPDATE ... WHERE rule_id=$1
// AND row_version=$2 does.
type fakeStore struct {
	mu    sync.Mutex
	rules map[string]*domain.SecurityRule
}

func newFakeStore(rules ...*domain.SecurityRule) *fakeStore {
	s := &fakeStore{rules: map[string]*domain.SecurityRule{}}
	for _, r := range rules {
		cp := *r
		s.rules[r.RuleID] = &cp
	}
	return s
}

func (f *fakeStore) EnabledRules(_ context.Context, limit int, after string) ([]*domain.SecurityRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, r := range f.rules {
		if r.Enabled && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]*domain.SecurityRule, 0, len(ids))
	for _, id := range ids {
		cp := *f.rules[id]
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeStore) RecordTransition(
	_ context.Context, ruleID string, rowVersion int64, state domain.SecurityRuleState, at time.Time, currentIncidentID *string,
) (*domain.SecurityRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[ruleID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if r.RowVersion != rowVersion {
		return nil, domain.ErrVersionMismatch
	}
	r.LastState = state
	r.LastTransitionAt = at
	r.CurrentIncidentID = currentIncidentID
	r.RowVersion++
	cp := *r
	return &cp, nil
}

func (f *fakeStore) get(ruleID string) *domain.SecurityRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.rules[ruleID]
	return &cp
}

// counterCall records one CountForTenant invocation, for the cross-tenant
// isolation test to assert against — the security-detector equivalent of
// alerting's telemetryCall.
type counterCall struct {
	tenantID, assetID, observationType string
	minSeverity                        domain.ObservationSeverity
	from, to                           time.Time
}

// fakeCounter is an in-memory security.ObservationCounter keyed by
// tenant+asset+observationType, so two different tenants' rules pointed at
// the SAME asset_id/observation_type string (an adversarial/collision
// scenario a globally unique asset id would not normally produce) can be
// configured with DIFFERENT counts and a test can prove neither ever
// receives the other's data — mirrors alerting's fakeTelemetry exactly.
type fakeCounter struct {
	mu     sync.Mutex
	counts map[string]int
	calls  []counterCall
}

func newFakeCounter() *fakeCounter {
	return &fakeCounter{counts: map[string]int{}}
}

func counterKey(tenantID, assetID, observationType string) string {
	return tenantID + "|" + assetID + "|" + observationType
}

func (f *fakeCounter) set(tenantID, assetID, observationType string, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[counterKey(tenantID, assetID, observationType)] = count
}

func (f *fakeCounter) CountForTenant(
	_ context.Context, tenantID, assetID, observationType string, minSeverity domain.ObservationSeverity, from, to time.Time,
) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, counterCall{tenantID, assetID, observationType, minSeverity, from, to})
	return f.counts[counterKey(tenantID, assetID, observationType)], nil
}

func (f *fakeCounter) callsFor(tenantID string) []counterCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []counterCall
	for _, c := range f.calls {
		if c.tenantID == tenantID {
			out = append(out, c)
		}
	}
	return out
}

// correlationCall records one IncidentCorrelator invocation — the security
// equivalent of alerting's correlationCall.
type correlationCall struct {
	kind                 string // "create", "link", "note"
	tenantID, incidentID string
	note, actor          string
}

// fakeCorrelator is an in-memory security.IncidentCorrelator. It replicates
// the invariants the real *postgres.IncidentStore enforces at the database —
// mirrors alerting's fakeCorrelator exactly, scoped to
// domain.IncidentSourceSecurity instead of domain.IncidentSourceAlert:
//
//   - FindOrCreateOpenSecurityIncident only ever matches an existing incident
//     that is (a) the same tenant, (b) the same asset, (c) Source ==
//     IncidentSourceSecurity, and (d) not resolved/closed — exactly
//     ux_incident_open_security_per_asset's predicate — so a manually-filed
//     OR alert-sourced incident on the same asset is never linked.
//   - AppendSecurityNote refuses (domain.ErrNotFound) unless the incident it
//     is asked to touch actually belongs to the tenantID it was given.
type fakeCorrelator struct {
	mu        sync.Mutex
	incidents map[string]*domain.Incident
	calls     []correlationCall
	failNext  error
}

func newFakeCorrelator(seed ...*domain.Incident) *fakeCorrelator {
	f := &fakeCorrelator{incidents: map[string]*domain.Incident{}}
	for _, inc := range seed {
		cp := *inc
		f.incidents[inc.IncidentID] = &cp
	}
	return f
}

func (f *fakeCorrelator) FindOrCreateOpenSecurityIncident(
	_ context.Context, want *domain.Incident, actor, noteOnLink string,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "", err
	}
	for _, inc := range f.incidents {
		if inc.TenantID != want.TenantID || inc.Source != domain.IncidentSourceSecurity {
			continue
		}
		if inc.AssetID == nil || want.AssetID == nil || *inc.AssetID != *want.AssetID {
			continue
		}
		if inc.Status == domain.IncidentResolved || inc.Status == domain.IncidentClosed {
			continue
		}
		f.calls = append(f.calls, correlationCall{kind: "link", tenantID: want.TenantID, incidentID: inc.IncidentID, note: noteOnLink, actor: actor})
		return inc.IncidentID, nil
	}
	cp := *want
	f.incidents[cp.IncidentID] = &cp
	f.calls = append(f.calls, correlationCall{kind: "create", tenantID: cp.TenantID, incidentID: cp.IncidentID, actor: actor})
	return cp.IncidentID, nil
}

func (f *fakeCorrelator) AppendSecurityNote(_ context.Context, tenantID, incidentID, note, actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	inc, ok := f.incidents[incidentID]
	if !ok || inc.TenantID != tenantID {
		return domain.ErrNotFound
	}
	f.calls = append(f.calls, correlationCall{kind: "note", tenantID: tenantID, incidentID: incidentID, note: note, actor: actor})
	return nil
}

func (f *fakeCorrelator) get(incidentID string) *domain.Incident {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.incidents[incidentID]
	return &cp
}

func (f *fakeCorrelator) callsOfKind(kind string) []correlationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []correlationCall
	for _, c := range f.calls {
		if c.kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// mkRule builds a rule with a fixed threshold/window, mirroring alerting's
// mkRule.
func mkRule(t *testing.T, tenantID, assetID, observationType string, windowSeconds, thresholdCount int) *domain.SecurityRule {
	t.Helper()
	r, err := domain.NewSecurityRule(tenantID, assetID, observationType, windowSeconds, thresholdCount, domain.IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
	}
	return r
}
