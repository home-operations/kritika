package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
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
// application role may do with it. It is partitioned by the list of legacy,
// which says whether a chunk is one that the table held when a build before
// partitioning created it. index_chunks_current holds every other chunk, and
// is hash partitioned by index_run_id. A table partitioned from one of a
// build before partitioning also has index_chunks_legacy for the chunks that
// it held then (see PartitionIndexChunks), until DropLegacyIndexChunks drops
// it. A chunk is written with legacy false, and only the drain
// (DrainLegacyIndexChunks) changes it, to move a chunk out of the legacy
// partition. The column routes the chunks rather than their created_at, so
// that a database clock that is behind, as after a failover to a server
// whose clock is, routes none to the wrong partition.
//
// Every partition has the vector index. Its access method is VectorChord's:
// it partitions and quantises rather than building a graph, so it builds fast
// and answers a filtered query in full. Its own partitioning (lists) is left
// off, as a hash partition stays far below the size at which VectorChord
// recommends it, and as it needs the vectors to exist when it is built.
//
// The application role is granted the partitioned table alone. Rows reach
// and leave the partitions through it, so its row-level security policy
// applies to them. A query of a partition itself applies the partition's
// policies, not index_chunks', so a partition holds no privilege of the
// application role, nor of every role, which the owner's default privileges
// can grant to a table as it is created. Each has row-level security too,
// with no policy, so a role that holds a partition's privileges through a
// role it is a member of still reads no row of it. Row-level security does
// not apply to TRUNCATE, though, so no table of the tree keeps a grant of it
// to any role but its owner (revokeChunksTruncate).
func createPartitionedIndexChunks(ctx context.Context, tx pgx.Tx, appRole string, dims int) error {
	if _, err := tx.Exec(ctx, strings.Join(partitionedIndexChunksDDL(appRole, dims), ";\n")); err != nil {
		return fmt.Errorf("store: create index_chunks: %w", err)
	}
	return nil
}

// partitionedIndexChunksDDL is what createPartitionedIndexChunks runs. With
// no arguments, pgx sends statements joined by semicolons in one request of
// the simple protocol, which runs them in order in the transaction, so the
// whole tree costs one round trip, not one for each of its statements.
func partitionedIndexChunksDDL(appRole string, dims int) []string {
	app := pgx.Identifier{appRole}.Sanitize()
	partitions := make([]string, 0, indexChunkPartitions+1)
	partitions = append(partitions, "index_chunks_current")
	stmts := make([]string, 0, 2*indexChunkPartitions+12)
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
			created_at    timestamptz NOT NULL DEFAULT now(),
			legacy        boolean     NOT NULL DEFAULT false
		) PARTITION BY LIST (legacy)`, dims),
		// A unique key must hold the partition keys, so the primary key is
		// the hash partitions' alone: the legacy partition keeps its own.
		`CREATE TABLE IF NOT EXISTS index_chunks_current PARTITION OF index_chunks (PRIMARY KEY (index_run_id, id))
			FOR VALUES IN (false) PARTITION BY HASH (index_run_id)`,
	)
	for i := range indexChunkPartitions {
		leaf := fmt.Sprintf("index_chunks_%02d", i)
		partitions = append(partitions, leaf)
		stmts = append(stmts, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF index_chunks_current
			FOR VALUES WITH (MODULUS %d, REMAINDER %d)`, leaf, indexChunkPartitions, i))
	}
	for _, partition := range partitions {
		stmts = append(stmts, `ALTER TABLE `+partition+` ENABLE ROW LEVEL SECURITY`)
	}
	stmts = append(stmts, `REVOKE ALL ON `+strings.Join(partitions, ", ")+` FROM PUBLIC, `+app)
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
		revokeChunksTruncate,
	)
	return stmts
}

// revokeChunksTruncate revokes TRUNCATE on every table of index_chunks' tree,
// and on index_chunks_legacy before it is attached, from every role but the
// table's owner. The owner's default privileges can grant it to a role that
// the application role is a member of, which a revoke from the application
// role leaves in place, and a truncate would empty every account's chunks.
const revokeChunksTruncate = `DO $$
DECLARE
	r record;
BEGIN
	FOR r IN SELECT DISTINCT c.oid::regclass AS rel, a.grantee FROM pg_class c, aclexplode(c.relacl) a
		WHERE c.oid IN (SELECT relid FROM pg_partition_tree('index_chunks') UNION ALL SELECT to_regclass('index_chunks_legacy'))
			AND a.privilege_type = 'TRUNCATE' AND a.grantee <> c.relowner
	LOOP
		EXECUTE format('REVOKE TRUNCATE ON %s FROM %s', r.rel,
			CASE WHEN r.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(r.grantee)) END);
	END LOOP;
END
$$`

// legacyIndexChunksIndexes are the indexes of an index_chunks that a build
// before partitioning created, by the suffix of their names.
var legacyIndexChunksIndexes = []string{"pkey", "run_path_idx", "content_hash_idx", "account_id_idx", "embedding_idx"}

// PartitionIndexChunks takes locks that publications and embedding batches
// also take, in another order, so it could close a cycle with one of them.
// It is to give way, not the other transaction. Once it holds index_chunks,
// it takes the tables that its foreign keys reference at once or gives up,
// rather than wait while it holds what a publication waits for: the
// publication's deadlock check, at a deadlock_timeout that its role can set
// shorter than the owner's, could find that cycle first and abort it. Its
// waits for index_chunks hold no lock that another waits for, but every
// search and build queues behind them, so yieldLocks bounds each to
// 1/tuning's Store.LockYieldShare of deadlock_timeout. DropLegacyIndexChunks takes
// index_chunks at once or gives up, but at its last try, and then waits for
// the legacy partition alone, which only the owner's own reads of it can
// hold. Each tries tuning's Store.PartitionTries times, tuning's Store.PartitionRetryPause apart, and the
// next sweep tries again.

// yieldLocks sets the transaction's lock_timeout to 1/tuning's Store.LockYieldShare of
// deadlock_timeout.
func yieldLocks(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout',
		greatest(1, extract(epoch FROM current_setting('deadlock_timeout')::interval) * 1000 / $1)::int || 'ms', true)`,
		tuning.Get().Store.LockYieldShare)
	return err
}

// lockNotAvailable reports whether err is a lock wait that lock_timeout ended.
func lockNotAvailable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "55P03"
}

// retryLocks runs fn again while it gives up a lock wait, tuning's Store.PartitionTries
// times at most.
func retryLocks(ctx context.Context, fn func() error) error {
	for try := 1; ; try++ {
		err := fn()
		if !lockNotAvailable(err) || try == tuning.Get().Store.PartitionTries {
			return err
		}
		if gaveWay != nil {
			gaveWay(err)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(tuning.Get().Store.PartitionRetryPause):
		}
	}
}

// gaveWay, when a test sets it, is called with the error each time
// retryLocks gives a lock wait up, before it pauses: a variable for the
// tests, which can then end what holds the lock at that point rather than
// after a guessed time.
var gaveWay func(error)

// PartitionIndexChunks makes an index_chunks that a build before
// partitioning created, a plain table, the legacy partition of a partitioned
// one, and reports whether it did. The legacy partition holds every chunk
// created until then, kept as it is. Its indexes and foreign keys become
// those of the partitioned table, so nothing is copied or built. Before
// that, it gains legacy, true for every chunk, and legacyMark, a CHECK that
// it is true, which proves the legacy partition's bound once validated
// (markLegacyIndexChunks). So attaching it under the table's exclusive lock
// reads no chunk: the CHECK's validation reads them all, before, under a
// lock that lets searches read the table and builds write it. A table that a
// build before this one's columns created gains them first, outside that lock
// too (prepareIndexChunks), and then the content hash's index, built
// concurrently (buildContentHashIndex).
//
// New chunks go to the hash partitions. DrainLegacyIndexChunks moves the
// legacy partition's chunks there, or deletes those that no build needs, a
// slice at a time, and DropLegacyIndexChunks then drops it, with legacyMark.
// Until then, a search reads the legacy partition's vector index too, which
// costs about what a vector search of the plain table did. Leader only.
func (s *Store) PartitionIndexChunks(ctx context.Context, appRole string) (bool, error) {
	if s.owner == nil {
		return false, errors.New("store: PartitionIndexChunks needs the owner connection")
	}
	// So that the conversion, under its exclusive lock, adds no column,
	// attaches the content hash's index rather than build one, and reads no
	// chunk to attach the table.
	if err := s.prepareIndexChunks(ctx, nil); err != nil {
		return false, fmt.Errorf("store: partition index_chunks: %w", err)
	}
	if err := s.buildContentHashIndex(ctx); err != nil {
		return false, fmt.Errorf("store: partition index_chunks: %w", err)
	}
	if err := s.markLegacyIndexChunks(ctx); err != nil {
		return false, fmt.Errorf("store: partition index_chunks: %w", err)
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
		if plain, err := plainIndexChunks(ctx, tx); err != nil || !plain {
			return err
		}
		schema, ok, err := IndexSchemaIn(ctx, tx)
		if err != nil || !ok {
			return err
		}
		var marked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint
			WHERE conrelid = 'index_chunks'::regclass AND conname = $1 AND convalidated)`, legacyMark).Scan(&marked); err != nil {
			return err
		}
		if !marked {
			return errLegacyUnmarked
		}
		started := time.Now()
		// index_chunks first: a transaction that writes it and then one of
		// the tables that its foreign keys reference, as an embedding batch
		// does, finishes before this one locks those. A publication takes
		// them the other way round, its repository and index_runs before
		// index_chunks, and this transaction gives way to it, taking those
		// at once or not at all.
		if err := yieldLocks(ctx, tx); err != nil {
			return err
		}
		// From here every search and build waits, so each request is a round
		// trip that they wait for too: the locks are one, the constraints'
		// names another, and the conversion, every statement of it, a third.
		if _, err := tx.Exec(ctx, `LOCK TABLE index_chunks IN ACCESS EXCLUSIVE MODE;
			LOCK TABLE accounts, repositories, index_runs IN SHARE ROW EXCLUSIVE MODE NOWAIT`); err != nil {
			return err
		}
		// The lock waited for every transaction that wrote a chunk, so the
		// table holds each one written before the partitioned table exists.
		constraints, err := renameLegacyConstraints(ctx, tx)
		if err != nil {
			return err
		}
		stmts := []string{`ALTER TABLE index_chunks RENAME TO index_chunks_legacy`}
		for _, suffix := range legacyIndexChunksIndexes {
			stmts = append(stmts, `ALTER INDEX IF EXISTS index_chunks_`+suffix+` RENAME TO index_chunks_legacy_`+suffix)
		}
		stmts = append(stmts, `REVOKE ALL ON index_chunks_legacy FROM PUBLIC, `+pgx.Identifier{appRole}.Sanitize())
		stmts = append(stmts, constraints...)
		stmts = append(stmts, partitionedIndexChunksDDL(appRole, schema.Dims)...)
		stmts = append(stmts, `ALTER TABLE index_chunks ATTACH PARTITION index_chunks_legacy FOR VALUES IN (true)`)
		if _, err := tx.Exec(ctx, strings.Join(stmts, ";\n")); err != nil {
			return err
		}
		s.logger.Info("index_chunks partitioned: its chunks move to the hash partitions a repository at a time",
			"elapsed", time.Since(started))
		partitioned = true
		return nil
	})
	return partitioned, err
}

// plainIndexChunks reports whether index_chunks is a plain table, as a build
// before partitioning created it.
func plainIndexChunks(ctx context.Context, tx pgx.Tx) (bool, error) {
	var plain bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT relkind = 'r' FROM pg_class WHERE oid = to_regclass('index_chunks')), false)`).
		Scan(&plain)
	return plain, err
}

// legacyMark is the CHECK constraint that every chunk of a plain index_chunks
// is legacy. Validated, it proves the bound of the legacy partition that the
// table becomes, so attaching it reads no chunk. The partition keeps it until
// it is dropped. It is named, as legacy's NOT NULL constraint is, as the
// legacy partition's constraints are, which renameLegacyConstraints leaves
// as they are, so that neither takes a name that the partitioned table's
// would.
const legacyMark = "index_chunks_legacy_legacy_check"

// errLegacyUnmarked says that a plain index_chunks lacks a validated
// legacyMark, as when a build before partitioning created it again after
// markLegacyIndexChunks. The next sweep marks it.
var errLegacyUnmarked = errors.New("store: index_chunks has no validated " + legacyMark)

// markLegacyIndexChunks readies a plain index_chunks to be attached as the
// legacy partition, in two transactions. The first adds legacy, true for
// every chunk, which writes no row, and legacyMark, NOT VALID, which reads
// none: it holds the table's exclusive lock for a moment, and gives way as
// the conversion does, as every search queues behind its wait. The second
// validates legacyMark, which reads every chunk under a lock that lets
// searches read the table and builds write it, while a chunk that a build
// writes meanwhile is checked as it is written. Each looks for what it adds
// first, so a try after the first committed only validates.
func (s *Store) markLegacyIndexChunks(ctx context.Context) error {
	for _, step := range []func(context.Context, pgx.Tx) error{addLegacyMark, validateLegacyMark} {
		if err := retryLocks(ctx, func() error {
			return pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
					return err
				}
				if plain, err := plainIndexChunks(ctx, tx); err != nil || !plain {
					return err
				}
				if err := yieldLocks(ctx, tx); err != nil {
					return err
				}
				return step(ctx, tx)
			})
		}); err != nil {
			return fmt.Errorf("store: mark legacy index chunks: %w", err)
		}
	}
	return nil
}

// addLegacyMark adds legacy and legacyMark to the plain index_chunks unless
// it holds them. It looks first, as ALTER TABLE takes the table's exclusive
// lock even when they exist.
func addLegacyMark(ctx context.Context, tx pgx.Tx) error {
	var column, check bool
	if err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'index_chunks'::regclass AND attname = 'legacy' AND NOT attisdropped),
			EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'index_chunks'::regclass AND conname = $1)`, legacyMark).
		Scan(&column, &check); err != nil {
		return err
	}
	var adds []string
	if !column {
		adds = append(adds, `ADD COLUMN legacy boolean CONSTRAINT index_chunks_legacy_legacy_not_null NOT NULL DEFAULT true`)
	}
	if !check {
		adds = append(adds, `ADD CONSTRAINT `+legacyMark+` CHECK (legacy) NOT VALID`)
	}
	if len(adds) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `ALTER TABLE index_chunks `+strings.Join(adds, ", "))
	return err
}

// validateLegacyMark validates legacyMark unless it is.
func validateLegacyMark(ctx context.Context, tx pgx.Tx) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid = 'index_chunks'::regclass AND conname = $1`,
		legacyMark).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return errLegacyUnmarked
	}
	if err != nil || valid {
		return err
	}
	_, err = tx.Exec(ctx, `ALTER TABLE index_chunks VALIDATE CONSTRAINT `+legacyMark)
	return err
}

// renameLegacyConstraints returns the statements that give the constraints
// of the plain index_chunks, its foreign keys and, from Postgres 18, its NOT
// NULL constraints, the index_chunks_legacy_ prefix, once the table is
// renamed index_chunks_legacy. Postgres names a new table's constraints
// after the table and makes each name unique within the schema, so with
// the old names in place the partitioned table's would take a suffix
// ("index_chunks_account_id_fkey1") and keep it once the legacy partition
// is dropped, unlike a table created partitioned.
func renameLegacyConstraints(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'index_chunks'::regclass AND contype <> 'p'
		AND conname LIKE 'index\_chunks\_%' AND conname NOT LIKE 'index\_chunks\_legacy\_%'`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	stmts := make([]string, 0, len(names))
	for _, name := range names {
		renamed := "index_chunks_legacy_" + strings.TrimPrefix(name, "index_chunks_")
		stmts = append(stmts, `ALTER TABLE index_chunks_legacy RENAME CONSTRAINT `+
			pgx.Identifier{name}.Sanitize()+` TO `+pgx.Identifier{renamed}.Sanitize())
	}
	return stmts, nil
}

// DrainLegacyIndexChunks empties index_chunks_legacy a slice at a time: up
// to tuning's Store.LegacyDrainSlice chunks of one generation, in a transaction of their
// own. An active generation's chunks go first, moved to the hash partitions,
// so that its searches read its partition alone. A generation is split
// between the two while it moves, which a search reads as one. Then the
// chunks of the other generations: moved when a build can still publish or
// resume them, deleted otherwise, as the next build's stray sweep would. It
// returns how many chunks it moved and how many it deleted: none once the
// legacy partition is empty, or when another holds the lock of every
// repository or chunk left there, or of the job of the build whose chunks
// it would move. A generation that another holds so is passed over for the
// next, each tried under a savepoint of its own, whose rollback lets go of
// the locks the try took. Each slice takes the repository's lock, as a
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
		// Not nil, which would be NULL and pass over every generation.
		passed := []string{}
		for {
			var run string
			err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
				var keep bool
				var err error
				if run, keep, err = lockLegacyGeneration(ctx, sp, passed); err != nil || run == "" {
					return err
				}
				moved, removed, err = drainLegacySlice(ctx, sp, run, keep)
				if err == nil && moved+removed == 0 {
					return errLegacyHeld
				}
				return err
			})
			if !errors.Is(err, errLegacyHeld) {
				return err
			}
			passed = append(passed, run)
		}
	})
	if err != nil {
		return 0, 0, fmt.Errorf("store: drain legacy index chunks: %w", err)
	}
	return moved, removed, nil
}

// errLegacyHeld says that another holds what a slice of a generation's
// legacy chunks needs: the build jobs of the generation, or every chunk of it
// left in index_chunks_legacy.
var errLegacyHeld = errors.New("store: the generation's legacy chunks are held")

// drainLegacySlice moves, or deletes when keep does not hold, up to
// tuning's Store.LegacyDrainSlice of run's chunks in index_chunks_legacy that another does
// not hold, under the locks that lockLegacyGeneration took.
func drainLegacySlice(ctx context.Context, tx pgx.Tx, run string, keep bool) (moved, removed int64, err error) {
	rows, err := tx.Query(ctx, `SELECT id FROM index_chunks_legacy WHERE index_run_id = $1 LIMIT $2 FOR UPDATE SKIP LOCKED`,
		run, tuning.Get().Store.LegacyDrainSlice)
	if err != nil {
		return 0, 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[pgtype.UUID])
	if err != nil || len(ids) == 0 {
		return 0, 0, err
	}
	if !keep {
		// No build will publish or resume the generation, so none of its
		// postings is needed, wherever its chunks are.
		if _, err := tx.Exec(ctx, `DELETE FROM index_postings WHERE index_run_id = $1`, run); err != nil {
			return 0, 0, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM index_chunks_legacy WHERE id = ANY($1)`, ids)
		return 0, tag.RowsAffected(), err
	}
	// legacy false routes the chunks out of the legacy partition, and leaves
	// the rest of each as it was.
	tag, err := tx.Exec(ctx, `UPDATE index_chunks SET legacy = false WHERE index_run_id = $1 AND id = ANY($2) AND legacy`, run, ids)
	if err != nil {
		return 0, 0, err
	}
	return tag.RowsAffected(), 0, nil
}

// lockLegacyGeneration locks the repository of a generation that holds
// chunks in index_chunks_legacy, other than those passed, an active one when
// there is one, and returns it, "" for none. keep says that its chunks are
// moved rather than deleted: it is active, or a build can still publish or
// resume it. One that is kept but not active has its build jobs locked too,
// or errLegacyHeld while another holds one. Only a publication makes a
// generation active, under the repository's lock.
func lockLegacyGeneration(ctx context.Context, tx pgx.Tx, passed []string) (run string, keep bool, err error) {
	err = tx.QueryRow(ctx, `SELECT active_index_run_id::text FROM repositories r WHERE active_index_run_id IS NOT NULL
		AND active_index_run_id <> ALL($1::uuid[])
		AND EXISTS (SELECT 1 FROM index_chunks_legacy c WHERE c.index_run_id = r.active_index_run_id)
		LIMIT 1 FOR UPDATE SKIP LOCKED`, passed).Scan(&run)
	if err == nil {
		return run, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `SELECT x.id::text, x.id IS NOT DISTINCT FROM r.active_index_run_id OR x.status = 'running' OR x.resume_key <> ''
		FROM index_runs x JOIN repositories r ON r.id = x.repository_id
		WHERE x.id <> ALL($1::uuid[]) AND EXISTS (SELECT 1 FROM index_chunks_legacy c WHERE c.index_run_id = x.id)
		LIMIT 1 FOR UPDATE OF r SKIP LOCKED`, passed).Scan(&run, &keep)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil || !keep {
		return run, keep, err
	}
	// The job of a build that lost its generation to another deletes the
	// generation's chunks holding the job's lock, not the repository's, as
	// does the sweep of a finished job's progress. A delete that reaches a
	// chunk that this moves fails once the move commits (40001), so the move
	// holds the generation's jobs too, and leaves it for later while another
	// holds one.
	var jobs, held int
	err = tx.QueryRow(ctx, `WITH j AS (SELECT id FROM river_job
			WHERE id IN (SELECT river_job_id FROM runner_runs WHERE index_run_id = $1)),
		l AS (SELECT id FROM river_job WHERE id IN (SELECT id FROM j) FOR UPDATE SKIP LOCKED)
		SELECT (SELECT count(*) FROM j), (SELECT count(*) FROM l)`, run).Scan(&jobs, &held)
	if err != nil {
		return "", false, err
	}
	if held < jobs {
		return run, keep, errLegacyHeld
	}
	return run, keep, nil
}

// DropLegacyIndexChunks drops index_chunks_legacy once DrainLegacyIndexChunks
// has emptied it, and reports whether it did. Searches then read the hash
// partition of their generation alone, and the table is laid out as one
// created partitioned is: index_chunks_current holds every chunk that is not
// legacy, and no chunk is written legacy.
//
// It reads the partition before it asks for an exclusive lock, under the
// index schema's lock and the partition's ACCESS SHARE lock alone, so that
// one that still holds chunks, or the dead rows of those that the drain
// moved, which VACUUM has not cleaned, holds up no search. Nothing moves a
// chunk into the partition, so one read empty stays so. It then takes
// index_chunks' exclusive lock at once or not at all, rather than queue
// every search behind its wait, and drops the partition, giving way as
// PartitionIndexChunks does. Searches and builds that overlap without a
// pause would keep that lock from it however often it tries, so its last
// try waits for the lock, as the conversion did, a wait that yieldLocks
// bounds. That takes no lock of the tables that the partition's foreign
// keys reference, which are the partitioned table's. Detaching the
// partition concurrently would not wait for index_chunks, but the detached
// table would own its foreign keys, and dropping it would then take an
// exclusive lock of accounts, repositories and index_runs, which blocks
// even their reads. Leader only.
func (s *Store) DropLegacyIndexChunks(ctx context.Context) (bool, error) {
	if s.owner == nil {
		return false, errors.New("store: DropLegacyIndexChunks needs the owner connection")
	}
	var dropped bool
	try := 0
	err := retryLocks(ctx, func() error {
		dropped = false
		try++
		lock := `LOCK TABLE ONLY index_chunks IN ACCESS EXCLUSIVE MODE NOWAIT`
		if try == tuning.Get().Store.PartitionTries {
			lock = `LOCK TABLE ONLY index_chunks IN ACCESS EXCLUSIVE MODE`
		}
		return pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('kritika-index-schema'))`); err != nil {
				return err
			}
			var legacy, empty bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass('index_chunks_legacy') IS NOT NULL`).Scan(&legacy); err != nil || !legacy {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM index_chunks_legacy)`).Scan(&empty); err != nil || !empty {
				return err
			}
			if err := yieldLocks(ctx, tx); err != nil {
				return err
			}
			for _, stmt := range []string{lock, `DROP TABLE index_chunks_legacy`} {
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
