//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/tuning"
)

func TestSimilarWithoutExclusions(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("review-similar"))); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "review-similar", "review-similar/one")
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id=NULL; DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	account := accountID(t, s, "review-similar")
	repo := configfile.RepositoryID(account, "review/similar")
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories(id,account_id,name) VALUES($1,$2,'review/similar')`, repo, account); err != nil {
		t.Fatal(err)
	}
	var run string
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status) VALUES($1,$2,'abc','test-embed',8,'full','completed') RETURNING id`, account, repo).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks(account_id,repository_id,index_run_id,path,start_line,end_line,text,embedding) VALUES($1,$2,$3,'main.go',1,1,'package main','[1,0,0,0,0,0,0,0]')`, account, repo, run); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		exclude []string
		want    int
	}{{"empty", []string{}, 1}, {"omitted", nil, 1}, {"excluded", []string{"main.go"}, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				hits, err := SimilarChunks(ctx, tx, run, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, Exclusions{Paths: tt.exclude}, 4)
				if err == nil && len(hits) != tt.want {
					t.Errorf("got %d hits, want %d", len(hits), tt.want)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	// Each hit says which query it is near.
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		v := []float32{1, 0, 0, 0, 0, 0, 0, 0}
		hits, err := SimilarChunks(ctx, tx, run, [][]float32{v, v}, Exclusions{}, 4)
		if err == nil && (len(hits) != 2 || hits[0].Query != 0 || hits[1].Query != 1) {
			t.Errorf("hits = %+v; want one near each query, in their order", hits)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSimilarExclusions: a run's report of the paths its searches leave out
// is written whole however many it holds, the first report holds, a search
// leaves them out, another account can neither read nor write them nor
// change what a search leaves out, and they go once the run has finished.
func TestSimilarExclusions(t *testing.T) {
	similarExcludeBatch := tuning.Get().Store.SimilarExcludeBatch
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	gen, runs := similarExclusionsFixture(t, s, alpha, beta)
	run, empty, foreign := runs[0], runs[1], runs[2]
	report := func(account, runID string, paths []string) {
		t.Helper()
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error { return SetSimilarExclude(ctx, tx, runID, paths) }); err != nil {
			t.Fatal(err)
		}
	}
	reported := func(account, runID string) []string {
		t.Helper()
		var paths []string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT coalesce(array_agg(path ORDER BY path), '{}') FROM similar_exclusions WHERE runner_run_id = $1`,
				runID).Scan(&paths)
		}); err != nil {
			t.Fatal(err)
		}
		return paths
	}
	found := func(exclude Exclusions) []string {
		t.Helper()
		var paths []string
		if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
			hits, err := SimilarChunks(ctx, tx, gen, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, exclude, 10)
			for _, h := range hits {
				paths = append(paths, h.Chunk.Path)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		slices.Sort(paths)
		return paths
	}

	// More paths than a batch, one of them twice, are written whole, and so
	// is a path longer than a B-tree entry can be, which compression leaves
	// as long.
	paths := make([]string, 0, 4+2*similarExcludeBatch)
	paths = append(paths, "moved/a.go", "moved/b.go", "moved/a.go", longGitPath())
	for i := range 2 * similarExcludeBatch {
		paths = append(paths, fmt.Sprintf("vendor/f%04d.go", i))
	}
	report(alpha, run, paths)
	want := slices.Compact(slices.Sorted(slices.Values(paths)))
	if got := reported(alpha, run); !slices.Equal(got, want) {
		t.Fatalf("reported %d paths, want %d", len(got), len(want))
	}
	// Beta claims no first report for alpha's run that has none yet.
	report(beta, empty, []string{"kept.go"})
	var claimed bool
	if err := s.owner.QueryRow(ctx, `SELECT similar_exclude_reported_at IS NOT NULL FROM runner_runs WHERE id = $1`, empty).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("beta made the first report for alpha's run")
	}
	if got := reported(alpha, empty); len(got) != 0 {
		t.Fatalf("beta's report for alpha's run = %q", got)
	}
	// The first report holds, even one of no path.
	report(alpha, run, []string{"kept.go"})
	report(alpha, empty, nil)
	report(alpha, empty, []string{"kept.go"})
	if got := reported(alpha, run); !slices.Equal(got, want) {
		t.Fatalf("a second report changed the first: %d paths", len(got))
	}
	if got := reported(alpha, empty); len(got) != 0 {
		t.Fatalf("a second report after one of no path = %q", got)
	}
	if got := found(Exclusions{Run: run}); !slices.Equal(got, []string{"kept.go"}) {
		t.Fatalf("a search of the run = %q", got)
	}
	if got := found(Exclusions{Run: empty, Paths: []string{"moved/a.go"}}); !slices.Equal(got, []string{"kept.go", "moved/b.go"}) {
		t.Fatalf("a search of the run that reported none, with a request's own = %q", got)
	}

	// Beta reads none of alpha's, and writes no row that names alpha's run,
	// under either account.
	if got := reported(beta, run); len(got) != 0 {
		t.Fatalf("beta reads %d of alpha's exclusions", len(got))
	}
	for _, account := range []string{beta, alpha} {
		err := s.WithAccount(ctx, beta, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO similar_exclusions (runner_run_id, account_id, path) VALUES ($1, $2, 'kept.go')`, empty, account)
			return err
		})
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "42501" {
			t.Fatalf("beta's row for alpha's run, in account %s: %v, want a refusal by row-level security", account, err)
		}
	}
	// A row of beta's that names alpha's run, which only the owner can write,
	// leaves alpha's search as it was.
	if _, err := s.owner.Exec(ctx, `INSERT INTO similar_exclusions (runner_run_id, account_id, path) VALUES ($1, $2, 'kept.go')`,
		empty, beta); err != nil {
		t.Fatal(err)
	}
	if got := found(Exclusions{Run: empty}); !slices.Equal(got, []string{"kept.go", "moved/a.go", "moved/b.go"}) {
		t.Fatalf("a search of alpha's run with beta's row = %q", got)
	}
	report(beta, foreign, []string{"kept.go"})

	// The sweep takes the finished run's alone.
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error { return FailRunnerRun(ctx, tx, run, "test finished") }); err != nil {
		t.Fatal(err)
	}
	// Other suites finish runs that reported paths too.
	if n, err := s.SweepSimilarExclusions(ctx); err != nil || n < int64(len(want)) {
		t.Fatalf("swept %d, %v; want at least the finished run's %d", n, err, len(want))
	}
	if got := reported(alpha, run); len(got) != 0 {
		t.Fatalf("the finished run kept %d", len(got))
	}
	if got := reported(beta, foreign); !slices.Equal(got, []string{"kept.go"}) {
		t.Fatalf("the sweep took a live run's: %q", got)
	}
}

// TestSimilarExclusionsWithoutMD5: Postgres that allows only FIPS-approved
// algorithms refuses md5(), and a run still reports the paths that its
// searches leave out, of any length, and both searches leave them out. The
// test takes md5() away from every role but the superuser, which refuses
// each call of it as such a server does.
func TestSimilarExclusionsWithoutMD5(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	super, err := pgxpool.New(ctx, testEnv(t, "KRITIKA_TEST_SUPER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(super.Close)
	const md5 = `FUNCTION md5(text), md5(bytea)`
	if _, err := super.Exec(ctx, `REVOKE EXECUTE ON `+md5+` FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(), `GRANT EXECUTE ON `+md5+` TO PUBLIC`); err != nil {
			t.Error(err)
		}
	})
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	gen, runs := similarExclusionsFixture(t, s, alpha, beta)
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		return SetSimilarExclude(ctx, tx, runs[0], []string{"moved/a.go", longGitPath()})
	}); err != nil {
		t.Fatalf("SetSimilarExclude without md5(): %v", err)
	}
	var similar []string
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		hits, err := SimilarChunks(ctx, tx, gen, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, Exclusions{Run: runs[0]}, 10)
		for _, h := range hits {
			similar = append(similar, h.Chunk.Path)
		}
		return err
	}); err != nil {
		t.Fatalf("SimilarChunks without md5(): %v", err)
	}
	slices.Sort(similar)
	if !slices.Equal(similar, []string{"kept.go", "moved/b.go"}) {
		t.Fatalf("SimilarChunks without md5() = %q, want kept.go and moved/b.go", similar)
	}
	var lexical []string
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "main"}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
		hits, err := LexicalChunks(ctx, tx, gen, []LexicalQuery{q}, Exclusions{Run: runs[0]}, 10, LexicalBar{Least: 100}, 100)
		for _, h := range hits {
			lexical = append(lexical, h.Chunk.Path)
		}
		return err
	}); err != nil {
		t.Fatalf("LexicalChunks without md5(): %v", err)
	}
	slices.Sort(lexical)
	if !slices.Equal(lexical, []string{"kept.go", "moved/b.go"}) {
		t.Fatalf("LexicalChunks without md5() = %q, want kept.go and moved/b.go", lexical)
	}
}

// similarExclusionsFixture is a generation of alpha's that holds kept.go,
// moved/a.go and moved/b.go, published with its postings, so both searches
// read it, and three runner runs, two of alpha's and one of beta's, which go
// when the test ends.
func similarExclusionsFixture(t *testing.T, s *Store, alpha, beta string) (gen string, runs [3]string) {
	t.Helper()
	ctx := t.Context()
	repo := configfile.RepositoryID(alpha, "exclusions/repo")
	t.Cleanup(func() {
		if _, err := s.owner.Exec(context.Background(), `DELETE FROM runner_runs WHERE id::text = ANY($1)`, runs[:]); err != nil {
			t.Error(err)
		}
		if _, err := s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL; DROP TABLE IF EXISTS index_chunks;
			DELETE FROM index_schema`); err != nil {
			t.Error(err)
		}
		for _, stmt := range []string{
			`DELETE FROM index_postings WHERE index_run_id IN (SELECT id FROM index_runs WHERE repository_id = $1)`,
			`DELETE FROM index_runs WHERE repository_id = $1`, `DELETE FROM repositories WHERE id = $1`,
		} {
			if _, err := s.owner.Exec(context.Background(), stmt, repo); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, 'exclusions/repo')`, repo, alpha); err != nil {
		t.Fatal(err)
	}
	gen = partitionRun(t, s, alpha, repo, true, "kept.go", "moved/a.go", "moved/b.go")
	for i, account := range []string{alpha, alpha, beta} {
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			runs[i], err = InsertRunnerRun(ctx, tx, account, RunnerKindReview, "", 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return gen, runs
}

// longGitPath is a nested path of about 4 KiB that compression cannot
// shorten, longer than PostgreSQL's limit on a B-tree entry of a little
// under 2.7 KiB on its default 8 KiB page. Git takes it as a path.
func longGitPath() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	r := rand.New(rand.NewPCG(1, 2))
	parts := make([]string, 20)
	for i := range parts {
		b := make([]byte, 200)
		for j := range b {
			b[j] = letters[r.IntN(len(letters))]
		}
		parts[i] = string(b)
	}
	return strings.Join(parts, "/") + ".go"
}

// TestSimilarZeroEmbedded: a chunk embedded as zeros, whose cosine
// similarity to any query is undefined, comes back with a finite one below
// any cosine, and after the chunk nearest the query, whether the search
// reads the vector index or scans the generation.
func TestSimilarZeroEmbedded(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("similar-zero"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "similar-zero")
	repo := configfile.RepositoryID(account, "similar/zero")
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL; DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
		_, _ = s.owner.Exec(ctx, `DELETE FROM index_runs WHERE repository_id = $1`, repo)
		_, _ = s.owner.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, repo)
	})
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, 'similar/zero')`, repo, account); err != nil {
		t.Fatal(err)
	}
	var run string
	if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
		VALUES ($1, $2, 'abc', 'test-embed', 8, 'full', 'completed') RETURNING id`, account, repo).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.owner.Exec(ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
		SELECT $1, $2, $3, p, 1, 1, 'package main', e::halfvec FROM unnest($4::text[], $5::text[]) AS t(p, e)`, account, repo, run,
		[]string{"zero.go", "near.go", "far.go"},
		[]string{"[0,0,0,0,0,0,0,0]", "[1,0.1,0,0,0,0,0,0]", "[0,1,0,0,0,0,0,0]"}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, session string
		// zero says the plan finds the zero-embedded chunk: an exact scan
		// does, and the vector index holds none.
		zero bool
	}{
		{"vector index", `SET LOCAL enable_seqscan = off; SET LOCAL enable_sort = off`, false},
		{"exact scan", `SET LOCAL enable_indexscan = off; SET LOCAL enable_bitmapscan = off`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, tt.session); err != nil {
					return err
				}
				hits, err := SimilarChunks(ctx, tx, run, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}, Exclusions{}, 3)
				if err != nil {
					return err
				}
				var got []string
				for _, h := range hits {
					got = append(got, h.Chunk.Path)
					if math.IsNaN(h.Similarity) || h.Similarity < -2 || h.Similarity > 1 {
						t.Errorf("%s: similarity %g, want a finite one", h.Chunk.Path, h.Similarity)
					}
					if h.Chunk.Path == "zero.go" && h.Similarity >= -1 {
						t.Errorf("zero.go: similarity %g, want one below any cosine", h.Similarity)
					}
				}
				want := []string{"near.go", "far.go"}
				if tt.zero {
					want = append(want, "zero.go")
				}
				if !slices.Equal(got, want) {
					t.Errorf("hits = %q, want %q", got, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
