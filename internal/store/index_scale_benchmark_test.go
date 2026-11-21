//go:build integration

package store

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/indexer"
	"github.com/home-operations/kritika/internal/lexical"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
)

// scaleRepo is one repository of the index scale benchmark: its chunks are
// drawn from a few of the corpus's topics, offset by a style of its own.
type scaleRepo struct {
	account, id, name, run string
	// runner is the runner run whose reported paths its searches leave out,
	// "" for none.
	runner string
	size   int
	topics []int
	style  []float32
	seed   uint64
}

// scaleCorpus is the synthetic, clustered corpus: topic centroids shared by
// every repository, and the repositories that draw on them.
type scaleCorpus struct {
	dims    int
	topics  [][]float32
	repos   []*scaleRepo
	targets []*scaleRepo
}

const (
	scaleTopics       = 512
	scaleRepoTopics   = 24
	scaleStyleNorm    = 0.5
	scaleNoiseNorm    = 0.7
	scaleRecallK      = 10
	scaleLoadWorkers  = 8
	scaleMaxRepoSize  = 100_000
	scaleMinRepoSize  = 100
	scaleMedianRepo   = 3_000
	scaleRepoSizeSkew = 1.2
	// scaleMinTarget is the smallest target repository. Each target fits in a
	// quarter of the table, so a table of fewer than four times as many rows
	// has none to search.
	scaleMinTarget = 1_000
)

func scaleEnvInt(b *testing.B, key string, def int) int {
	b.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		b.Fatalf("%s=%q is not a positive integer", key, v)
	}
	return n
}

func gaussian(rng *rand.Rand, dims int, norm float64) []float32 {
	v := make([]float32, dims)
	sd := norm / math.Sqrt(float64(dims))
	for i := range v {
		v[i] = float32(rng.NormFloat64() * sd)
	}
	return v
}

// newScaleCorpus lays out rows chunks: the targets, one repository of each
// size that fits in a quarter of the table, and other repositories, of a
// log-normal size, spread over the accounts until the table is full.
func newScaleCorpus(dims, rows int, accounts []string) *scaleCorpus {
	rng := rand.New(rand.NewPCG(2026, 11))
	c := &scaleCorpus{dims: dims}
	for range scaleTopics {
		c.topics = append(c.topics, gaussian(rng, dims, 1))
	}
	add := func(account string, size int) *scaleRepo {
		r := &scaleRepo{account: account, size: size, seed: rng.Uint64()}
		r.name = fmt.Sprintf("scale/repo-%d", len(c.repos))
		r.id = configfile.RepositoryID(account, r.name)
		r.style = gaussian(rng, dims, scaleStyleNorm)
		for range scaleRepoTopics {
			r.topics = append(r.topics, rng.IntN(scaleTopics))
		}
		c.repos = append(c.repos, r)
		return r
	}
	total := 0
	for _, size := range []int{scaleMinTarget, 10_000, 100_000} {
		if total+size > rows/4 {
			break
		}
		c.targets = append(c.targets, add(accounts[0], size))
		total += size
	}
	for i := 0; total < rows; i++ {
		size := int(math.Exp(math.Log(scaleMedianRepo) + scaleRepoSizeSkew*rng.NormFloat64()))
		size = min(max(size, scaleMinRepoSize), scaleMaxRepoSize, rows-total)
		add(accounts[i%len(accounts)], size)
		total += size
	}
	return c
}

// vector is the repository's chunk i: a topic of the repository, its style
// and noise. A query is a chunk past the repository's last, so it is drawn
// like the repository's own code without being one of its chunks.
func (c *scaleCorpus) vector(r *scaleRepo, i int) []float32 {
	rng := rand.New(rand.NewPCG(r.seed, uint64(i)))
	topic := c.topics[r.topics[rng.IntN(len(r.topics))]]
	v := gaussian(rng, c.dims, scaleNoiseNorm)
	for d := range v {
		v[d] += topic[d] + r.style[d]
	}
	return v
}

// halfvecBinary is pgvector's binary halfvec: the dimension, a reserved
// zero and the IEEE half-precision values, big-endian.
func halfvecBinary(v []float32) []byte {
	out := make([]byte, 4+2*len(v))
	binary.BigEndian.PutUint16(out, uint16(len(v)))
	for i, f := range v {
		binary.BigEndian.PutUint16(out[4+2*i:], halfBits(f))
	}
	return out
}

// halfBits rounds f to half precision. The corpus's values are far inside
// its normal range, so values too small for it flush to zero.
func halfBits(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xff) - 127 + 15
	mant := b & 0x7fffff
	switch {
	case exp >= 31:
		return sign | 0x7bff
	case exp <= 0:
		return sign
	}
	h := sign | uint16(exp)<<10 | uint16(mant>>13)
	if mant&0x1000 != 0 {
		h++
	}
	return h
}

func scaleUUID(b *testing.B, s string) pgtype.UUID {
	b.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		b.Fatal(err)
	}
	return u
}

// load writes the corpus into index_chunks with binary COPY on the owner
// connection, several repositories at a time.
func (c *scaleCorpus) load(b *testing.B, s *Store) {
	b.Helper()
	ctx := b.Context()
	started := time.Now()
	for _, r := range c.repos {
		if _, err := s.owner.Exec(ctx, `INSERT INTO repositories(id, account_id, name) VALUES($1, $2, $3)`, r.id, r.account, r.name); err != nil {
			b.Fatal(err)
		}
		if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs(account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES($1, $2, 'scale', 'scale-embed', $3, 'full', 'completed') RETURNING id`, r.account, r.id, c.dims).Scan(&r.run); err != nil {
			b.Fatal(err)
		}
		if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, r.id, r.run); err != nil {
			b.Fatal(err)
		}
	}
	text := strings.Repeat("func example() { return }\n", 8)
	var next int
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for range scaleLoadWorkers {
		g.Go(func() error {
			conn, err := s.owner.Acquire(gctx)
			if err != nil {
				return err
			}
			defer conn.Release()
			var oid uint32
			if err := conn.QueryRow(gctx, `SELECT 'halfvec'::regtype::oid`).Scan(&oid); err != nil {
				return err
			}
			conn.Conn().TypeMap().RegisterType(&pgtype.Type{Name: "halfvec", OID: oid, Codec: pgtype.ByteaCodec{}})
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(c.repos) {
					return nil
				}
				r := c.repos[i]
				account, repo, run := scaleUUID(b, r.account), scaleUUID(b, r.id), scaleUUID(b, r.run)
				chunk := 0
				if _, err := conn.CopyFrom(gctx, pgx.Identifier{"index_chunks"},
					[]string{"account_id", "repository_id", "index_run_id", "path", "start_line", "end_line", "language", "text", "embedding"},
					pgx.CopyFromFunc(func() ([]any, error) {
						if chunk == r.size {
							return nil, nil
						}
						row := []any{account, repo, run, fmt.Sprintf("%s/chunk-%d.go", r.name, chunk), int32(1), int32(8), "go", text,
							halfvecBinary(c.vector(r, chunk))}
						chunk++
						return row, nil
					})); err != nil {
					return fmt.Errorf("copy %s: %w", r.name, err)
				}
			}
		})
	}
	if err := g.Wait(); err != nil {
		b.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `VACUUM ANALYZE index_chunks`); err != nil {
		b.Fatal(err)
	}
	b.Logf("loaded %d repositories in %s", len(c.repos), time.Since(started).Round(time.Second))
}

// scaleLayout is a way to store and search index_chunks: what it builds
// over the loaded table, what it drops again, and the query it runs.
type scaleLayout struct {
	name string
	// setup builds the layout over the loaded, unindexed table.
	setup func(ctx context.Context, s *Store) error
	// teardown leaves the loaded, unindexed table again.
	teardown func(ctx context.Context, s *Store) error
	// query is the search; its parameters are those of similarChunksSQL,
	// then the repository.
	query string
	// session is run in the search's transaction before the search.
	session []string
}

// vectorLiteral is a query vector in a plan, which is too long to log.
var vectorLiteral = regexp.MustCompile(`'\[[^\]]*\]'`)

const scaleQueryRepo = `SELECT path, start_line, end_line, language, symbol, kind, scope, text, 1 - (embedding <=> $1::halfvec)
			FROM index_chunks c WHERE repository_id = $6 AND index_run_id = $2 AND NOT (path = ANY($3))
				AND NOT EXISTS (SELECT 1 FROM similar_exclusions e WHERE e.runner_run_id = $5::uuid AND md5(e.path) = md5(c.path) AND e.path = c.path)
			ORDER BY embedding <=> $1::halfvec LIMIT $4`

// scaleSearch runs layout's search for one query vector as the application
// does: as the application role, in the account's transaction, with
// VectorChord's prefilter.
func scaleSearch(ctx context.Context, s *Store, l scaleLayout, r *scaleRepo, q []float32, explain bool) ([]string, string, error) {
	var paths []string
	var plan string
	err := s.WithAccount(ctx, r.account, func(tx pgx.Tx) error {
		for _, stmt := range append([]string{`SET LOCAL vchordrq.prefilter = on`}, l.session...) {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		runner, _ := Exclusions{Run: r.runner}.args()
		args := []any{model.VectorLiteral(q), r.run, []string{}, scaleRecallK, runner, r.id}
		if !strings.Contains(l.query, "$6") {
			args = args[:5]
		}
		if explain {
			rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+l.query, args...)
			if err != nil {
				return err
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			plan = vectorLiteral.ReplaceAllString(strings.Join(lines, "\n"), "'[query]'")
		}
		rows, err := tx.Query(ctx, l.query, args...)
		if err != nil {
			return err
		}
		paths, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var h SimilarHit
			c := &h.Chunk
			err := row.Scan(&c.Path, &c.StartLine, &c.EndLine, &c.Language, &c.Symbol, &c.Kind, &c.Scope, &c.Text, &h.Similarity)
			return c.Path, err
		})
		return err
	})
	return paths, plan, err
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(math.Ceil(p*float64(len(sorted))))-1)]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// BenchmarkIndexScale measures similar-code search in a table holding many
// repositories, for a small, a medium and a large target repository, under
// each candidate layout of index_chunks. It reports p50 and p95 latency and
// recall@10 against an exact search of the same repository.
//
// It is opt-in: KRITIKA_INDEX_SCALE_ROWS is the table's row count, at least
// 4*scaleMinTarget so that it holds a target. Loading a million rows takes
// minutes and gigabytes of disk.
// KRITIKA_INDEX_SCALE_DIMS (1024), KRITIKA_INDEX_SCALE_QUERIES (50) and
// KRITIKA_INDEX_SCALE_LAYOUTS (scaleDefaultLayouts, comma-separated names
// from scaleLayouts) vary it. KRITIKA_INDEX_SCALE_EXCLUDE (none) is how many
// paths a runner run reports for each target's searches to leave out: up to
// half of them, and of the target's chunks, the target's own, the rest
// paths that no chunk has.
func BenchmarkIndexScale(b *testing.B) {
	if os.Getenv("KRITIKA_INDEX_SCALE_ROWS") == "" {
		b.Skip("KRITIKA_INDEX_SCALE_ROWS not set")
	}
	rows := scaleEnvInt(b, "KRITIKA_INDEX_SCALE_ROWS", 0)
	if rows < 4*scaleMinTarget {
		b.Fatalf("KRITIKA_INDEX_SCALE_ROWS=%d holds no target repository to search: it must be at least %d", rows, 4*scaleMinTarget)
	}
	s, corpus := loadIndexScale(b, rows, scaleEnvInt(b, "KRITIKA_INDEX_SCALE_DIMS", 1024))
	ctx := b.Context()
	if n := scaleEnvInt(b, "KRITIKA_INDEX_SCALE_EXCLUDE", 0); n > 0 {
		reportScaleExclusions(b, s, corpus, n)
	}
	qs, truth := scaleTruth(b, s, corpus, scaleEnvInt(b, "KRITIKA_INDEX_SCALE_QUERIES", 50))
	wanted := strings.Split(cmp.Or(os.Getenv("KRITIKA_INDEX_SCALE_LAYOUTS"), scaleDefaultLayouts), ",")
	for _, l := range scaleLayouts(rows) {
		if !slices.Contains(wanted, l.name) {
			continue
		}
		b.Run("layout="+l.name, func(b *testing.B) {
			if l.setup != nil {
				started := time.Now()
				if err := l.setup(ctx, s); err != nil {
					b.Fatal(err)
				}
				b.Logf("%s set up in %s", l.name, time.Since(started).Round(time.Second))
			}
			if l.teardown != nil {
				defer func() {
					if err := l.teardown(context.WithoutCancel(ctx), s); err != nil {
						b.Fatal(err)
					}
				}()
			}
			for _, r := range corpus.targets {
				b.Run(fmt.Sprintf("repo=%d", r.size), func(b *testing.B) {
					measureIndexScale(b, s, l, r, qs[r], truth[r])
				})
			}
		})
	}
}

// loadIndexScale loads the corpus into an index_chunks such as a build
// before partitioning created, with no vector index, and drops it with the
// corpus's repositories once the benchmark ends.
func loadIndexScale(b *testing.B, rows, dims int) (*Store, *scaleCorpus) {
	s := openStore(b)
	ctx := b.Context()
	var postgres, vchord string
	if err := s.owner.QueryRow(ctx, `SELECT version(), extversion FROM pg_extension WHERE extname = 'vchord'`).Scan(&postgres, &vchord); err != nil {
		b.Fatal(err)
	}
	b.Logf("database: %s; VectorChord %s", postgres, vchord)
	b.Setenv("VECTOR_SCALE_KEY", "fixture")
	names := make([]string, 8)
	for i := range names {
		names[i] = fmt.Sprintf("vector-scale-%d", i)
	}
	file := configfiletest.Load(b, fmt.Sprintf(`apps:
  scale:
    accounts: [%s]
    clientId: Iv1.scale
    privateKey: { env: VECTOR_SCALE_KEY }
    webhookSecret: { env: VECTOR_SCALE_KEY }
`, strings.Join(names, ", ")))
	if err := s.ApplyConfig(ctx, file); err != nil {
		b.Fatal(err)
	}
	accounts := make([]string, len(names))
	for i, n := range names {
		a, _ := file.Account(configfile.ForgeGitHub, n)
		accounts[i] = a.ID()
	}
	if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL; DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`); err != nil {
		b.Fatal(err)
	}
	createUnpartitionedIndexChunks(b, s, "scale-embed", dims, false, true)
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		for _, query := range []string{
			`UPDATE repositories SET active_index_run_id = NULL`,
			`DROP TABLE IF EXISTS index_chunks`,
			`DELETE FROM index_schema`,
			`DELETE FROM runner_runs WHERE account_id = ANY($1::uuid[])`,
			`DELETE FROM index_runs WHERE account_id = ANY($1::uuid[])`,
			`DELETE FROM repositories WHERE account_id = ANY($1::uuid[])`,
		} {
			var args []any
			if strings.Contains(query, "$1") {
				args = append(args, accounts)
			}
			if _, err := s.owner.Exec(ctx, query, args...); err != nil {
				b.Error(err)
			}
		}
	})
	corpus := newScaleCorpus(dims, rows, accounts)
	corpus.load(b, s)
	return s, corpus
}

// reportScaleExclusions has a runner run of each target report n paths for
// its searches to leave out, as a change of n paths does: up to half of them,
// and of the target's chunks, the target's own, the rest paths that no
// chunk has, so that each search still has chunks to find. The runs go with
// the corpus's accounts.
func reportScaleExclusions(b *testing.B, s *Store, corpus *scaleCorpus, n int) {
	ctx := b.Context()
	for _, r := range corpus.targets {
		if err := s.owner.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`,
			r.account).Scan(&r.runner); err != nil {
			b.Fatal(err)
		}
		own := min(n/2, r.size/2)
		if _, err := s.owner.Exec(ctx, `INSERT INTO similar_exclusions (runner_run_id, account_id, path)
			SELECT $1, $2, CASE WHEN i < $5 THEN $3 || '/chunk-' || i || '.go' ELSE 'moved/' || $3 || '/file-' || i || '.go' END
			FROM generate_series(0, $4 - 1) AS i`, r.runner, r.account, r.name, n, own); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := s.owner.Exec(ctx, `ANALYZE similar_exclusions`); err != nil {
		b.Fatal(err)
	}
	b.Logf("each target's runner run reported %d paths", n)
}

// scaleTruth draws the queries of each target repository and their exact
// answers: with no vector index, the generation's chunks are read through
// its B-tree and sorted by distance.
func scaleTruth(b *testing.B, s *Store, corpus *scaleCorpus, queries int) (qs map[*scaleRepo][][]float32, truth map[*scaleRepo][][]string) {
	ctx := b.Context()
	exact := scaleLayout{name: "exact", query: similarChunksSQL}
	qs, truth = map[*scaleRepo][][]float32{}, map[*scaleRepo][][]string{}
	for _, r := range corpus.targets {
		for i := range queries {
			q := corpus.vector(r, r.size+i)
			paths, plan, err := scaleSearch(ctx, s, exact, r, q, i == 0)
			if err != nil {
				b.Fatal(err)
			}
			if len(paths) != scaleRecallK {
				b.Fatalf("exact search of %s returned %d chunks", r.name, len(paths))
			}
			if i == 0 && strings.Contains(plan, "vchordrq") {
				b.Fatalf("exact search used a vector index:\n%s", plan)
			}
			qs[r] = append(qs[r], q)
			truth[r] = append(truth[r], paths)
		}
	}
	return qs, truth
}

// measureIndexScale runs every query of the target repository once to warm
// the cache and log the first plan, then times each and scores its answer.
func measureIndexScale(b *testing.B, s *Store, l scaleLayout, r *scaleRepo, qs [][]float32, truth [][]string) {
	ctx := b.Context()
	for i, q := range qs {
		if _, plan, err := scaleSearch(ctx, s, l, r, q, i == 0); err != nil {
			b.Fatal(err)
		} else if i == 0 {
			b.Logf("plan:\n%s", plan)
		}
	}
	var took []time.Duration
	var matched, total int
	for b.Loop() {
		for i, q := range qs {
			started := time.Now()
			paths, _, err := scaleSearch(ctx, s, l, r, q, false)
			took = append(took, time.Since(started))
			if err != nil {
				b.Fatal(err)
			}
			for _, p := range paths {
				if !strings.HasPrefix(p, r.name+"/") {
					b.Fatalf("search returned another repository's chunk %s", p)
				}
				if slices.Contains(truth[i], p) {
					matched++
				}
			}
			total += scaleRecallK
		}
	}
	slices.Sort(took)
	b.ReportMetric(ms(percentile(took, 0.5)), "p50-ms")
	b.ReportMetric(ms(percentile(took, 0.95)), "p95-ms")
	b.ReportMetric(float64(matched)/float64(total), "recall@10")
}

// scaleDefaultLayouts are the layouts that the benchmark compares unless
// KRITIKA_INDEX_SCALE_LAYOUTS names others: the table as a build before
// partitioning made it, an exact search, the table just partitioned with
// every chunk still in its legacy partition, and the table after the drain.
const scaleDefaultLayouts = "flat,exact,partitioned,drained"

// scaleLayouts are the layouts the benchmark can compare, for a table of
// rows, in the order they run. Each starts from the table that the one
// before leaves, but partitioned and drained convert the loaded table
// themselves when the layouts before them did not run. The ones before flat, which are alternatives that were not
// taken, start from and leave the loaded table with no vector index.
func scaleLayouts(rows int) []scaleLayout {
	exec := func(stmts ...string) func(context.Context, *Store) error {
		return func(ctx context.Context, s *Store) error {
			for _, stmt := range stmts {
				if _, err := s.owner.Exec(ctx, stmt); err != nil {
					return fmt.Errorf("%s: %w", stmt, err)
				}
			}
			return nil
		}
	}
	// inTx runs stmts in one transaction, so that a setup that fails part
	// way, as a copy of the corpus that runs out of disk does, leaves the
	// table it renamed under its own name. VACUUM cannot run in one, so the
	// layouts that run it use exec.
	inTx := func(stmts ...string) func(context.Context, *Store) error {
		return func(ctx context.Context, s *Store) error {
			return pgx.BeginFunc(ctx, s.owner, func(tx pgx.Tx) error {
				for _, stmt := range stmts {
					if _, err := tx.Exec(ctx, stmt); err != nil {
						return fmt.Errorf("%s: %w", stmt, err)
					}
				}
				return nil
			})
		}
	}
	// VectorChord 1.1.1 reports no heap tuples from an index build, which
	// leaves the table with no row count until it is analyzed again.
	analyze := `ANALYZE index_chunks`
	dropIndex := `DROP INDEX index_chunks_embedding_idx`

	// IVF: VectorChord's own partitioning, with lists as its documentation
	// recommends for the row count and probes a share of them.
	var layouts []scaleLayout
	lists := rows / 500
	if rows >= 2_000_000 {
		lists = int(4 * math.Sqrt(float64(rows)))
	}
	ivf := fmt.Sprintf(`CREATE INDEX index_chunks_embedding_idx ON index_chunks USING vchordrq (embedding halfvec_cosine_ops)
		WITH (options = $$
residual_quantization = true
[build.internal]
lists = [%d]
spherical_centroids = true
build_threads = 8
$$)`, lists)
	for _, pct := range []int{1, 5, 20} {
		layouts = append(layouts, scaleLayout{
			name: fmt.Sprintf("ivf-probes%dpct", pct), setup: exec(ivf, analyze), teardown: exec(dropIndex), query: similarChunksSQL,
			session: []string{fmt.Sprintf(`SET LOCAL vchordrq.probes = '%d'`, max(1, lists*pct/100))},
		})
	}
	// Hash partitions of the repository, which the search must then name,
	// in a copy of the table.
	stmts := make([]string, 0, indexChunkPartitions+9)
	stmts = append(stmts,
		`ALTER TABLE index_chunks RENAME TO index_chunks_flat`,
		`CREATE TABLE index_chunks (LIKE index_chunks_flat INCLUDING DEFAULTS) PARTITION BY HASH (repository_id)`,
	)
	for i := range indexChunkPartitions {
		stmts = append(stmts, fmt.Sprintf(`CREATE TABLE index_chunks_p%d PARTITION OF index_chunks FOR VALUES WITH (MODULUS %d, REMAINDER %d)`,
			i, indexChunkPartitions, i))
	}
	stmts = append(stmts,
		`INSERT INTO index_chunks SELECT * FROM index_chunks_flat`,
		`CREATE INDEX ON index_chunks (index_run_id, path)`,
		`CREATE INDEX ON index_chunks USING vchordrq (embedding halfvec_cosine_ops)`,
		`ALTER TABLE index_chunks ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY account_isolation ON index_chunks TO kritika_app
			USING (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)`,
		`GRANT SELECT ON index_chunks TO kritika_app`,
		analyze,
	)
	layouts = append(layouts, scaleLayout{
		name: "repo-hash", setup: inTx(stmts...), query: scaleQueryRepo,
		teardown: inTx(`DROP TABLE index_chunks`, `ALTER TABLE index_chunks_flat RENAME TO index_chunks`),
	})

	// partition converts the table, with its vector index as flat builds it
	// when flat did not run, as when a filter leaves flat out: partitioned and
	// drained measure the converted table whichever layouts run before them.
	flatIndex := `CREATE INDEX IF NOT EXISTS index_chunks_embedding_idx ON index_chunks USING vchordrq (embedding halfvec_cosine_ops)`
	partition := func(ctx context.Context, s *Store) error {
		var plain bool
		if err := s.owner.QueryRow(ctx, `SELECT relkind = 'r' FROM pg_class WHERE oid = 'index_chunks'::regclass`).Scan(&plain); err != nil {
			return err
		}
		if plain {
			if err := exec(flatIndex, analyze)(ctx, s); err != nil {
				return err
			}
		}
		if _, err := s.PartitionIndexChunks(ctx, "kritika_app"); err != nil {
			return err
		}
		return scaleLayoutIs(ctx, s, true)
	}

	return append(layouts,
		// The table as a build before partitioning made it, which the
		// layouts after it convert.
		scaleLayout{
			name: "flat", query: similarChunksSQL,
			setup: exec(flatIndex, analyze),
		},
		scaleLayout{name: "exact", query: similarChunksSQL, session: []string{`SET LOCAL vchordrq.enable_scan = off`}},
		scaleLayout{name: "partitioned", query: similarChunksSQL, setup: partition},
		scaleLayout{
			name: "drained", query: similarChunksSQL,
			setup: func(ctx context.Context, s *Store) error {
				if err := partition(ctx, s); err != nil {
					return err
				}
				for {
					moved, removed, err := s.DrainLegacyIndexChunks(ctx)
					if err != nil {
						return err
					}
					if moved+removed == 0 {
						break
					}
				}
				if _, err := s.DropLegacyIndexChunks(ctx); err != nil {
					return err
				}
				if err := scaleLayoutIs(ctx, s, false); err != nil {
					return err
				}
				// Autovacuum's work, once it catches up.
				return exec(`VACUUM ANALYZE index_chunks`)(ctx, s)
			},
		},
	)
}

// Shape of the lexical scale benchmark's synthetic code: a vocabulary of
// word parts drawn by a Zipf law, as names are, identifiers of one to three
// parts, and chunks and queries of so many lines of identifiers.
const (
	lexicalScaleParts       = 4000
	lexicalScaleLines       = 30
	lexicalScaleQueryLines  = 8
	lexicalScalePerLine     = 5
	lexicalScaleQueries     = 12
	lexicalScaleQueryTerms  = 32
	lexicalScalePerQuery    = 4
	lexicalScaleRequests    = 30
	lexicalScaleInsertBatch = 500
	lexicalScaleDims        = 16
)

// lexicalScaleText is n lines of synthetic code drawn by rng.
func lexicalScaleText(rng *rand.Rand, zipf *rand.Zipf, words []string, n int) string {
	var b strings.Builder
	for range n {
		for range lexicalScalePerLine {
			for p := range 1 + rng.IntN(3) {
				w := words[zipf.Uint64()]
				if p > 0 {
					w = strings.ToUpper(w[:1]) + w[1:]
				}
				b.WriteString(w)
			}
			b.WriteString("(x) ")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// lexicalScaleWords is the vocabulary: pronounceable words of two to four
// syllables, none a stop word.
func lexicalScaleWords(rng *rand.Rand) []string {
	syllables := strings.Fields("ka lo mi ser ta ven dor qui ran pel sto bri cul fen gar hol ix jun mar nod pra rek sil tor ul vor wex yal zen")
	seen := map[string]bool{}
	var out []string
	for len(out) < lexicalScaleParts {
		var b strings.Builder
		for range 2 + rng.IntN(3) {
			b.WriteString(syllables[rng.IntN(len(syllables))])
		}
		if w := b.String(); !seen[w] && len(lexical.Words(w)) == 1 {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// lexicalScaleSteps is how many incremental steps BenchmarkLexicalScale
// publishes, each of lexicalScaleStepPaths changed paths, after the
// searches.
const (
	lexicalScaleSteps     = 5
	lexicalScaleStepPaths = 10
)

// lexicalScaleCorpus is the chunks of a generation of size chunks and the
// texts of its queries. With tree, a repository's path, the chunks are those
// of its HEAD's tree, as an index build makes them, repeated under a prefix
// of each copy until there are size, and each query is lines of one of them.
// Without it, they are synthetic code.
func lexicalScaleCorpus(b testing.TB, rng *rand.Rand, tree string, size int) (chunks []StagedChunk, queries []string) {
	if tree == "" {
		words := lexicalScaleWords(rng)
		zipf := rand.NewZipf(rng, 1.1, 1, lexicalScaleParts-1)
		for i := range size {
			chunks = append(chunks, StagedChunk{Path: fmt.Sprintf("pkg%d/file%d.go", i/50, i), StartLine: 1,
				EndLine: lexicalScaleLines, Language: "go", Text: lexicalScaleText(rng, zipf, words, lexicalScaleLines)})
		}
		for range lexicalScaleRequests * lexicalScaleQueries {
			queries = append(queries, lexicalScaleText(rng, zipf, words, lexicalScaleQueryLines))
		}
		return chunks, queries
	}
	repo, err := git.PlainOpenWithOptions(tree, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		b.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		b.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		b.Fatal(err)
	}
	root, err := commit.Tree()
	if err != nil {
		b.Fatal(err)
	}
	built, _, _, err := indexer.Build(b.Context(), root, nil, configfile.DefaultIgnore, indexer.DefaultOptions)
	if err != nil {
		b.Fatal(err)
	}
	if len(built) == 0 {
		b.Fatalf("KRITIKA_LEXICAL_SCALE_TREE %s: its HEAD has no file that the indexer makes chunks of", tree)
	}
	for i := 0; len(chunks) < size; i++ {
		c := built[i%len(built)]
		path := c.Path
		if n := i / len(built); n > 0 {
			path = fmt.Sprintf("copy%d/%s", n, path)
		}
		chunks = append(chunks, StagedChunk{Path: path, Language: c.Language, Symbol: c.Symbol, Kind: c.Kind, Scope: c.Scope,
			Text: c.Text, StartLine: c.StartLine, EndLine: c.EndLine})
	}
	for range lexicalScaleRequests * lexicalScaleQueries {
		lines := strings.Split(built[rng.IntN(len(built))].Text, "\n")
		start := rng.IntN(max(1, len(lines)-lexicalScaleQueryLines))
		queries = append(queries, strings.Join(lines[start:min(len(lines), start+lexicalScaleQueryLines)], "\n"))
	}
	return chunks, queries
}

// BenchmarkLexicalScale measures the search by name, LexicalChunks, over a
// generation of each size of KRITIKA_LEXICAL_SCALE_CHUNKS (comma-separated
// chunk counts, e.g. 10000,100000), with requests of twelve queries of up to
// 32 terms each, as a review's similar-code stage sends. It reports the p50
// and p95 latency of a request, how long the chunks took to insert and the
// generation to publish, which writes its postings, the size of the postings
// and of the chunks with their indexes, and how long an incremental step of
// ten changed paths took to publish. KRITIKA_LEXICAL_SCALE_TREE, a
// repository's path, makes the chunks of its HEAD rather than synthetic
// code. KRITIKA_LEXICAL_SCALE_EXCLUDE is how many paths a runner run reports
// for the searches to leave out: up to half of them, and of the generation's
// paths, its own, the rest paths that no chunk has. It is opt-in: a hundred
// thousand chunks take minutes to load.
func BenchmarkLexicalScale(b *testing.B) {
	sizes := os.Getenv("KRITIKA_LEXICAL_SCALE_CHUNKS")
	if sizes == "" {
		b.Skip("KRITIKA_LEXICAL_SCALE_CHUNKS not set")
	}
	s := openStore(b)
	ctx := b.Context()
	b.Setenv("LEXICAL_SCALE_KEY", "fixture")
	file := configfiletest.Load(b, `apps:
  scale:
    accounts: [lexical-scale]
    clientId: Iv1.scale
    privateKey: { env: LEXICAL_SCALE_KEY }
    webhookSecret: { env: LEXICAL_SCALE_KEY }
`)
	if err := s.ApplyConfig(ctx, file); err != nil {
		b.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "lexical-scale")
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		for _, query := range []string{
			`UPDATE repositories SET active_index_run_id = NULL WHERE account_id = $1`,
			`DROP TABLE IF EXISTS index_chunks`,
			`DELETE FROM index_schema`,
			`TRUNCATE index_postings`,
			`DELETE FROM index_runs WHERE account_id = $1`,
			`DELETE FROM repositories WHERE account_id = $1`,
		} {
			var args []any
			if strings.Contains(query, "$1") {
				args = append(args, account.ID())
			}
			if _, err := s.owner.Exec(ctx, query, args...); err != nil {
				b.Error(err)
			}
		}
	})
	rng := rand.New(rand.NewPCG(2026, 11))
	vector := make([]float32, lexicalScaleDims)
	vector[0] = 1
	for field := range strings.SplitSeq(sizes, ",") {
		size, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || size <= 0 {
			b.Fatalf("KRITIKA_LEXICAL_SCALE_CHUNKS: %q is not a positive integer", field)
		}
		b.Run(fmt.Sprintf("chunks=%d", size), func(b *testing.B) {
			// A table of this generation alone, so that its size is the
			// generation's.
			if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL; DROP TABLE IF EXISTS index_chunks;
				DELETE FROM index_schema; TRUNCATE index_postings`); err != nil {
				b.Fatal(err)
			}
			if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "lexical-scale", lexicalScaleDims, ""); err != nil {
				b.Fatal(err)
			}
			chunks, texts := lexicalScaleCorpus(b, rng, os.Getenv("KRITIKA_LEXICAL_SCALE_TREE"), size)
			name := fmt.Sprintf("lexical-scale/repo-%d", size)
			repo := configfile.RepositoryID(account.ID(), name)
			newRun := func(mode string) string {
				var run string
				if err := s.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
					return tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
						index_format) VALUES ($1, $2, 'scale', 'lexical-scale', $3, $4, 'completed', $5) RETURNING id`,
						account.ID(), repo, lexicalScaleDims, mode, runspec.IndexFormat).Scan(&run)
				}); err != nil {
					b.Fatal(err)
				}
				return run
			}
			insert := func(run string, chunks []StagedChunk) {
				for start := 0; start < len(chunks); start += lexicalScaleInsertBatch {
					batch := chunks[start:min(len(chunks), start+lexicalScaleInsertBatch)]
					vectors := make([][]float32, len(batch))
					for i := range vectors {
						vectors[i] = vector
					}
					if err := s.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
						return InsertIndexChunks(ctx, tx, account.ID(), repo, run, batch, vectors)
					}); err != nil {
						b.Fatal(err)
					}
				}
			}
			if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repo, account.ID(), name); err != nil {
				b.Fatal(err)
			}
			run := newRun(IndexModeFull)
			started := time.Now()
			insert(run, chunks)
			inserted := time.Since(started)
			started = time.Now()
			if err := s.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error { return ActivateGeneration(ctx, tx, repo, run) }); err != nil {
				b.Fatal(err)
			}
			published := time.Since(started)
			if _, err := s.owner.Exec(ctx, `VACUUM ANALYZE index_chunks, index_postings`); err != nil {
				b.Fatal(err)
			}
			var postings, postingsBytes, chunkBytes int64
			if err := s.owner.QueryRow(ctx, `SELECT (SELECT coalesce(sum(chunks), 0) FROM index_postings WHERE index_run_id = $1),
					pg_total_relation_size('index_postings'),
					(SELECT sum(pg_total_relation_size(inhrelid)) FROM pg_inherits WHERE inhparent = 'index_chunks_current'::regclass)`,
				run).Scan(&postings, &postingsBytes, &chunkBytes); err != nil {
				b.Fatal(err)
			}
			requests := make([][]LexicalQuery, lexicalScaleRequests)
			for r := range requests {
				for q := range lexicalScaleQueries {
					w := lexical.Words(texts[r*lexicalScaleQueries+q])
					slices.SortStableFunc(w, func(a, b lexical.Word) int { return cmp.Compare(len(b.Lexeme), len(a.Lexeme)) })
					lq := LexicalQuery{Vector: vector}
					for _, t := range w[:min(len(w), lexicalScaleQueryTerms)] {
						lq.Terms = append(lq.Terms, LexicalTerm{Lexeme: t.Lexeme, Compound: t.Compound})
					}
					requests[r] = append(requests[r], lq)
				}
			}
			common := LexicalBar{Per: 100, Least: 20, Share: 2}
			exclude := lexicalScaleExclusions(b, s, account.ID(), chunks)
			var took []time.Duration
			var hits int
			for b.Loop() {
				for _, req := range requests {
					began := time.Now()
					if err := s.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
						h, err := LexicalChunks(ctx, tx, run, req, exclude, lexicalScalePerQuery, common, lexicalScalePerQuery)
						hits += len(h)
						return err
					}); err != nil {
						b.Fatal(err)
					}
					took = append(took, time.Since(began))
				}
			}
			slices.Sort(took)
			// Incremental steps, each of a few changed paths, whose every
			// chunk changes.
			commit := "scale"
			stepped := make([]time.Duration, 0, lexicalScaleSteps)
			paths := lexicalScalePaths(chunks)
			for i := range lexicalScaleSteps {
				// A generation can hold fewer paths than a step changes.
				rng.Shuffle(len(paths), func(x, y int) { paths[x], paths[y] = paths[y], paths[x] })
				changed := map[string]bool{}
				for _, p := range paths[:min(len(paths), lexicalScaleStepPaths)] {
					changed[p] = true
				}
				var batch []StagedChunk
				for _, c := range chunks {
					if changed[c.Path] {
						c.Text += fmt.Sprintf("\nfunc steppedHelper%d() {}\n", i)
						batch = append(batch, c)
					}
				}
				step := newRun(IndexModeIncremental)
				insert(step, batch)
				next := fmt.Sprintf("step-%d", i)
				began := time.Now()
				if err := s.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
					return AdvanceGeneration(ctx, tx, repo, Generation{ID: run, Commit: commit}, step, next, slices.Collect(maps.Keys(changed)))
				}); err != nil {
					b.Fatal(err)
				}
				stepped = append(stepped, time.Since(began))
				commit = next
			}
			slices.Sort(stepped)
			b.ReportMetric(ms(percentile(took, 0.5)), "p50-ms")
			b.ReportMetric(ms(percentile(took, 0.95)), "p95-ms")
			b.ReportMetric(float64(hits)/float64(len(took)), "hits/request")
			b.ReportMetric(inserted.Seconds(), "insert-s")
			b.ReportMetric(published.Seconds(), "publish-s")
			b.ReportMetric(ms(percentile(stepped, 0.5)), "step-ms")
			b.ReportMetric(float64(postings)/float64(size), "postings/chunk")
			b.ReportMetric(float64(postingsBytes)/1e6, "postings-MB")
			b.ReportMetric(float64(chunkBytes)/1e6, "chunks-MB")
		})
	}
}

// scaleLayoutIs checks that index_chunks is partitioned, with the legacy
// partition when legacy is set and without it otherwise, so that a layout
// that converts the table never measures the flat one under its name.
func scaleLayoutIs(ctx context.Context, s *Store, legacy bool) error {
	var kind string
	var hasLegacy bool
	if err := s.owner.QueryRow(ctx, `SELECT relkind::text, to_regclass('index_chunks_legacy') IS NOT NULL
		FROM pg_class WHERE oid = 'index_chunks'::regclass`).Scan(&kind, &hasLegacy); err != nil {
		return err
	}
	if kind != "p" || hasLegacy != legacy {
		return fmt.Errorf("index_chunks is of kind %q, with a legacy partition %t; want partitioned, with one %t", kind, hasLegacy, legacy)
	}
	return nil
}

// lexicalScalePaths is the distinct paths of chunks, sorted.
// lexicalScaleExclusions are the KRITIKA_LEXICAL_SCALE_EXCLUDE paths that a
// runner run of the account reports, none when it is unset: up to half of
// them, and of the chunks' paths, the chunks' own, the rest paths that no
// chunk has.
func lexicalScaleExclusions(b *testing.B, s *Store, account string, chunks []StagedChunk) Exclusions {
	n := scaleEnvInt(b, "KRITIKA_LEXICAL_SCALE_EXCLUDE", 0)
	if n == 0 {
		return Exclusions{}
	}
	own := lexicalScalePaths(chunks)
	reported := slices.Clone(own[:min(n/2, len(own)/2)])
	for i := len(reported); i < n; i++ {
		reported = append(reported, fmt.Sprintf("moved/file-%d.go", i))
	}
	return reportSimilarBenchmark(b, s, account, reported)
}

func lexicalScalePaths(chunks []StagedChunk) []string {
	paths := make([]string, 0, len(chunks))
	for _, c := range chunks {
		paths = append(paths, c.Path)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// TestScaleRepoHashSetupRollsBack: a repo-hash setup that fails part way,
// as one whose copy of the corpus runs out of disk does, leaves the loaded
// table as it was, under its own name, for the benchmark's cleanup to drop.
func TestScaleRepoHashSetupRollsBack(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL; DROP TABLE IF EXISTS index_chunks;
		DROP TABLE IF EXISTS index_chunks_flat; DELETE FROM index_schema`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DROP TABLE IF EXISTS index_chunks; DROP TABLE IF EXISTS index_chunks_flat;
			DROP TABLE IF EXISTS index_chunks_p1; DELETE FROM index_schema`)
	})
	createUnpartitionedIndexChunks(t, s, "test-embed", 8, false, true)
	// A table in the way of the second partition fails the setup after the
	// rename and the first partition.
	if _, err := s.owner.Exec(ctx, `CREATE TABLE index_chunks_p1 (id int)`); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(scaleLayouts(1000), func(l scaleLayout) bool { return l.name == "repo-hash" })
	if err := scaleLayouts(1000)[i].setup(ctx, s); err == nil {
		t.Fatal("the repo-hash setup succeeded beside a table in its way")
	}
	var loaded, flat bool
	if err := s.owner.QueryRow(ctx, `SELECT to_regclass('index_chunks') IS NOT NULL AND
		(SELECT relkind = 'r' FROM pg_class WHERE oid = to_regclass('index_chunks')), to_regclass('index_chunks_flat') IS NOT NULL`).
		Scan(&loaded, &flat); err != nil {
		t.Fatal(err)
	}
	if !loaded || flat {
		t.Fatalf("after the failed setup, the loaded table is under its own name: %v, and renamed: %v; want it as it was", loaded, flat)
	}
}
