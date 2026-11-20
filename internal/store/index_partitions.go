package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
var legacyIndexChunksIndexes = []string{"pkey", "run_path_idx", "content_hash_idx", "account_id_idx", "embedding_idx"}

// partitionLockTimeout bounds each lock that PartitionIndexChunks waits for.
// While it waits for index_chunks, every search and build queues behind it,
// so it gives up rather than stall them, and the next sweep tries again.
const partitionLockTimeout = "5s"

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
// active generations' chunks there, a repository at a time. Until it does, a
// search reads the legacy partition's vector index too, which costs about
// what a vector search of the plain table did. Leader only.
func (s *Store) PartitionIndexChunks(ctx context.Context, appRole string) (bool, error) {
	if s.owner == nil {
		return false, errors.New("store: PartitionIndexChunks needs the owner connection")
	}
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
		// does, finishes before this one locks those.
		for _, stmt := range []string{
			`SET LOCAL lock_timeout = '` + partitionLockTimeout + `'`,
			`LOCK TABLE index_chunks IN ACCESS EXCLUSIVE MODE`,
			`LOCK TABLE accounts, repositories, index_runs IN SHARE ROW EXCLUSIVE MODE`,
		} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		if err := addContentHash(ctx, tx); err != nil {
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
	if err != nil {
		return false, fmt.Errorf("store: partition index_chunks: %w", err)
	}
	return partitioned, nil
}

// DrainLegacyIndexChunks moves one repository's active generation from
// index_chunks_legacy to the hash partitions, so that its searches read its
// partition alone. It returns how many chunks it moved: none once no active
// generation is left there, or when another holds the lock of every
// repository that has one. It takes the repository's lock, as a publication
// does, so no publication changes the generation while it moves. The chunks of
// other generations stay in the legacy partition until their generation is
// dropped. Leader only.
func (s *Store) DrainLegacyIndexChunks(ctx context.Context) (int64, error) {
	if s.owner == nil {
		return 0, errors.New("store: DrainLegacyIndexChunks needs the owner connection")
	}
	var moved int64
	err := pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
		// The index schema's lock, so a rebuild for a new embedder, which
		// drops the table, runs before or after the move.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('kritika-index-schema'))`); err != nil {
			return err
		}
		var legacy bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('index_chunks_legacy') IS NOT NULL`).Scan(&legacy); err != nil || !legacy {
			return err
		}
		var run string
		err := tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories r WHERE active_index_run_id IS NOT NULL
			AND EXISTS (SELECT 1 FROM index_chunks_legacy c WHERE c.index_run_id = r.active_index_run_id)
			LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&run)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// A new created_at routes the chunks out of the legacy partition.
		tag, err := tx.Exec(ctx, `UPDATE index_chunks SET created_at = statement_timestamp()
			WHERE index_run_id = $1 AND tableoid = 'index_chunks_legacy'::regclass`, run)
		if err != nil {
			return err
		}
		var left bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM index_chunks_legacy WHERE index_run_id = $1)`, run).Scan(&left); err != nil {
			return err
		}
		if left {
			return errors.New("the database clock is behind the legacy partition's bound")
		}
		moved = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: drain legacy index chunks: %w", err)
	}
	return moved, nil
}
