// Package store owns kritika's Postgres access: the two connection pools,
// the startup assertion that keeps row-level security honest, schema
// migrations, the leader lock, and account-scoped transactions. It also
// applies the running configuration, the file's and the dashboard's, to the
// rows that it declares.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store holds the application pool every request and job uses and, on
// leader-eligible roles, the owner pool that runs migrations and the
// configuration sync.
type Store struct {
	app    *pgxpool.Pool
	owner  *pgxpool.Pool
	logger *slog.Logger
}

// newPool opens a pool with server-side TCP keepalives. So Postgres drops
// the session of a client that died without closing it (a node lost, a pod
// killed) within tuning's Store.KeepaliveIdle, KeepaliveInterval and
// KeepaliveCount, and any advisory lock that the session held goes with it.
// The pool's own ping of an idle connection is bounded by Store.PingTimeout.
// So a server that died the same way costs a client seconds, not the kernel's
// retransmissions. A statement timeout, when given, bounds every statement
// on the pool.
// applicationNameParam is the connection parameter that names a connection
// in pg_stat_activity, and applicationName is the name of a connection for
// role: kritika-app, kritika-listen and so on.
const applicationNameParam = "application_name"

func applicationName(role string) string { return tuning.Product + "-" + role }

func newPool(ctx context.Context, url, application string, statementTimeout time.Duration, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url, application, statementTimeout, maxConns)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// poolConfig is newPool's configuration. maxConns is the pool's ceiling
// and tuning's Store.PingTimeout is its ping bound, unless the URI names its own with
// pool_max_conns or pool_ping_timeout. pgx already read those by the time
// that the configuration is parsed.
func poolConfig(url, application string, statementTimeout time.Duration, maxConns int32) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(url, "pool_max_conns") {
		cfg.MaxConns = maxConns
	}
	if !strings.Contains(url, "pool_ping_timeout") {
		cfg.PingTimeout = tuning.Get().Store.PingTimeout
	}
	params := cfg.ConnConfig.RuntimeParams
	params[applicationNameParam] = application
	t := tuning.Get().Store
	params["tcp_keepalives_idle"] = strconv.FormatInt(int64(t.KeepaliveIdle/time.Second), 10)
	params["tcp_keepalives_interval"] = strconv.FormatInt(int64(t.KeepaliveInterval/time.Second), 10)
	params["tcp_keepalives_count"] = strconv.Itoa(t.KeepaliveCount)
	if statementTimeout > 0 {
		// Rounded up: Postgres reads 0 as no timeout at all.
		params["statement_timeout"] = strconv.FormatInt(int64((statementTimeout+time.Millisecond-1)/time.Millisecond), 10)
	}
	return cfg, nil
}

// Options configure Open.
type Options struct {
	// AppURL is the application role's DSN. Required.
	AppURL string
	// OwnerURL is the owner role's DSN. It is optional. Without it, the process
	// can never become leader.
	OwnerURL string
	Logger   *slog.Logger
}

// Open connects both pools and runs the startup assertions. It fails, rather
// than serving, when the application DSN can bypass row-level security or
// the vector extension is missing. Either one is invisible at runtime and
// wrong.
func Open(ctx context.Context, opts Options) (*Store, error) {
	t := tuning.Get().Store
	app, err := newPool(ctx, opts.AppURL, applicationName("app"), t.AppStatementTimeout, int32(t.AppPoolMaxConns))
	if err != nil {
		return nil, fmt.Errorf("store: application pool: %w", err)
	}
	s := &Store{app: app, logger: opts.Logger}
	if err := s.assertApplicationRole(ctx); err != nil {
		app.Close()
		return nil, err
	}
	if err := s.assertExtension(ctx); err != nil {
		app.Close()
		return nil, err
	}
	if opts.OwnerURL != "" {
		// Migrations can build an index for minutes. Anything longer on the
		// owner connection is a hang worth breaking.
		owner, err := newPool(ctx, opts.OwnerURL, applicationName("owner"), t.OwnerStatementTimeout, int32(t.OwnerPoolMaxConns))
		if err != nil {
			app.Close()
			return nil, fmt.Errorf("store: owner pool: %w", err)
		}
		if err := assertOwnerRole(ctx, owner); err != nil {
			app.Close()
			owner.Close()
			return nil, err
		}
		s.owner = owner
	}
	return s, nil
}

// Close releases both pools.
func (s *Store) Close() {
	s.app.Close()
	if s.owner != nil {
		s.owner.Close()
	}
}

// App exposes the application pool for account-scoped work. Prefer
// [Store.WithAccount].
func (s *Store) App() *pgxpool.Pool { return s.app }

// Now is the database's clock, for a time a query will compare a row's
// clock_timestamp() against: the application's clock can run ahead of or
// behind it.
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := s.app.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("store: read the clock: %w", err)
	}
	return now, nil
}

// LeaderEligible reports whether an owner DSN was configured.
func (s *Store) LeaderEligible() bool { return s.owner != nil }

// PoolStats is each pool's live state by name, app and, when configured,
// owner, for the metrics.
func (s *Store) PoolStats() map[string]func() *pgxpool.Stat {
	stats := map[string]func() *pgxpool.Stat{"app": s.app.Stat}
	if s.owner != nil {
		stats["owner"] = s.owner.Stat
	}
	return stats
}

// ErrIsolationOff is a database role that can bypass row-level security.
var ErrIsolationOff = errors.New("store: application role would bypass row-level security")

// assertApplicationRole refuses a DSN whose role is a superuser, has
// BYPASSRLS, or owns any table in the schema. Any one of those silently
// disables every policy.
func (s *Store) assertApplicationRole(ctx context.Context) error {
	var super, bypass bool
	var owned int
	err := s.app.QueryRow(ctx, `
		SELECT r.rolsuper, r.rolbypassrls,
		       (SELECT count(*) FROM pg_tables t WHERE t.schemaname = current_schema() AND t.tableowner = r.rolname)
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&super, &bypass, &owned)
	if err != nil {
		return fmt.Errorf("store: inspect application role: %w", err)
	}
	switch {
	case super:
		return fmt.Errorf("%w: role is a superuser", ErrIsolationOff)
	case bypass:
		return fmt.Errorf("%w: role has BYPASSRLS", ErrIsolationOff)
	case owned > 0:
		return fmt.Errorf("%w: role owns %d tables in the schema", ErrIsolationOff, owned)
	}
	return nil
}

// ErrOwnerSuperuser is returned when the owner DSN's role is a superuser.
var ErrOwnerSuperuser = errors.New("store: owner role must not be a superuser")

// assertOwnerRole refuses a superuser as owner: the owner is meant to own the
// tables and nothing more, and the extension is the admin's job.
func assertOwnerRole(ctx context.Context, owner *pgxpool.Pool) error {
	var super bool
	if err := owner.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		return fmt.Errorf("store: inspect owner role: %w", err)
	}
	if super {
		return ErrOwnerSuperuser
	}
	return nil
}

// OpenWhenReady opens the store once the database answers. It retries until
// the database does, because in a fresh deployment Postgres is usually still
// bootstrapping when the pod starts. A configuration error (a DSN that
// can bypass row-level security, no vector extension) is returned at
// once: waiting does not change it.
func OpenWhenReady(ctx context.Context, opts Options) (*Store, error) {
	retry := tuning.Get().Store.OpenRetry
	for {
		st, err := Open(ctx, opts)
		if err == nil {
			return st, nil
		}
		if IsConfigurationError(err) || ctx.Err() != nil {
			return nil, err
		}
		opts.Logger.Warn("database not ready, retrying", "error", err, "after", retry)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retry):
		}
	}
}

// IsConfigurationError reports whether err is one of the startup assertions,
// as opposed to a database that is not reachable yet. No amount of retrying
// will fix a startup assertion.
func IsConfigurationError(err error) bool {
	return errors.Is(err, ErrIsolationOff) ||
		errors.Is(err, ErrNoVectorExtension) || errors.Is(err, ErrOwnerSuperuser)
}

// ErrNoVectorExtension is returned when VectorChord (vchord) or pgvector
// (vector, whose types it builds on) is absent.
var ErrNoVectorExtension = errors.New("store: the vchord and vector extensions are not both installed in this database")

func (s *Store) assertExtension(ctx context.Context) error {
	var missing []string
	if err := s.app.QueryRow(ctx, `SELECT array_agg(name ORDER BY name) FROM unnest(ARRAY['vchord', 'vector']) AS name
		WHERE NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = name)`).Scan(&missing); err != nil {
		return fmt.Errorf("store: inspect extensions: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w (missing: %s); use a VectorChord image with vchord in shared_preload_libraries, and on CloudNativePG "+
			"declare vector and vchord on the Database resource, elsewhere CREATE EXTENSION vchord CASCADE as a superuser",
			ErrNoVectorExtension, strings.Join(missing, ", "))
	}
	return nil
}

// WithAccount runs fn in a transaction on the application pool with the
// account set transaction-locally. So every policy resolves to that account
// and nothing survives on the pooled connection after commit or rollback.
func (s *Store) WithAccount(ctx context.Context, accountID string, fn func(pgx.Tx) error) error {
	return withAccount(ctx, s.app, pgx.TxOptions{}, accountID, fn)
}

// WithAccountSnapshot is WithAccount in a read-only, repeatable-read
// transaction: every statement of fn reads the snapshot that its first took,
// so reads that the caller joins see one version of the rows, whatever
// commits between them.
func (s *Store) WithAccountSnapshot(ctx context.Context, accountID string, fn func(pgx.Tx) error) error {
	return withAccount(ctx, s.app, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, accountID, fn)
}

// txBeginner is what withAccount begins its transaction on: the application
// pool, or a connection of the application role's own.
type txBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

func withAccount(ctx context.Context, db txBeginner, opts pgx.TxOptions, accountID string, fn func(pgx.Tx) error) error {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id', $1, true)`, accountID); err != nil {
		return fmt.Errorf("store: set account: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
