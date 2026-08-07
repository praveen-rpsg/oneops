package security

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeIOCLister is an in-memory security.IOCLister, mirroring fakeStore's
// shape: TenantsWithEnabledIOCs/EnabledIOCsForTenant replicate the real
// store's enabled=true filter and per-tenant/keyset-paginated contract.
type fakeIOCLister struct {
	mu   sync.Mutex
	iocs map[string]*domain.IOC // ioc_id -> ioc
}

func newFakeIOCLister(iocs ...*domain.IOC) *fakeIOCLister {
	f := &fakeIOCLister{iocs: map[string]*domain.IOC{}}
	for _, i := range iocs {
		cp := *i
		f.iocs[i.IOCID] = &cp
	}
	return f
}

func (f *fakeIOCLister) TenantsWithEnabledIOCs(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for _, i := range f.iocs {
		if i.Enabled {
			seen[i.TenantID] = true
		}
	}
	var out []string
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeIOCLister) EnabledIOCsForTenant(_ context.Context, tenantID string, limit int, after string) ([]*domain.IOC, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, i := range f.iocs {
		if i.TenantID == tenantID && i.Enabled && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]*domain.IOC, 0, len(ids))
	for _, id := range ids {
		cp := *f.iocs[id]
		out = append(out, &cp)
	}
	return out, nil
}

// observationKey mirrors security_observation's own natural key, for the
// fake's ordering/pagination.
type observationKey struct {
	observedAt                       time.Time
	assetID, observationType, source string
}

func keyOf(o *domain.SecurityObservation) observationKey {
	return observationKey{o.ObservedAt, o.AssetID, o.ObservationType, o.Source}
}

func (k observationKey) less(other observationKey) bool {
	if !k.observedAt.Equal(other.observedAt) {
		return k.observedAt.Before(other.observedAt)
	}
	if k.assetID != other.assetID {
		return k.assetID < other.assetID
	}
	if k.observationType != other.observationType {
		return k.observationType < other.observationType
	}
	return k.source < other.source
}

// fakeObservationReader is an in-memory security.ObservationWindowReader,
// mirroring *postgres.SecurityObservationCounterStore.RecentForTenant's exact
// contract: tenant-explicit, (from, to] exclusive-lower/inclusive-upper,
// ordered and keyset-paginated over the natural key.
type fakeObservationReader struct {
	mu           sync.Mutex
	observations []domain.SecurityObservation
}

func newFakeObservationReader(obs ...domain.SecurityObservation) *fakeObservationReader {
	return &fakeObservationReader{observations: append([]domain.SecurityObservation{}, obs...)}
}

func (f *fakeObservationReader) RecentForTenant(
	_ context.Context, tenantID string, from, to time.Time, limit int, after *domain.SecurityObservation,
) ([]domain.SecurityObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var afterKey *observationKey
	if after != nil {
		k := keyOf(after)
		afterKey = &k
	}

	var matched []domain.SecurityObservation
	for _, o := range f.observations {
		if o.TenantID != tenantID {
			continue
		}
		if !o.ObservedAt.After(from) || o.ObservedAt.After(to) {
			continue // (from, to] exclusive-lower/inclusive-upper
		}
		if afterKey != nil && !afterKey.less(keyOf(&o)) {
			continue
		}
		matched = append(matched, o)
	}
	sort.Slice(matched, func(i, j int) bool { return keyOf(&matched[i]).less(keyOf(&matched[j])) })
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}
