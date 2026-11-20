package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/textcut"
)

// Similarity retrieval bounds: neighbours per query, chunks kept of one
// file, and chunks kept. The floor and the margin below a query's best
// match, under which a neighbour is noise, are the embedder's.
const (
	similarPerQuery = 4
	similarPerFile  = 2
	similarMax      = 10
)

// maxSimilarBody bounds a similar-code request: its queries, each cut to
// contextpack.SimilarQueryChars, with room for their encoding. The paths a
// search leaves out are the run's report, not the request's, so the bound
// holds however many files a change touches.
const maxSimilarBody = 256 << 10

// What became of a similar-code request, as kritika_similar_requests_total
// counts it, and of a hit that it found, as kritika_similar_hits_total does.
const (
	similarOK         = "ok"
	similarEmpty      = "empty"
	similarNoQueries  = "no_queries"
	similarNoEmbedder = "no_embedder"
	similarNoIndex    = "no_index"
	similarTooLarge   = "too_large"
	similarInvalid    = "invalid"
	similarRefused    = "refused"
	similarError      = "error"

	hitKept      = "kept"
	hitFloor     = "floor"
	hitMargin    = "margin"
	hitDuplicate = "duplicate"
	hitFileCap   = "file_cap"
	hitLimit     = "limit"
)

// similarCode searches the run's repository index for the request's queries.
// It serves stage 4 of the review, asked once over the diff's hunks before
// the agent starts, and the agent's own search_code tool. The embedding is
// reserved against the run's budget at four characters a token before it
// runs. A request without queries embeds nothing and only learns whether
// the repository has an index, which the runner asks when the diff gives it
// nothing to search for, to know whether to offer search_code. The answer
// is the default branch's code as the index has it, which can be older or
// newer than the pull request's head and base.
func (g *Server) similarCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := g.admit(w, r)
	if !ok {
		return
	}
	kind, outcome := "unknown", similarInvalid
	defer func() { g.Metrics.SimilarRequest(c.account.Key(), kind, outcome) }()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSimilarBody))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		outcome = similarTooLarge
		refuse(w, http.StatusRequestEntityTooLarge, "invalid_request", err.Error())
		return
	}
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "reading the request: "+err.Error())
		return
	}
	var req contextpack.SimilarRequest
	if err := json.Unmarshal(body, &req); err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "decoding the request: "+err.Error())
		return
	}
	switch req.Kind {
	case "", contextpack.SimilarKindDiff:
		kind = contextpack.SimilarKindDiff
	case contextpack.SimilarKindSearch:
		kind = contextpack.SimilarKindSearch
	default:
		refuse(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("the query kind %q is neither %q nor %q", req.Kind,
			contextpack.SimilarKindDiff, contextpack.SimilarKindSearch))
		return
	}
	if len(req.Queries) > contextpack.SimilarQueries {
		refuse(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("the request must carry at most %d queries, not %d", contextpack.SimilarQueries, len(req.Queries)))
		return
	}
	var prompt string
	if e := c.file.Embedding; e != nil {
		prompt = e.PromptFor(kind == contextpack.SimilarKindSearch)
	}
	var chars int
	for i, q := range req.Queries {
		if strings.TrimSpace(q) == "" {
			refuse(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("query %d is empty", i))
			return
		}
		req.Queries[i] = prompt + textcut.Prefix(q, contextpack.SimilarQueryChars)
		chars += len(req.Queries[i])
	}
	outcome = similarError
	var jobID int64
	var excluded []string
	err = g.Store.WithAccount(ctx, c.grant.AccountID, func(tx pgx.Tx) error {
		var err error
		if jobID, err = store.RunnerRunJobID(ctx, tx, c.grant.RunID); err != nil {
			return err
		}
		excluded, err = store.SimilarExclude(ctx, tx, c.grant.RunID)
		return err
	})
	if err != nil {
		c.logger.Error("gateway: run not read", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the run could not be read")
		return
	}
	var reserved int64
	if chars > 0 {
		reserved = int64(chars)/4 + 1
		if !g.reserve(ctx, w, c, reserved) {
			outcome = similarRefused
			return
		}
	}
	chunks, tokens, searched, err := g.similar(ctx, c.file, similarRequest{
		account: c.account, repositoryID: c.grant.RepositoryID, reviewID: c.usageReview(), jobID: jobID, kind: kind,
		queries: req.Queries, exclude: slices.Concat(excluded, req.Exclude),
	}, c.logger)
	outcome = searched
	if reserved > 0 || tokens > 0 {
		cctx, cancel := detach(ctx)
		defer cancel()
		if cerr := g.Store.ChargeGatewayToken(cctx, c.token, tokens-reserved); cerr != nil {
			c.logger.Error("gateway: similar code not charged", "error", cerr)
		}
	}
	if err != nil {
		// The embedder's error can carry its key or URL. The runner only
		// learns that the search failed.
		c.logger.Warn("gateway: similar code failed", "error", err)
		refuse(w, http.StatusBadGateway, "upstream_error", "similar-code retrieval failed")
		return
	}
	if chunks == nil {
		chunks = []contextpack.Chunk{}
	}
	indexed := searched != similarNoEmbedder && searched != similarNoIndex
	out, err := json.Marshal(contextpack.SimilarResponse{Chunks: chunks, Indexed: indexed})
	if err != nil {
		outcome = similarError
		refuse(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// similarRequest is one search of a repository's index.
type similarRequest struct {
	account                *configfile.Account
	repositoryID, reviewID string
	// jobID is the review's job, for which the embedding slot is held.
	jobID int64
	// kind is the queries' contextpack kind, never "".
	kind string
	// queries are the texts to embed: the embedder's prompt for their kind,
	// then a query cut to contextpack.SimilarQueryChars. exclude are the
	// paths left out of the answer.
	queries []string
	exclude []string
}

// similar searches the repository's active index generation for the chunks
// nearest each query and leaves out the given paths. Its outcome says what
// became of the search: no chunks, when the instance has no embedder, the
// repository has no completed generation built with it, or there are no
// queries. It returns the tokens that the embedding spent, recorded as the
// review's usage.
func (g *Server) similar(
	ctx context.Context, file *configfile.File, r similarRequest, logger *slog.Logger,
) (chunks []contextpack.Chunk, tokens int64, outcome string, err error) {
	embedder, emb := g.Embedders.Embedder(file)
	if embedder == nil {
		return nil, 0, similarNoEmbedder, nil
	}
	var runID string
	err = g.Store.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
		var err error
		runID, err = store.ActiveGenerationFor(ctx, tx, r.repositoryID, emb.Model, emb.Dims, emb.DocumentPrompt)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, similarNoIndex, nil
	}
	if err != nil {
		return nil, 0, similarError, err
	}
	if len(r.queries) == 0 {
		return nil, 0, similarNoQueries, nil
	}
	waitStarted := time.Now()
	lease, err := g.Store.AcquireLease(ctx, r.account.ID(), "embed:"+emb.Model, file.Slots(r.account, emb.Ref), r.jobID)
	logger.Info("similar embedding slot", "elapsed", time.Since(waitStarted), "outcome", adapter.Outcome(err))
	if err != nil {
		return nil, 0, similarError, err
	}
	var vectors [][]float32
	embedStarted := time.Now()
	vectors, tokens, err = embedder.Embed(ctx, r.queries)
	logger.Info("similar queries embedded", "queries", len(r.queries), "elapsed", time.Since(embedStarted),
		"outcome", adapter.Outcome(err))
	g.Metrics.ModelCall(r.account.Key(), emb.Model, store.RoleEmbedding, adapter.Outcome(err), tokens, 0, 0, 0, false)
	rctx, cancel := detach(ctx)
	if _, rerr := lease.Release(rctx); rerr != nil {
		logger.Warn("lease not released", "key", "embed:"+emb.Model, "error", rerr)
	}
	cancel()
	if err == nil || tokens > 0 {
		// Provider usage must survive cancellation and failed vector searches.
		uctx, ucancel := detach(ctx)
		uerr := g.Store.WithAccount(uctx, r.account.ID(), func(tx pgx.Tx) error {
			return store.InsertUsage(uctx, tx, store.Usage{
				AccountID: r.account.ID(), RepositoryID: r.repositoryID, ReviewID: r.reviewID,
				Role: store.RoleEmbedding, Model: emb.Model, Input: tokens,
			})
		})
		ucancel()
		err = errors.Join(err, uerr)
	}
	if err != nil {
		return nil, tokens, similarError, err
	}
	var hits []store.SimilarHit
	started := time.Now()
	err = g.Store.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
		var err error
		hits, err = store.SimilarChunks(ctx, tx, runID, vectors, r.exclude, similarPerQuery)
		return err
	})
	searched := adapter.Outcome(err)
	if err != nil && ctx.Err() != nil {
		searched = "canceled"
	}
	g.Metrics.VectorSearch(r.account.Key(), searched, time.Since(started))
	if err != nil {
		return nil, tokens, similarError, err
	}
	for _, s := range bestSimilarity(hits) {
		g.Metrics.SimilarBest(r.account.Key(), r.kind, s)
	}
	chunks, counts := keepSimilar(hits, emb.Floor(), emb.Margin())
	for reason, n := range counts {
		g.Metrics.SimilarHits(r.account.Key(), reason, n)
	}
	logger.Info("similar chunks", "queries", len(r.queries), "floor", emb.Floor(), "margin", emb.Margin(), "kept", len(chunks),
		"tokens", tokens, "hits", counts)
	if len(chunks) == 0 {
		return chunks, tokens, similarEmpty, nil
	}
	return chunks, tokens, similarOK, nil
}

// bestSimilarity is the similarity of each query's best hit, of the queries
// that have any.
func bestSimilarity(hits []store.SimilarHit) map[int]float64 {
	best := map[int]float64{}
	for _, h := range hits {
		if s, ok := best[h.Query]; !ok || h.Similarity > s {
			best[h.Query] = h.Similarity
		}
	}
	return best
}

// keepSimilar is the hits at or above floor and within margin of the best hit
// of their query. It keeps each chunk once, at most similarPerFile of one
// file and at most similarMax in all, and labels each with its similarity.
// It takes every query's best hit, the most similar first, before any
// query's second, and so on. So neither one query whose neighbours all score
// high nor one file's neighbouring declarations take every place. Each
// query's scores sit at a height of their own, set by the code that it is
// near. So one floor cuts one query's good matches and keeps another's
// noise. The margin is measured from each query's best instead.
func keepSimilar(hits []store.SimilarHit, floor, margin float64) ([]contextpack.Chunk, map[string]int) {
	counts := map[string]int{}
	best := bestSimilarity(hits)
	kept := make([]store.SimilarHit, 0, len(hits))
	for _, h := range hits {
		switch {
		case h.Similarity < floor:
			counts[hitFloor]++
		// The tolerance keeps a hit right at the margin, which the
		// subtraction's rounding can put a hair below it.
		case h.Similarity < best[h.Query]-margin-1e-9:
			counts[hitMargin]++
		default:
			kept = append(kept, h)
		}
	}
	slices.SortStableFunc(kept, func(a, b store.SimilarHit) int { return cmp.Compare(b.Similarity, a.Similarity) })
	// A hit's rank is its place among its own query's kept hits.
	ranked := make([]rankedHit, len(kept))
	next := map[int]int{}
	for i, h := range kept {
		ranked[i] = rankedHit{hit: h, rank: next[h.Query]}
		next[h.Query]++
	}
	slices.SortStableFunc(ranked, func(a, b rankedHit) int { return cmp.Compare(a.rank, b.rank) })
	seen := map[string]bool{}
	perFile := map[string]int{}
	chunks := make([]contextpack.Chunk, 0, min(len(kept), similarMax))
	for _, r := range ranked {
		h := r.hit
		key := fmt.Sprintf("%s:%d", h.Chunk.Path, h.Chunk.StartLine)
		switch {
		case len(chunks) == similarMax:
			counts[hitLimit]++
			continue
		case seen[key]:
			counts[hitDuplicate]++
			continue
		case perFile[h.Chunk.Path] == similarPerFile:
			counts[hitFileCap]++
			continue
		}
		seen[key] = true
		perFile[h.Chunk.Path]++
		h.Chunk.Ref = fmt.Sprintf("similarity %.2f", h.Similarity)
		chunks = append(chunks, h.Chunk)
		counts[hitKept]++
	}
	return chunks, counts
}

// rankedHit is a hit and its place among its query's hits, 0 for the best.
type rankedHit struct {
	hit  store.SimilarHit
	rank int
}
