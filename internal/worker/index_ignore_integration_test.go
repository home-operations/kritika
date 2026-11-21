//go:build integration

package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

func checkIndexIgnoreChanges(
	ctx context.Context, t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], lf *localForge,
	current *configfile.Current, account, repository, head string,
) {
	t.Helper()
	original := current.Get()
	t.Cleanup(func() {
		current.Set(original)
		lf.setTip(head)
		// Cleanup also runs after a subtest aborts. Restore the index for
		// later checks.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		job, err := queue.Insert(cctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: jobs.TriggerPush}, nil)
		if err != nil {
			t.Error(err)
			return
		}
		indexJobDone(cctx, t, st, job.Job.ID)
	})
	active := activeIndexGeneration(ctx, t, st, account, repository)
	r, err := git.PlainOpen(lf.dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, global, repoConfig string
		other, rebuild           bool
	}{
		{name: "global rule added at same commit", global: "ignore: [other.go]\n", rebuild: true},
		{name: "unchanged rules at same commit", global: "ignore: [other.go]\n"},
		{name: "global rule removed at same commit", other: true, rebuild: true},
		{name: "repository rule added", repoConfig: "ignore: [other.go]\n", rebuild: true},
		{name: "repository rule removed", repoConfig: "ignore: []\n", other: true, rebuild: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current.Set(configfiletest.Load(t, configYAML+"\n"+tt.global))
			if tt.repoConfig != "" {
				if err := os.WriteFile(filepath.Join(lf.dir, ".kritika.yaml"), []byte(tt.repoConfig), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := wt.Add(".kritika.yaml"); err != nil {
					t.Fatal(err)
				}
				commit, err := wt.Commit(tt.name, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@x", When: time.Now()}})
				if err != nil {
					t.Fatal(err)
				}
				lf.setTip(commit.String())
			}
			job, err := queue.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: jobs.TriggerPush}, nil)
			if err != nil {
				t.Fatal(err)
			}
			indexJobDone(ctx, t, st, job.Job.ID)
			next := activeIndexGeneration(ctx, t, st, account, repository)
			if (next != active) != tt.rebuild {
				t.Fatalf("generation %s -> %s, want rebuilt %v", active, next, tt.rebuild)
			}
			if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
				var other bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM index_chunks WHERE index_run_id=$1 AND path='other.go')`, next).Scan(&other); err != nil {
					return err
				}
				if other != tt.other {
					t.Errorf("unchanged other.go indexed=%v, want %v", other, tt.other)
				}
				g, err := store.FindGeneration(ctx, tx, next)
				if err != nil {
					return err
				}
				if g.IgnoreHash == "" {
					t.Error("generation has no ignore fingerprint")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			active = next
		})
	}
}

// checkIndexRepoConfigOff checks that an index job that finds the
// repository's .kritika.yaml turning it off keeps the index, and marks the
// repository so that the retention sweep drops the index once it is off for
// the grace. A later job that finds it on again clears the mark and the
// sweep's clock, and builds the index again once it was dropped.
func checkIndexRepoConfigOff(
	ctx context.Context, t *testing.T, st *store.Store, queue *river.Client[pgx.Tx], lf *localForge,
	current *configfile.Current, account, repository, head string,
) {
	t.Helper()
	t.Cleanup(func() {
		lf.setTip(head)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		job, err := queue.Insert(cctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: jobs.TriggerOnboard}, nil)
		if err != nil {
			t.Error(err)
			return
		}
		indexJobDone(cctx, t, st, job.Job.ID)
	})
	r, err := git.PlainOpen(lf.dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	push := func(repoConfig, trigger string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(lf.dir, ".kritika.yaml"), []byte(repoConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(".kritika.yaml"); err != nil {
			t.Fatal(err)
		}
		commit, err := wt.Commit(repoConfig, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@x", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		lf.setTip(commit.String())
		job, err := queue.Insert(ctx, jobs.IndexArgs{AccountID: account, RepositoryID: repository, Trigger: trigger}, nil)
		if err != nil {
			t.Fatal(err)
		}
		indexJobDone(ctx, t, st, job.Job.ID)
	}
	sweep := func() {
		t.Helper()
		f := current.Get()
		if _, err := st.SweepStoppedIndexes(ctx, time.Hour, func(accountID string, traits configfile.RepoTraits) bool {
			a, ok := f.AccountByID(accountID)
			return ok && f.Runs(a, traits)
		}); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string) {
		t.Helper()
		if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, repository)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(what string, wantOff, wantStopped, wantIndexed bool) string {
		t.Helper()
		var off, stopped bool
		var active string
		if err := st.WithAccount(ctx, account, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT repo_config_off, stopped_at IS NOT NULL, coalesce(active_index_run_id::text, '')
				FROM repositories WHERE id = $1`, repository).Scan(&off, &stopped, &active)
		}); err != nil {
			t.Fatal(err)
		}
		if off != wantOff || stopped != wantStopped || (active != "") != wantIndexed {
			t.Fatalf("%s: off %v, stopped %v, generation %q; want off %v, stopped %v, indexed %v",
				what, off, stopped, active, wantOff, wantStopped, wantIndexed)
		}
		return active
	}

	active := check("before", false, false, true)
	push("enabled: false\n", jobs.TriggerPush)
	if got := check("off in .kritika.yaml", true, false, true); got != active {
		t.Fatalf("the generation %s became %s while indexing is off", active, got)
	}
	sweep()
	check("off in .kritika.yaml, swept within the grace", true, true, true)

	push("ignore: []\n", jobs.TriggerPush)
	check("on again within the grace", false, false, true)
	sweep()
	check("on again within the grace, swept", false, false, true)

	push("enabled: false\n", jobs.TriggerPush)
	sweep()
	exec(`UPDATE repositories SET stopped_at = now() - interval '2 hours' WHERE id = $1`)
	sweep()
	check("off in .kritika.yaml past the grace", true, false, false)

	push("ignore: []\n", jobs.TriggerOnboard)
	rebuilt := check("on again after the drop", false, false, true)
	checkRunStatus(ctx, t, st, account, rebuilt, store.IndexCompleted)

	// An admin's default of off is not the file's word: the dashboard turned
	// the repository on, and a file that leaves enabled unset keeps it on.
	original := current.Get()
	t.Cleanup(func() { current.Set(original) })
	current.Set(configfiletest.Load(t, configYAML+"\nenabled: false\n"))
	push("ignore: [unset.go]\n", jobs.TriggerPush)
	check("turned on in the dashboard under a default of off", false, false, true)
}
