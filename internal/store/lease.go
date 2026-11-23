package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
)

// Lease timing. A holder renews every heartbeat. A lease older than expiry
// belongs to a process that died and is free to take.
const (
	leaseHeartbeat = 30 * time.Second
	leaseExpiry    = 2 * time.Minute
)

// How long AcquireLease waits between tries for a slot, per try.
const (
	leasePollMin = 2 * time.Second
	leasePollMax = 30 * time.Second
)

// Backoff is base doubled n times, capped at limit, then jittered into its
// upper half, so waiters that started together do not try again together.
func Backoff(n int, base, limit time.Duration) time.Duration {
	d := base
	for i := 0; i < n && d < limit; i++ {
		d *= 2
	}
	d = min(d, limit)
	return d/2 + rand.N(d/2+1)
}

// sharedScope is the scope of the slots that every account shares: those of
// a model of a provider of the instance.
const sharedScope = ""

// leaseScope is the scope of slots for the account accountID: the shared
// one, or the account's own.
func leaseScope(accountID string, slots configfile.Slots) string {
	if slots.Shared {
		return sharedScope
	}
	return accountID
}

// Lease is one held slot of model_leases: one of the calls that can run at
// once on one model key, renewed until released.
type Lease struct {
	st        *Store
	accountID string
	modelKey  string
	slot      int
	slots     configfile.Slots
	jobID     int64
	cancel    context.CancelFunc
	done      chan struct{}
}

// AcquireLease blocks until a slot of the model is free for the account or
// ctx ends. slots are the model's: its limit, and whether every account
// shares them. Rows are created on demand, so a raised limit takes effect
// on the next review and a lowered one leaves the extra rows unused.
func (s *Store) AcquireLease(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (*Lease, error) {
	for try := 0; ; try++ {
		l, err := s.TakeLease(ctx, accountID, modelKey, slots, jobID)
		if err != nil || l != nil {
			return l, err
		}
		if err := leaseWait(ctx, modelKey, Backoff(try, leasePollMin, leasePollMax)); err != nil {
			return nil, err
		}
	}
}

// indexWaitLock is the advisory lock of scope $1 and model key $2 that an
// index update holds, shared, while it waits for a slot (AcquireIndexLease).
const indexWaitLock = `hashtextextended('kritika-index-wait ' || $1::text || ' ' || $2::text, 0)`

// indexBuildClaimLock is the advisory lock of scope $1 and model key $2
// that an index build's claim of a slot holds until it commits, so that the
// builds' claims take indexWaitLock one at a time.
const indexBuildClaimLock = `hashtextextended('kritika-index-build-claim ' || $1::text || ' ' || $2::text, 0)`

// AcquireIndexLease is AcquireLease for one embedding call of an index job,
// which holds the slot for that call alone. An update, of a repository
// already indexed, comes before every build, onboarding or forced: while one
// waits for a slot, no build takes one (TakeIndexBuildLease), so it gets the
// next that a build lets go, between two of the build's calls. It tries
// again at the shortest of AcquireLease's waits, since the builds leave it
// the slot meanwhile. Its tries run on the wait's session, which needs no
// connection of the pool. Where that session cannot be set up, as when
// Postgres at its connection limit refuses it, or fails later, the update
// waits on the pool without coming first.
func (s *Store) AcquireIndexLease(
	ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64, update bool,
) (*Lease, error) {
	if !update {
		for try := 0; ; try++ {
			l, err := s.TakeIndexBuildLease(ctx, accountID, modelKey, slots, jobID)
			if err != nil || l != nil {
				return l, err
			}
			if err := leaseWait(ctx, modelKey, Backoff(try, leasePollMin, leasePollMax)); err != nil {
				return nil, err
			}
		}
	}
	// A free slot is taken on the pool, and the wait's session below, a
	// login of its own, opened only for an update that has to wait. Postgres
	// at its connection limit refuses that login, which would otherwise fail
	// an update that the pool can serve.
	if l, err := s.TakeLease(ctx, accountID, modelKey, slots, jobID); err != nil || l != nil {
		return l, err
	}
	// The wait is a session of its own, which takes none of the pool's
	// connections for as long as it lasts, and whose end, closed or lost,
	// lets the lock go. Where it cannot be set up, as when Postgres at its
	// connection limit refuses it (53300), a new connection's dial fails
	// while the pool's open connections still work, or a pooler logs it in
	// but has no server connection for its first query, the update waits on
	// the pool as AcquireLease does, though without coming before the
	// builds, rather than fail while the pool can serve it.
	conn, err := s.openIndexWait(ctx, leaseScope(accountID, slots), modelKey)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("store: wait for a %s slot: %w", modelKey, err)
		}
		s.logger.Warn("index update waits for a slot without coming before the builds: its session could not be set up",
			"model", modelKey, "error", err)
	}
	defer func() {
		if conn != nil {
			s.closeIndexWait(ctx, conn, modelKey)
		}
	}()
	for {
		var l *Lease
		if conn == nil {
			l, err = s.TakeLease(ctx, accountID, modelKey, slots, jobID)
		} else {
			// On the session, as the pool may have to open a connection,
			// which Postgres refuses when the session took the last that it
			// allows.
			l, err = s.takeLease(ctx, conn, accountID, modelKey, slots, jobID, claimAny)
			if err != nil && ctx.Err() == nil {
				s.logger.Warn("index update waits for a slot without coming before the builds: its session failed",
					"model", modelKey, "error", err)
				s.closeIndexWait(ctx, conn, modelKey)
				conn = nil
				continue
			}
		}
		if err != nil || l != nil {
			return l, err
		}
		if err := leaseWait(ctx, modelKey, Backoff(0, leasePollMin, leasePollMin)); err != nil {
			return nil, err
		}
	}
}

// indexWaitSetup bounds setting up, its login and its wait lock, and
// indexWaitClose closing, the session of an index update's wait for a slot.
const (
	indexWaitSetup = 5 * time.Second
	indexWaitClose = 5 * time.Second
)

// openIndexWait opens the session of an index update's wait for a slot of
// scope and model, which holds the wait lock shared. The login and the
// lock share indexWaitSetup, so a setup that hangs at either costs the
// update its priority alone: a pooler can finish a login without a server
// connection and then hold the first query until one is free. The lock
// waits only for a build's claim, a transaction of a moment.
func (s *Store) openIndexWait(ctx context.Context, scope, modelKey string) (*pgx.Conn, error) {
	cfg := s.app.Config().ConnConfig
	cfg.RuntimeParams["application_name"] = "kritika-index-wait"
	sctx, cancel := context.WithTimeout(ctx, indexWaitSetup)
	defer cancel()
	conn, err := pgx.ConnectConfig(sctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(sctx, `SELECT pg_advisory_lock_shared(`+indexWaitLock+`)`, scope, modelKey); err != nil {
		s.closeIndexWait(ctx, conn, modelKey)
		return nil, err
	}
	return conn, nil
}

// closeIndexWait closes the session of an index update's wait, which lets
// its lock go, however ctx ended.
func (s *Store) closeIndexWait(ctx context.Context, conn *pgx.Conn, modelKey string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexWaitClose)
	defer cancel()
	if err := conn.Close(cctx); err != nil {
		s.logger.Warn("index slot wait not closed cleanly", "model", modelKey, "error", err)
	}
}

// leaseWait waits d before another try for a slot of modelKey, or until ctx
// ends.
func leaseWait(ctx context.Context, modelKey string, d time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("store: waiting for a %s slot: %w", modelKey, ctx.Err())
	case <-time.After(d):
		return nil
	}
}

// TakeIndexBuildLease claims a slot for one embedding call of an index
// build, as TakeLease does, unless an index update waits for one: nil then
// too.
func (s *Store) TakeIndexBuildLease(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (*Lease, error) {
	return s.takeLease(ctx, s.app, accountID, modelKey, slots, jobID, claimIndexBuild)
}

// leaseClaim is whose claim of a slot it is, which decides the reviews that
// come before it.
type leaseClaim int

const (
	// claimAny takes any free slot.
	claimAny leaseClaim = iota
	// claimReview is a review's first slot of a model. A draft's comes
	// after the reviews of its account's ready pull requests that wait for
	// theirs.
	claimReview
	// claimExtra is a slot beyond a review's first. It comes after every
	// review that waits for its first, of whichever account.
	claimExtra
	// claimIndexBuild is an index build's, which comes after every index
	// update that waits for a slot, of whichever account.
	claimIndexBuild
)

func claimSQL(c leaseClaim) string { return strconv.Itoa(int(c)) }

// TakeLease claims a free slot of the model for the account without
// waiting: nil when every slot is held.
func (s *Store) TakeLease(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (*Lease, error) {
	return s.takeLease(ctx, s.app, accountID, modelKey, slots, jobID, claimAny)
}

// TakeReviewLease claims the first slot of the model for the review job
// jobID of the account, as TakeLease does. When the review is of a draft, it
// leaves a free slot to each review of a ready pull request of its account
// that waits for its first slot of the model, and claims one only beyond
// those, and only once each of those reviews is running.
func (s *Store) TakeReviewLease(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (*Lease, error) {
	return s.takeLease(ctx, s.app, accountID, modelKey, slots, jobID, claimReview)
}

// TakeExtraLease claims capacity for an already admitted review only when
// no recorded slot waiter needs its first lease: of any account, where the
// slots are shared. An awakened waiter keeps priority until it acquires a
// lease or leaves the review queue.
func (s *Store) TakeExtraLease(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (*Lease, error) {
	return s.takeLease(ctx, s.app, accountID, modelKey, slots, jobID, claimExtra)
}

// slotWaiters selects the reviews, other than job $3, that wait for their
// first slot of model $2 in scope $1: marked as waiting for one, and holding
// none. Those of every account wait for a shared slot.
const slotWaiters = `FROM river_job j WHERE j.kind = 'review' AND j.id <> $3
	AND j.state IN ('available', 'scheduled', 'running')
	AND ($1::text = '` + sharedScope + `' OR j.args->>'account_id' = $1::text) AND j.metadata->>'` + SlotWaitKey + `' = $2
	AND NOT EXISTS (SELECT 1 FROM model_leases held
		WHERE held.scope = $1::text AND held.model_key = $2 AND held.job_id = j.id AND held.expires_at >= now())`

// draftReview is whether the pull request that the review job j is of is a
// draft, as the forge last said.
const draftReview = `coalesce((SELECT p.draft FROM pull_requests p WHERE p.repository_id = (j.args->>'repository_id')::uuid
	AND p.number = (j.args->>'number')::int), false)`

// readyWaiters is the slotWaiters of account $4 whose pull requests are
// ready. A pull request of another account cannot be read, so whether it is
// a draft is unknown here: a draft's review comes after the ready ones of
// its own account alone.
const readyWaiters = slotWaiters + ` AND j.args->>'account_id' = $4::uuid::text AND NOT ` + draftReview

// heldSQL counts the slots of scope $1 and model $2 that a live lease
// holds, out of the first slots, a parameter.
func heldSQL(slots string) string {
	return `(SELECT count(*) FROM model_leases WHERE scope = $1::text AND model_key = $2 AND slot <= ` + slots + `::int
		AND job_id IS NOT NULL AND expires_at >= now())`
}

// draftWaits is whether a draft's review gets none of the first slots, a
// parameter. The reviews of its account's ready pull requests that wait for
// their first slot (readyWaiters) are as many as the free slots, or more, so none is
// left over. Or one of those reviews is not running yet: a draft's review
// that started its runner could hold the worker that review needs, for as
// long as it runs.
func draftWaits(slots string) string {
	return `((SELECT count(*) ` + readyWaiters + `) >= ` + slots + `::int - ` + heldSQL(slots) + `
		OR EXISTS (SELECT 1 ` + readyWaiters + ` AND j.state <> 'running'))`
}

// SlotsOwed is how many slots of the model the reviews that wait for their
// first one are short of: those reviews, of every account where the slots
// are shared, less the slots that are free. The review jobID, which holds
// extra slots, gives that many back.
func (s *Store) SlotsOwed(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (int, error) {
	scope := leaseScope(accountID, slots)
	var owed int
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		held, err := heldSlots(ctx, tx, scope, modelKey, slots.Limit)
		if err != nil {
			return err
		}
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) `+slotWaiters, scope, modelKey, jobID).Scan(&waiting); err != nil {
			return fmt.Errorf("store: count slot waiters: %w", err)
		}
		owed = max(0, waiting-(slots.Limit-held))
		return nil
	})
	return owed, err
}

// takeLease claims a slot as claim says, in a transaction on db.
func (s *Store) takeLease(
	ctx context.Context, db txBeginner, accountID, modelKey string, slots configfile.Slots, jobID int64, claim leaseClaim,
) (*Lease, error) {
	scope := leaseScope(accountID, slots)
	slot := 0
	err := withAccount(ctx, db, pgx.TxOptions{}, accountID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO model_leases (scope, model_key, slot)
			SELECT $1, $2, s FROM generate_series(1, $3::int) AS s ON CONFLICT DO NOTHING`, scope, modelKey, slots.Limit); err != nil {
			return fmt.Errorf("store: ensure lease slots: %w", err)
		}
		if claim == claimReview {
			// One review at a time counts the free slots against the
			// reviews that wait, so two drafts' reviews cannot both take
			// the one slot that is left over.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || ' ' || $2::text, 0))`,
				scope, modelKey); err != nil {
				return fmt.Errorf("store: lock review claims: %w", err)
			}
		}
		if claim == claimIndexBuild {
			// An update that waits holds the lock shared, so a build cannot
			// take it. A build that holds it while its claim commits would
			// also keep another from it, which would then back off as if
			// every slot were held, so the builds' claims queue for it in
			// turn. Each holds it for a claim alone, never for a wait.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+indexBuildClaimLock+`)`, scope, modelKey); err != nil {
				return fmt.Errorf("store: lock index build claims: %w", err)
			}
			var free bool
			if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(`+indexWaitLock+`)`, scope, modelKey).Scan(&free); err != nil {
				return fmt.Errorf("store: look for index updates waiting: %w", err)
			}
			if !free {
				return nil
			}
		}
		err := tx.QueryRow(ctx, `UPDATE model_leases SET job_id = $3, account_id = $4::uuid, expires_at = now() + $5::interval
			WHERE (scope, model_key, slot) = (
				SELECT scope, model_key, slot FROM model_leases
				WHERE scope = $1::text AND model_key = $2 AND slot <= $6 AND (job_id IS NULL OR expires_at < now())
				AND ($7::int <> `+claimSQL(claimExtra)+` OR NOT EXISTS (SELECT 1 `+slotWaiters+`))
				AND ($7::int <> `+claimSQL(claimReview)+` OR NOT (coalesce((SELECT `+draftReview+` FROM river_job j WHERE j.id = $3), false)
					AND `+draftWaits("$6")+`))
				ORDER BY slot LIMIT 1 FOR UPDATE SKIP LOCKED)
			RETURNING slot`, scope, modelKey, jobID, accountID, leaseExpiry, slots.Limit, int(claim)).Scan(&slot)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: claim lease: %w", err)
		}
		return nil
	})
	if err != nil || slot == 0 {
		return nil, err
	}
	l := &Lease{st: s, accountID: accountID, modelKey: modelKey, slot: slot, slots: slots, jobID: jobID, done: make(chan struct{})}
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l.cancel = cancel
	go l.heartbeat(hctx)
	return l, nil
}

// SlotFree reports whether the model has a slot for the account that no
// live lease holds, without claiming it: a hint that a review about to
// start its runner will find one when it needs it.
func (s *Store) SlotFree(ctx context.Context, accountID, modelKey string, slots configfile.Slots) (bool, error) {
	var held int
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) (err error) {
		held, err = heldSlots(ctx, tx, leaseScope(accountID, slots), modelKey, slots.Limit)
		return err
	})
	return held < slots.Limit, err
}

// heldSlots counts the slots of (scope, model) a live lease holds.
func heldSlots(ctx context.Context, tx pgx.Tx, scope, modelKey string, slots int) (int, error) {
	var held int
	err := tx.QueryRow(ctx, `SELECT `+heldSQL("$3"), scope, modelKey, slots).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("store: read lease slots: %w", err)
	}
	return held, nil
}

func (l *Lease) heartbeat(ctx context.Context) {
	defer close(l.done)
	t := time.NewTicker(leaseHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// A failed renewal is not fatal here: the slot expires and
			// another process can take it, which only means one extra
			// concurrent call for a while.
			err := l.st.WithAccount(ctx, l.accountID, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE model_leases SET expires_at = now() + $4::interval
					WHERE scope = $1 AND model_key = $2 AND slot = $3 AND job_id = $5`,
					leaseScope(l.accountID, l.slots), l.modelKey, l.slot, leaseExpiry, l.jobID)
				return err
			})
			if err != nil && ctx.Err() == nil {
				l.st.logger.Warn("model lease not renewed", "job", l.jobID, "model", l.modelKey, "slot", l.slot, "error", err)
			}
		}
	}
}

// SlotWaitKey is the river_job metadata key that a review sets to its model
// key when it snoozes because every slot of that model was held. Its next
// attempt clears the key. Release wakes the oldest such review of the
// account. So a freed slot is taken as soon as a worker gets to the job
// rather than when its snooze runs out. River saves the snooze, and the key,
// some time after the review returns them. A release in between finds no
// waiter. Once the snooze is saved, WakeIfSlotFree looks for the slot that
// release let go. A shared slot wakes a review of the account that holds the
// fewest slots of the model, so that one account's backlog cannot keep the
// others from them.
const SlotWaitKey = "slot_wait"

// Release frees the slot and wakes the review that is next in line for one
// on the same model (wakeNext). It returns that job's id, or 0 when none waits. Pass a ctx
// that outlives job cancellation, so that the slot is not left to expire.
//
// The slot is freed in a transaction of its own, committed before the
// wake looks for a waiter: a review whose snooze was saved after that look
// counts the slots after this commit, so one of the two sees the other.
func (l *Lease) Release(ctx context.Context) (woken int64, err error) {
	l.cancel()
	<-l.done
	err = l.st.WithAccount(ctx, l.accountID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE model_leases SET job_id = NULL, account_id = NULL, expires_at = NULL
			WHERE scope = $1 AND model_key = $2 AND slot = $3 AND job_id = $4`,
			leaseScope(l.accountID, l.slots), l.modelKey, l.slot, l.jobID)
		if err != nil {
			return fmt.Errorf("store: release lease: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return l.st.wakeNext(ctx, l.accountID, l.modelKey, l.slots, false)
}

// wakeNext wakes the review that is next in line for a slot of the model,
// and returns its id, or 0 when none waits. Where the account's slots are
// its own, that is its oldest waiter that wakeWaiter can wake. Where every
// account shares them, it is such a waiter of the account that holds the
// fewest of them. Among accounts that hold as many, the account whose
// oldest waiter is the oldest comes first, whether that waiter is the one
// woken or a draft's that stays asleep: each account's waiters are read in
// a transaction of that account, which alone can tell its drafts. With
// ifFree, it wakes none while every slot is held.
func (s *Store) wakeNext(ctx context.Context, accountID, modelKey string, slots configfile.Slots, ifFree bool) (int64, error) {
	var woken int64
	if !slots.Shared {
		err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
			if ifFree {
				held, err := heldSlots(ctx, tx, accountID, modelKey, slots.Limit)
				if err != nil || held >= slots.Limit {
					return err
				}
			}
			var err error
			woken, err = wakeWaiter(ctx, tx, accountID, modelKey, slots, 0)
			return err
		})
		return woken, err
	}
	var accounts []string
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		if ifFree {
			held, err := heldSlots(ctx, tx, sharedScope, modelKey, slots.Limit)
			if err != nil || held >= slots.Limit {
				return err
			}
		}
		// River's jobs are outside row-level security, and a shared slot
		// names the account that holds it.
		rows, err := tx.Query(ctx, `SELECT w.account FROM (
				SELECT j.args->>'account_id' AS account, min(j.id) AS first FROM river_job j
				WHERE j.kind = 'review' AND j.state IN ('scheduled', 'available') AND j.scheduled_at > now()
				AND j.metadata->>'`+SlotWaitKey+`' = $1 GROUP BY 1) w
			ORDER BY (SELECT count(*) FROM model_leases l WHERE l.scope = '`+sharedScope+`' AND l.model_key = $1
				AND l.account_id::text = w.account AND l.job_id IS NOT NULL AND l.expires_at >= now()), w.first`, modelKey)
		if err != nil {
			return fmt.Errorf("store: list the accounts that wait for a slot: %w", err)
		}
		if accounts, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return fmt.Errorf("store: list the accounts that wait for a slot: %w", err)
		}
		return nil
	})
	for _, id := range accounts {
		if err != nil || woken != 0 {
			break
		}
		err = s.WithAccount(ctx, id, func(tx pgx.Tx) (err error) {
			woken, err = wakeWaiter(ctx, tx, id, modelKey, slots, 0)
			return err
		})
	}
	return woken, err
}

// WakeIfSlotFree wakes the review job jobID of the account, snoozed for a
// slot of modelKey, when a slot is free, and reports whether it did. It runs
// once River saves the snooze, for the slot that a release let go before
// then. A jobID of 0 is the review that is next in line (wakeNext). It runs
// once a review has its first slot, for a draft's review that waited for
// that one to take it.
func (s *Store) WakeIfSlotFree(ctx context.Context, accountID, modelKey string, slots configfile.Slots, jobID int64) (bool, error) {
	if jobID == 0 {
		woken, err := s.wakeNext(ctx, accountID, modelKey, slots, true)
		return woken != 0, err
	}
	var woken int64
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		held, err := heldSlots(ctx, tx, leaseScope(accountID, slots), modelKey, slots.Limit)
		if err != nil || held >= slots.Limit {
			return err
		}
		woken, err = wakeWaiter(ctx, tx, accountID, modelKey, slots, jobID)
		return err
	})
	return woken != 0, err
}

// ClearSlotWait removes the mark of the model that the review job jobID
// waited for a slot of, and returns that model, or "" when the job has no
// mark. A review that has its slots clears it. River saves the worker's own
// clear only as the attempt ends, and until then the review would count as
// waiting for a slot of that model whenever it holds none: once it lets its
// slot go, or all along when it no longer reviews with that model.
func (s *Store) ClearSlotWait(ctx context.Context, accountID string, jobID int64) (string, error) {
	var model string
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `WITH marked AS (SELECT id, metadata->>'`+SlotWaitKey+`' AS model FROM river_job
				WHERE id = $1 AND metadata->>'`+SlotWaitKey+`' IS NOT NULL FOR UPDATE)
			UPDATE river_job j SET metadata = j.metadata - '`+SlotWaitKey+`' FROM marked WHERE j.id = marked.id
			RETURNING marked.model`, jobID).Scan(&model)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store: clear a review's slot wait: %w", err)
		}
		return nil
	})
	return model, err
}

// wakeWaiter makes a review available now and returns its id, or 0 when
// there is none. The review is one of the account's snoozed for a slot of
// modelKey: the oldest of a pull request that is ready, or else the oldest of
// a draft. A draft's is left asleep while the reviews of the account's ready
// pull requests that wait for their first slot leave it none of the slots
// free, or are not all running, since it can claim none until then (see
// TakeReviewLease). When jobID is not 0, the review is the one of them that
// jobID names. tx is a transaction of the account.
func wakeWaiter(ctx context.Context, tx pgx.Tx, accountID, modelKey string, slots configfile.Slots, jobID int64) (int64, error) {
	var woken int64
	// River keeps a job snoozed, or retried, for less than its scheduler
	// interval available with a scheduled_at ahead, rather than scheduled
	// or retryable. Only the key tells a slot's waiter.
	err := tx.QueryRow(ctx, `UPDATE river_job SET state = 'available', scheduled_at = now()
		WHERE id = (SELECT j.id FROM river_job j WHERE j.kind = 'review' AND j.state IN ('scheduled', 'available')
			AND j.scheduled_at > now() AND j.args->>'account_id' = $4::uuid::text AND j.metadata->>'`+SlotWaitKey+`' = $2
			AND ($3::bigint = 0 OR j.id = $3::bigint)
			AND NOT (`+draftReview+` AND `+draftWaits("$5")+`)
			ORDER BY `+draftReview+`, j.id
			LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id`, leaseScope(accountID, slots), modelKey, jobID, accountID, slots.Limit).Scan(&woken)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("store: wake a snoozed review: %w", err)
	}
	return woken, nil
}

// CapReason says which of the account's caps its usage reached, or "".
func CapReason(u MonthUsage, limits configfile.Limits) string {
	if limits.ReviewsPerDay > 0 && u.ReviewsToday >= int64(limits.ReviewsPerDay) {
		return fmt.Sprintf("reviewsPerDay (%d) reached", limits.ReviewsPerDay)
	}
	if limits.TokensPerMonth > 0 && u.Tokens >= limits.TokensPerMonth {
		return fmt.Sprintf("tokensPerMonth (%d) reached", limits.TokensPerMonth)
	}
	return ""
}

// AccountUsage is what the account's caps count: completed reviews today
// and tokens this month.
func (s *Store) AccountUsage(ctx context.Context, accountID string) (MonthUsage, error) {
	var m MonthUsage
	err := s.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var err error
		m, err = readCapUsage(ctx, tx)
		return err
	})
	return m, err
}

// CapReached says which of the account's caps is exhausted, or "". With
// no cap set it reads nothing.
func (s *Store) CapReached(ctx context.Context, accountID string, limits configfile.Limits) (string, error) {
	if limits.ReviewsPerDay <= 0 && limits.TokensPerMonth <= 0 {
		return "", nil
	}
	u, err := s.AccountUsage(ctx, accountID)
	if err != nil {
		return "", err
	}
	return CapReason(u, limits), nil
}
