package tuning

import (
	"errors"
	"fmt"
	"time"
)

// Store holds the tunable values of package store.
type Store struct {
	// LoginStateTTL is how long a sign-in can take between leaving for the
	// provider and coming back.
	LoginStateTTL time.Duration `env:"KRITIKA_TUNING_STORE_LOGIN_STATE_TTL" envDefault:"10m" yaml:"loginStateTtl,omitempty"`
	// MaxLoginStates caps the sign-ins in flight at once. Starting one needs
	// no session, so without a cap anyone can grow login_states without
	// bound for LoginStateTTL. The default is far more than any real
	// dashboard's users start in that time.
	MaxLoginStates int `env:"KRITIKA_TUNING_STORE_MAX_LOGIN_STATES" envDefault:"10000" yaml:"maxLoginStates,omitempty"`
	// RefusalsPerRun bounds the egress refusal rows that a run has, one per
	// kind, host and reason. Past it, a run's further refusals are counted
	// on the rows it has and new ones go unrecorded, so a looping agent
	// cannot fill the table.
	RefusalsPerRun int `env:"KRITIKA_TUNING_STORE_REFUSALS_PER_RUN" envDefault:"100" yaml:"refusalsPerRun,omitempty"`
	// LockYieldShare is the share of deadlock_timeout, 1/LockYieldShare,
	// that each of the index partition maintenance's lock waits gets, so
	// that a publication's deadlock check never finds the cycle first and
	// every search and build queued behind the wait waits briefly.
	LockYieldShare int `env:"KRITIKA_TUNING_STORE_LOCK_YIELD_SHARE" envDefault:"8" yaml:"lockYieldShare,omitempty"`
	// PartitionTries is how many times the partition maintenance tries a
	// lock wait that it gave up, PartitionRetryPause apart. The next sweep
	// tries again.
	PartitionTries int `env:"KRITIKA_TUNING_STORE_PARTITION_TRIES" envDefault:"5" yaml:"partitionTries,omitempty"`
	// PartitionRetryPause is the pause between those tries.
	PartitionRetryPause time.Duration `env:"KRITIKA_TUNING_STORE_PARTITION_RETRY_PAUSE" envDefault:"1s" yaml:"partitionRetryPause,omitempty"`
	// LegacyDrainSlice is how many chunks the drain of the legacy index
	// partition moves or deletes at a time. It holds the repository's lock
	// while it does, which bounds how long a publication or a webhook's
	// write to the repository waits for it.
	LegacyDrainSlice int `env:"KRITIKA_TUNING_STORE_LEGACY_DRAIN_SLICE" envDefault:"5000" yaml:"legacyDrainSlice,omitempty"`
	// LockTimeout bounds one attempt at the leader lock and each ping of
	// the held connection: a round trip each on a server that is up. It
	// turns a server that is gone into a step-down or a retry instead of a
	// wait on TCP retransmissions.
	LockTimeout time.Duration `env:"KRITIKA_TUNING_STORE_LOCK_TIMEOUT" envDefault:"5s" yaml:"lockTimeout,omitempty"`
	// SessionCloseTimeout bounds the best-effort release of a dedicated
	// connection: the leader lock's unlock, and the close of the listen and
	// publication connections.
	SessionCloseTimeout time.Duration `env:"KRITIKA_TUNING_STORE_SESSION_CLOSE_TIMEOUT" envDefault:"5s" yaml:"sessionCloseTimeout,omitempty"`
	// LeaseHeartbeat is how often a model lease's holder renews it.
	LeaseHeartbeat time.Duration `env:"KRITIKA_TUNING_STORE_LEASE_HEARTBEAT" envDefault:"30s" yaml:"leaseHeartbeat,omitempty"`
	// LeaseExpiry is the age past which a lease belongs to a process that
	// died and is free to take. Validate holds it above LeaseHeartbeat.
	LeaseExpiry time.Duration `env:"KRITIKA_TUNING_STORE_LEASE_EXPIRY" envDefault:"2m" yaml:"leaseExpiry,omitempty"`
	// LeasePollMin is the first wait between tries for a model slot. Each
	// try doubles it, up to LeasePollMax.
	LeasePollMin time.Duration `env:"KRITIKA_TUNING_STORE_LEASE_POLL_MIN" envDefault:"2s" yaml:"leasePollMin,omitempty"`
	// LeasePollMax caps that wait.
	LeasePollMax time.Duration `env:"KRITIKA_TUNING_STORE_LEASE_POLL_MAX" envDefault:"30s" yaml:"leasePollMax,omitempty"`
	// IndexWaitSetup bounds setting up the session of an index update's wait
	// for a slot: its login and its wait lock.
	IndexWaitSetup time.Duration `env:"KRITIKA_TUNING_STORE_INDEX_WAIT_SETUP" envDefault:"5s" yaml:"indexWaitSetup,omitempty"`
	// IndexWaitClose bounds closing that session.
	IndexWaitClose time.Duration `env:"KRITIKA_TUNING_STORE_INDEX_WAIT_CLOSE" envDefault:"5s" yaml:"indexWaitClose,omitempty"`
	// ListenBufferSize bounds the queue of notifications waiting for their
	// callbacks to run. It only needs to absorb a burst: in a steady state
	// the consumer drains it as fast as Postgres delivers.
	ListenBufferSize int `env:"KRITIKA_TUNING_STORE_LISTEN_BUFFER_SIZE" envDefault:"1024" yaml:"listenBufferSize,omitempty"`
	// ListenDropWarnInterval is the least time between the warnings that the
	// notification buffer is full, each of which counts the notifications
	// dropped since the last.
	ListenDropWarnInterval time.Duration `env:"KRITIKA_TUNING_STORE_LISTEN_DROP_WARN_INTERVAL" envDefault:"1s" yaml:"listenDropWarnInterval,omitempty"` //nolint:lll // a struct tag does not wrap
	// ListenBackoffMin is the first reconnect delay of the notification
	// listener. It doubles per failed attempt, up to ListenBackoffMax, and is
	// jittered so that listeners that one failover disconnected do not all
	// reconnect in lockstep.
	ListenBackoffMin time.Duration `env:"KRITIKA_TUNING_STORE_LISTEN_BACKOFF_MIN" envDefault:"1s" yaml:"listenBackoffMin,omitempty"`
	// ListenBackoffMax caps that delay.
	ListenBackoffMax time.Duration `env:"KRITIKA_TUNING_STORE_LISTEN_BACKOFF_MAX" envDefault:"30s" yaml:"listenBackoffMax,omitempty"`
	// MaxCheckpointRead bounds what one read of a run's part checkpoints
	// holds in memory. What a runner saved decides the number and size of
	// the rows, and the read runs in the service.
	MaxCheckpointRead ByteSize `env:"KRITIKA_TUNING_STORE_MAX_CHECKPOINT_READ" envDefault:"32Mi" yaml:"maxCheckpointRead,omitempty"`
	// PollFailedReviews is how many failed reviews of a head settle it for a
	// poll. A failed review's own comment moves the pull request, so the
	// next poll lists it again and makes up for a failure that does not
	// repeat. One that does repeat is the review's own.
	PollFailedReviews int `env:"KRITIKA_TUNING_STORE_POLL_FAILED_REVIEWS" envDefault:"2" yaml:"pollFailedReviews,omitempty"`
	// SimilarExcludeBatch is the most paths that one statement recording a
	// run's similar-code exclusions writes, so that a statement stays small
	// however many paths a report holds.
	SimilarExcludeBatch int `env:"KRITIKA_TUNING_STORE_SIMILAR_EXCLUDE_BATCH" envDefault:"1000" yaml:"similarExcludeBatch,omitempty"`
	// ErrorTextChars is how much of an error its row keeps: a review's, a
	// runner run's, an agent run's or an index run's.
	ErrorTextChars int `env:"KRITIKA_TUNING_STORE_ERROR_TEXT_CHARS" envDefault:"2000" yaml:"errorTextChars,omitempty"`
	// DismissalReasonChars is how much of a dismissal's reason its row
	// keeps.
	DismissalReasonChars int `env:"KRITIKA_TUNING_STORE_DISMISSAL_REASON_CHARS" envDefault:"500" yaml:"dismissalReasonChars,omitempty"`
	// FollowupReasonChars is how much of a follow-up's reason its row keeps.
	FollowupReasonChars int `env:"KRITIKA_TUNING_STORE_FOLLOWUP_REASON_CHARS" envDefault:"500" yaml:"followupReasonChars,omitempty"`
	// AppPoolMaxConns is the application pool's ceiling when its URI sets no
	// pool_max_conns. pgx's own default is the node's CPU count, and a serve
	// pod has no CPU limit, so on a large node two replicas alone could take
	// most of Postgres's connections. Connections open on demand, so a
	// ceiling costs an idle process nothing.
	AppPoolMaxConns int `env:"KRITIKA_TUNING_STORE_APP_POOL_MAX_CONNS" envDefault:"16" yaml:"appPoolMaxConns,omitempty"`
	// OwnerPoolMaxConns is the owner pool's, which carries only the
	// leader's duties.
	OwnerPoolMaxConns int `env:"KRITIKA_TUNING_STORE_OWNER_POOL_MAX_CONNS" envDefault:"4" yaml:"ownerPoolMaxConns,omitempty"`
	// KeepaliveIdle is how long a pool connection is idle before Postgres
	// probes its client, KeepaliveInterval how long it waits between probes,
	// and KeepaliveCount how many unanswered probes drop the session: so a
	// client that died without closing its socket (a node lost, a pod
	// killed) loses its session, and any advisory lock that it held, within
	// about a minute at the defaults, rather than the kernel's two hours.
	// Postgres takes them in whole seconds.
	KeepaliveIdle     time.Duration `env:"KRITIKA_TUNING_STORE_KEEPALIVE_IDLE" envDefault:"30s" yaml:"keepaliveIdle,omitempty"`
	KeepaliveInterval time.Duration `env:"KRITIKA_TUNING_STORE_KEEPALIVE_INTERVAL" envDefault:"10s" yaml:"keepaliveInterval,omitempty"`
	KeepaliveCount    int           `env:"KRITIKA_TUNING_STORE_KEEPALIVE_COUNT" envDefault:"3" yaml:"keepaliveCount,omitempty"`
	// AppStatementTimeout bounds every statement on the application pool.
	// River, webhook ingest, the gateway and the dashboard share the pool,
	// so an unbounded dashboard query over a large account could hold its
	// connections and stall the rest.
	AppStatementTimeout time.Duration `env:"KRITIKA_TUNING_STORE_APP_STATEMENT_TIMEOUT" envDefault:"1m" yaml:"appStatementTimeout,omitempty"`
	// OwnerStatementTimeout bounds every statement on the owner pool.
	// Migrations can build an index for minutes; anything longer is a hang
	// worth breaking.
	OwnerStatementTimeout time.Duration `env:"KRITIKA_TUNING_STORE_OWNER_STATEMENT_TIMEOUT" envDefault:"10m" yaml:"ownerStatementTimeout,omitempty"` //nolint:lll // a struct tag does not wrap
	// PingTimeout bounds the ping a pool gives an idle connection before it
	// hands it out. Without it the ping waits on the kernel's TCP
	// retransmissions, many minutes, when the server died without closing
	// the socket. A ping is an empty query.
	PingTimeout time.Duration `env:"KRITIKA_TUNING_STORE_PING_TIMEOUT" envDefault:"5s" yaml:"pingTimeout,omitempty"`
	// OpenRetry is how long the start waits to open the store again while
	// the database does not answer, as in a fresh deployment whose Postgres
	// is still bootstrapping.
	OpenRetry time.Duration `env:"KRITIKA_TUNING_STORE_OPEN_RETRY" envDefault:"5s" yaml:"openRetry,omitempty"`
	// ModelCallSweepBatch is how many rows one statement of the model-call,
	// egress refusal, model invocation and similar-code exclusion retention
	// sweeps takes. A sweep is many such statements, each committed on its
	// own, so a backlog too large for the owner connection's statement
	// timeout is worked off rather than rolled back whole every time.
	ModelCallSweepBatch int `env:"KRITIKA_TUNING_STORE_MODEL_CALL_SWEEP_BATCH" envDefault:"5000" yaml:"modelCallSweepBatch,omitempty"`
	// DiffSweepBatch is the same for review diffs and conversations, which
	// carry a whole diff or exchange each, so their batch is the smaller.
	DiffSweepBatch int `env:"KRITIKA_TUNING_STORE_DIFF_SWEEP_BATCH" envDefault:"200" yaml:"diffSweepBatch,omitempty"`
	// SummaryTop is how many groups of each kind the dashboard's finding
	// summary keeps.
	SummaryTop int `env:"KRITIKA_TUNING_STORE_SUMMARY_TOP" envDefault:"6" yaml:"summaryTop,omitempty"`
	// QueueFinished bounds how many finished jobs the queue view lists.
	QueueFinished int `env:"KRITIKA_TUNING_STORE_QUEUE_FINISHED" envDefault:"50" yaml:"queueFinished,omitempty"`
	// QueueActive bounds how many unfinished jobs it lists, which a healthy
	// queue keeps far below it.
	QueueActive int `env:"KRITIKA_TUNING_STORE_QUEUE_ACTIVE" envDefault:"500" yaml:"queueActive,omitempty"`
	// RecentFailureWindow is how long a discarded job shows in the queue
	// view as a recent failure, unless a newer job of its kind for the same
	// pull request or comment came after it.
	RecentFailureWindow time.Duration `env:"KRITIKA_TUNING_STORE_RECENT_FAILURE_WINDOW" envDefault:"24h" yaml:"recentFailureWindow,omitempty"`
	// APITokenUseInterval is how often an API token's last use is written:
	// the dashboard needs to know when it was used, not each use.
	APITokenUseInterval time.Duration `env:"KRITIKA_TUNING_STORE_API_TOKEN_USE_INTERVAL" envDefault:"1m" yaml:"apiTokenUseInterval,omitempty"`
	// WebhookDeliveryInterval is how often a connection's last webhook
	// delivery, signed or unsigned, is written: the dashboard needs to know
	// that deliveries arrive, not to count them.
	WebhookDeliveryInterval time.Duration `env:"KRITIKA_TUNING_STORE_WEBHOOK_DELIVERY_INTERVAL" envDefault:"1m" yaml:"webhookDeliveryInterval,omitempty"` //nolint:lll // a struct tag does not wrap
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			if v.Store.LeaseExpiry <= v.Store.LeaseHeartbeat {
				return fmt.Errorf("%s %w %s", fieldLabel("store.leaseExpiry"), errMustExceed, fieldLabel("store.leaseHeartbeat"))
			}
			return nil
		},
		func(v Values) error {
			return notAbove("store.leasePollMin", v.Store.LeasePollMin, "store.leasePollMax", v.Store.LeasePollMax)
		},
		func(v Values) error {
			return notAbove("store.listenBackoffMin", v.Store.ListenBackoffMin, "store.listenBackoffMax", v.Store.ListenBackoffMax)
		},
		func(v Values) error {
			// Postgres takes keepalive timings in whole seconds, and 0 is
			// its system default.
			var errs []error
			for path, d := range map[string]time.Duration{
				"store.keepaliveIdle": v.Store.KeepaliveIdle, "store.keepaliveInterval": v.Store.KeepaliveInterval,
			} {
				if d < time.Second {
					errs = append(errs, fmt.Errorf("%s must be at least 1s: Postgres counts it in whole seconds", fieldLabel(path)))
				}
			}
			return errors.Join(errs...)
		},
	)
}
