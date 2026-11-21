//go:build integration

package store

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestHybridSearchOneSnapshot: the vector and lexical searches of one
// request, run in WithAccountSnapshot, read the same version of a chunk
// although an incremental publication of the same index format, which
// rewrites the chunk at the same path and line, commits between them. The
// gateway joins their hits by path and line, so a lexical hit of the new
// version would lend its names to the vector hit's old text.
func TestHybridSearchOneSnapshot(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	f := newPostingsFixture(t, s, "hybrid-snapshot")
	full := f.run(IndexModeFull, "running")
	f.insert(full, "", map[string]string{"a.go": "func oldName() {}", "b.go": "func other() {}"})
	f.publish(full)
	step := f.run(IndexModeIncremental, "running")
	f.insert(step, full, map[string]string{"a.go": "func newName() {}"})

	vector := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	var hits, named []SimilarHit
	if err := s.WithAccountSnapshot(ctx, f.account, func(tx pgx.Tx) error {
		var err error
		if hits, err = SimilarChunks(ctx, tx, full, [][]float32{vector}, Exclusions{}, 10); err != nil {
			return err
		}
		f.in(func(tx pgx.Tx) error {
			return AdvanceGeneration(ctx, tx, f.repo, Generation{ID: full, Commit: "A"}, step, "B", []string{"a.go"})
		})
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "newname", Compound: true}, {Lexeme: "oldname", Compound: true}}, Vector: vector}
		named, err = LexicalChunks(ctx, tx, full, []LexicalQuery{q}, Exclusions{}, 10, LexicalBar{Least: 100}, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var vectorText string
	for _, h := range hits {
		if h.Chunk.Path == "a.go" {
			vectorText = h.Chunk.Text
		}
	}
	if vectorText != "func oldName() {}" {
		t.Fatalf("the vector search found a.go as %q, want the version before the step", vectorText)
	}
	if len(named) != 1 || named[0].Chunk.Path != "a.go" || named[0].Chunk.Text != vectorText {
		t.Fatalf("the search by name found %+v after the step published; want a.go as the vector search read it, %q",
			named, vectorText)
	}
	// The step did publish: a search that starts after it finds the new version.
	f.in(func(tx pgx.Tx) error {
		q := LexicalQuery{Terms: []LexicalTerm{{Lexeme: "newname", Compound: true}}, Vector: vector}
		after, err := LexicalChunks(ctx, tx, full, []LexicalQuery{q}, Exclusions{}, 10, LexicalBar{Least: 100}, 100)
		if err == nil && (len(after) != 1 || after[0].Chunk.Text != "func newName() {}") {
			t.Errorf("a search after the step found %+v, want a.go's new version", after)
		}
		return err
	})
}
