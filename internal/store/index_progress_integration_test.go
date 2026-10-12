//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
)

func TestIndexProgressIsolationAndRetention(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureIndexSchema(ctx, "kritika_app", "progress-test", 8); err != nil {
		t.Fatal(err)
	}
	account, other := accountID(t, s, "alpha"), accountID(t, s, "beta")
	var repo, run, runner string
	if err := s.owner.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id=$1 LIMIT 1`, account).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	q, err := river.NewClient(riverpgxv5.New(s.app), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := q.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repo}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.app.Exec(context.Background(), `DELETE FROM river_job WHERE id=$1`, result.Job.ID); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.app.Exec(ctx, `UPDATE river_job SET state='running',attempt=1 WHERE id=$1`, result.Job.ID); err != nil {
		t.Fatal(err)
	}
	spec := NewIndexRun{AccountID: account, RepositoryID: repo, Commit: "head", Mode: IndexModeFull, Embedding: configfile.Embedding{Model: "progress-test", Dims: 8}, ResumeKey: "key", JobID: result.Job.ID, StrayAfter: time.Second}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		run, runner, err = StartIndexRun(ctx, tx, spec)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count) VALUES($1,$2,'full',1)`, runner, account); err != nil {
			return err
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text) VALUES($1,$2,'main.go',1,1,'chunk') RETURNING id`, runner, account).Scan(&id); err != nil {
			return err
		}
		if err = InsertIndexChunks(ctx, tx, account, repo, run, []StagedChunk{{ID: id, Path: "main.go", Text: "chunk"}}, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
			return err
		}
		if err = CheckpointIndexBatch(ctx, tx, run, 0, id, 1); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runner_runs SET phase='done',finished_at=now() WHERE id=$1`, runner); err != nil {
			return err
		}
		return ReadyIndexRun(ctx, tx, run, runner)
	}); err != nil {
		t.Fatal(err)
	}
	checkIndexProgressIsolation(t, s, other, result.Job.ID, run)
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE index_runs SET created_at=now()-interval '1 day' WHERE id=$1`, run); err != nil {
			return err
		}
		// Another job starting must not reap an old but resumable generation.
		otherSpec := spec
		otherSpec.JobID = 0
		otherSpec.ResumeKey = ""
		_, otherRunner, err := StartIndexRun(ctx, tx, otherSpec)
		if err != nil {
			return err
		}
		if err := FailRunnerRun(ctx, tx, otherRunner, "fixture finished"); err != nil {
			return err
		}
		if err := FinishIndexRun(ctx, tx, run, IndexFailed, 0, "lost commit acknowledgement"); err != nil {
			return err
		}
		resumed, _, err := ResumeIndexRun(ctx, tx, result.Job.ID, "key")
		if err != nil {
			return err
		}
		if resumed != run {
			t.Errorf("resumed=%q, want %q", resumed, run)
		}
		_, total, err := IndexProgress(ctx, tx, run)
		if total != 1 {
			t.Errorf("durable count=%d, want 1", total)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.Exec(ctx, `DELETE FROM river_job WHERE id=$1`, result.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SweepIndexProgress(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var retained int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM index_staging WHERE runner_run_id=$1`, runner).Scan(&retained); err != nil {
			return err
		}
		if retained != 0 {
			t.Errorf("deleted job retained %d staged chunks", retained)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func checkIndexProgressIsolation(t *testing.T, s *Store, other string, jobID int64, run string) {
	t.Helper()
	ctx := t.Context()
	if err := s.WithAccount(ctx, other, func(tx pgx.Tx) error {
		if err := LockIndexJob(ctx, tx, other, jobID, 1); err == nil {
			t.Error("another account locked the job")
		}
		if _, _, err := IndexProgress(ctx, tx, run); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("other account read progress: %v", err)
		}
		if err := CheckpointIndexBatch(ctx, tx, run, 0, 1, 1); err == nil {
			t.Error("other account changed progress")
		}
		id, _, err := ResumeIndexRun(ctx, tx, jobID, "key")
		if id != "" || err != nil {
			t.Errorf("other account resumed: %q, %v", id, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
