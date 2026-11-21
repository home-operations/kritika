package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/runspec"
)

// EnsureIndexSchema makes index_chunks match the instance's embedder. It
// creates the table at dims on first use and records model, dims and the
// document prompt in index_schema. When any differs from what is recorded,
// every generation is dropped with the table and the table is rebuilt. So
// the onboarding that follows indexes each repository afresh. rebuilt reports
// that. The dashboard asks the admin to confirm such a change before it is
// saved.
//
// It also records this build's index format, runspec.IndexFormat. A new
// format drops nothing: the generations of another format still answer
// searches while the onboarding feeder rebuilds them, a few at a time. A
// table that it keeps, it first brings up to this build's columns
// (prepareIndexChunks). Leader only.
func (s *Store) EnsureIndexSchema(
	ctx context.Context, appRole, model string, dims int, documentPrompt string,
) (rebuilt bool, err error) {
	if s.owner == nil {
		return false, errors.New("store: EnsureIndexSchema needs the owner connection")
	}
	if dims <= 0 || dims > configfile.MaxEmbedDims {
		return false, fmt.Errorf("store: embedding dimension %d is outside the index limit of %d", dims, configfile.MaxEmbedDims)
	}
	// Before the format is applied, which lets this build's index jobs
	// write the columns.
	if err := s.prepareIndexChunks(ctx, func(cur IndexSchema) bool {
		return cur.Model == model && cur.Dims == dims && cur.DocumentPrompt == documentPrompt
	}); err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
			return fmt.Errorf("store: lock index schema: %w", err)
		}
		cur, ok, err := IndexSchemaIn(ctx, tx)
		switch {
		case err != nil:
			return err
		case !ok:
			return createIndexChunks(ctx, tx, appRole, model, dims, documentPrompt)
		case cur.Model == model && cur.Dims == dims && cur.DocumentPrompt == documentPrompt:
			return s.applyIndexFormat(ctx, tx, cur.Format)
		}
		// No generation is valid for a different embedder, so detach every
		// repository and drop the vectors with the table. The table goes
		// before the postings, the order in which a search reads them and
		// generationTables deletes from them, so the rebuild waits for a
		// search between the two holding neither. The postings then go at
		// once, rather than a generation at a time as each is superseded
		// (migration 0069's trigger). By then the rebuild holds every
		// repository, which a publication holds before it writes postings,
		// so it waits for no publication.
		stmts := []string{
			`UPDATE repositories SET active_index_run_id = NULL`,
			`DROP TABLE IF EXISTS index_chunks`,
			`TRUNCATE index_postings`,
			`UPDATE index_runs SET status = 'superseded', finished_at = coalesce(finished_at, now()) WHERE status IN ('running', 'completed')`,
			`DELETE FROM index_schema WHERE id = 1`,
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("store: rebuild index schema: %w", err)
			}
		}
		rebuilt = true
		return createIndexChunks(ctx, tx, appRole, model, dims, documentPrompt)
	})
	return rebuilt && err == nil, err
}

// applyIndexFormat records this build's index format in index_schema when
// another is recorded.
func (s *Store) applyIndexFormat(ctx context.Context, tx pgx.Tx, current int) error {
	if current == runspec.IndexFormat {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE index_schema SET index_format = $1 WHERE id = 1`, runspec.IndexFormat); err != nil {
		return fmt.Errorf("store: record index format: %w", err)
	}
	s.logger.Info("index format applied: onboarding rebuilds the indexes of another format a few at a time",
		"format", runspec.IndexFormat, "previous", current)
	return nil
}

// IndexSchema is what index_chunks was built for: the embedding model,
// dimension and document prompt, and the index format that the leader's
// build applied.
type IndexSchema struct {
	Model          string
	Dims           int
	DocumentPrompt string
	Format         int
}

// IndexSchemaIn is the index schema, read in tx. ok is false when
// index_chunks was never built.
func IndexSchemaIn(ctx context.Context, tx pgx.Tx) (IndexSchema, bool, error) {
	var s IndexSchema
	err := tx.QueryRow(ctx, `SELECT embed_model, embed_dims, document_prompt, index_format FROM index_schema WHERE id = 1`).
		Scan(&s.Model, &s.Dims, &s.DocumentPrompt, &s.Format)
	if errors.Is(err, pgx.ErrNoRows) {
		return IndexSchema{}, false, nil
	}
	if err != nil {
		return IndexSchema{}, false, fmt.Errorf("store: read index schema: %w", err)
	}
	return s, true, nil
}

// contentHashIndexOn is the content hash's index, which finds the chunks of
// a repository that hold a content hash. It is partial: the chunks that have
// none, every chunk of a table that a build before content hashes created at
// first, cost it nothing.
const contentHashIndexOn = `index_chunks_content_hash_idx ON index_chunks (repository_id, content_hash)
	WHERE content_hash IS NOT NULL`

// contentHashIndex creates the content hash's index of a table created
// partitioned, as it is created. buildContentHashIndex builds it on a plain
// one.
const contentHashIndex = `CREATE INDEX IF NOT EXISTS ` + contentHashIndexOn

// addedColumns are the columns of index_chunks that migrations added, with
// their types. Of them, the content hash alone is indexed, by
// contentHashIndex. The lexemes are not: a search reads index_postings,
// which a publication writes from the lexemes of the chunks it adds and
// replaces.
var addedColumns = []struct{ name, typ string }{
	{"content_hash", "bytea"},
	{"lexemes", "tsvector"},
}

// addColumns adds each of addedColumns that index_chunks lacks. It looks
// first, as ALTER TABLE takes the table's exclusive lock even when the
// column exists.
func addColumns(ctx context.Context, tx pgx.Tx) error {
	for _, c := range addedColumns {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_attribute
			WHERE attrelid = 'index_chunks'::regclass AND attname = $1 AND NOT attisdropped)`, c.name).Scan(&exists); err != nil {
			return fmt.Errorf("store: read index_chunks columns: %w", err)
		}
		if exists {
			continue
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE index_chunks ADD COLUMN `+c.name+` `+c.typ); err != nil {
			return fmt.Errorf("store: add index_chunks.%s: %w", c.name, err)
		}
	}
	return nil
}

// prepareIndexChunks brings an index_chunks that a build before
// addedColumns created up to them. During a rollout, such a build can still
// be leader after the migrations ran and create the table again for a new
// embedder. keep, nil for any, says whether the table built for the recorded
// index schema stays: one that the caller drops instead is left as it is.
//
// Adding a column takes the table's exclusive lock, which every search
// queues behind, for a moment: it writes no row. It looks for each column
// first, and, as the migrations do, gives up its wait for the lock after a
// few seconds rather than hold up what queues behind it, and the caller
// tries again later. The content hash's index is left to
// buildContentHashIndex.
func (s *Store) prepareIndexChunks(ctx context.Context, keep func(IndexSchema) bool) error {
	return pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
			return fmt.Errorf("store: lock index schema: %w", err)
		}
		schema, ok, err := IndexSchemaIn(ctx, tx)
		if err != nil || !ok || keep != nil && !keep(schema) {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('index_chunks') IS NOT NULL`).Scan(&exists); err != nil || !exists {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
			return fmt.Errorf("store: prepare index_chunks: %w", err)
		}
		return addColumns(ctx, tx)
	})
}

// contentHashBuildLock is the advisory lock of the session that builds the
// content hash's index of a plain index_chunks (buildContentHashIndex).
const contentHashBuildLock = `hashtext('kritika-index-content-hash')`

// errContentHashBuildHeld says that another session holds
// contentHashBuildLock, as one of a leader that stopped can until it ends.
var errContentHashBuildHeld = errors.New("store: another session builds the content hash's index")

// buildContentHashIndex builds the content hash's index of a plain
// index_chunks that lacks a valid one, concurrently: it reads every chunk
// under a lock that conflicts with no search or write, nor makes either queue
// behind its wait. It waits instead for every transaction that writes the
// table, or that is older than the build, to end, which holds up no search or
// write either. So neither a lock_timeout, which would end it at any
// transaction that lasts a few seconds, nor the owner's statement_timeout
// bounds it. A build
// that ends early, as when ctx does, leaves its index invalid: the next
// drops it and builds it again. A table created partitioned has the index
// from the start.
//
// The build cannot run in a transaction, so it runs on a session of its own,
// closed once it is done, which holds contentHashBuildLock. It takes the lock
// at once or not at all: a session that waited for it would be a
// transaction older than the build that holds it, which that build waits
// for in turn.
func (s *Store) buildContentHashIndex(ctx context.Context) error {
	conn, err := s.owner.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: build the content hash's index: %w", err)
	}
	defer func() {
		// Closed, the session lets its lock and its settings go, and the
		// pool then drops it rather than reuse it.
		_ = conn.Conn().Close(ctx) // its end is all that is wanted of it
		conn.Release()
	}()
	var mine, plain, exists, valid bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+contentHashBuildLock+`),
		coalesce((SELECT relkind = 'r' FROM pg_class WHERE oid = to_regclass('index_chunks')), false),
		i.indexrelid IS NOT NULL, coalesce(i.indisvalid, false)
		FROM (SELECT 1) AS one LEFT JOIN pg_index i
			ON i.indexrelid = to_regclass('index_chunks_content_hash_idx') AND i.indrelid = to_regclass('index_chunks')`).
		Scan(&mine, &plain, &exists, &valid)
	switch {
	case err != nil:
		return fmt.Errorf("store: build the content hash's index: %w", err)
	case !mine:
		return errContentHashBuildHeld
	case !plain || valid:
		return nil
	}
	stmts := []string{`SET statement_timeout = 0`}
	if exists {
		stmts = append(stmts, `DROP INDEX CONCURRENTLY index_chunks_content_hash_idx`)
	}
	stmts = append(stmts, `CREATE INDEX CONCURRENTLY `+contentHashIndexOn)
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("store: build the content hash's index: %w", err)
		}
	}
	s.logger.Info("index_chunks_content_hash_idx built")
	return nil
}

// createIndexChunks creates index_chunks for the embedder. A new table holds
// no chunk, so no posting of one is left either: a rebuild for a new
// embedder supersedes every generation, and drops its chunks with the table.
func createIndexChunks(ctx context.Context, tx pgx.Tx, appRole, model string, dims int, documentPrompt string) error {
	if err := createPartitionedIndexChunks(ctx, tx, appRole, dims); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE index_postings`); err != nil {
		return fmt.Errorf("store: drop index postings: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO index_schema (id, embed_model, embed_dims, document_prompt, index_format)
		VALUES (1, $1, $2, $3, $4)`, model, dims, documentPrompt, runspec.IndexFormat); err != nil {
		return fmt.Errorf("store: record index schema: %w", err)
	}
	return nil
}

// SweepStoppedIndexes drops the index of every repository that did not run
// for longer than grace and returns how many it dropped. A repository runs
// while it is enabled, while its .kritika.yaml was last found not turning it
// off (see RecordRepoConfigOff), and while runs, given its account ID and
// traits, says that it does. Its clock, stopped_at, starts at the first
// sweep that finds it holding an index and not running. The first sweep that
// finds it running or holding none clears the clock.
//
// The repository is left with no active generation, which is marked
// superseded, so running it again onboards a fresh index. It runs on the
// owner connection, which row-level security does not restrict. Leader only.
//
// Only the repositories whose index is dropped are locked, and read again
// under the lock. So no webhook or index job waits on a repository that the
// sweep merely looked at while the chunks, which can be many, are deleted.
func (s *Store) SweepStoppedIndexes(
	ctx context.Context, grace time.Duration, runs func(accountID string, t configfile.RepoTraits) bool,
) (int64, error) {
	if s.owner == nil {
		return 0, errors.New("store: SweepStoppedIndexes needs the owner connection")
	}
	var dropped []string
	err := pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		// The index schema's lock, so a rebuild for a new embedder runs
		// before or after the sweep, not during it. The rebuild holds every
		// repository's row while its DROP TABLE index_chunks waits for an
		// exclusive lock on repositories. The FOR UPDATE below will otherwise
		// hold that lock while it waits for one of those rows.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('kritika-index-schema'))`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, account_id::text, enabled AND NOT repo_config_off, archived, fork, turned_on,
				active_index_run_id IS NOT NULL
			FROM repositories WHERE active_index_run_id IS NOT NULL OR stopped_at IS NOT NULL`)
		if err != nil {
			return err
		}
		var stopped, others []string
		var id, accountID string
		var enabled, indexed bool
		var t configfile.RepoTraits
		_, err = pgx.ForEachRow(rows, []any{&id, &accountID, &enabled, &t.Archived, &t.Fork, &t.TurnedOn, &indexed}, func() error {
			if indexed && (!enabled || !runs(accountID, t)) {
				stopped = append(stopped, id)
			} else {
				others = append(others, id)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET stopped_at = NULL WHERE id = ANY($1::uuid[]) AND stopped_at IS NOT NULL`,
			others); err != nil {
			return err
		}
		// The stopped ones are locked and read again under the lock, so one
		// turned on or reported running since the first read is seen so:
		// its clock is cleared, not started, and its index kept.
		rows, err = tx.Query(ctx, `SELECT id::text, account_id::text, enabled AND NOT repo_config_off, archived, fork, turned_on,
				active_index_run_id::text, stopped_at <= now() - make_interval(secs => $2)
			FROM repositories WHERE id = ANY($1::uuid[]) AND active_index_run_id IS NOT NULL FOR UPDATE`, stopped, grace.Seconds())
		if err != nil {
			return err
		}
		var resumed []string
		var run string
		var expired *bool
		stopped = stopped[:0]
		_, err = pgx.ForEachRow(rows, []any{&id, &accountID, &enabled, &t.Archived, &t.Fork, &t.TurnedOn, &run, &expired}, func() error {
			switch {
			case enabled && runs(accountID, t):
				resumed = append(resumed, id)
			case expired != nil && *expired:
				dropped = append(dropped, run)
			default:
				stopped = append(stopped, id)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET stopped_at = NULL WHERE id = ANY($1::uuid[])`, resumed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repositories SET stopped_at = now() WHERE id = ANY($1::uuid[]) AND stopped_at IS NULL`,
			stopped); err != nil {
			return err
		}
		if len(dropped) == 0 {
			return nil
		}
		for _, stmt := range []string{
			`UPDATE repositories SET active_index_run_id = NULL, stopped_at = NULL, updated_at = now() WHERE active_index_run_id = ANY($1::uuid[])`,
			`UPDATE index_runs SET status = 'superseded', finished_at = coalesce(finished_at, now()) WHERE id = ANY($1::uuid[])`,
			`DELETE FROM index_postings WHERE index_run_id = ANY($1::uuid[])`,
			`DELETE FROM index_chunks WHERE index_run_id = ANY($1::uuid[])`,
		} {
			if _, err := tx.Exec(ctx, stmt, dropped); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: sweep stopped indexes: %w", err)
	}
	return int64(len(dropped)), nil
}

// RepoRef names a repository and its account.
type RepoRef struct{ ID, AccountID string }

// liveIndexJob is an index job of a repository still queued or running,
// in River's own table: the states its unique key spans.
var liveIndexJob = `j.kind = 'index' AND j.state IN (` + jobs.LiveStatesSQL() + `)`

// OnboardingInFlight counts onboarding index jobs queued or running.
// Owner connection: it spans every account.
func (s *Store) OnboardingInFlight(ctx context.Context) (int, error) {
	if s.owner == nil {
		return 0, errors.New("store: OnboardingInFlight needs the owner connection")
	}
	var n int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM river_job j WHERE `+liveIndexJob+` AND j.args->>'trigger' = 'onboard'`).
		Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count onboarding jobs: %w", err)
	}
	return n, nil
}

// onboardCandidate holds for a repository, r, that OnboardCandidates can
// offer, whatever enabled says of it: one without an active generation, or
// whose generation another embedder or index format built, or a step of
// another format advanced. $1 is the retryAfter, in seconds.
// onboardActivity is when its pull requests last moved, which orders an
// account's candidates.
var onboardCandidate = `r.enabled AND r.turned_on IS DISTINCT FROM false
	AND (r.active_index_run_id IS NULL OR EXISTS (SELECT 1 FROM index_runs g, index_schema s
	    WHERE g.id = r.active_index_run_id AND ((g.embed_model, g.embed_dims, g.document_prompt, g.index_format)
	      IS DISTINCT FROM (s.embed_model, s.embed_dims, s.document_prompt, s.index_format) OR ` + steppedInAnotherFormat + `)))
	AND NOT EXISTS (SELECT 1 FROM river_job j WHERE ` + liveIndexJob + ` AND j.args->>'repository_id' = r.id::text)
	AND NOT EXISTS (SELECT 1 FROM river_job j WHERE j.kind = 'index' AND j.args->>'repository_id' = r.id::text
	                AND j.args->>'trigger' = 'onboard' AND j.finalized_at > now() - make_interval(secs => $1)
	                AND NOT EXISTS (SELECT 1 FROM index_runs ir WHERE ir.repository_id = r.id
	                                AND ir.status IN ('completed', 'superseded') AND ir.created_at >= j.created_at))`

const onboardActivity = `(SELECT max(p.updated_at) FROM pull_requests p WHERE p.repository_id = r.id)`

// BacklogRepo is a repository waiting for the onboarding feeder to offer
// its first index, and what the forge says of it.
type BacklogRepo struct {
	ID string
	configfile.RepoTraits
}

// OnboardBacklog lists the transaction's account's repositories that the
// onboarding feeder can offer, as OnboardCandidates selects them, in the
// order that it will offer them. The dashboard uses the list to say where
// each one waits. The caller drops those that the account's configuration
// leaves off, as OnboardCandidates' enabled does.
func OnboardBacklog(ctx context.Context, tx pgx.Tx, retryAfter time.Duration) ([]BacklogRepo, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.archived, r.fork, r.turned_on FROM repositories r
		WHERE `+onboardCandidate+`
		ORDER BY `+onboardActivity+` DESC NULLS LAST, r.created_at, r.id`, retryAfter.Seconds())
	if err != nil {
		return nil, fmt.Errorf("store: list onboarding backlog: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BacklogRepo, error) {
		var r BacklogRepo
		err := row.Scan(&r.ID, &r.Archived, &r.Fork, &r.TurnedOn)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: list onboarding backlog: %w", err)
	}
	return out, nil
}

// OnboardCandidates lists up to limit enabled repositories, of those that
// enabled also admits by account ID and traits. A listed repository's active
// index generation is missing, or another embedder or index format than
// index_schema records built it, or a step of another index format advanced
// it. A listed repository has no index job queued
// or running. It also has no onboarding job that finished within retryAfter
// without building an index: one that failed or was skipped will fail or be
// skipped again.
//
// A pod still on the previous configuration during a rollout can publish a
// generation of another embedder. The gateway never queries that generation,
// and a repository without pushes otherwise keeps it. Accounts take turns.
// Within an account, the repositories whose pull requests moved last come
// first, as the ones a review is likeliest to need soon. Owner connection: it
// spans every account.
func (s *Store) OnboardCandidates(
	ctx context.Context, limit int, retryAfter time.Duration, enabled func(accountID string, t configfile.RepoTraits) bool,
) ([]RepoRef, error) {
	if s.owner == nil {
		return nil, errors.New("store: OnboardCandidates needs the owner connection")
	}
	rows, err := s.owner.Query(ctx, `WITH candidates AS (
			SELECT r.id, r.account_id, r.archived, r.fork, r.turned_on, r.created_at, `+onboardActivity+` AS active
			FROM repositories r
			WHERE `+onboardCandidate+`
		)
		SELECT id, account_id, archived, fork, turned_on FROM (
			SELECT c.*, row_number() OVER (PARTITION BY account_id ORDER BY active DESC NULLS LAST, created_at, id) AS turn FROM candidates c
		) ranked
		ORDER BY turn, active DESC NULLS LAST, created_at, id`, retryAfter.Seconds())
	if err != nil {
		return nil, fmt.Errorf("store: list onboarding candidates: %w", err)
	}
	defer rows.Close()
	var refs []RepoRef
	for len(refs) < limit && rows.Next() {
		var r RepoRef
		var t configfile.RepoTraits
		if err := rows.Scan(&r.ID, &r.AccountID, &t.Archived, &t.Fork, &t.TurnedOn); err != nil {
			return nil, fmt.Errorf("store: list onboarding candidates: %w", err)
		}
		if enabled(r.AccountID, t) {
			refs = append(refs, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list onboarding candidates: %w", err)
	}
	return refs, nil
}
