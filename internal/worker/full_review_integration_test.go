//go:build integration

package worker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

func checkFullSettled(
	ctx context.Context, t *testing.T, st *store.Store, lf *localForge, fc *fakeCompleter, accountID string,
	last reviewScopeRow, repo *git.Repository, reset func(), commit func(string) string,
	reviewHead func(string) (reviewScopeRow, string, int),
) {
	t.Helper()
	ageConversation(ctx, t, last.id)
	const content = "package main\n\nfunc f3() {}\n\nfunc f4() {}\n\nfunc f5() {}\n\nfunc f6() {}\n\nfunc f7() {}\n\nfunc f8() {}\n"
	head := commit(content)
	left := review.Finding{Path: "main.go", Title: "f8 is still wrong"}
	fixed := review.Finding{Path: "main.go", Title: "f8 is fixed"}
	fc.find(`{"path":"main.go","line":13,"severity":"p1","category":"correctness","title":"f8 is still wrong","explanation":"e"},` +
		`{"path":"main.go","line":13,"severity":"p2","category":"maintainability","title":"f8 is fixed","explanation":"e"}`)
	seed, _, inline := reviewHead(head)
	fc.find("")
	if seed.scope != "incremental" || inline != 2 {
		t.Fatalf("seed review = %+v, %d inline comments; want incremental with two new threads", seed, inline)
	}
	threads := map[string]int64{}
	if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT title, forge_comment_id FROM findings WHERE review_id = $1 AND title LIKE 'f8 %'`, seed.id)
		if err != nil {
			return err
		}
		var title string
		var id int64
		_, err = pgx.ForEachRow(rows, []any{&title, &id}, func() error { threads[title] = id; return nil })
		return err
	}); err != nil || threads[left.Title] == 0 || threads[fixed.Title] == 0 {
		t.Fatalf("seed threads = %v, %v", threads, err)
	}
	lf.mu.Lock()
	resolvedBefore := len(lf.resolved)
	lf.mu.Unlock()

	reset()
	if err := repo.DeleteObject(plumbing.NewHash(head)); err != nil {
		t.Fatal(err)
	}
	next := commit(content + "\nfunc f9() {}\n")
	fc.resolve(review.PriorID(fixed))
	full, prompt, inline := reviewHead(next)
	fc.resolve()
	if full.scope != "full" || full.reason != "prior head unreachable" || full.prior != seed.id || inline != 0 {
		t.Fatalf("full review = %+v, %d inline comments; want unreachable prior with no duplicate threads", full, inline)
	}
	if !strings.Contains(prompt, left.Title) || !strings.Contains(prompt, "one you neither report again nor list in resolved stays open") {
		t.Fatalf("full review did not ask to account for earlier findings:\n%s", prompt)
	}
	lf.mu.Lock()
	var body string
	for _, comment := range lf.comments {
		if strings.HasPrefix(comment, "<!-- kritika:pr-5 -->\n") {
			body = comment
		}
	}
	resolved := slices.Clone(lf.resolved[resolvedBefore:])
	lf.mu.Unlock()
	for _, want := range []string{
		"**3 findings** · 1 P0 · 2 P1",
		fmt.Sprintf("[f8 is still wrong](local://onedr0p/home-ops/pull/5#r%d) · correctness · not rechecked since `%s`", threads[left.Title], head[:7]),
		"<summary>Earlier findings (1 resolved)</summary>",
		fmt.Sprintf("[f8 is fixed](local://onedr0p/home-ops/pull/5#r%d) · maintainability · resolved", threads[fixed.Title]),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("full summary lacks %q:\n%s", want, body)
		}
	}
	if !slices.Equal(resolved, []int64{threads[fixed.Title]}) {
		t.Fatalf("resolved threads = %v, want only %d", resolved, threads[fixed.Title])
	}
	if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var prID string
		if err := tx.QueryRow(ctx, `SELECT pull_request_id FROM reviews WHERE id = $1`, full.id).Scan(&prID); err != nil {
			return err
		}
		prior, err := lastCompleted(ctx, tx, prID)
		if err != nil {
			return err
		}
		var carried []string
		for _, f := range prior.findings {
			if strings.HasPrefix(f.Title, "f8 ") {
				carried = append(carried, fmt.Sprintf("%s@%s#%d", f.Title, f.CarriedFrom, f.commentID))
			}
		}
		if want := fmt.Sprintf("%s@%s#%d", left.Title, head, threads[left.Title]); !slices.Equal(carried, []string{want}) {
			t.Fatalf("stored findings = %v, want %q", carried, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ageConversation(ctx, t, full.id)
	_, prompt, inline = reviewHead(commit(content + "\nfunc f9() {}\n\nfunc f10() {}\n"))
	if !strings.Contains(prompt, left.Title) || strings.Contains(prompt, fixed.Title) || inline != 0 {
		t.Fatalf("next review posted %d comments or lost the carried finding:\n%s", inline, prompt)
	}
}
