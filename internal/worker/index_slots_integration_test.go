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
// the account's indexing slot for one batch's embedding call at a time, and
// an update of another of the account's repositories that waits for the
// slot gets it before the build's next batch, not once the build is done.
func TestIndexUpdateEmbedsBetweenOnboardingBatches(t *testing.T) {
	onboarding, update := setupIndexResume(t, "onboarding"), setupIndexResume(t, "")
	ctx := t.Context()
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
	worker := func(e model.Embedder) *Index {
		return &Index{Store: onboarding.st, Current: configfile.NewCurrent(onboarding.file), Forges: &forges{f: &resumeForge{tip: "head"}},
			Logger: slog.New(slog.DiscardHandler), Executor: &resumeExecutor{st: onboarding.st, account: onboarding.account.ID()},
			Embedders: &adapter.Embedders{Build: func(configfile.Embedding) model.Embedder { return e }}, batch: 1}
	}
	built := make(chan error, 1)
	onboardJob := onboarding.claim(1)
	if onboardJob.Queue != jobs.QueueIndex {
		t.Fatalf("the onboarding job is on %s, want %s", onboardJob.Queue, jobs.QueueIndex)
	}
	go func() {
		built <- worker(embedder("onboarding", func() { once.Do(func() { close(inBatch); <-release }) })).
			Work(rivertest.WorkContext(ctx, onboarding.queue), onboardJob)
	}()
	<-inBatch
	updated := make(chan error, 1)
	updateJob := update.claim(1)
	if updateJob.Queue != jobs.QueueIndexUpdate {
		t.Fatalf("the update job is on %s, want %s", updateJob.Queue, jobs.QueueIndexUpdate)
	}
	go func() {
		updated <- worker(embedder("update", nil)).Work(rivertest.WorkContext(ctx, update.queue), updateJob)
	}()
	// The update waits for the slot that the onboarding build holds.
	waitFor(t, 30*time.Second, "the update waiting for the indexing slot", func() bool {
		var waiting bool
		if err := onboarding.st.App().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks l,
				(SELECT hashtextextended('kritika-index-wait ' || $1::text || ' ' || $2::text, 0) AS k) w
			WHERE l.locktype = 'advisory' AND l.mode = 'ShareLock' AND l.granted AND l.objsubid = 1
				AND l.classid::bigint = (w.k >> 32) & 4294967295 AND l.objid::bigint = w.k & 4294967295)`,
			onboarding.account.ID(), "embed:"+onboarding.file.Embedding.Model).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	close(release)
	if err := <-updated; err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := <-built; err != nil {
		t.Fatalf("onboarding: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	next := slices.Index(calls[1:], "onboarding") + 1
	if len(calls) != 6 || calls[0] != "onboarding" || slices.Index(calls, "update") > next {
		t.Fatalf("embedding calls %q, want the update's first before the onboarding build's second", calls)
	}
}
