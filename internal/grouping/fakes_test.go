package grouping

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeIncident is the in-memory shape the fake grouping.Store administers —
// deliberately narrower than domain.Incident, mirroring OpenAlertIncident's
// own doc comment: grouping never sees, and therefore cannot mutate, Status.
type fakeIncident struct {
	incidentID string
	assetID    string
	root       *string
}

// setCall records one SetRootIncidentID invocation, so a test can assert
// exactly how many writes happened (idempotence) and to what.
type setCall struct {
	tenantID   string
	incidentID string
	root       *string
}

// fakeStore is an in-memory grouping.Store. Deleting an incident (via
// resolve) reproduces exactly what the real store's openAlertIncidentPredicate
// does when an incident transitions to resolved/closed — it simply stops
// appearing in OpenAlertIncidents.
type fakeStore struct {
	mu       sync.Mutex
	byTenant map[string]map[string]*fakeIncident
	setCalls []setCall
	openErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{byTenant: map[string]map[string]*fakeIncident{}}
}

func (s *fakeStore) addIncident(tenantID, incidentID, assetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byTenant[tenantID] == nil {
		s.byTenant[tenantID] = map[string]*fakeIncident{}
	}
	s.byTenant[tenantID][incidentID] = &fakeIncident{incidentID: incidentID, assetID: assetID}
}

// resolve removes incidentID from tenantID's open set — the fake's stand-in
// for a status transition to resolved/closed.
func (s *fakeStore) resolve(tenantID, incidentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byTenant[tenantID], incidentID)
}

func (s *fakeStore) root(tenantID, incidentID string) *string {
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.byTenant[tenantID][incidentID]
	if !ok {
		return nil
	}
	return inc.root
}

func (s *fakeStore) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.setCalls)
}

func (s *fakeStore) TenantsWithOpenAlertIncidents(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for tenantID, incs := range s.byTenant {
		if len(incs) > 0 {
			out = append(out, tenantID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *fakeStore) OpenAlertIncidents(_ context.Context, tenantID string) ([]OpenAlertIncident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		return nil, s.openErr
	}
	var out []OpenAlertIncident
	for _, inc := range s.byTenant[tenantID] {
		out = append(out, OpenAlertIncident{IncidentID: inc.incidentID, AssetID: inc.assetID, RootIncidentID: inc.root})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IncidentID < out[j].IncidentID })
	return out, nil
}

func (s *fakeStore) SetRootIncidentID(_ context.Context, tenantID, incidentID string, root *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls = append(s.setCalls, setCall{tenantID: tenantID, incidentID: incidentID, root: root})
	inc, ok := s.byTenant[tenantID][incidentID]
	if !ok {
		return fmt.Errorf("set root_incident_id: no incident %q in tenant %q", incidentID, tenantID)
	}
	inc.root = root
	return nil
}

var _ Store = (*fakeStore)(nil)

// graphCall records one RecursiveDependenciesOfTypes invocation with the
// tenant it actually carried (via domain.TenantFrom(ctx)) — how tests prove
// the Reconciler re-scopes ctx to the tenant it is currently reconciling
// rather than reusing one context across the whole sweep.
type graphCall struct {
	tenantID string
	assetID  string
}

// fakeGraph is an in-memory grouping.Graph, keyed by (tenantID, assetID) so a
// test can give two tenants identical asset_id strings with entirely
// different topologies — the shape needed to prove a leak between them would
// actually show up as a wrong answer, not merely an unexercised code path.
type fakeGraph struct {
	mu    sync.Mutex
	nodes map[string]map[string][]domain.TraversalNode
	calls []graphCall
	err   error
}

func newFakeGraph() *fakeGraph {
	return &fakeGraph{nodes: map[string]map[string][]domain.TraversalNode{}}
}

func (g *fakeGraph) set(tenantID, assetID string, nodes []domain.TraversalNode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.nodes[tenantID] == nil {
		g.nodes[tenantID] = map[string][]domain.TraversalNode{}
	}
	g.nodes[tenantID][assetID] = nodes
}

func (g *fakeGraph) RecursiveDependenciesOfTypes(ctx context.Context, id string, _ []string) ([]domain.TraversalNode, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tenantID := ""
	if tn, ok := domain.TenantFrom(ctx); ok {
		tenantID = tn.TenantID
	}
	g.calls = append(g.calls, graphCall{tenantID: tenantID, assetID: id})
	if g.err != nil {
		return nil, g.err
	}
	return g.nodes[tenantID][id], nil
}

var _ Graph = (*fakeGraph)(nil)
