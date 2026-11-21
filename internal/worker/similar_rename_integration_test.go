//go:build integration

package worker

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/model"
)

// TestSimilarCodeRenameEndToEnd: a review of a pull request that renames
// and edits a file gets similar code from elsewhere in the repository, not
// the file's own copy under its old name. The index still holds that copy,
// which matches the change's hunk best of all and, through the margin below
// a query's best, would cut every real neighbour. The runner reports both
// names of the rename, and the gateway leaves both out of its searches. The
// neighbour it keeps is shown at its lines in the head, not the index's.
func TestSimilarCodeRenameEndToEnd(t *testing.T) {
	h := newAgenticHarness(t, 1241)
	withIndex, err := configfiletest.Parse(t, h.config+"embedding: { model: gateway/fake-embed, dims: 8 }\n")
	if err != nil {
		t.Fatal(err)
	}
	h.review.Current.Set(withIndex)
	t.Cleanup(func() { h.review.Current.Set(h.file) })
	repoID := configfile.RepositoryID(h.account.ID(), "acme/widgets")

	keys := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, "\t\""+k+"\": \""+strings.ToUpper(k)+"\",")
	}
	oldCode := "package lib\n\nvar names = map[string]string{\n" + strings.Join(lines, "\n") + "\n}\n"
	added := "\nfunc name(key string) string {\n\treturn names[key]\n}\n"
	base := h.commitTree(t, map[string]string{"lib/names.go": oldCode}, nil)
	h.lf.setBase(base)
	head := h.commitTree(t, map[string]string{"lib/labels.go": oldCode + added}, []string{"lib/names.go"})

	// What the runner asks about: the added lines and three lines of
	// context before them, as the head has them.
	query := "\t\"zeta\": \"ZETA\",\n}\n" + added
	vectors, _, err := h.fe.Embed(h.ctx, []string{query})
	if err != nil {
		t.Fatal(err)
	}
	// The old name's copy, embedded as the query is, matches it best.
	// other.go's chunk is a real neighbour whose similarity, 0.8, clears the
	// floor but sits more than the margin below the copy's. The index has it
	// at line 7, as an older commit had it, and the head at line 3.
	if _, err := h.st.EnsureIndexSchema(h.ctx, "kritika_app", withIndex.Embedding.Model, withIndex.Embedding.Dims,
		withIndex.Embedding.DocumentPrompt); err != nil {
		t.Fatal(err)
	}
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		var runID string
		if err := tx.QueryRow(h.ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
			VALUES ($1, $2, $3, $4, $5, 'full', 'completed') RETURNING id`, h.account.ID(), repoID, base, withIndex.Embedding.Model,
			withIndex.Embedding.Dims).Scan(&runID); err != nil {
			return err
		}
		for _, c := range []struct {
			path       string
			start, end int
			text       string
			vector     []float32
		}{
			{"lib/names.go", 1, 10, oldCode, vectors[0]},
			{"other.go", 7, 7, "func c() {}", rotated(vectors[0], 0.8)},
		} {
			if _, err := tx.Exec(h.ctx, `INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, text, embedding)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::halfvec)`, h.account.ID(), repoID, runID, c.path, c.start, c.end, c.text,
				model.VectorLiteral(c.vector)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(h.ctx, `UPDATE repositories SET active_index_run_id = $2 WHERE id = $1`, repoID, runID)
		return err
	}); err != nil {
		t.Fatalf("seed the index: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := h.st.WithAccount(ctx, h.account.ID(), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE repositories SET active_index_run_id = NULL WHERE id = $1`, repoID)
			return err
		}); err != nil {
			t.Error(err)
		}
	})

	h.sm.reset(scriptSubmit)
	h.sm.mu.Lock()
	first := len(h.sm.bodies)
	h.sm.mu.Unlock()
	h.dispatch(t, head)
	reviewID, status, errText := h.waitReview(t, head)
	if status != "completed" {
		t.Fatalf("status = %s (%s)", status, errText)
	}
	h.sm.mu.Lock()
	prompt := string(h.sm.bodies[first])
	h.sm.mu.Unlock()
	if strings.Contains(prompt, "### similar: lib/names.go") || !strings.Contains(prompt, "### similar: other.go lines 3-3 ") {
		t.Fatalf("stage 4 shows the renamed file's old copy, or not its neighbour at the head's lines:\n%s", prompt)
	}
	var excluded []string
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT coalesce(similar_exclude, '{}') FROM runner_runs WHERE review_id = $1`, reviewID).Scan(&excluded)
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib/labels.go", "lib/names.go"}; !slices.Equal(excluded, want) {
		t.Fatalf("the run reported exclusions %q, want %q", excluded, want)
	}
}

// commitTree commits files, written at their paths, and removes the paths of
// remove, in one commit.
func (h *agenticHarness) commitTree(t *testing.T, files map[string]string, remove []string) string {
	t.Helper()
	r, err := git.PlainOpen(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range remove {
		if _, err := wt.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(h.dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(path); err != nil {
			t.Fatal(err)
		}
	}
	c, err := wt.Commit("change", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@x", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return c.String()
}

// rotated is a unit vector whose cosine similarity with v is cos.
func rotated(v []float32, cos float64) []float32 {
	unit := normalized(v)
	// Any vector not parallel to v gives a direction orthogonal to it.
	other := make([]float64, len(v))
	other[slices.Index(unit, slices.Min(unit))] = 1
	var dot float64
	for i := range unit {
		dot += unit[i] * other[i]
	}
	for i := range other {
		other[i] -= dot * unit[i]
	}
	orth := normalized(other)
	sin := math.Sqrt(1 - cos*cos)
	out := make([]float32, len(v))
	for i := range out {
		out[i] = float32(cos*unit[i] + sin*orth[i])
	}
	return out
}

// normalized is v scaled to unit length.
func normalized[T float32 | float64](v []T) []float64 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x) / norm
	}
	return out
}
