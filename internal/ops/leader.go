// Package ops — leadership for singleton background work.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// WorkerLeaderKey is the advisory-lock key that designates the one instance
// permitted to run background workers. It is an arbitrary constant, unique to
// this purpose within the database.
const WorkerLeaderKey int64 = 0x0117E0_1EADE7 // "oneops leader"

// leaderRetryInterval is how often a standby campaigns for the lock, and a
// demoted leader waits before re-campaigning.
const leaderRetryInterval = 15 * time.Second

// leaderWatchInterval is how often the leader pings its lock connection. It
// bounds how long a demoted leader can keep running its workers after silently
// losing the lock, before it notices and stops them. It is intentionally short:
// correctness during that window rests on idempotent production and the atomic
// claim (ADR-CONCURRENCY-002/003), but the window should still be small.
const leaderWatchInterval = 5 * time.Second

// LeaderLock holds a PostgreSQL session-level advisory lock on a dedicated
// connection. The lock is the single-writer guarantee the background workers
// were designed around but never had: the relay's cursor is a read-modify-write
// and ClaimDue is not an atomic claim, so two instances running the workers
// double-deliver every webhook and double-execute every policy action. Verified
// against two replicas on one database — one governance event produced two
// signed deliveries.
//
// The lock lives for as long as its connection does. If the leader crashes, its
// connection drops, PostgreSQL releases the lock automatically, and a standby
// acquires it — failover with no external coordinator, fitting the single-
// database architecture.
type LeaderLock struct {
	conn *pgx.Conn
	key  int64
}

// TryAcquireLeadership opens a dedicated connection and attempts the advisory
// lock without blocking. It returns (lock, true) when this instance becomes the
// leader, (nil, false) when another instance already holds it, and an error only
// on connection failure.
func TryAcquireLeadership(ctx context.Context, dsn string, key int64) (*LeaderLock, bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, false, fmt.Errorf("leader: connect: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		_ = conn.Close(ctx)
		return nil, false, fmt.Errorf("leader: try lock: %w", err)
	}
	if !acquired {
		_ = conn.Close(ctx)
		return nil, false, nil
	}
	return &LeaderLock{conn: conn, key: key}, true, nil
}

// Healthy reports whether the leader connection is still alive and thus whether
// the advisory lock is still held. A failed ping means leadership may have been
// lost (network partition, connection reset, backend termination), and the
// caller must stop acting as leader — continuing would risk two active leaders.
func (l *LeaderLock) Healthy(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	return l.conn.Ping(ctx) == nil
}

// Close releases the lock by closing its connection.
func (l *LeaderLock) Close(ctx context.Context) {
	if l != nil && l.conn != nil {
		_ = l.conn.Close(ctx)
	}
}

// RunAsLeader gates the background workers behind leadership and keeps an
// instance campaigning for the lock for the life of ctx.
//
// start is called with a *leadership context* each time this instance becomes
// leader. That context is a child of ctx and is cancelled the moment leadership
// is lost — so the workers started under it stop when this instance is demoted,
// not merely when the process exits. start must launch its workers under the
// context it is given (typically `go run(lctx)`), and must not block. It may be
// called more than once over a process's life: a demoted leader that later
// re-acquires the lock starts a fresh set of workers under a fresh leadership
// context. The previously started workers have, by then, already stopped because
// their context was cancelled.
//
// This is the fix for a demoted leader that used to keep running its workers
// (it only logged on lock loss). A leader whose lock connection dies — crash,
// partition, or its backend terminated out from under it — now cancels its
// workers and re-enters the election, so it never keeps producing and
// dispatching after another instance has been promoted. The overlap between
// losing the lock and noticing it (bounded by leaderWatchInterval) is made safe,
// not merely short, by idempotent production and the atomic claim
// (ADR-CONCURRENCY-002/003): during it neither instance can mint a duplicate row
// or double-claim a due row.
func RunAsLeader(ctx context.Context, dsn string, key int64, log *slog.Logger, start func(context.Context)) {
	go campaign(ctx, dsn, key, log, start)
}

// campaign runs the leadership state machine until ctx is done: it tries to
// acquire the lock; as leader it starts the workers and holds the lock until the
// connection dies, then cancels the workers and loops back to campaign again; as
// standby it waits and retries.
func campaign(ctx context.Context, dsn string, key int64, log *slog.Logger, start func(context.Context)) {
	for ctx.Err() == nil {
		lock, leader, err := TryAcquireLeadership(ctx, dsn, key)
		if err != nil {
			log.Warn("leader: acquisition failed; retrying", "err", err)
			if !waitOrDone(ctx, leaderRetryInterval) {
				return
			}
			continue
		}
		if !leader {
			log.Info("standby: another instance holds leadership; not running background workers")
			if !waitOrDone(ctx, leaderRetryInterval) {
				return
			}
			continue
		}

		// This instance is the leader. Start the workers under a leadership
		// context we cancel the instant we lose the lock.
		log.Info("leader: this instance runs the background workers")
		lctx, cancel := context.WithCancel(ctx)
		start(lctx)
		holdLeadership(ctx, lock, log)
		cancel() // stop the workers: demotion, not just process exit
		lock.Close(context.Background())
		if ctx.Err() != nil {
			return
		}
		log.Warn("leader: leadership lost — workers stopped; re-entering election")
	}
}

// holdLeadership blocks while this instance is leader, pinging the lock
// connection on an interval. It returns when the connection is no longer healthy
// (the lock is gone) or ctx is done. It does not close the lock; the caller does.
func holdLeadership(ctx context.Context, lock *LeaderLock, log *slog.Logger) {
	t := time.NewTicker(leaderWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			ok := lock.Healthy(pingCtx)
			cancel()
			if !ok {
				log.Error("leader: lock connection lost — this instance is no longer the leader")
				return
			}
		}
	}
}

// waitOrDone sleeps for d or until ctx is done. It returns true if it slept the
// full duration (caller should continue), false if ctx was cancelled (caller
// should stop).
func waitOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
