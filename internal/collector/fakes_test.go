package collector

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is an in-memory Store. Its DueChecks mirrors the real store's
// due-scan predicate closely enough to exercise the scheduler: enabled,
// never-run-or-interval-elapsed, and RecordRun writes LastRunAt/LastStatus
// without touching RowVersion — the same "background write does not consume
// the optimistic-lock version" invariant CollectorCheckStore.RecordRun
// documents.
type fakeStore struct {
	mu       sync.Mutex
	m        map[string]*domain.CollectorCheck
	dueErr   error
	recorded []recordedRun
}

type recordedRun struct {
	CheckID string
	RunAt   time.Time
	Status  domain.CollectorCheckStatus
}

func newFakeStore(seed ...*domain.CollectorCheck) *fakeStore {
	f := &fakeStore{m: map[string]*domain.CollectorCheck{}}
	for _, c := range seed {
		cp := *c
		f.m[c.CheckID] = &cp
	}
	return f
}

func (f *fakeStore) DueChecks(_ context.Context, now time.Time, limit int) ([]*domain.CollectorCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	var out []*domain.CollectorCheck
	for _, c := range f.m {
		if !c.Enabled {
			continue
		}
		due := c.LastRunAt == nil || !c.LastRunAt.Add(time.Duration(c.IntervalSeconds)*time.Second).After(now)
		if due {
			cp := *c
			out = append(out, &cp)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) RecordRun(_ context.Context, checkID string, runAt time.Time, status domain.CollectorCheckStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, recordedRun{CheckID: checkID, RunAt: runAt, Status: status})
	if c, ok := f.m[checkID]; ok {
		t := runAt
		c.LastRunAt = &t
		c.LastStatus = status
	}
	return nil
}

func (f *fakeStore) runsFor(id string) []recordedRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRun
	for _, r := range f.recorded {
		if r.CheckID == id {
			out = append(out, r)
		}
	}
	return out
}

// fakeTelemetry is an in-memory domain.TelemetryRepository, recording every
// written sample for the scheduler tests to assert against.
type fakeTelemetry struct {
	mu      sync.Mutex
	written []domain.Sample
	err     error
}

func (f *fakeTelemetry) WriteSamples(_ context.Context, samples []domain.Sample) ([]domain.SampleWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.SampleWriteResult, len(samples))
	for i, s := range samples {
		f.written = append(f.written, s)
		out[i] = domain.SampleWriteResult{Accepted: true}
	}
	return out, nil
}

func (f *fakeTelemetry) QueryRange(context.Context, string, string, time.Time, time.Time, int, time.Time, domain.TelemetryResolution) ([]domain.Sample, error) {
	return nil, nil
}

func (f *fakeTelemetry) samplesFor(assetID string) []domain.Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Sample
	for _, s := range f.written {
		if s.AssetID == assetID {
			out = append(out, s)
		}
	}
	return out
}

func newHTTPCheck(assetID, target string) *domain.CollectorCheck {
	c, err := domain.NewCollectorCheck("t-test", assetID, domain.CollectorCheckTypeHTTP, target, 30, "chk")
	if err != nil {
		panic(err)
	}
	return c
}
