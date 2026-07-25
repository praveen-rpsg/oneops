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
// lost (network partition, connection reset), and the caller must stop acting as
// leader — continuing would risk two active leaders.
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

// RunAsLeader gates start behind leadership. If this instance acquires the
// leader lock it calls start immediately (in the caller's goroutine model —
// start must not block) and holds the lock for the process lifetime. Otherwise
// it becomes a standby, retrying on interval, and calls start once — if it is
// ever promoted after the current leader dies. start is invoked at most once.
//
// A standby that is promoted has, by construction, waited for the previous
// leader's connection to drop, so the two never run the workers at once. On the
// leader side, if the lock connection stops being healthy the process logs and
// exits its leadership so the orchestrator restarts it and a clean election
// happens — the platform never assumes it is still leader once its lock
// connection has failed.
func RunAsLeader(ctx context.Context, dsn string, key int64, log *slog.Logger, start func()) {
	if lock, leader, err := TryAcquireLeadership(ctx, dsn, key); err != nil {
		log.Error("leader: acquisition failed; not starting workers", "err", err)
		return
	} else if leader {
		log.Info("leader: this instance runs the background workers")
		start()
		go watchLeadership(ctx, lock, log)
		return
	}

	log.Info("standby: another instance holds leadership; not running background workers")
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				lock, leader, err := TryAcquireLeadership(ctx, dsn, key)
				if err != nil {
					log.Warn("standby: leadership retry failed", "err", err)
					continue
				}
				if leader {
					log.Info("standby promoted to leader; starting background workers")
					start()
					go watchLeadership(ctx, lock, log)
					return
				}
			}
		}
	}()
}

// watchLeadership holds the leader lock and watches its connection. If the
// connection dies the lock is gone; the process must not keep behaving as
// leader, so it releases and stops the watch. A crashed process drops the lock
// automatically; this covers the case where the connection fails but the process
// survives.
func watchLeadership(ctx context.Context, lock *LeaderLock, log *slog.Logger) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	defer lock.Close(context.Background())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			ok := lock.Healthy(pingCtx)
			cancel()
			if !ok {
				log.Error("leader: lock connection lost — this instance is no longer the leader; " +
					"workers must be restarted under a fresh election")
				return
			}
		}
	}
}
