//go:build integration

package worker

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertest"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// baseRecorder is a resumeExecutor that records the base of each run it is
// given: "" for a full build, the active generation's commit for a step.
type baseRecorder struct {
	*resumeExecutor
	bases []string
}

func (e *baseRecorder) Run(ctx context.Context, spec executor.Spec) executor.Result {
	e.bases = append(e.bases, spec.Job.Base)
	return e.resumeExecutor.Run(ctx, spec)
}

// TestIndexStepsOrRebuildsByFormat: a push steps the active generation
// incrementally whatever index format built it, so a new format does not
// rebuild every pushed repository at once. The onboarding feeder's job then
// rebuilds in full a generation of another format, or one of this format
// that a step of another format advanced during a rollout, and leaves one
// of this format alone.
func TestIndexStepsOrRebuildsByFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		// format is the index format of the build that published the
		// active generation, and mixed whether a step of an older format
		// advanced it since.
		format int
		mixed  bool
		// onboard is the base the onboarding feeder's job is run with, after
		// the push: nil when it runs none, the index being at the tip.
		onboard []string
	}{
		{name: "this format", format: runspec.IndexFormat, onboard: nil},
		{name: "an older format", format: runspec.IndexFormat - 1, onboard: []string{""}},
		{name: "stepped by an older format", format: runspec.IndexFormat, mixed: true, onboard: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupIndexResume(t, "")
			ctx := rivertest.WorkContext(t.Context(), env.queue)
			seedFormatBase(t, env, tc.format, tc.mixed)
			exec := &baseRecorder{resumeExecutor: &resumeExecutor{st: env.st, account: env.account.ID()}}
			w := &Index{
				Store: env.st, Current: configfile.NewCurrent(env.file), Forges: &forges{f: &resumeForge{tip: "head-1"}},
				Logger: slog.New(slog.DiscardHandler), Executor: exec,
				Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return &fakeEmbedder{} }},
			}
			work := func(trigger string) []string {
				t.Helper()
				exec.bases = nil
				job := env.claim(1)
				job.Args.Trigger = trigger
				if err := w.Work(ctx, job); err != nil {
					t.Fatalf("%s job: %v", trigger, err)
				}
				return exec.bases
			}
			if got := work(jobs.TriggerPush); !slices.Equal(got, []string{"base"}) {
				t.Fatalf("push ran with bases %q, want one step from base", got)
			}
			if got := work(jobs.TriggerOnboard); !slices.Equal(got, tc.onboard) {
				t.Fatalf("onboarding job ran with bases %q, want %q", got, tc.onboard)
			}
			var format int
			var commit string
			if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT g.index_format, g.commit_sha FROM repositories r JOIN index_runs g ON g.id = r.active_index_run_id
					WHERE r.id = $1`, env.repo).Scan(&format, &commit)
			}); err != nil {
				t.Fatal(err)
			}
			if commit != "head-1" {
				t.Fatalf("active generation at %s, want head-1", commit)
			}
			if tc.onboard != nil && format != runspec.IndexFormat {
				t.Fatalf("rebuilt generation of format %d, want %d", format, runspec.IndexFormat)
			}
		})
	}
}

// seedFormatBase publishes an active generation at commit "base" that a
// build of format built an hour ago. With mixed, a step of an older format
// ran on it since.
func seedFormatBase(t *testing.T, env *resumeSetup, format int, mixed bool) {
	t.Helper()
	ctx := t.Context()
	eff, _ := effective(env.file.Settings(env.account), nil)
	if err := env.st.WithAccount(ctx, env.account.ID(), func(tx pgx.Tx) error {
		var run string
		if err := tx.QueryRow(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status,ignore_hash,
				index_format,created_at,finished_at)
			VALUES($1,$2,'base',$3,8,'full','completed',$4,$5,now()-interval '2 hours',now()-interval '1 hour') RETURNING id`,
			env.account.ID(), env.repo, env.file.Embedding.Model, indexIgnoreHash(eff.Ignore), format).Scan(&run); err != nil {
			return err
		}
		if err := store.InsertIndexChunks(ctx, tx, env.account.ID(), env.repo, run, []store.StagedChunk{{Path: "other.go", Text: "unchanged"}},
			[][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
			return err
		}
		if err := store.ActivateGeneration(ctx, tx, env.repo, run); err != nil {
			return err
		}
		if mixed {
			if _, err := tx.Exec(ctx, `INSERT INTO index_runs(account_id,repository_id,commit_sha,embed_model,embed_dims,mode,status,ignore_hash,
					index_format,created_at,finished_at)
				VALUES($1,$2,'base',$3,8,'incremental','completed',$4,$5,now()-interval '30 minutes',now()-interval '29 minutes')`,
				env.account.ID(), env.repo, env.file.Embedding.Model, indexIgnoreHash(eff.Ignore), format-1); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
