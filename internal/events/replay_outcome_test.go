package events

import (
	"context"
	"testing"
	"time"
)

// ctxAwareJobs is a job store that behaves like the real one: a write on a
// cancelled context fails, because the database call fails.
type ctxAwareJobs struct {
	fakeJobs
	lastWrite *ReplayJob
}

func (c *ctxAwareJobs) UpdateJob(ctx context.Context, j ReplayJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cp := j
	c.lastWrite = &cp
	return c.fakeJobs.UpdateJob(ctx, j)
}

// Trust Register audit: entry 21 records "outcome lost when the worker is
// stopped" as an eliminated class — ADR-CONCURRENCY-006 established that an
// outcome the platform has already produced in the outside world is not the
// worker's to forget, and must be written on a context detached from the
// worker's cancellation.
//
// Its enforcement names two workers: the dispatcher and the policy executor. The
// replay worker is a third, and it writes its outcome with the worker context:
//
//	if err := w.jobs.UpdateJob(ctx, job); errors.Is(err, ErrStaleClaim) { … }
//
// A demotion or shutdown mid-replay therefore loses the outcome. Worse, the
// error is only compared against ErrStaleClaim, so a context.Canceled falls
// through to the success metrics below it — the same "the metric claims an
// outcome the database does not hold" defect entry 21 records as closed.
//
// This asserts the property entry 21 claims platform-wide: an outcome already
// produced is recorded even while the worker is being stopped.
func TestReplayOutcome_SurvivesWorkerCancellation(t *testing.T) {
	jobs := &ctxAwareJobs{fakeJobs: fakeJobs{m: map[string]ReplayJob{}}}
	del := newFakeDeliveries()
	w := NewReplayWorker(jobs, &fakeSource{}, newFakeWebhooks(), del, del, nil, quiet(), ReplayConfig{})

	job := ReplayJob{
		ID: "rj_cancel", WebhookID: "wh_1", Status: JobRunning,
		ClaimedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(),
	}
	if err := jobs.CreateJob(context.Background(), job); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	// The instance is demoted while the replay is in flight.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w.execute(ctx, job)

	got, _, err := jobs.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	t.Logf("after execute on a cancelled context: status=%s events_replayed=%d",
		got.Status, got.EventsReplayed)

	if got.Status == JobRunning {
		t.Errorf("LOST OUTCOME: the replay finished but its outcome was not recorded — the job is "+
			"still %s. Combined with the absence of lease recovery on this queue "+
			"(ADR-CONCURRENCY-007), it is stuck in that state permanently. Entry 21 records this "+
			"class as eliminated, but the replay worker writes its outcome on the worker's "+
			"cancellable context", got.Status)
	}
}
