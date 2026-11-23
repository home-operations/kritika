//go:build integration

package worker

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertest"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
)

// TestIndexUpdateEmbedsBetweenOnboardingBatches: an onboarding build holds
// the embedder's indexing slot for one batch's embedding call at a time, and
// an update of another of the account's repositories that waits for the
// slot gets it before the build's next batch, not once the build is done.
func TestIndexUpdateEmbedsBetweenOnboardingBatches(t *testing.T) {
	onboarding, update := setupIndexResume(t, "onboarding"), setupIndexResume(t, "")
	// A bound on the test's waits alone, for a worker that never reaches its
	// first embedding call or never ends a wait: the test then fails rather
	// than hold up the suite.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var mu sync.Mutex
	var calls []string
	embedder := func(job string, first func()) model.Embedder {
		return indexEmbedFunc(func(_ context.Context, inputs []string) ([][]float32, int64, error) {
			mu.Lock()
			calls = append(calls, job)
			mu.Unlock()
			if first != nil {
				first()
			}
			vectors := make([][]float32, len(inputs))
			for i := range vectors {
				vectors[i] = make([]float32, onboarding.file.Embedding.Dims)
				vectors[i][0] = 1
			}
			return vectors, 1, nil
		})
	}
	inBatch, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	// The onboarding build's first embedding call waits for release, or for
	// the end of the test's context.
	holdFirst := func() {
		once.Do(func() {
			close(inBatch)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}
	worker := func(e model.Embedder) *Index {
		return &Index{Store: onboarding.st, Current: configfile.NewCurrent(onboarding.file), Forges: &forges{f: &resumeForge{tip: "head"}},
			Logger: slog.New(slog.DiscardHandler), Executor: &resumeExecutor{st: onboarding.st, account: onboarding.account.ID()},
			Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return e }}, batch: 1}
	}
	// A test that fails ends both workers' waits, and waits for them to
	// return before the stores they work in are cleaned up.
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		workers.Wait()
	})
	built, updated := make(chan error, 1), make(chan error, 1)
	onboardJob := onboarding.claim(1)
	if onboardJob.Queue != jobs.QueueIndex {
		t.Fatalf("the onboarding job is on %s, want %s", onboardJob.Queue, jobs.QueueIndex)
	}
	workers.Go(func() {
		built <- worker(embedder("onboarding", holdFirst)).Work(rivertest.WorkContext(ctx, onboarding.queue), onboardJob)
	})
	select {
	case <-inBatch:
	case err := <-built:
		t.Fatalf("the onboarding build ended with %v before its first embedding call", err)
	case <-ctx.Done():
		t.Fatalf("the onboarding build's first embedding call not seen: %v", ctx.Err())
	}
	updateJob := update.claim(1)
	if updateJob.Queue != jobs.QueueIndexUpdate {
		t.Fatalf("the update job is on %s, want %s", updateJob.Queue, jobs.QueueIndexUpdate)
	}
	workers.Go(func() {
		updated <- worker(embedder("update", nil)).Work(rivertest.WorkContext(ctx, update.queue), updateJob)
	})
	// The update waits for the slot that the onboarding build holds. The
	// embedder is a provider of the instance, so the wait is of the shared
	// scope, the empty one.
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var waiting bool
		if err := onboarding.st.App().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks l,
				(SELECT hashtextextended('kritika-index-wait ' || $1::text || ' ' || $2::text, 0) AS k) w
			WHERE l.locktype = 'advisory' AND l.mode = 'ShareLock' AND l.granted AND l.objsubid = 1
				AND l.classid::bigint = (w.k >> 32) & 4294967295 AND l.objid::bigint = w.k & 4294967295)`,
			"", "embed:"+onboarding.file.Embedding.Model).Scan(&waiting); err != nil {
			t.Fatalf("the update's wait for the indexing slot not seen: %v", err)
		}
		if waiting {
			break
		}
		select {
		case err := <-updated:
			t.Fatalf("the update ended with %v while the onboarding build held the indexing slot", err)
		case <-ctx.Done():
			t.Fatalf("the update's wait for the indexing slot not seen: %v", ctx.Err())
		case <-poll.C:
		}
	}
	close(release)
	for _, w := range []struct {
		name string
		done chan error
	}{{"update", updated}, {"onboarding", built}} {
		select {
		case err := <-w.done:
			if err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
		case <-ctx.Done():
			t.Fatalf("%s did not end: %v", w.name, ctx.Err())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 6 || calls[0] != "onboarding" {
		t.Fatalf("embedding calls %q, want six starting with onboarding", calls)
	}
	// Index is -1 for a call that is missing, so both are checked present.
	next := slices.Index(calls[1:], "onboarding") + 1
	firstUpdate := slices.Index(calls, "update")
	if next < 1 || firstUpdate < 1 || firstUpdate >= next {
		t.Fatalf("embedding calls %q, want the update's first before the onboarding build's second", calls)
	}
}
