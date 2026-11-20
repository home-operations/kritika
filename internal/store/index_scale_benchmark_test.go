//go:build integration

package store

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/model"
)

// scaleRepo is one repository of the index scale benchmark: its chunks are
// drawn from a few of the corpus's topics, offset by a style of its own.
type scaleRepo struct {
	account, id, name, run string
	size                   int
	topics                 []int
	style                  []float32
	seed                   uint64
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
	for _, size := range []int{1_000, 10_000, 100_000} {
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
			FROM index_chunks WHERE repository_id = $5 AND index_run_id = $2 AND NOT (path = ANY($3))
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
		args := []any{model.VectorLiteral(q), r.run, []string{}, scaleRecallK, r.id}
		if !strings.Contains(l.query, "$5") {
			args = args[:4]
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
// It is opt-in: KRITIKA_INDEX_SCALE_ROWS is the table's row count. Loading
// a million rows takes minutes and gigabytes of disk.
// KRITIKA_INDEX_SCALE_DIMS (1024), KRITIKA_INDEX_SCALE_QUERIES (50) and
// KRITIKA_INDEX_SCALE_LAYOUTS (scaleDefaultLayouts, comma-separated names
// from scaleLayouts) vary it.
func BenchmarkIndexScale(b *testing.B) {
	if os.Getenv("KRITIKA_INDEX_SCALE_ROWS") == "" {
		b.Skip("KRITIKA_INDEX_SCALE_ROWS not set")
	}
	rows := scaleEnvInt(b, "KRITIKA_INDEX_SCALE_ROWS", 0)
	s, corpus := loadIndexScale(b, rows, scaleEnvInt(b, "KRITIKA_INDEX_SCALE_DIMS", 1024))
	ctx := b.Context()
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
// before leaves. The ones before flat, which are alternatives that were not
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
		name: "repo-hash", setup: exec(stmts...), query: scaleQueryRepo,
		teardown: exec(`DROP TABLE index_chunks`, `ALTER TABLE index_chunks_flat RENAME TO index_chunks`),
	})

	return append(layouts,
		// The table as a build before partitioning made it, which the
		// layouts after it convert.
		scaleLayout{
			name: "flat", query: similarChunksSQL,
			setup: exec(`CREATE INDEX index_chunks_embedding_idx ON index_chunks USING vchordrq (embedding halfvec_cosine_ops)`, analyze),
		},
		scaleLayout{name: "exact", query: similarChunksSQL, session: []string{`SET LOCAL vchordrq.enable_scan = off`}},
		scaleLayout{
			name: "partitioned", query: similarChunksSQL,
			setup: func(ctx context.Context, s *Store) error {
				_, err := s.PartitionIndexChunks(ctx, "kritika_app")
				return err
			},
		},
		scaleLayout{
			name: "drained", query: similarChunksSQL,
			setup: func(ctx context.Context, s *Store) error {
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
				// Autovacuum's work, once it catches up.
				return exec(`VACUUM ANALYZE index_chunks`)(ctx, s)
			},
		},
	)
}
