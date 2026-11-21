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
// searches while the onboarding feeder rebuilds them, a few at a time.
// Leader only.
func (s *Store) EnsureIndexSchema(
	ctx context.Context, appRole, model string, dims int, documentPrompt string,
) (rebuilt bool, err error) {
	if s.owner == nil {
		return false, errors.New("store: EnsureIndexSchema needs the owner connection")
	}
	if dims <= 0 || dims > configfile.MaxEmbedDims {
		return false, fmt.Errorf("store: embedding dimension %d is outside the index limit of %d", dims, configfile.MaxEmbedDims)
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
			if err := addColumns(ctx, tx); err != nil {
				return err
			}
			return s.applyIndexFormat(ctx, tx, cur.Format)
		}
		// No generation is valid for a different embedder, so detach every
		// repository and drop the vectors with the table.
		stmts := []string{
			`UPDATE repositories SET active_index_run_id = NULL`,
			`UPDATE index_runs SET status = 'superseded', finished_at = coalesce(finished_at, now()) WHERE status IN ('running', 'completed')`,
			`DROP TABLE IF EXISTS index_chunks`,
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

// contentHashIndex finds the chunks of a repository that hold a content
// hash, as migration 0064 creates it.
const contentHashIndex = `CREATE INDEX IF NOT EXISTS index_chunks_content_hash_idx ON index_chunks (repository_id, content_hash)
	WHERE content_hash IS NOT NULL`

// addedColumns are the columns of index_chunks that migrations added, with
// their types and indexes, "" for none. The lexemes have none: a search
// reads index_postings, which a publication writes from the lexemes of the
// chunks it adds and replaces.
var addedColumns = []struct{ name, typ, index string }{
	{"content_hash", "bytea", contentHashIndex},
	{"lexemes", "tsvector", ""},
}

// addColumns adds each of addedColumns, and its index, to an index_chunks
// that a build before it created. During a rollout, such a build can still
// be leader after the migration ran and rebuild the table for a new
// embedder. It looks first, as ALTER TABLE takes the table's exclusive lock
// even when the column exists.
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
		for _, stmt := range []string{`ALTER TABLE index_chunks ADD COLUMN ` + c.name + ` ` + c.typ, c.index} {
			if stmt == "" {
				continue
			}
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("store: add index_chunks.%s: %w", c.name, err)
			}
		}
	}
	return nil
}

// createIndexChunks creates index_chunks for the embedder. A new table holds
// no chunk, so no posting of one is left either: a rebuild for a new
// embedder supersedes every generation, and drops its chunks with the table.
func createIndexChunks(ctx context.Context, tx pgx.Tx, appRole, model string, dims int, documentPrompt string) error {
	if err := createPartitionedIndexChunks(ctx, tx, appRole, dims, "MINVALUE"); err != nil {
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
