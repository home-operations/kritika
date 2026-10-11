//go:build integration

package worker

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

func checkStaleIndex(
	ctx context.Context, t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], lf *localForge, exec *gateExecutor,
	account, repository, head, newer string,
) {
	t.Helper()
	var once atomic.Bool
	var displaced string
	exec.setBeforeIndex(func() error {
		if once.Swap(true) {
			return nil
		}
		lf.setTip(newer)
		return st.WithAccount(ctx, account, func(tx pgx.Tx) error {
			repo, err := store.FindIndexRepo(ctx, tx, repository)
			if err != nil {
				return err
			}
			active, err := store.FindGeneration(ctx, tx, repo.ActiveRun)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT id::text FROM index_runs WHERE repository_id = $1 AND status = 'running' ORDER BY created_at DESC LIMIT 1`, repository).Scan(&displaced); err != nil {
				return err
			}
			var step string
			if err := tx.QueryRow(ctx, `INSERT INTO index_runs (account_id, repository_id, commit_sha, embed_model, embed_dims, mode, status)
    VALUES ($1, $2, $3, $4, $5, 'incremental', 'running') RETURNING id`, account, repository, newer, active.Model, active.Dims).Scan(&step); err != nil {
				return err
			}
			batch := []store.StagedChunk{{Path: "main.go", Language: "go", StartLine: 1, EndLine: 3, Text: "package main\n\nfunc newer() {}"}}
			if err := store.InsertIndexChunks(ctx, tx, account, repository, step, batch, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
				return err
			}
			if err := store.AdvanceGeneration(ctx, tx, repository, active, step, newer, []string{"main.go"}); err != nil {
				return err
			}
			return store.FinishIndexRun(ctx, tx, step, store.IndexCompleted, 1, "")
		})
	})
	t.Cleanup(func() { exec.setBeforeIndex(nil); lf.setTip(head) })
	job, err := queue.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: jobs.TriggerManual, Full: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var attempt, snoozes int
	waitFor(t, 30*time.Second, "the stale index job to rebuild", func() bool {
		var state string
		if err := st.App().QueryRow(ctx, `SELECT state, attempt, coalesce((metadata->>'snoozes')::int, 0) FROM river_job WHERE id = $1`, job.Job.ID).Scan(&state, &attempt, &snoozes); err != nil {
			t.Fatal(err)
		}
		return state == "completed"
	})
	if attempt != 1 || snoozes != 1 {
		t.Fatalf("attempt=%d snoozes=%d, want 1 and 1", attempt, snoozes)
	}
	if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var status, commit, text string
		if err := tx.QueryRow(ctx, `SELECT status FROM index_runs WHERE id = $1`, displaced).Scan(&status); err != nil {
			return err
		}
		if status != string(store.IndexSuperseded) {
			t.Fatalf("stale run status = %s", status)
		}
		if err := tx.QueryRow(ctx, `SELECT g.commit_sha, string_agg(c.text, '') FROM repositories r
   JOIN index_runs g ON g.id = r.active_index_run_id JOIN index_chunks c ON c.index_run_id = g.id
   WHERE r.id = $1 AND c.path = 'main.go' GROUP BY g.commit_sha`, repository).Scan(&commit, &text); err != nil {
			return err
		}
		if commit != newer || !strings.Contains(text, "func newer()") {
			t.Fatalf("active index = %s/%s", commit, text)
		}
		var staging, unused int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_staging), (SELECT count(*) FROM index_chunks c
   WHERE c.repository_id = $1 AND NOT EXISTS (SELECT 1 FROM repositories r WHERE r.active_index_run_id = c.index_run_id))`, repository).Scan(&staging, &unused); err != nil {
			return err
		}
		if staging != 0 || unused != 0 {
			t.Fatalf("staging=%d unused=%d", staging, unused)
		}
		var usage int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM usage WHERE role = 'embedding' AND repository_id = $1 AND created_at >= $2`, repository, job.Job.CreatedAt).Scan(&usage); err != nil {
			return err
		}
		if usage != 2 {
			t.Fatalf("embedding usage rows = %d, want both passes", usage)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
