package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// indexChunkPartitions is how many hash partitions of index_run_id hold the
// chunks. A search names one generation, so it reads that generation's
// partition and its vector index alone: about 1/64 of the table, rather than
// every account's and repository's chunks. The modulus is fixed once the
// table exists. Only a rebuild of the table, for a new embedder, applies
// another.
const indexChunkPartitions = 64

// createPartitionedIndexChunks creates index_chunks at dims, and what the
// application role may do with it. It is partitioned by the range of
// created_at. index_chunks_current holds the chunks created from from, a
// range bound, on, and is hash partitioned by index_run_id. A table
// partitioned from one of a build before partitioning also has
// index_chunks_legacy for the chunks created before from (see
// PartitionIndexChunks).
//
// Every partition has the vector index. Its access method is VectorChord's:
// it partitions and quantises rather than building a graph, so it builds fast
// and answers a filtered query in full. Its own partitioning (lists) is left
// off, as a hash partition stays far below the size at which VectorChord
// recommends it, and as it needs the vectors to exist when it is built.
//
// The application role is granted the partitioned table alone. Rows reach
// and leave the partitions through it, so its row-level security policy
// applies to them.
func createPartitionedIndexChunks(ctx context.Context, tx pgx.Tx, appRole string, dims int, from string) error {
	app := pgx.Identifier{appRole}.Sanitize()
	stmts := make([]string, 0, indexChunkPartitions+10)
	stmts = append(stmts,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS index_chunks (
			id            uuid        NOT NULL DEFAULT gen_random_uuid(),
			account_id     uuid        NOT NULL REFERENCES accounts (id),
			repository_id uuid        NOT NULL REFERENCES repositories (id),
			index_run_id  uuid        NOT NULL REFERENCES index_runs (id) ON DELETE CASCADE,
			path          text        NOT NULL,
			start_line    int         NOT NULL,
			end_line      int         NOT NULL,
			language      text        NOT NULL DEFAULT '',
			symbol        text        NOT NULL DEFAULT '',
			kind          text        NOT NULL DEFAULT '',
			scope         text        NOT NULL DEFAULT '',
			text          text        NOT NULL,
			embedding     halfvec(%d) NOT NULL,
			content_hash  bytea,
			lexemes       tsvector,
			created_at    timestamptz NOT NULL DEFAULT now()
		) PARTITION BY RANGE (created_at)`, dims),
		// A unique key must hold the partition keys, so the primary key is
		// the hash partitions' alone: the legacy partition keeps its own.
		`CREATE TABLE IF NOT EXISTS index_chunks_current PARTITION OF index_chunks (PRIMARY KEY (index_run_id, id))
			FOR VALUES FROM (`+from+`) TO (MAXVALUE) PARTITION BY HASH (index_run_id)`,
	)
	for i := range indexChunkPartitions {
		stmts = append(stmts, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS index_chunks_%02d PARTITION OF index_chunks_current
			FOR VALUES WITH (MODULUS %d, REMAINDER %d)`, i, indexChunkPartitions, i))
	}
	stmts = append(stmts,
		`CREATE INDEX IF NOT EXISTS index_chunks_run_path_idx ON index_chunks (index_run_id, path)`,
		contentHashIndex,
		lexemesIndex,
		`CREATE INDEX IF NOT EXISTS index_chunks_account_id_idx ON index_chunks (account_id)`,
		`CREATE INDEX IF NOT EXISTS index_chunks_embedding_idx ON index_chunks USING vchordrq (embedding halfvec_cosine_ops)`,
		`ALTER TABLE index_chunks ENABLE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS account_isolation ON index_chunks`,
		`CREATE POLICY account_isolation ON index_chunks TO `+app+`
			USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
			WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON index_chunks TO `+app,
	)
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("store: create index_chunks: %w", err)
		}
	}
	return nil
}

// legacyIndexChunksIndexes are the indexes of an index_chunks that a build
// before partitioning created, by the suffix of their names.
var legacyIndexChunksIndexes = []string{"pkey", "run_path_idx", "content_hash_idx", "lexemes_idx", "account_id_idx", "embedding_idx"}

// PartitionIndexChunks waits for locks that publications and embedding
// batches also take, in another order, so it can close a cycle with one of
// them. It is to give way, not the other transaction: yieldLocks bounds each
// of its waits to 1/lockYieldShare of deadlock_timeout. A transaction that
// waits on it began to wait after its first lock request, so its waits, four
// at most, end before that transaction runs the deadlock check, which would
// abort it. While it, or DropLegacyIndexChunks, waits for index_chunks, every
// search and build queues behind it, which the short waits also bound. Each
// tries partitionTries times, partitionRetryPause apart, and the next sweep
// tries again.
const (
	lockYieldShare      = 8
	partitionTries      = 5
	partitionRetryPause = time.Second
)

// yieldLocks sets the transaction's lock_timeout to 1/lockYieldShare of
// deadlock_timeout.
func yieldLocks(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout',
		greatest(1, extract(epoch FROM current_setting('deadlock_timeout')::interval) * 1000 / $1)::int || 'ms', true)`,
		lockYieldShare)
	return err
}

// lockNotAvailable reports whether err is a lock wait that lock_timeout ended.
func lockNotAvailable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "55P03"
}

// retryLocks runs fn again while it gives up a lock wait, partitionTries
// times at most.
func retryLocks(ctx context.Context, fn func() error) error {
	for try := 1; ; try++ {
		err := fn()
		if !lockNotAvailable(err) || try == partitionTries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(partitionRetryPause):
		}
	}
}

// PartitionIndexChunks makes an index_chunks that a build before
// partitioning created, a plain table, the legacy partition of a partitioned
// one, and reports whether it did. The legacy partition holds every chunk
// created until then, kept as it is. Its indexes and foreign keys become
// those of the partitioned table, so nothing is copied or built. Attaching it
// reads the table once, to check that every chunk is older than the bound,
// under the table's exclusive lock. Searches and builds wait for that scan,
// seconds for millions of chunks.
//
// New chunks go to the hash partitions. DrainLegacyIndexChunks moves the
// legacy partition's chunks there, or deletes those that no build needs, a
// slice at a time, and DropLegacyIndexChunks then drops it. Until then, a
// search reads the legacy partition's vector index too, which costs about
// what a vector search of the plain table did. Leader only.
func (s *Store) PartitionIndexChunks(ctx context.Context, appRole string) (bool, error) {
	if s.owner == nil {
		return false, errors.New("store: PartitionIndexChunks needs the owner connection")
	}
	var partitioned bool
	err := retryLocks(ctx, func() error {
		var err error
		partitioned, err = s.partitionIndexChunks(ctx, appRole)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: partition index_chunks: %w", err)
	}
	return partitioned, nil
}

func (s *Store) partitionIndexChunks(ctx context.Context, appRole string) (bool, error) {
	var partitioned bool
	err := pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
			return err
		}
		var kind *string
		if err := tx.QueryRow(ctx, `SELECT relkind::text FROM pg_class WHERE oid = to_regclass('index_chunks')`).Scan(&kind); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if kind == nil || *kind != "r" {
			return nil
		}
		schema, ok, err := IndexSchemaIn(ctx, tx)
		if err != nil || !ok {
			return err
		}
		started := time.Now()
		// index_chunks first: a transaction that writes it and then one of
		// the tables that its foreign keys reference, as an embedding batch
		// does, finishes before this one locks those. A publication takes
		// them the other way round, its repository and index_runs before
		// index_chunks, and this transaction gives way to it.
		if err := yieldLocks(ctx, tx); err != nil {
			return err
		}
		for _, stmt := range []string{
			`LOCK TABLE index_chunks IN ACCESS EXCLUSIVE MODE`,
			`LOCK TABLE accounts, repositories, index_runs IN SHARE ROW EXCLUSIVE MODE`,
		} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		if err := addColumns(ctx, tx); err != nil {
			return err
		}
		// The lock waited for every transaction that wrote a chunk, so each
		// one is older than the bound.
		var bound time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&bound); err != nil {
			return err
		}
		from := "'" + bound.UTC().Format(time.RFC3339Nano) + "'"
		stmts := []string{`ALTER TABLE index_chunks RENAME TO index_chunks_legacy`}
		for _, suffix := range legacyIndexChunksIndexes {
			stmts = append(stmts, `ALTER INDEX IF EXISTS index_chunks_`+suffix+` RENAME TO index_chunks_legacy_`+suffix)
		}
		stmts = append(stmts, `REVOKE ALL ON index_chunks_legacy FROM `+pgx.Identifier{appRole}.Sanitize())
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		if err := createPartitionedIndexChunks(ctx, tx, appRole, schema.Dims, from); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE index_chunks ATTACH PARTITION index_chunks_legacy
			FOR VALUES FROM (MINVALUE) TO (`+from+`)`); err != nil {
			return err
		}
		s.logger.Info("index_chunks partitioned: its chunks move to the hash partitions a repository at a time",
			"elapsed", time.Since(started))
		partitioned = true
		return nil
	})
	return partitioned, err
}

// legacyDrainSlice is how many chunks DrainLegacyIndexChunks moves or
// deletes at a time. It holds the repository's lock while it does, which
// bounds how long a publication or a webhook's write to the repository waits
// for it.
var legacyDrainSlice = 5000

// DrainLegacyIndexChunks empties index_chunks_legacy a slice at a time: up
// to legacyDrainSlice chunks of one generation, in a transaction of their
// own. An active generation's chunks go first, moved to the hash partitions,
// so that its searches read its partition alone. A generation is split
// between the two while it moves, which a search reads as one. Then the
// chunks of the other generations: moved when a build can still publish or
// resume them, deleted otherwise, as the next build's stray sweep would. It
// returns how many chunks it moved and how many it deleted: none once the
// legacy partition is empty, or when another holds the lock of every
// repository or chunk left there. Each slice takes the repository's lock, as a
// publication does, and reads the generation again under it, so no
// publication changes the generation while a slice moves it. Leader only.
func (s *Store) DrainLegacyIndexChunks(ctx context.Context) (moved, removed int64, err error) {
	if s.owner == nil {
		return 0, 0, errors.New("store: DrainLegacyIndexChunks needs the owner connection")
	}
	err = pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		// The index schema's lock, so a rebuild for a new embedder, which
		// drops the table, runs before or after the move.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('kritika-index-schema'))`); err != nil {
			return err
		}
		var legacy bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('index_chunks_legacy') IS NOT NULL`).Scan(&legacy); err != nil || !legacy {
			return err
		}
		run, keep, err := lockLegacyGeneration(ctx, tx)
		if err != nil || run == "" {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM index_chunks_legacy WHERE index_run_id = $1 LIMIT $2 FOR UPDATE SKIP LOCKED`,
			run, legacyDrainSlice)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[pgtype.UUID])
		if err != nil || len(ids) == 0 {
			return err
		}
		if !keep {
			tag, err := tx.Exec(ctx, `DELETE FROM index_chunks_legacy WHERE id = ANY($1)`, ids)
			removed = tag.RowsAffected()
			return err
		}
		// A new created_at routes the chunks out of the legacy partition.
		tag, err := tx.Exec(ctx, `UPDATE index_chunks SET created_at = statement_timestamp()
			WHERE index_run_id = $1 AND id = ANY($2) AND tableoid = 'index_chunks_legacy'::regclass`, run, ids)
		if err != nil {
			return err
		}
		var left bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM index_chunks_legacy WHERE id = ANY($1))`, ids).Scan(&left); err != nil {
			return err
		}
		if left {
			return errors.New("the database clock is behind the legacy partition's bound")
		}
		moved = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("store: drain legacy index chunks: %w", err)
	}
	return moved, removed, nil
}

// lockLegacyGeneration locks the repository of a generation that holds
// chunks in index_chunks_legacy, an active one when there is one, and
// returns it, "" for none. keep says that its chunks are moved rather than
// deleted: it is active, or a build can still publish or resume it. Only a
// publication makes a generation active, under the repository's lock.
func lockLegacyGeneration(ctx context.Context, tx pgx.Tx) (run string, keep bool, err error) {
	err = tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories r WHERE active_index_run_id IS NOT NULL
		AND EXISTS (SELECT 1 FROM index_chunks_legacy c WHERE c.index_run_id = r.active_index_run_id)
		LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&run)
	if err == nil {
		return run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `SELECT x.id::text, x.id IS NOT DISTINCT FROM r.active_index_run_id OR x.status = 'running' OR x.resume_key <> ''
		FROM index_runs x JOIN repositories r ON r.id = x.repository_id
		WHERE EXISTS (SELECT 1 FROM index_chunks_legacy c WHERE c.index_run_id = x.id)
		LIMIT 1 FOR UPDATE OF r SKIP LOCKED`).Scan(&run, &keep)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return run, keep, err
}

// DropLegacyIndexChunks drops index_chunks_legacy once DrainLegacyIndexChunks
// has emptied it, and reports whether it did. Searches then read the hash
// partition of their generation alone. Dropping it takes the exclusive lock
// of index_chunks, for which it gives way as PartitionIndexChunks does, but
// none on the tables that its foreign keys reference, which detaching it
// first would take. Leader only.
func (s *Store) DropLegacyIndexChunks(ctx context.Context) (bool, error) {
	if s.owner == nil {
		return false, errors.New("store: DropLegacyIndexChunks needs the owner connection")
	}
	var dropped bool
	err := retryLocks(ctx, func() error {
		dropped = false
		return pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
				return err
			}
			var legacy, empty bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass('index_chunks_legacy') IS NOT NULL`).Scan(&legacy); err != nil || !legacy {
				return err
			}
			// Read before the lock, so that the read does not hold up
			// searches. Every created_at that a write sets is past the
			// partition's bound, so no chunk enters it again.
			if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM index_chunks_legacy)`).Scan(&empty); err != nil || !empty {
				return err
			}
			if err := yieldLocks(ctx, tx); err != nil {
				return err
			}
			for _, stmt := range []string{`LOCK TABLE ONLY index_chunks IN ACCESS EXCLUSIVE MODE`, `DROP TABLE index_chunks_legacy`} {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					return err
				}
			}
			s.logger.Info("index_chunks_legacy dropped: every chunk is in the hash partitions")
			dropped = true
			return nil
		})
	})
	if err != nil {
		return false, fmt.Errorf("store: drop legacy index chunks: %w", err)
	}
	return dropped, nil
}
