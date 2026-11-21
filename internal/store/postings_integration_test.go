//go:build integration

package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/runspec"
)

// postingsFixture is a repository whose generations a postings test builds.
type postingsFixture struct {
	t             *testing.T
	s             *Store
	account, repo string
}

// newPostingsFixture serves the account, with an index schema and a
// repository, name/one, that the test's generations are of.
func newPostingsFixture(t *testing.T, s *Store, name string) postingsFixture {
	t.Helper()
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount(name))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "test-embed", 8, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `UPDATE repositories SET active_index_run_id = NULL;
			DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema; TRUNCATE index_postings`)
	})
	account := accountID(t, s, name)
	repo := configfile.RepositoryID(account, name+"/one")
	if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repo, account, name+"/one"); err != nil {
		t.Fatal(err)
	}
	return postingsFixture{t: t, s: s, account: account, repo: repo}
}

func (f postingsFixture) run(mode, status string) string {
	f.t.Helper()
	var id string
	if err := f.s.owner.QueryRow(f.t.Context(), `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
		index_format) VALUES ($1, $2, 'A', 'test-embed', 8, $3, $4, $5) RETURNING id`, f.account, f.repo, mode, status, runspec.IndexFormat).
		Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// insert stores a chunk of each path's text under the run, with the text's
// hash. As a build does, it takes the vector of a chunk of the run or of the
// active generation that has the hash, and returns how many it took.
func (f postingsFixture) insert(run, active string, chunks map[string]string) (reused int) {
	f.t.Helper()
	batch := make([]StagedChunk, 0, len(chunks))
	hashes := make([][]byte, 0, len(chunks))
	for _, path := range slices.Sorted(maps.Keys(chunks)) {
		hash := sha256.Sum256([]byte(chunks[path]))
		batch = append(batch, StagedChunk{Path: path, Language: "go", StartLine: 1, EndLine: 1, Text: chunks[path], ContentHash: hash[:]})
		hashes = append(hashes, hash[:])
	}
	f.in(func(tx pgx.Tx) error {
		found, err := IndexVectors(f.t.Context(), tx, f.repo, run, active, hashes)
		if err != nil {
			return err
		}
		vectors := make([][]float32, len(batch))
		for i, c := range batch {
			vectors[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
			if v, ok := found[string(c.ContentHash)]; ok {
				vectors[i] = v
				reused++
			}
		}
		return InsertIndexChunks(f.t.Context(), tx, f.account, f.repo, run, batch, vectors)
	})
	return reused
}

func (f postingsFixture) in(fn func(tx pgx.Tx) error) {
	f.t.Helper()
	if err := f.s.WithAccount(f.t.Context(), f.account, fn); err != nil {
		f.t.Fatal(err)
	}
}

// publish publishes a full build, as the worker does.
func (f postingsFixture) publish(run string) {
	f.t.Helper()
	f.in(func(tx pgx.Tx) error {
		if err := ActivateGeneration(f.t.Context(), tx, f.repo, run); err != nil {
			return err
		}
		return FinishIndexRun(f.t.Context(), tx, run, IndexCompleted, 0, "")
	})
}

// postings checks the run's postings and returns how many rows they hold.
func (f postingsFixture) postings(run string) int {
	f.t.Helper()
	return checkPostings(f.t, f.s, run)
}

// rows counts the rows of the run's postings.
func (f postingsFixture) rows(run string) (n int) {
	f.t.Helper()
	if err := f.s.owner.QueryRow(f.t.Context(), `SELECT count(*) FROM index_postings WHERE index_run_id = $1`, run).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// chunks counts the run's chunks.
func (f postingsFixture) chunks(run string) (n int) {
	f.t.Helper()
	if err := f.s.owner.QueryRow(f.t.Context(), `SELECT count(*) FROM index_chunks WHERE index_run_id = $1`, run).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// held is the chunks that the run's publication counted, -1 for none.
func (f postingsFixture) held(run string) (n int) {
	f.t.Helper()
	if err := f.s.owner.QueryRow(f.t.Context(), `SELECT coalesce(generation_chunks, -1) FROM index_runs WHERE id = $1`, run).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// checkPostings checks the run's postings against its chunks' lexemes: a row
// of each word they hold, no other, with how many of the chunks hold it and,
// while fewer than LexicalCommonMax do, which. It returns how many rows the
// run's postings hold.
func checkPostings(t *testing.T, s *Store, run string) int {
	t.Helper()
	rows, err := s.owner.Query(t.Context(), `WITH truth AS (
			SELECT l AS lexeme, count(*)::int AS chunks, array_agg(c.id ORDER BY c.id) AS ids
			FROM index_chunks c, unnest(tsvector_to_array(c.lexemes)) AS l WHERE c.index_run_id = $1 GROUP BY l
		), got AS (
			SELECT lexeme, chunks, (SELECT array_agg(x ORDER BY x) FROM unnest(chunk_ids) AS x) AS ids
			FROM index_postings WHERE index_run_id = $1
		)
		SELECT lexeme || ': ' || coalesce(t.chunks::text, 'none') || ' chunks, posted ' || coalesce(g.chunks::text, 'none')
			|| ' with ' || coalesce(cardinality(g.ids), 0) || ' listed'
		FROM truth t FULL JOIN got g USING (lexeme)
		WHERE t.chunks IS DISTINCT FROM g.chunks OR (CASE WHEN t.chunks < $2 THEN t.ids END) IS DISTINCT FROM g.ids
		ORDER BY lexeme`, run, LexicalCommonMax)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(wrong) > 0 {
		t.Fatalf("%d words' postings do not match the chunks, such as %q", len(wrong), wrong[:min(len(wrong), 5)])
	}
	var n int
	if err := s.owner.QueryRow(t.Context(), `SELECT count(*) FROM index_postings WHERE index_run_id = $1`, run).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// bruteLexicalSQL is lexicalChunksSQL with the postings of each term read
// from every chunk of the generation that holds it, by its lexemes.
var bruteLexicalSQL = func() string {
	head, rest, ok := strings.Cut(lexicalChunksSQL, "), held AS MATERIALIZED (")
	if ok {
		_, rest, ok = strings.Cut(rest, "), weights AS (")
	}
	if !ok {
		panic("lexicalChunksSQL has no held or weights")
	}
	return head + `), held AS MATERIALIZED (
		SELECT h.t, h.chunk_id, h.n FROM g, (SELECT t.t, c.id AS chunk_id, count(*) OVER (PARTITION BY t.t) AS n
			FROM unnest($5::text[]) AS t (t), index_chunks c
			WHERE c.index_run_id = $1 AND t.t = ANY (tsvector_to_array(c.lexemes))) h
		WHERE h.n < least(greatest(trunc(g.n / nullif($6::int, 0)), nullif($7::int, 0)), $13::int)
			AND ($14::int = 0 OR h.n::float8 * $14::int <= g.n)
	), weights AS (` + rest
}()

// searchesMatch searches the run by name for each word that its chunks hold,
// and each of words, a query of each: the word, as a compound one, so that it
// finds every chunk that holds it. It checks that the search finds what a
// read of every chunk's lexemes does, and returns the paths found for each
// word that finds any.
func searchesMatch(t *testing.T, s *Store, account, run string, words ...string) map[string][]string {
	t.Helper()
	ctx := t.Context()
	rows, err := s.owner.Query(ctx, `SELECT DISTINCT l FROM index_chunks c, unnest(tsvector_to_array(c.lexemes)) AS l
		WHERE c.index_run_id = $1 ORDER BY l`, run)
	if err != nil {
		t.Fatal(err)
	}
	held, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	words = append(held, words...)
	queries := make([]LexicalQuery, len(words))
	for i, w := range words {
		queries[i] = LexicalQuery{Terms: []LexicalTerm{{Lexeme: w, Compound: true}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
	}
	search := func(query string) []string {
		var got []string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			// Every chunk that holds a word is kept, up to the most a word
			// can be in and still be searched for.
			hits, err := lexicalChunks(ctx, tx, query, run, queries, nil, 1000, LexicalBar{Least: LexicalCommonMax}, 1000)
			for _, h := range hits {
				got = append(got, fmt.Sprintf("%s\t%s:%d\t%.6f\t%v", words[h.Query], h.Chunk.Path, h.Chunk.StartLine, h.Lexical, h.Matched))
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got, want := search(lexicalChunksSQL), search(bruteLexicalSQL)
	if !slices.Equal(got, want) {
		t.Fatalf("the search by name found %d hits, a read of every chunk %d:\n%s\nwant\n%s", len(got), len(want),
			strings.Join(got[:min(len(got), 10)], "\n"), strings.Join(want[:min(len(want), 10)], "\n"))
	}
	found := map[string][]string{}
	for _, hit := range got {
		fields := strings.Split(hit, "\t")
		found[fields[0]] = append(found[fields[0]], strings.TrimSuffix(fields[1], ":1"))
	}
	for _, paths := range found {
		slices.Sort(paths)
	}
	return found
}

// paths is how many paths a search found and the first of them.
func paths(found []string) string {
	if len(found) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d from %s", len(found), found[0])
}

// TestIndexPostingsFollowChunks: every path that writes, moves or deletes a
// generation's chunks keeps its postings those of its chunks' lexemes, and
// the search by name finds what a read of every chunk's lexemes does. A
// publication writes them, a full build's at once and an incremental step's
// for the words it changes: a word that falls under LexicalCommonMax is
// listed again, and one that reaches it keeps its count alone. A run that
// does not publish holds none. Each publication counts the chunks that its
// generation holds.
func TestIndexPostingsFollowChunks(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "postings")

	// A full build: chunks of their own, sharedHelper in one chunk more than
	// LexicalCommonMax, and nearCap in one fewer.
	full := f.run(IndexModeFull, "running")
	chunks := map[string]string{"a.go": "func acquireLease() {}", "b.go": "func releaseLease() {}", "r.go": "func renamedLease() {}"}
	for i := range LexicalCommonMax + 1 {
		chunks[fmt.Sprintf("shared/s%03d.go", i)] = "func sharedHelper() {}"
	}
	for i := range LexicalCommonMax - 1 {
		chunks[fmt.Sprintf("near/n%03d.go", i)] = "func nearCap() {}"
	}
	f.insert(full, "", chunks)
	if n := f.rows(full); n != 0 {
		t.Fatalf("a full build holds %d postings before it publishes, want none", n)
	}
	f.publish(full)
	if n := f.postings(full); n == 0 || f.held(full) != len(chunks) {
		t.Fatalf("published full build: %d postings, %d chunks held; want some, and %d", n, f.held(full), len(chunks))
	}
	found := searchesMatch(t, s, f.account, full)
	if got := fmt.Sprintf("%v %s %s", found["releaselease"], paths(found["sharedhelper"]), paths(found["nearcap"])); got !=
		fmt.Sprintf("[b.go] none %d from near/n000.go", LexicalCommonMax-1) {
		t.Fatalf("releaseLease, sharedHelper and nearCap are found in %s", got)
	}

	// An incremental step: b.go changes, c.go is added and calls nearCap, so
	// that it reaches LexicalCommonMax, a.go is deleted, r.go is renamed to
	// s.go, whose vector is reused, and two of sharedHelper's chunks change,
	// so that it falls under.
	step := f.run(IndexModeIncremental, "running")
	if reused := f.insert(step, full, map[string]string{
		"b.go": "func renewLease() {}", "c.go": "func dropLease() { nearCap() }", "s.go": "func renamedLease() {}",
		"shared/s000.go": "func otherHelper() {}", "shared/s001.go": "func otherHelper() {}",
	}); reused != 1 {
		t.Fatalf("the step reused %d vectors, want s.go's", reused)
	}
	if n := f.rows(step); n != 0 {
		t.Fatalf("an incremental step holds %d postings before it publishes, want none", n)
	}
	f.in(func(tx pgx.Tx) error {
		return AdvanceGeneration(ctx, tx, f.repo, Generation{ID: full, Commit: "A"}, step, "B",
			[]string{"a.go", "b.go", "c.go", "r.go", "s.go", "shared/s000.go", "shared/s001.go"})
	})
	if n := f.postings(full); f.rows(step) != 0 || f.chunks(step) != 0 || n == 0 || f.held(full) != len(chunks) {
		t.Fatalf("advanced: %d postings and %d chunks left under the step, %d chunks held; want none, none, and %d",
			f.rows(step), f.chunks(step), f.held(full), len(chunks))
	}
	found = searchesMatch(t, s, f.account, full, "acquirelease")
	if got := fmt.Sprintf("%v%v%v%v %s %s", found["renewlease"], found["releaselease"], found["acquirelease"], found["renamedlease"],
		paths(found["sharedhelper"]), paths(found["nearcap"])); got != fmt.Sprintf("[b.go][][][s.go] %d from shared/s002.go none",
		LexicalCommonMax-1) {
		t.Fatalf("after the step, renewLease, releaseLease, acquireLease, renamedLease, sharedHelper and nearCap are found in %s", got)
	}

	// Another step: the list of the word listed again loses a chunk, and
	// nearCap falls under again as c.go is deleted.
	again := f.run(IndexModeIncremental, "running")
	f.insert(again, full, map[string]string{"shared/s002.go": "func otherHelper() {}"})
	f.in(func(tx pgx.Tx) error {
		return AdvanceGeneration(ctx, tx, f.repo, Generation{ID: full, Commit: "B"}, again, "C", []string{"c.go", "shared/s002.go"})
	})
	f.postings(full)
	found = searchesMatch(t, s, f.account, full)
	if got := fmt.Sprintf("%s %s %v", paths(found["sharedhelper"]), paths(found["nearcap"]), found["droplease"]); got !=
		fmt.Sprintf("%d from shared/s003.go %d from near/n000.go []", LexicalCommonMax-2, LexicalCommonMax-1) {
		t.Fatalf("after another step, sharedHelper, nearCap and dropLease are found in %s", got)
	}

	postingsDropped(t, f, full)
}

// postingsDropped checks that each path that drops the chunks of a run, or of
// the fixture's active generation, full, drops its postings.
func postingsDropped(t *testing.T, f postingsFixture, full string) {
	s, ctx := f.s, t.Context()
	// A run whose progress is discarded.
	failed := f.run(IndexModeFull, "running")
	f.insert(failed, full, map[string]string{"d.go": "func failedLease() {}"})
	f.in(func(tx pgx.Tx) error {
		return DiscardIndexProgress(ctx, tx, failed, "00000000-0000-0000-0000-000000000000")
	})
	if n := f.postings(failed); n != 0 || f.chunks(failed) != 0 {
		t.Fatalf("discarded run: %d postings and %d chunks, want none", n, f.chunks(failed))
	}

	// A run that a job left behind, which the next run's start drops.
	stray := f.run(IndexModeFull, "failed")
	f.insert(stray, full, map[string]string{"e.go": "func strayLease() {}"})
	f.in(func(tx pgx.Tx) error {
		_, runner, err := StartIndexRun(ctx, tx, NewIndexRun{AccountID: f.account, RepositoryID: f.repo, Commit: "C", Mode: IndexModeFull,
			Embedding: configfile.Embedding{Model: "test-embed", Dims: 8}})
		if err != nil {
			return err
		}
		// Ended, so that no other suite finds it orphaned.
		_, err = tx.Exec(ctx, `UPDATE runner_runs SET finished_at = now() WHERE id = $1`, runner)
		return err
	})
	if n := f.postings(stray); n != 0 || f.chunks(stray) != 0 {
		t.Fatalf("stray run: %d postings and %d chunks, want none", n, f.chunks(stray))
	}

	// A full build that supersedes the generation.
	rebuild := f.run(IndexModeFull, "running")
	f.insert(rebuild, full, map[string]string{"f.go": "func rebuiltLease() {}"})
	f.publish(rebuild)
	if n := f.postings(full); n != 0 || f.chunks(full) != 0 || f.postings(rebuild) == 0 || f.held(rebuild) != 1 {
		t.Fatalf("superseded: %d postings and %d chunks left, %d chunks held by the new generation; want none, none, and 1",
			n, f.chunks(full), f.held(rebuild))
	}
	if found := searchesMatch(t, s, f.account, rebuild, "renewlease"); fmt.Sprintf("%v%v", found["rebuiltlease"], found["renewlease"]) != "[f.go][]" {
		t.Fatalf("the new generation's rebuiltLease and renewLease are found in %v and %v", found["rebuiltlease"], found["renewlease"])
	}

	// A repository that stopped running, whose index the sweep drops.
	for range 2 {
		if _, err := s.SweepStoppedIndexes(ctx, 0, func(string, configfile.RepoTraits) bool { return false }); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.postings(rebuild); n != 0 || f.chunks(rebuild) != 0 {
		t.Fatalf("stopped repository's generation: %d postings and %d chunks, want none", n, f.chunks(rebuild))
	}

	// A rebuild of index_chunks for another embedder.
	last := f.run(IndexModeFull, "running")
	f.insert(last, "", map[string]string{"g.go": "func lastLease() {}"})
	f.publish(last)
	if n := f.postings(last); n == 0 {
		t.Fatal("the last generation has no postings")
	}
	if rebuilt, err := s.EnsureIndexSchema(ctx, "kritika_app", "other-embed", 8, ""); err != nil || !rebuilt {
		t.Fatalf("EnsureIndexSchema for another embedder = %v, %v; want a rebuild", rebuilt, err)
	}
	if n := f.postings(last); n != 0 {
		t.Fatalf("after a rebuild for another embedder: %d postings, want none", n)
	}
}

// commonGeneration builds a generation of the fixture's repository whose
// chunks all hold commonWord, and one of them rareWord, published when
// publish is set.
func commonGeneration(f postingsFixture, publish bool) string {
	f.t.Helper()
	run := f.run(IndexModeFull, "running")
	chunks := map[string]string{"rare.go": "func commonWord() { rareWord() }"}
	for i := range LexicalCommonMax - 1 {
		chunks[fmt.Sprintf("c%03d.go", i)] = "func commonWord() {}"
	}
	f.insert(run, "", chunks)
	if publish {
		f.publish(run)
	}
	return run
}

// TestIndexPostingsWrittenAtPublication: a run's postings are written as it
// publishes, a row of each word, and not as its chunks are embedded, so that
// a run that does not publish costs none.
func TestIndexPostingsWrittenAtPublication(t *testing.T) {
	s := openStore(t)
	f := newPostingsFixture(t, s, "postings-publish")
	run := commonGeneration(f, false)
	var rows, words int
	count := func() {
		t.Helper()
		if err := s.owner.QueryRow(t.Context(), `SELECT count(*), count(DISTINCT lexeme) FROM index_postings WHERE index_run_id = $1`, run).
			Scan(&rows, &words); err != nil {
			t.Fatal(err)
		}
	}
	if count(); rows != 0 {
		t.Fatalf("a run holds %d postings before it publishes, want none", rows)
	}
	f.publish(run)
	if count(); rows == 0 || rows != words {
		t.Fatalf("a published run holds %d postings of %d words, want a row of each word", rows, words)
	}
}

// TestIndexPostingsCountCommonWords: a word in LexicalCommonMax of a
// generation's chunks, which no search reads, has a row of its count alone.
func TestIndexPostingsCountCommonWords(t *testing.T) {
	s := openStore(t)
	f := newPostingsFixture(t, s, "postings-common")
	run := commonGeneration(f, true)
	var rows int
	if err := s.owner.QueryRow(t.Context(), `SELECT count(*) FROM index_postings WHERE index_run_id = $1 AND lexeme = 'commonword'`, run).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("a word in %d chunks has %d rows of postings, want one", LexicalCommonMax, rows)
	}
	var chunks int
	var listed bool
	if err := s.owner.QueryRow(t.Context(), `SELECT chunks, chunk_ids IS NOT NULL FROM index_postings
		WHERE index_run_id = $1 AND lexeme = 'commonword'`, run).Scan(&chunks, &listed); err != nil {
		t.Fatal(err)
	}
	if chunks != LexicalCommonMax || listed {
		t.Fatalf("the common word's row counts %d chunks and lists them %v, want %d and not", chunks, listed, LexicalCommonMax)
	}
}

// TestLexicalChunksCommonMax: a word in LexicalCommonMax of a generation's
// chunks is too common to search for, whatever bar the caller sets.
func TestLexicalChunksCommonMax(t *testing.T) {
	s := openStore(t)
	f := newPostingsFixture(t, s, "postings-cap")
	run := commonGeneration(f, true)
	var found []string
	f.in(func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "commonword", Compound: true}, {Lexeme: "rareword", Compound: true}},
			Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
		hits, err := LexicalChunks(t.Context(), tx, run, []LexicalQuery{q}, nil, 1000, LexicalBar{Per: 1, Least: 2 * LexicalCommonMax}, 1000)
		for _, h := range hits {
			found = append(found, fmt.Sprint(h.Chunk.Path, h.Matched))
		}
		return err
	})
	if !slices.Equal(found, []string{"rare.go[rareword]"}) {
		t.Fatalf("a search with a bar over %d found %d chunks, such as %q; want rare.go by rareWord alone",
			LexicalCommonMax, len(found), found[:min(len(found), 3)])
	}
}

// TestActiveGenerationForLexical: a generation is searched by name only when
// a build that writes postings built it and no step of another format, such
// as an older build's during a rollout, advanced it.
func TestActiveGenerationForLexical(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("lexical-ready"))); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "lexical-ready")
	for _, tt := range []struct {
		name         string
		format, step int
		want         bool
	}{
		{"built before postings", 1, 0, false},
		{"built with postings", runspec.IndexFormat, 0, true},
		{"stepped by a build before postings", runspec.IndexFormat, 1, false},
		{"stepped by a build with postings", runspec.IndexFormat, runspec.IndexFormat, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := configfile.RepositoryID(account, "lexical-ready/"+tt.name)
			var run string
			if _, err := s.owner.Exec(ctx, `INSERT INTO repositories (id, account_id, name) VALUES ($1, $2, $3)`, repo, account,
				"lexical-ready/"+tt.name); err != nil {
				t.Fatal(err)
			}
			if err := s.owner.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
				index_format, finished_at) VALUES ($1, $2, 'A', 'test-embed', 8, 'full', 'completed', $3, now() - interval '1 hour') RETURNING id`,
				account, repo, tt.format).Scan(&run); err != nil {
				t.Fatal(err)
			}
			if _, err := s.owner.Exec(ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, repo, run); err != nil {
				t.Fatal(err)
			}
			if tt.step != 0 {
				if _, err := s.owner.Exec(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
					index_format) VALUES ($1, $2, 'B', 'test-embed', 8, 'incremental', 'completed', $3)`, account, repo, tt.step); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
				found, lexical, err := ActiveGenerationFor(ctx, tx, repo, "test-embed", 8, "")
				if err != nil || found != run || lexical != tt.want {
					t.Errorf("ActiveGenerationFor = %s, %v, %v; want %s, %v", found, lexical, err, run, tt.want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// publishingTx is a transaction that, before each query it sends for rows,
// lets publish commit on another connection, and counts the statements it
// sends that read the index.
type publishingTx struct {
	pgx.Tx
	publish    func()
	statements *int
}

func (p publishingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	*p.statements++
	p.publish()
	return p.Tx.Query(ctx, sql, args...)
}

func (p publishingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	*p.statements++
	return p.Tx.QueryRow(ctx, sql, args...)
}

// TestLexicalChunksOneSnapshot: a search by name reads the generation's
// count of chunks and its postings in one statement, so an incremental
// publication that commits while it runs cannot weigh the new postings
// against the old count, which gave a word held by more chunks than the
// count a negative weight. One statement also falls under one statement
// timeout, which bounds the whole search.
func TestLexicalChunksOneSnapshot(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "postings-snapshot")
	full := f.run(IndexModeFull, "running")
	f.insert(full, "", map[string]string{"a.go": "func fooBar() {}", "b.go": "func other() {}", "c.go": "func third() {}"})
	f.publish(full)
	// The step adds five chunks that hold fooBar: the generation holds eight,
	// six of them fooBar.
	step := f.run(IndexModeIncremental, "running")
	added := map[string]string{}
	for i := range 5 {
		added[fmt.Sprintf("s%d.go", i)] = fmt.Sprintf("func fooBar() { step%d() }", i)
	}
	f.insert(step, full, added)
	published := false
	publish := func() {
		if published {
			return
		}
		published = true
		f.in(func(tx pgx.Tx) error {
			return AdvanceGeneration(ctx, tx, f.repo, Generation{ID: full, Commit: "A"}, step, "B", slices.Sorted(maps.Keys(added)))
		})
	}
	var statements int
	var weights []string
	f.in(func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "foobar", Compound: true}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
		hits, err := LexicalChunks(ctx, publishingTx{Tx: tx, publish: publish, statements: &statements}, full, []LexicalQuery{q}, nil,
			100, LexicalBar{Least: 100}, 100)
		for _, h := range hits {
			weights = append(weights, fmt.Sprintf("%.3f", h.Lexical))
		}
		return err
	})
	want := slices.Repeat([]string{fmt.Sprintf("%.3f", idf(6, 8))}, 6)
	if !published || !slices.Equal(weights, want) {
		t.Errorf("a search during a publication weighed fooBar %q, want %q, of six of eight chunks", weights, want)
	}
	if statements != 1 {
		t.Errorf("a search by name sent %d statements that read the index, want one", statements)
	}
}

// TestLexicalChunksStepOfAnotherFormat: a step of another index format, an
// older build's during a rollout, that publishes after the caller found the
// generation searchable by name (ActiveGenerationFor) but before the search
// reads its postings, which it left as they were, leaves the search with no
// hit rather than one of the stale postings.
func TestLexicalChunksStepOfAnotherFormat(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "postings-old-step")
	full := f.run(IndexModeFull, "running")
	f.insert(full, "", map[string]string{"a.go": "func fooBar() {}", "b.go": "func other() {}"})
	f.publish(full)
	f.in(func(tx pgx.Tx) error {
		_, lexical, err := ActiveGenerationFor(ctx, tx, f.repo, "test-embed", 8, "")
		if err == nil && !lexical {
			err = fmt.Errorf("the generation is not searched by name before the step")
		}
		return err
	})
	published := false
	publish := func() {
		if published {
			return
		}
		published = true
		if _, err := s.owner.Exec(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status,
			index_format) VALUES ($1, $2, 'B', 'test-embed', 8, 'incremental', 'completed', $3)`, f.account, f.repo, runspec.IndexFormat-1); err != nil {
			t.Fatal(err)
		}
	}
	var statements int
	var got []string
	f.in(func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "foobar", Compound: true}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
		hits, err := LexicalChunks(ctx, publishingTx{Tx: tx, publish: publish, statements: &statements}, full, []LexicalQuery{q}, nil,
			10, LexicalBar{Least: 100}, 10)
		for _, h := range hits {
			got = append(got, h.Chunk.Path)
		}
		return err
	})
	if !published || len(got) != 0 {
		t.Fatalf("a search during a step of another format found %q, want nothing", got)
	}
}

// TestLexicalChunksTiesBeyondTheCut: of more chunks that tie at a query's
// cut than it keeps, the nearest the query is kept, however its ID sorts.
func TestLexicalChunksTiesBeyondTheCut(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "postings-ties")
	run := f.run(IndexModeFull, "running")
	chunks := map[string]string{}
	for i := range 6 {
		chunks[fmt.Sprintf("t%d.go", i)] = "func leaseSlots() {}"
	}
	f.insert(run, "", chunks)
	f.publish(run)
	// The chunk of the greatest ID, which a cut by ID reaches last, is the
	// one nearest the query.
	var nearest string
	if err := s.owner.QueryRow(ctx, `UPDATE index_chunks SET embedding = '[0,1,0,0,0,0,0,0]'
		WHERE id = (SELECT max(id::text)::uuid FROM index_chunks WHERE index_run_id = $1) RETURNING path`, run).Scan(&nearest); err != nil {
		t.Fatal(err)
	}
	var got []string
	f.in(func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "leaseslots", Compound: true}}, Vector: []float32{0, 1, 0, 0, 0, 0, 0, 0}}
		hits, err := LexicalChunks(ctx, tx, run, []LexicalQuery{q}, nil, 1, LexicalBar{Least: 100}, 0)
		for _, h := range hits {
			got = append(got, h.Chunk.Path)
		}
		return err
	})
	if !slices.Equal(got, []string{nearest}) {
		t.Fatalf("of six chunks that tie, the search kept %q, want the nearest, %s", got, nearest)
	}
}

// TestAdvanceGenerationCount: an incremental step moves its generation's
// count of chunks by those it drops and adds, whether it only deletes or
// replaces several chunks by one, rather than counting the chunks it leaves
// alone again. A generation that no publication counted is counted whole.
func TestAdvanceGenerationCount(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "postings-count")
	full := f.run(IndexModeFull, "running")
	chunks := map[string]string{}
	for i := range 20 {
		chunks[fmt.Sprintf("p%03d.go", i)] = fmt.Sprintf("func p%03d() { shared() }", i)
	}
	f.insert(full, "", chunks)
	f.publish(full)
	commit := "A"
	for _, tt := range []struct {
		name    string
		added   map[string]string
		changed []string
		// counted is the count the step finds, "" for the publication's;
		// off is how far it stays from the chunks the generation holds.
		counted string
		off     int
	}{
		{"a deletion alone", nil, []string{"p000.go"}, "", 0},
		{"three chunks for one", map[string]string{"q.go": "func q() { shared() }"}, []string{"p001.go", "p002.go", "p003.go", "q.go"}, "", 0},
		{"a count it does not read again", map[string]string{"r.go": "func r() {}"}, []string{"p004.go", "p005.go", "r.go"},
			"generation_chunks + 1000", 1000},
		{"no count", map[string]string{"p006.go": "func p006() { other() }"}, []string{"p006.go"}, "NULL", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.counted != "" {
				if _, err := s.owner.Exec(ctx, `UPDATE index_runs SET generation_chunks = `+tt.counted+` WHERE id = $1`, full); err != nil {
					t.Fatal(err)
				}
			}
			step := f.run(IndexModeIncremental, "running")
			f.insert(step, full, tt.added)
			f.in(func(tx pgx.Tx) error {
				return AdvanceGeneration(ctx, tx, f.repo, Generation{ID: full, Commit: commit}, step, commit+"'", tt.changed)
			})
			commit += "'"
			if held, n := f.held(full), f.chunks(full); held != n+tt.off {
				t.Errorf("the step left a count of %d for %d chunks, want %d", held, n, n+tt.off)
			}
			if _, err := s.owner.Exec(ctx, `UPDATE index_runs SET generation_chunks = (SELECT count(*) FROM index_chunks
				WHERE index_run_id = $1) WHERE id = $1`, full); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSweepIndexProgressBesideSchemaRebuild: the sweep that discards a
// left-behind run's progress, which deletes its postings and chunks and
// marks its run, takes turns with a rebuild of index_chunks for another
// embedder, which marks the runs and drops the chunks and postings, rather
// than take their locks the other way round and deadlock it.
func TestSweepIndexProgressBesideSchemaRebuild(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "postings-sweep")
	// A run whose job is gone, as the sweep finds one, with a chunk.
	var run, runner string
	f.in(func(tx pgx.Tx) error {
		var err error
		run, runner, err = StartIndexRun(ctx, tx, NewIndexRun{AccountID: f.account, RepositoryID: f.repo, Commit: "A", Mode: IndexModeFull,
			Embedding: configfile.Embedding{Model: "test-embed", Dims: 8}, ResumeKey: "key", JobID: 1 << 40})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE runner_runs SET finished_at = now() WHERE id = $1`, runner)
		return err
	})
	f.insert(run, "", map[string]string{"a.go": "func left() {}"})

	// A transaction that holds index_chunks keeps the sweep and the rebuild
	// waiting, the sweep first, until both are under way.
	hold, err := s.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(ctx, `LOCK TABLE index_chunks IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	swept, rebuilt := make(chan error, 1), make(chan error, 1)
	go func() { swept <- s.SweepIndexProgress(ctx, 1000) }()
	lockWaiters(t, s, 1)
	go func() {
		_, err := s.EnsureIndexSchema(ctx, "kritika_app", "other-embed", 8, "")
		rebuilt <- err
	}()
	lockWaiters(t, s, 2)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for name, done := range map[string]chan error{"sweep": swept, "rebuild": rebuilt} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the %s failed: %v", name, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("the %s did not end", name)
		}
	}
}

// TestLexicalChunksBarWithoutPer: a LexicalBar without Per bounds a word by
// Least alone, and one without Least either by LexicalCommonMax alone, so a
// rare word is found.
func TestLexicalChunksBarWithoutPer(t *testing.T) {
	s := openStore(t)
	f := newPostingsFixture(t, s, "postings-bar")
	run := f.run(IndexModeFull, "running")
	f.insert(run, "", map[string]string{"a.go": "func rareWord() {}", "b.go": "func other() {}", "c.go": "func third() {}"})
	f.publish(run)
	for _, tt := range []struct {
		name string
		bar  LexicalBar
		want []string
	}{
		{"no bar", LexicalBar{}, []string{"a.go"}},
		{"a Least the word reaches", LexicalBar{Least: 1}, nil},
		{"a Least above the word", LexicalBar{Least: 2}, []string{"a.go"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			f.in(func(tx pgx.Tx) error {
				q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "rareword", Compound: true}}, Vector: []float32{1, 0, 0, 0, 0, 0, 0, 0}}
				hits, err := LexicalChunks(t.Context(), tx, run, []LexicalQuery{q}, nil, 10, tt.bar, 10)
				for _, h := range hits {
					got = append(got, h.Chunk.Path)
				}
				return err
			})
			if !slices.Equal(got, tt.want) {
				t.Fatalf("a search with %+v found %q, want %q", tt.bar, got, tt.want)
			}
		})
	}
}
