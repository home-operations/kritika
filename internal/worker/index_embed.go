package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/textcut"
)

// vectorsFor returns a vector for each chunk of batch and sets each chunk's
// content hash. A chunk whose hash run, the run being built, or activeRun,
// the repository's active generation, already holds takes the vector held.
// The others are embedded, each distinct text once, under one of the
// account's slots of the embedder, of which index jobs take the first slots
// alone. Only their tokens count as usage.
func (w *Index) vectorsFor(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account *configfile.Account, embedder model.Embedder, emb *configfile.Embedding,
	runID, runnerRunID, activeRun string, batch []store.StagedChunk, slots configfile.Slots,
) ([][]float32, error) {
	args := job.Args
	texts := make([]string, len(batch))
	hashes := make([][]byte, len(batch))
	for i := range batch {
		texts[i] = batch[i].EmbedText(emb.DocumentPrompt)
		hashes[i] = contentHash(emb, texts[i])
		batch[i].ContentHash = hashes[i]
	}
	var held map[string][]float32
	err := w.Store.WithAccount(ctx, args.AccountID, func(tx pgx.Tx) error {
		var err error
		held, err = store.IndexVectors(ctx, tx, args.RepositoryID, runID, activeRun, hashes)
		return err
	})
	if err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(batch))
	var missing []string
	sent := map[string]int{}
	for i, h := range hashes {
		if v, ok := held[string(h)]; ok {
			vectors[i] = v
		} else if _, ok := sent[string(h)]; !ok {
			sent[string(h)] = len(missing)
			missing = append(missing, texts[i])
		}
	}
	if len(missing) == 0 {
		w.Metrics.IndexVectors(account.Key(), len(batch), 0)
		return vectors, nil
	}
	var embedded [][]float32
	var used int64
	err = w.withEmbedLease(ctx, account, "embed:"+emb.Model, slots, job, func(ctx context.Context) error {
		var err error
		embedded, used, err = embedder.Embed(ctx, missing)
		w.Metrics.ModelCall(account.Key(), emb.Model, store.RoleEmbedding, adapter.Outcome(err), used, 0, 0, 0, false)
		return err
	})
	if err == nil || used > 0 {
		// Provider usage survives cancellation and failures to store its vectors.
		dctx, cancel := detach(ctx)
		uerr := w.Store.WithAccount(dctx, args.AccountID, func(tx pgx.Tx) error {
			return store.InsertUsage(dctx, tx, store.Usage{
				AccountID: args.AccountID, RepositoryID: args.RepositoryID, RunnerRunID: runnerRunID,
				Role: store.RoleEmbedding, Model: emb.Model, Input: used,
			})
		})
		cancel()
		err = errors.Join(err, uerr)
	}
	if err != nil {
		return nil, err
	}
	if len(embedded) != len(missing) {
		return nil, fmt.Errorf("worker: %d vectors for %d texts", len(embedded), len(missing))
	}
	// Counted once the embedder has answered for every text: a batch whose
	// embedding fails, and is tried again, counts none of its vectors.
	w.Metrics.IndexVectors(account.Key(), len(batch)-len(missing), len(missing))
	for i, h := range hashes {
		if vectors[i] == nil {
			vectors[i] = embedded[sent[string(h)]]
		}
	}
	return vectors, nil
}

// withEmbedLease runs fn, one call to the embedder, while job holds one of
// the first slots on key for the account, and records the wait, as withLease does.
// A job of QueueIndexUpdate comes before the builds that wait for a slot, so
// that an onboarding wave, which takes a slot again for each batch, does not
// keep an indexed repository's update from its turn
// (store.AcquireIndexLease).
func (w *Index) withEmbedLease(
	ctx context.Context, account *configfile.Account, key string, slots configfile.Slots, job *river.Job[jobs.IndexArgs],
	fn func(ctx context.Context) error,
) error {
	waited := time.Now()
	l, err := w.Store.AcquireIndexLease(ctx, account.ID(), key, slots, job.ID, job.Queue == jobs.QueueIndexUpdate)
	if err != nil {
		return err
	}
	w.Metrics.LeaseWait(account.Key(), key, time.Since(waited))
	defer w.releaseLease(ctx, w.Logger, l, key)
	return fn(ctx)
}

// contentHash identifies the vector of an index chunk whose embedded text is
// text: the text as the embedder is sent it, cut to its item limit, and the
// embedder that embeds it. Two chunks of one hash take the same vector.
func contentHash(emb *configfile.Embedding, text string) []byte {
	_, _, itemChars := emb.Bounds()
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%q %d %q\n", emb.Model, emb.Dims, emb.DocumentPrompt) // A hash.Hash never fails to write.
	_, _ = io.WriteString(h, textcut.Prefix(text, itemChars))
	return h.Sum(nil)
}
