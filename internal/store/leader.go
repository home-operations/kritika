package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// leaderKey is the advisory lock every leader-eligible replica competes for.
const leaderKey = "kritika-leader"

// lockTimeout bounds one lock attempt and each ping of the held connection.
// A lock attempt acquires the connection and tries the lock. Both are a round
// trip on a server that is up. A server that is gone answers neither. The
// bound turns that into a step-down or a retry instead of a wait on TCP
// retransmissions.
const lockTimeout = 5 * time.Second

// LeaderStepDown is the longest a leader whose lock the server dropped goes
// on leading, its duties running, before RunAsLeader notices: until its next
// ping of the lock, every interval, fails within lockTimeout.
func LeaderStepDown(interval time.Duration) time.Duration { return interval + lockTimeout }

// RunAsLeader competes for the leader lock and, once held, calls lead with a
// context that is canceled if the lock is lost or ctx ends. It returns when
// ctx ends. Only one replica per database holds the lock at a time. The
// holder is the only replica that migrates and writes configuration.
//
// An error from lead ends that tenure, not the process. The lock is released,
// and this replica and the others compete for it again after retry. A
// failover, a lock timeout or a deadlock under a migration or a configuration
// apply clears on a later try. Ending the process for it will cut the reviews
// that this replica is running.
//
// The lock is session-level on a dedicated connection held for the whole
// tenure, so it releases on its own if the process dies. The holder pings
// that connection every interval, because a dropped connection releases the
// lock server-side without the client noticing. The ping and each lock
// attempt are bounded by lockTimeout. A server that died without closing its
// sockets answers nothing. An unbounded ping will wait on the kernel's TCP
// retransmissions, about fifteen minutes. During that wait, lead runs the
// whole time while another replica can hold the lock on the new primary.
//
// A database that cannot be reached when the lock is tried, as during a
// failover, is tried again after retry rather than ending the process. A
// standby that restarts on every failover is no standby.
func (s *Store) RunAsLeader(ctx context.Context, retry time.Duration, lead func(ctx context.Context) error) error {
	if s.owner == nil {
		return errors.New("store: leadership needs the owner DSN")
	}
	for {
		held, err := s.tryLead(ctx, retry, lead)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			s.logger.Error("leader duties failed, lock released and retried", "error", err, "after", retry)
		}
		if !held {
			s.logger.Debug("leader lock held elsewhere, retrying", "after", retry)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retry):
		}
	}
}

// tryLead makes one attempt. It reports whether the lock was held during
// this attempt. A false return means that someone else has it, or that the
// database did not answer, which is logged. The error is lead's own. The lock
// connection is only ever touched from this goroutine: lead runs on its own
// goroutine and never sees the connection.
func (s *Store) tryLead(ctx context.Context, interval time.Duration, lead func(ctx context.Context) error) (bool, error) {
	// The bound covers the attempt alone: the connection it yields is held
	// for the tenure under ctx.
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, lockTimeout)
	defer cancelAttempt()
	conn, err := s.owner.Acquire(attemptCtx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("leader connection not acquired, retrying", "error", err, "after", interval)
		}
		return false, nil
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(attemptCtx, `SELECT pg_try_advisory_lock(hashtext($1))`, leaderKey).Scan(&got); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("leader lock not tried, retrying", "error", err, "after", interval)
		}
		return false, nil
	}
	cancelAttempt()
	if !got {
		return false, nil
	}
	s.logger.Info("leader lock acquired")

	leadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lead(leadCtx) }()

	t := time.NewTicker(interval)
	defer t.Stop()
	var leadErr error
loop:
	for {
		select {
		case leadErr = <-done:
			break loop
		case <-ctx.Done():
			cancel()
			leadErr = <-done
			break loop
		case <-t.C:
			if err := s.pingLock(ctx, conn); err != nil {
				s.logger.Warn("leader lock connection lost, stepping down", "error", err)
				cancel()
				leadErr = <-done
				break loop
			}
		}
	}

	// Best effort: the session ends with the connection anyway.
	unlockCtx, unlockCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer unlockCancel()
	_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1))`, leaderKey)
	s.logger.Info("leader lock released")
	return true, leadErr
}

// pingLock pings the held lock connection within lockTimeout. A ping that
// runs out closes the connection. So the release that follows a step-down
// discards it rather than handing a dead socket back to the pool.
func (s *Store) pingLock(ctx context.Context, conn *pgxpool.Conn) error {
	pingCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	return conn.Ping(pingCtx)
}
