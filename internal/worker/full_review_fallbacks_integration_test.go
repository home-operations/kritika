//go:build integration

package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

func TestFullReviewFallbacksKeepFindings(t *testing.T) {
	h := newAgenticHarness(t, 868)
	configured := *h.file
	configured.Defaults.Review.Approve = &configfile.Switch{On: true}
	configured.Defaults.Review.Incremental = new(1)
	h.review.Current.Set(&configured)
	h.sm.reset(scriptSubmit)
	h.dispatch(t, h.head)
	seed, status, errText := h.waitReview(t, h.head)
	if status != "completed" {
		t.Fatalf("seed review = %s: %s", status, errText)
	}
	original := review.Finding{Path: "main.go", Title: "b is unused"}
	var thread int64
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT forge_comment_id FROM findings WHERE review_id = $1 AND fingerprint = $2`,
			seed, review.Fingerprint(original)).Scan(&thread)
	}); err != nil || thread == 0 {
		t.Fatalf("seed thread = %d, %v", thread, err)
	}
	const content = "package main\n\nfunc b() {}\n\nfunc c() {}\n"
	var next string
	for _, tc := range []struct {
		name, reason string
		prepare      func(*testing.T) string
		manual       bool
		sameHead     bool
	}{
		{
			name: "file threshold", reason: "1 files changed since last review",
			prepare: func(t *testing.T) string { return h.commit(t, "main.go", content) },
		},
		{
			name: "unchanged change after rebase", reason: "the change is as the last review saw it",
			prepare: func(t *testing.T) string {
				repo, err := git.PlainOpen(h.dir)
				if err != nil {
					t.Fatal(err)
				}
				wt, err := repo.Worktree()
				if err != nil {
					t.Fatal(err)
				}
				if err := wt.Reset(&git.ResetOptions{Commit: plumbing.NewHash(h.base), Mode: git.HardReset}); err != nil {
					t.Fatal(err)
				}
				h.lf.setBase(h.commit(t, "base.txt", "a change on the base branch\n"))
				return h.commit(t, "main.go", content)
			},
		},
		{
			name: "manual at a new head", reason: "re-run asked for at a new head", manual: true,
			prepare: func(t *testing.T) string { return h.commit(t, "main.go", content+"\nfunc manual() {}\n") },
		},
		{
			name: "same head", reason: "re-run at the reviewed head", sameHead: true,
			prepare: func(*testing.T) string { return next },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next = tc.prepare(t)
			h.sm.reset(scriptParts)
			switch {
			case tc.sameHead:
				if status := h.rerun(t, next); status != "completed" {
					t.Fatalf("rerun = %s", status)
				}
			case tc.manual:
				err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
					_, err := tx.Exec(h.ctx, `UPDATE pull_requests SET head_sha = $1 WHERE repository_id = $2 AND number = $3`,
						next, configfile.RepositoryID(h.account.ID(), "acme/widgets"), h.number)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.insertOnly.Insert(h.ctx, jobs.ReviewArgs{AccountID: h.account.ID(),
					RepositoryID: configfile.RepositoryID(h.account.ID(), "acme/widgets"), Number: h.number, HeadSHA: next, Trigger: jobs.TriggerManual}, nil); err != nil {
					t.Fatal(err)
				}
			default:
				h.dispatch(t, next)
			}
			id, status, errText := h.waitReview(t, next)
			if status != "completed" {
				t.Fatalf("review = %s: %s", status, errText)
			}
			row := scopeRow(h.ctx, t, h.st, h.account.ID(), next)
			if row.scope != "full" || row.reason != tc.reason {
				t.Fatalf("scope = %+v, want full because %q", row, tc.reason)
			}
			h.sm.mu.Lock()
			body := string(h.sm.bodies[len(h.sm.bodies)-1])
			h.sm.mu.Unlock()
			if shown := strings.Contains(body, "Findings from the last review"); shown == (tc.manual || tc.sameHead) {
				t.Fatalf("earlier findings shown = %v, manual = %v, same head = %v", shown, tc.manual, tc.sameHead)
			}
			checkFullCarried(t, h, id, original, thread, false)
		})
	}

	t.Run("confidence sees the carried P1 and enforces its ceiling", func(t *testing.T) {
		fc := &fakeCompleter{}
		h.review.Steppers = &adapter.Steppers{Build: func(configfile.Provider) (model.Stepper, error) { return fc, nil }}
		scored := configured
		scored.Defaults.Confidence.Model = new(configfile.ModelRef("gateway/judge"))
		scored.Defaults.Confidence.Threshold = new(4)
		scored.Defaults.Confidence.Gate = &configfile.Switch{On: true}
		h.review.Current.Set(&scored)
		h.sm.reset(scriptParts)
		next = h.commit(t, "main.go", content+"\nfunc scored() {}\n")
		h.dispatch(t, next)
		id, status, errText := h.waitReview(t, next)
		if status != "completed" {
			t.Fatalf("scored review = %s: %s", status, errText)
		}
		fc.mu.Lock()
		prompt := strings.Join(fc.users, "\n")
		fc.mu.Unlock()
		if !strings.Contains(prompt, original.Title) {
			t.Fatalf("confidence prompt lost the carried finding:\n%s", prompt)
		}
		var score int
		if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			return tx.QueryRow(h.ctx, `SELECT (confidence->>'score')::int FROM reviews WHERE id = $1`, id).Scan(&score)
		}); err != nil || score != 3 {
			t.Fatalf("score = %d, %v; want the P1 ceiling of 3 despite the model answering 5", score, err)
		}
		checkFullCarried(t, h, id, original, thread, false)
	})

	t.Run("unfinished split review retains earlier findings", func(t *testing.T) {
		h.review.Current.Set(&configured)
		h.sm.reset(scriptPartFails)
		body := strings.Repeat("// "+strings.Repeat("x", 60)+"\n", 650)
		for _, dir := range []string{"a", "b"} {
			if err := os.MkdirAll(filepath.Join(h.dir, "split", dir), 0o755); err != nil {
				t.Fatal(err)
			}
			next = h.commit(t, "split/"+dir+"/file.go", "package "+dir+"\n"+body)
		}
		h.lf.setSize(forge.OpenPullRequest{Additions: 1300, ChangedFiles: 3})
		h.dispatch(t, next)
		id, status, errText := h.waitReview(t, next)
		if status != "completed" {
			t.Fatalf("split review = %s: %s", status, errText)
		}
		checkFullCarried(t, h, id, original, thread, true)
	})
}

func checkFullCarried(t *testing.T, h *agenticHarness, id string, original review.Finding, thread int64, partial bool) {
	t.Helper()
	var carried, fingerprint string
	var posted bool
	var comment int64
	var gotPartial bool
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT f.carried_from, f.fingerprint, f.posted_inline, f.forge_comment_id, r.partial
			FROM findings f JOIN reviews r ON r.id = f.review_id WHERE r.id = $1 AND f.title = $2`, id, original.Title).
			Scan(&carried, &fingerprint, &posted, &comment, &gotPartial)
	}); err != nil || carried != h.head || fingerprint != review.Fingerprint(original) || !posted || comment != thread || gotPartial != partial {
		t.Fatalf("carried = %s, fingerprint = %s, posted = %v, thread = %d, partial = %v, err = %v", carried, fingerprint, posted, comment, gotPartial, err)
	}
	var findings []store.AccountFinding
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		var err error
		findings, _, err = store.ListAccountFindings(h.ctx, tx, store.FindingFilter{Query: fmt.Sprint(h.number)}, store.Page{Limit: 10})
		return err
	}); err != nil || len(findings) != 1 || findings[0].Status != store.FindingOpen {
		t.Fatalf("dashboard findings = %+v, %v; want one open finding", findings, err)
	}
	h.lf.mu.Lock()
	body := h.lf.comments[commentBase+1]
	inline, resolved, approvals := len(h.lf.inline), len(h.lf.resolved), len(h.lf.approvals)
	status := h.lf.status
	h.lf.mu.Unlock()
	if inline != 1 || resolved != 0 || approvals != 0 {
		t.Fatalf("inline = %d, resolved = %d, approvals = %d; want one original thread, unresolved, and no approval", inline, resolved, approvals)
	}
	for _, want := range []string{"**Not approved**", "not rechecked since `" + h.head[:7] + "`",
		fmt.Sprintf("[b is unused](local://acme/widgets/pull/%d#r%d)", h.number, thread)} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary lacks %q:\n%s", want, body)
		}
	}
	if !strings.Contains(status, "1 finding(s)") {
		t.Fatalf("status lost the carried finding: %s", status)
	}
}
