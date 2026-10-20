//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertest"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

type resumeForge struct {
	forge.Client
	tip      string
	config   []byte
	gitToken func() error
}

func (f *resumeForge) BranchTip(context.Context, string, string, string) (string, string, error) {
	return f.tip, "main", nil
}
func (f *resumeForge) FileAt(context.Context, string, string, string, string) ([]byte, error) {
	if f.config == nil {
		return nil, fs.ErrNotExist
	}
	return f.config, nil
}
func (f *resumeForge) GitToken(context.Context, string) (string, error) {
	if f.gitToken != nil {
		return "", f.gitToken()
	}
	return "", nil
}
func (*resumeForge) CloneURL(string, string) string { return "unused" }

type tipRetryForge struct {
	*resumeForge
	calls        int
	afterPublish func(context.Context) error
}

func (f *tipRetryForge) BranchTip(ctx context.Context, owner, name, branch string) (string, string, error) {
	f.calls++
	if f.calls == 2 {
		return "", "", f.afterPublish(ctx)
	}
	return f.resumeForge.BranchTip(ctx, owner, name, branch)
}

func TestIndexRetriesFailedTipCheck(t *testing.T) {
	env := setupIndexResume(t, "")
	ctx := rivertest.WorkContext(t.Context(), env.queue)
	job := env.claim(1)
	wantErr := errors.New("tip lookup unavailable")
	f := &tipRetryForge{resumeForge: &resumeForge{tip: "head-1"}}
	f.afterPublish = func(ctx context.Context) error {
		args := job.Args
		args.CommitSHA = "head-2"
		inserted, err := env.queue.Insert(ctx, args, nil)
		if err != nil {
			return err
		}
		if !inserted.UniqueSkippedAsDuplicate || inserted.Job.ID != job.ID {
			return errors.New("push was not absorbed into the running job")
		}
		f.tip = args.CommitSHA
		return wantErr
	}
	exec := &resumeExecutor{st: env.st, account: env.account.ID()}
	w := &Index{
		Store: env.st, Current: configfile.NewCurrent(env.file), Forges: &forges{f: f}, Logger: slog.New(slog.DiscardHandler),
		Executor: exec, Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return &fakeEmbedder{} }},
	}
	if err := w.Work(ctx, job); !errors.Is(err, wantErr) {
		t.Fatalf("Work error = %v, want %v", err, wantErr)
	}
	if err := w.Work(ctx, env.claim(2)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		repo, err := store.FindIndexRepo(ctx, tx, env.repo)
		if err != nil {
			return err
		}
		generation, err := store.FindGeneration(ctx, tx, repo.ActiveRun)
		if err == nil && generation.Commit != "head-2" {
			t.Errorf("indexed commit = %s, want head-2", generation.Commit)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if exec.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", exec.calls)
	}
}

type resumeExecutor struct {
	st      *store.Store
	account string
	calls   int
	after   func()
}

func (e *resumeExecutor) Run(ctx context.Context, spec executor.Spec) executor.Result {
	e.calls++
	mode := store.IndexModeFull
	if spec.Job.Base != "" {
		mode = store.IndexModeIncremental
	}
	err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO index_packs(runner_run_id,account_id,mode,chunk_count,base_sha,changed_paths) VALUES($1,$2,$3,3,$4,ARRAY['main.go'])`, spec.Job.RunID, e.account, mode, spec.Job.Base); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO index_staging(runner_run_id,account_id,path,start_line,end_line,text)
   SELECT $1,$2,'main.go',n,n,'chunk-' || n FROM generate_series(1,3) n`, spec.Job.RunID, e.account)
		return err
	})
	if e.after != nil {
		e.after()
	}
	return executor.Result{Err: err}
}

func TestIndexResumesCommittedBatches(t *testing.T) {
	for _, scenario := range []string{"provider error", "crash after commit", "cancel before commit", "rejected checkpoint", "newer attempt", "changed commit", "changed ignores", "changed endpoint", "changed model", "changed dimensions", "cancelled job", "discarded job", "cancel request", "missing vectors", "missing staging", "forced rebuild", "incremental"} {
		t.Run(scenario, func(t *testing.T) { testIndexResume(t, scenario) })
	}
}

func testIndexResume(t *testing.T, scenario string) {
	env := setupIndexResume(t, scenario)
	st, file, account, repo, queue, claim := env.st, env.file, env.account, env.repo, env.queue, env.claim
	ctx := t.Context()
	job := claim(1)
	f := &resumeForge{tip: "head-1"}
	current := configfile.NewCurrent(file)
	exec := &resumeExecutor{st: st, account: account.ID()}
	firstCtx, cancel := context.WithCancel(rivertest.WorkContext(ctx, queue))
	defer cancel()
	calls := map[string]int{}
	owner := resumeCheckpointOwner(t, scenario)
	embed := &resumeFault{ctx: ctx, scenario: scenario, st: st, jobID: job.ID, current: current, owner: owner, first: true, cancel: cancel, calls: calls}

	worker := func() *Index {
		return &Index{Store: st, Current: current, Forges: &forges{f: f}, Logger: slog.New(slog.DiscardHandler),
			Executor: exec, Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return embed }}, batch: 1}
	}
	workResumeAttempt(t, worker(), firstCtx, job, scenario == "crash after commit")
	var run, runner string
	var cursor int64
	var chunks int
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT x.id::text, rr.id::text, x.embedding_cursor, x.chunk_count FROM index_runs x
   JOIN runner_runs rr ON rr.index_run_id = x.id WHERE rr.river_job_id = $1`, job.ID).Scan(&run, &runner, &cursor, &chunks)
	}); err != nil {
		t.Fatal(err)
	}
	wantChunks := 1
	if scenario == "rejected checkpoint" {
		wantChunks = 0
	}
	if chunks != wantChunks || (cursor > 0) != (wantChunks > 0) {
		t.Fatalf("checkpoint = %d/%d, want %d chunks", cursor, chunks, wantChunks)
	}
	checkProgress := func(want, staged int) { checkResumeProgress(t, st, account.ID(), run, runner, want, staged) }
	checkProgress(wantChunks, 3)
	// Cleanup must retain checkpoints while River can retry the job.
	if _, err := st.App().Exec(ctx, `UPDATE river_job SET state='retryable' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SweepIndexProgress(ctx, 100); err != nil {
		t.Fatal(err)
	}
	checkProgress(wantChunks, 3)
	if strings.HasSuffix(scenario, "job") || scenario == "cancel request" {
		state := strings.TrimSuffix(scenario, " job")
		if scenario == "cancel request" {
			state = "cancelled"
		}
		if _, err := st.App().Exec(ctx, `UPDATE river_job SET state=$2::text::river_job_state, finalized_at=now() WHERE id=$1`, job.ID, state); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := st.SweepIndexProgress(ctx, 100); err != nil {
				t.Fatal(err)
			}
		}
		checkProgress(0, 0)
		return
	}
	if embed.rejected {
		if _, err := owner.Exec(ctx, `ALTER TABLE index_runs DROP CONSTRAINT test_reject_checkpoint`); err != nil {
			t.Fatal(err)
		}
	}
	changed := strings.HasPrefix(scenario, "changed") || strings.HasPrefix(scenario, "missing")
	changeIndexResumeInputs(t, scenario, st, account.ID(), run, runner, file, current, f)
	embed.first = false
	job = claim(2)
	if err := worker().Work(rivertest.WorkContext(ctx, queue), job); err != nil {
		t.Fatalf("retry: %v", err)
	}
	wantStages, wantFirst := 1, 1
	if changed {
		wantStages, wantFirst = 2, 2
		checkProgress(0, 0)
	}
	if embed.rejected {
		wantFirst = 2
	}
	if exec.calls != wantStages || calls["main.go\nchunk-1"] != wantFirst {
		t.Fatalf("stages=%d embedding calls=%v, want %d stages and %d calls for first chunk", exec.calls, calls, wantStages, wantFirst)
	}
	if scenario == "incremental" {
		checkIncrementalResume(t, st, account.ID(), repo, run, runner)
		return
	}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var active, status string
		var count, vectors, staged int
		if err := tx.QueryRow(ctx, `SELECT x.id::text,x.status,x.chunk_count,
   (SELECT count(*) FROM index_chunks WHERE index_run_id=x.id),
   (SELECT count(*) FROM index_staging s JOIN runner_runs rr ON rr.id=s.runner_run_id WHERE rr.river_job_id=$2)
   FROM repositories r JOIN index_runs x ON x.id=r.active_index_run_id WHERE r.id=$1`, repo, job.ID).Scan(&active, &status, &count, &vectors, &staged); err != nil {
			return err
		}
		if (active == run) == changed || status != "completed" || count != 3 || vectors != 3 || staged != 0 {
			return fmt.Errorf("published %s/%s/count:%d/vectors:%d/staging:%d", active, status, count, vectors, staged)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type resumeFault struct {
	ctx             context.Context
	scenario        string
	st              *store.Store
	jobID           int64
	current         *configfile.Current
	owner           *pgx.Conn
	first, rejected bool
	cancel          context.CancelFunc
	calls           map[string]int
}

func (f *resumeFault) Embed(_ context.Context, inputs []string) ([][]float32, int64, error) {
	text := inputs[0]
	f.calls[text]++
	if f.first && f.scenario == "rejected checkpoint" && !f.rejected {
		if _, err := f.owner.Exec(f.ctx, `ALTER TABLE index_runs ADD CONSTRAINT test_reject_checkpoint CHECK (embedding_cursor = 0) NOT VALID`); err != nil {
			return nil, 0, err
		}
		f.rejected = true
	}
	if f.first && strings.HasSuffix(text, "chunk-2") {
		switch f.scenario {
		case "crash after commit":
			panic("simulated process exit")
		case "cancel before commit":
			f.cancel()
		case "cancel request":
			if _, err := f.st.App().Exec(f.ctx, `UPDATE river_job SET metadata = metadata || '{"cancel_attempted_at":"2026-10-12T00:00:00Z"}'::jsonb WHERE id = $1`, f.jobID); err != nil {
				return nil, 0, err
			}
		case "newer attempt":
			if _, err := f.st.App().Exec(f.ctx, `UPDATE river_job SET attempt = 2 WHERE id = $1`, f.jobID); err != nil {
				return nil, 0, err
			}
		default:
			return nil, 0, errors.New("transient embedding failure")
		}
	}
	vector := make([]float32, f.current.Get().Embedding.Dims)
	vector[0] = 1
	return [][]float32{vector}, 1, nil
}
func changeIndexResumeInputs(
	t *testing.T, scenario string, st *store.Store, accountID, run, runner string,
	file *configfile.File, current *configfile.Current, f *resumeForge,
) {
	t.Helper()
	ctx := t.Context()
	switch scenario {
	case "missing vectors", "missing staging":
		if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
			query, id := `DELETE FROM index_chunks WHERE index_run_id=$1`, run
			if scenario == "missing staging" {
				query, id = `DELETE FROM index_staging WHERE runner_run_id=$1`, runner
			}
			_, err := tx.Exec(ctx, query, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	case "changed commit":
		f.tip = "head-2"
	case "changed ignores":
		f.config = []byte("ignore: [vendor/**]\n")
	case "changed endpoint", "changed model", "changed dimensions":
		next, embedding := *file, *file.Embedding
		switch scenario {
		case "changed endpoint":
			embedding.BaseURL += "/other"
		case "changed model":
			embedding.Model = "other-embed"
		case "changed dimensions":
			embedding.Dims = 4
		}
		next.Embedding = &embedding
		current.Set(&next)
		if _, err := st.EnsureIndexSchema(ctx, "kritika_app", embedding.Model, embedding.Dims, embedding.DocumentPrompt); err != nil {
			t.Fatal(err)
		}
	}
}

type resumeSetup struct {
	st      *store.Store
	file    *configfile.File
	account *configfile.Account
	repo    string
	queue   *river.Client[pgx.Tx]
	claim   func(int) *river.Job[jobs.IndexArgs]
}

func setupIndexResume(t *testing.T, scenario string) *resumeSetup {
	t.Helper()
	st := storetest.Open(t)
	ctx := t.Context()
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	name := "onedr0p/resume-" + uuid.NewString()
	file := configfiletest.Load(t, strings.ReplaceAll(configYAML, "onedr0p/home-ops", name))
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureIndexSchema(ctx, "kritika_app", file.Embedding.Model, 8, ""); err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repo := configfile.RepositoryID(account.ID(), name)
	queue, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	args := jobs.IndexArgs{AccountID: account.ID(), RepositoryID: repo, Trigger: jobs.TriggerPush, Full: scenario == "forced rebuild"}
	inserted, err := queue.Insert(ctx, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.App().Exec(context.Background(), `DELETE FROM river_job WHERE id = $1 OR args->>'repository_id' = $2`, inserted.Job.ID, repo); err != nil {
			t.Error(err)
		}
	})
	claim := func(attempt int) *river.Job[jobs.IndexArgs] {
		t.Helper()
		if _, err := st.App().Exec(ctx, `UPDATE river_job SET state = 'running', attempt = $2, attempted_at = now(), finalized_at = NULL WHERE id = $1`, inserted.Job.ID, attempt); err != nil {
			t.Fatal(err)
		}
		row, err := queue.JobGet(ctx, inserted.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		return &river.Job[jobs.IndexArgs]{JobRow: row, Args: args}
	}
	if scenario == "incremental" {
		seedResumeBase(t, st, file, account, repo, name)
	}
	return &resumeSetup{st, file, account, repo, queue, claim}
}
func resumeCheckpointOwner(t *testing.T, scenario string) *pgx.Conn {
	t.Helper()
	ctx := t.Context()
	if scenario != "rejected checkpoint" {
		return nil
	}
	{
		owner, err := pgx.Connect(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := owner.Exec(context.Background(), `ALTER TABLE index_runs DROP CONSTRAINT IF EXISTS test_reject_checkpoint`); err != nil {
				t.Error(err)
			}
			if err := owner.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		return owner
	}
}
func checkResumeProgress(t *testing.T, st *store.Store, accountID, run, runner string, want, staged int) {
	ctx := t.Context()
	t.Helper()
	if err := st.WithAccount(ctx, accountID, func(tx pgx.Tx) error {
		var vectors, staging int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_chunks WHERE index_run_id=$1),
    (SELECT count(*) FROM index_staging WHERE runner_run_id=$2)`, run, runner).Scan(&vectors, &staging); err != nil {
			return err
		}
		if vectors != want || staging != staged {
			t.Errorf("vectors/staging = %d/%d, want %d/%d", vectors, staging, want, staged)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func workResumeAttempt(t *testing.T, w *Index, ctx context.Context, job *river.Job[jobs.IndexArgs], wantCrash bool) {
	t.Helper()
	crashed := false
	func() {
		defer func() {
			if p := recover(); p != nil {
				if p != "simulated process exit" {
					panic(p)
				}
				crashed = true
			}
		}()
		if err := w.Work(ctx, job); err == nil {
			t.Fatal("first attempt unexpectedly completed")
		}
	}()
	if crashed != wantCrash {
		t.Fatalf("crashed = %v", crashed)
	}
}

func seedResumeBase(t *testing.T, st *store.Store, file *configfile.File, account *configfile.Account, repo, name string) {
	t.Helper()
	ctx := t.Context()
	eff, _ := effective(file.Settings(account, name), nil)
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status,ignore_hash)
   VALUES($1,$2,'base',$3,8,'full','completed',$4) RETURNING id`, account.ID(), repo, file.Embedding.Model, indexIgnoreHash(eff.Ignore)).Scan(&run); err != nil {
			return err
		}
		if err := store.InsertIndexChunks(ctx, tx, account.ID(), repo, run, []store.StagedChunk{{Path: "other.go", Text: "unchanged"}}, [][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
			return err
		}
		return store.ActivateGeneration(ctx, tx, repo, run)
	}); err != nil {
		t.Fatal(err)
	}
}
func checkIncrementalResume(t *testing.T, st *store.Store, account, repo, step, runner string) {
	t.Helper()
	ctx := t.Context()
	if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var commit, status, mode string
		var count, staged int
		if err := tx.QueryRow(ctx, `SELECT a.commit_sha,x.status,x.mode,x.chunk_count,
   (SELECT count(*) FROM index_staging WHERE runner_run_id=$3)
   FROM repositories r JOIN index_runs a ON a.id=r.active_index_run_id JOIN index_runs x ON x.id=$2 WHERE r.id=$1`, repo, step, runner).Scan(&commit, &status, &mode, &count, &staged); err != nil {
			return err
		}
		if commit != "head-1" || status != "completed" || mode != "incremental" || count != 3 || staged != 0 {
			t.Errorf("incremental commit/status/mode/count/staged = %s/%s/%s/%d/%d", commit, status, mode, count, staged)
		}
		var changed, untouched int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE path='main.go'), count(*) FILTER(WHERE path='other.go') FROM index_chunks
   WHERE index_run_id=(SELECT active_index_run_id FROM repositories WHERE id=$1)`, repo).Scan(&changed, &untouched); err != nil {
			return err
		}
		if changed != 3 || untouched != 1 {
			t.Errorf("incremental chunks changed=%d untouched=%d", changed, untouched)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
