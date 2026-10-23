//go:build integration

package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/model"
)

// BenchmarkSimilarChunks uses deterministic clustered vectors, not model
// calls. It measures the application query under RLS with other accounts
// and generations competing in the same vector index.
func BenchmarkSimilarChunks(b *testing.B) {
	s := openStore(b)
	ctx := b.Context()
	var postgres, vchord string
	if err := s.owner.QueryRow(ctx, `SELECT version(), extversion FROM pg_extension WHERE extname = 'vchord'`).Scan(&postgres, &vchord); err != nil {
		b.Fatal(err)
	}
	b.Logf("database: %s; VectorChord %s", postgres, vchord)
	b.Setenv("VECTOR_BENCH_KEY", "fixture")
	file := configfiletest.Load(b, `apps:
  bench:
    accounts: [vector-bench, vector-other]
    clientId: Iv1.bench
    privateKey: { env: VECTOR_BENCH_KEY }
    webhookSecret: { env: VECTOR_BENCH_KEY }
`)
	if err := s.ApplyConfig(ctx, file); err != nil {
		b.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "vector-bench")
	other, _ := file.Account(configfile.ForgeGitHub, "vector-other")
	for _, dims := range []int{1024, 1536} {
		for _, size := range []int{1000, 10000} {
			b.Run(fmt.Sprintf("dims=%d/chunks=%d", dims, size), func(b *testing.B) {
				run, foreign := seedSimilarBenchmark(b, s, account.ID(), other.ID(), dims, size)
				for _, excluded := range []int{0, size / 10} {
					b.Run(fmt.Sprintf("exclude=%d", excluded), func(b *testing.B) {
						exclude := make([]string, excluded)
						for i := range exclude {
							exclude[i] = fmt.Sprintf("group-0/chunk-%d.go", i)
						}
						queries := make([][]float32, 20)
						rng := rand.New(rand.NewPCG(911, 1))
						for i := range queries {
							queries[i] = benchmarkVector(rng, dims, i%16)
						}
						recall := checkSimilarRecall(b, s, account.ID(), run, foreign, queries, exclude)
						i := 0
						for b.Loop() {
							if err := s.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
								_, err := SimilarChunks(ctx, tx, run, [][]float32{queries[i%len(queries)]}, exclude, 4)
								return err
							}); err != nil {
								b.Fatal(err)
							}
							i++
						}
						b.ReportMetric(recall, "recall@4")
					})
				}
			})
		}
	}
}

func benchmarkVector(rng *rand.Rand, dims, cluster int) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = float32(rng.NormFloat64() * 0.1)
		if i%16 == cluster {
			v[i] += 1
		}
	}
	return v
}

func seedSimilarBenchmark(b *testing.B, s *Store, account, other string, dims, size int) (run, foreign string) {
	b.Helper()
	ctx := b.Context()
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "bench-embed", dims, ""); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, query := range []string{
			`UPDATE repositories SET active_index_run_id = NULL WHERE account_id IN ($1::uuid, $2::uuid)`,
			`DELETE FROM index_runs WHERE account_id IN ($1::uuid, $2::uuid)`,
			`DELETE FROM repositories WHERE account_id IN ($1::uuid, $2::uuid)`,
		} {
			if _, err := s.owner.Exec(ctx, query, account, other); err != nil {
				b.Error(err)
			}
		}
	})
	for group, who := range []string{account, account, other} {
		repo := configfile.RepositoryID(who, fmt.Sprintf("bench/repo-%d", group))
		var id string
		if err := s.WithAccount(ctx, who, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO repositories(id, account_id, name) VALUES($1, $2, $3)`, repo, who, fmt.Sprintf("bench/repo-%d", group)); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `INSERT INTO index_runs(account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
				VALUES($1, $2, 'bench', 'bench-embed', $3, 'full', 'completed') RETURNING id`, who, repo, dims).Scan(&id)
		}); err != nil {
			b.Fatal(err)
		}
		switch group {
		case 0:
			run = id
		case 2:
			foreign = id
		}
		rng := rand.New(rand.NewPCG(911, uint64(group)+10))
		for start := 0; start < size; start += 128 {
			n := min(128, size-start)
			chunks := make([]StagedChunk, n)
			vectors := make([][]float32, n)
			for i := range n {
				chunks[i] = StagedChunk{Path: fmt.Sprintf("group-%d/chunk-%d.go", group, start+i), StartLine: 1, EndLine: 20, Language: "go", Text: strings.Repeat("func example() {}\n", 20)}
				vectors[i] = benchmarkVector(rng, dims, (start+i)%16)
			}
			if err := s.WithAccount(ctx, who, func(tx pgx.Tx) error {
				return InsertIndexChunks(ctx, tx, who, repo, id, chunks, vectors)
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
	if _, err := s.owner.Exec(ctx, `VACUUM ANALYZE index_chunks`); err != nil {
		b.Fatal(err)
	}
	return run, foreign
}

func checkSimilarRecall(b *testing.B, s *Store, account, run, foreign string, queries [][]float32, exclude []string) float64 {
	b.Helper()
	ctx := b.Context()
	var matched, total int
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var role, rls string
		if err := tx.QueryRow(ctx, `SELECT current_user, current_setting('row_security')`).Scan(&role, &rls); err != nil {
			return err
		}
		if role != "kritika_app" || rls != "on" {
			return fmt.Errorf("benchmark must run as kritika_app with RLS, got %s/%s", role, rls)
		}
		if hits, err := SimilarChunks(ctx, tx, foreign, queries[:1], exclude, 4); err != nil || len(hits) != 0 {
			return fmt.Errorf("foreign account search: %d hits, %v", len(hits), err)
		}
		for i, q := range queries {
			approx, err := SimilarChunks(ctx, tx, run, [][]float32{q}, exclude, 4)
			if err != nil {
				return err
			}
			if i == 0 {
				rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+similarChunksSQL, model.VectorLiteral(q), run, exclude, 4)
				if err != nil {
					return err
				}
				plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				b.Logf("application query plan:\n%s", strings.Join(plan, "\n"))
			}
			if _, err := tx.Exec(ctx, `SET LOCAL enable_indexscan = off; SET LOCAL enable_bitmapscan = off`); err != nil {
				return err
			}
			exact, err := SimilarChunks(ctx, tx, run, [][]float32{q}, exclude, 4)
			if err != nil {
				return err
			}
			if i == 0 {
				rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+similarChunksSQL, model.VectorLiteral(q), run, exclude, 4)
				if err != nil {
					return err
				}
				plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				text := strings.Join(plan, "\n")
				if !strings.Contains(text, "Seq Scan") || strings.Contains(text, "Index Scan") {
					return fmt.Errorf("exact baseline did not use a table scan: %s", text)
				}
				b.Logf("exact query plan:\n%s", text)
			}
			if _, err := tx.Exec(ctx, `SET LOCAL enable_indexscan = on; SET LOCAL enable_bitmapscan = on`); err != nil {
				return err
			}
			if len(exact) != 4 {
				return fmt.Errorf("exact search returned %d hits, want 4", len(exact))
			}
			total += len(exact)
			for _, h := range append(slices.Clone(approx), exact...) {
				if !strings.HasPrefix(h.Chunk.Path, "group-0/") {
					return fmt.Errorf("search leaked another generation: %s", h.Chunk.Path)
				}
			}
			for _, h := range approx {
				if slices.Contains(exclude, h.Chunk.Path) {
					return fmt.Errorf("search returned excluded path %s", h.Chunk.Path)
				}
				if slices.ContainsFunc(exact, func(e SimilarHit) bool { return e.Chunk.Path == h.Chunk.Path }) {
					matched++
				}
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return float64(matched) / float64(total)
}
