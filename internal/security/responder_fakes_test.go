package security

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// fakeResponseRuleLister is an in-memory security.ResponseRuleLister,
// mirroring fakeIOCLister's shape.
type fakeResponseRuleLister struct {
	mu    sync.Mutex
	rules map[string]*domain.SecurityResponseRule // rule_id -> rule
}

func newFakeResponseRuleLister(rules ...*domain.SecurityResponseRule) *fakeResponseRuleLister {
	f := &fakeResponseRuleLister{rules: map[string]*domain.SecurityResponseRule{}}
	for _, r := range rules {
		cp := *r
		f.rules[r.RuleID] = &cp
	}
	return f
}

func (f *fakeResponseRuleLister) TenantsWithEnabledSecurityResponseRules(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range f.rules {
		if r.Enabled {
			seen[r.TenantID] = true
		}
	}
	var out []string
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeResponseRuleLister) EnabledSecurityResponseRulesForTenant(
	_ context.Context, tenantID string, limit int, after string,
) ([]*domain.SecurityResponseRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, r := range f.rules {
		if r.TenantID == tenantID && r.Enabled && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]*domain.SecurityResponseRule, 0, len(ids))
	for _, id := range ids {
		cp := *f.rules[id]
		out = append(out, &cp)
	}
	return out, nil
}

// fakeSecurityIncidentReader is an in-memory security.IncidentWindowReader,
// mirroring fakeObservationReader's exclusive-lower/inclusive-upper window
// and keyset-over-incident_id contract.
type fakeSecurityIncidentReader struct {
	mu        sync.Mutex
	incidents []domain.Incident
}

func newFakeSecurityIncidentReader(incidents ...domain.Incident) *fakeSecurityIncidentReader {
	return &fakeSecurityIncidentReader{incidents: append([]domain.Incident{}, incidents...)}
}

func (f *fakeSecurityIncidentReader) RecentSecurityIncidentsForTenant(
	_ context.Context, tenantID string, from, to time.Time, limit int, after *domain.Incident,
) ([]domain.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	afterID := ""
	if after != nil {
		afterID = after.IncidentID
	}

	var matched []domain.Incident
	for _, inc := range f.incidents {
		if inc.TenantID != tenantID || inc.Source != domain.IncidentSourceSecurity {
			continue
		}
		if !inc.CreatedAt.After(from) || inc.CreatedAt.After(to) {
			continue // (from, to] exclusive-lower/inclusive-upper
		}
		if inc.IncidentID <= afterID {
			continue
		}
		matched = append(matched, inc)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].IncidentID < matched[j].IncidentID })
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// fakeDispatchClaimer is an in-memory security.DispatchClaimer, replicating
// the real store's UNIQUE (tenant_id, incident_id, rule_id) claim-once
// semantics.
type fakeDispatchClaimer struct {
	mu      sync.Mutex
	claimed map[string]bool
	calls   int
}

func newFakeDispatchClaimer() *fakeDispatchClaimer {
	return &fakeDispatchClaimer{claimed: map[string]bool{}}
}

func dispatchKey(tenantID, incidentID, ruleID string) string {
	return tenantID + "|" + incidentID + "|" + ruleID
}

func (f *fakeDispatchClaimer) ClaimDispatch(_ context.Context, tenantID, incidentID, ruleID, _ string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	key := dispatchKey(tenantID, incidentID, ruleID)
	if f.claimed[key] {
		return false, nil
	}
	f.claimed[key] = true
	return true, nil
}

func (f *fakeDispatchClaimer) isClaimed(tenantID, incidentID, ruleID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimed[dispatchKey(tenantID, incidentID, ruleID)]
}

// actionRun records one ActionRunner.Run invocation.
type actionRun struct {
	actionType string
	ev         policy.Event
	config     json.RawMessage
}

// fakeActionRunner is an in-memory security.ActionRunner, the recording
// double the story's own success criteria requires ("uses a fake/recording
// action in tests to assert the action ran with the right input").
type fakeActionRunner struct {
	mu       sync.Mutex
	runs     []actionRun
	failNext error
}

func newFakeActionRunner() *fakeActionRunner { return &fakeActionRunner{} }

func (f *fakeActionRunner) Run(_ context.Context, actionType string, ev policy.Event, config json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.runs = append(f.runs, actionRun{actionType: actionType, ev: ev, config: config})
	return nil
}

func (f *fakeActionRunner) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}
