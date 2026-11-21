package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/lexical"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/textcut"
)

// Similarity retrieval bounds: neighbours per query, chunks kept of one
// file, and chunks kept when a request asks for no other number. The floor
// and the margin below a query's best match, under which a neighbour is
// noise, are the embedder's.
const (
	similarPerQuery = 4
	similarPerFile  = 2
	similarMax      = 10
)

// similarDepth is how many chunks each search finds for a query, without a
// reranker, for the fusion to rank: twice what a query keeps, so that the
// places of chunks that the file cap or a duplicate turns away go to the
// query's next ones.
const similarDepth = 2 * similarPerQuery

// maxSimilarBody bounds a similar-code request: its queries, each cut to
// contextpack.SimilarQueryChars, with room for their encoding. The paths a
// search leaves out are the run's report, not the request's, so the bound
// holds however many files a change touches.
const maxSimilarBody = 256 << 10

// similarAnswerWithin is how long after a similar-code request arrives its
// search by name and its reranking end at the latest, leaving the vector
// search's hits, in the fused order, to the queries that they have not
// reached. A run waits a minute for its answer from before it sends
// the request, whatever the embedding and the searches took of it. The rest
// is for the answer, its accounting, and the request's way to the gateway. A
// variable for the tests.
var similarAnswerWithin = 50 * time.Second

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

	hitKept   = "kept"
	hitFloor  = "floor"
	hitMargin = "margin"
	// hitLexicalFloor is a chunk that only the search by name found, under
	// lexicalShare of its query's best weight by name.
	hitLexicalFloor = "lexical_floor"
	hitDuplicate    = "duplicate"
	hitFileCap      = "file_cap"
	hitLimit        = "limit"
)

// similarCode searches the run's repository index for the request's queries.
// It serves stage 4 of the review, asked once over the diff's hunks before
// the agent starts, and the agent's own search_code tool. The search
// reserves what it spends against the run's budget before it spends it, and
// the request is refused when the budget is spent before it embeds. A
// request without queries embeds nothing and only learns whether the
// repository has an index, which the runner asks when the diff gives it
// nothing to search for, to know whether to offer search_code. The answer
// is the default branch's code as the index has it, which can be older or
// newer than the pull request's head and base.
func (g *Server) similarCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	arrived := time.Now()
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
	texts := make([]string, len(req.Queries))
	for i, q := range req.Queries {
		if strings.TrimSpace(q) == "" {
			refuse(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("query %d is empty", i))
			return
		}
		texts[i] = textcut.Prefix(q, contextpack.SimilarQueryChars)
		req.Queries[i] = prompt + texts[i]
	}
	outcome = similarError
	var jobID int64
	// A request without queries searches nothing, so it reads nothing of the
	// run: it costs what any request that the run token admits does, and one
	// read of the repository's generation.
	if len(req.Queries) > 0 {
		err = g.Store.WithAccount(ctx, c.grant.AccountID, func(tx pgx.Tx) error {
			var err error
			jobID, err = store.RunnerRunJobID(ctx, tx, c.grant.RunID)
			return err
		})
		if err != nil {
			c.logger.Error("gateway: run not read", "error", err)
			refuse(w, http.StatusInternalServerError, "server_error", "the run could not be read")
			return
		}
	}
	chunks, searched, err := g.similar(ctx, c.file, similarRequest{
		account: c.account, repositoryID: c.grant.RepositoryID, reviewID: c.usageReview(), jobID: jobID, kind: kind,
		token: c.token, queries: req.Queries, texts: texts, limit: answerLimit(req.Limit),
		exclude:  store.Exclusions{Run: c.grant.RunID, Paths: req.Exclude},
		answerBy: arrived.Add(similarAnswerWithin),
	}, c.logger)
	outcome = searched
	if errors.Is(err, errBudgetSpent) {
		reason := fmt.Sprintf("the run's budget of %d tokens is spent", c.grant.Budget)
		c.logger.Info("gateway: call refused", "reason", reason)
		refuse(w, http.StatusTooManyRequests, model.BudgetCode, reason)
		return
	}
	if errors.Is(err, errBudgetUnchecked) {
		c.logger.Error("gateway: call not reserved", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the run's budget could not be checked")
		return
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
	// token is the run's gateway token, whose budget the embedding and the
	// reranking are reserved against and charged to.
	token string
	// queries are the texts to embed: the embedder's prompt for their kind,
	// then a query cut to contextpack.SimilarQueryChars, which texts holds
	// without the prompt. exclude are the paths left out of the answer.
	queries []string
	texts   []string
	exclude store.Exclusions
	// limit is the most chunks the answer holds.
	limit int
	// answerBy is when the search by name and the reranking end at the
	// latest, so that the answer reaches the runner while it still waits.
	answerBy time.Time
}

// Why similar did not search: the run's budget was spent before it, or it
// could not be read.
var (
	errBudgetSpent     = errors.New("gateway: the run's budget is spent")
	errBudgetUnchecked = errors.New("gateway: the run's budget could not be checked")
)

// answerLimit is the most chunks an answer holds when its request asks for
// asked: similarMax unless it asks for a number, which is at most
// contextpack.SimilarLimit.
func answerLimit(asked int) int {
	if asked <= 0 {
		return similarMax
	}
	return min(asked, contextpack.SimilarLimit)
}

// reserveRun reserves tokens against the budget of the run whose gateway
// token is token, before a call spends them. It returns errBudgetSpent when
// the run spent its budget, and an error wrapping errBudgetUnchecked when the
// budget could not be read.
func (g *Server) reserveRun(ctx context.Context, token string, tokens int64) error {
	ok, err := g.Store.ReserveGatewayTokens(ctx, token, tokens)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %w", errBudgetUnchecked, err)
	case !ok:
		return errBudgetSpent
	}
	return nil
}

// chargeRun adds tokens, which can be negative, to what the run whose
// gateway token is token spent: a call's actual spend less its reservation.
// It outlives ctx, as the spend does.
func (g *Server) chargeRun(ctx context.Context, token string, tokens int64, logger *slog.Logger) {
	if tokens == 0 {
		return
	}
	cctx, cancel := detach(ctx)
	defer cancel()
	if err := g.Store.ChargeGatewayToken(cctx, token, tokens); err != nil {
		logger.Error("gateway: similar code not charged", "error", err)
	}
}

// similar searches the repository's active index generation for the chunks
// nearest each query and leaves out the given paths. Its outcome says what
// became of the search: no chunks, when the instance has no embedder, the
// repository has no completed generation built with it, or there are no
// queries. The embedding is reserved against the run's budget at four
// characters a token before it runs, and settled at the tokens that the
// provider reports, recorded as the review's usage, before anything is
// reranked.
func (g *Server) similar(
	ctx context.Context, file *configfile.File, r similarRequest, logger *slog.Logger,
) (chunks []contextpack.Chunk, outcome string, err error) {
	embedder, emb := g.Embedders.Embedder(file)
	if embedder == nil {
		return nil, similarNoEmbedder, nil
	}
	var runID string
	var lexicalReady bool
	err = g.Store.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
		var err error
		runID, lexicalReady, err = store.ActiveGenerationFor(ctx, tx, r.repositoryID, emb.Model, emb.Dims, emb.DocumentPrompt)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, similarNoIndex, nil
	}
	if err != nil {
		return nil, similarError, err
	}
	if len(r.queries) == 0 {
		return nil, similarNoQueries, nil
	}
	var chars int
	for _, q := range r.queries {
		chars += len(q)
	}
	reserved := int64(chars)/4 + 1
	if err := g.reserveRun(ctx, r.token, reserved); err != nil {
		if errors.Is(err, errBudgetSpent) {
			return nil, similarRefused, err
		}
		return nil, similarError, err
	}
	waitStarted := time.Now()
	lease, err := g.Store.AcquireLease(ctx, r.account.ID(), "embed:"+emb.Model, file.Slots(r.account, emb.Ref), r.jobID)
	logger.Info("similar embedding slot", "elapsed", time.Since(waitStarted), "outcome", adapter.Outcome(err))
	if err != nil {
		g.chargeRun(ctx, r.token, -reserved, logger)
		return nil, similarError, err
	}
	embedStarted := time.Now()
	vectors, tokens, err := embedder.Embed(ctx, r.queries)
	logger.Info("similar queries embedded", "queries", len(r.queries), "elapsed", time.Since(embedStarted),
		"outcome", adapter.Outcome(err))
	g.Metrics.ModelCall(r.account.Key(), emb.Model, store.RoleEmbedding, adapter.Outcome(err), tokens, 0, 0, 0, false)
	// The slot first: the charge can wait for the run's row, and the
	// embedder's other callers must not wait with it.
	rctx, cancel := detach(ctx)
	if _, rerr := lease.Release(rctx); rerr != nil {
		logger.Warn("lease not released", "key", "embed:"+emb.Model, "error", rerr)
	}
	cancel()
	g.chargeRun(ctx, r.token, tokens-reserved, logger)
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
		return nil, similarError, err
	}
	if len(vectors) != len(r.queries) {
		return nil, similarError, fmt.Errorf("gateway: %d vectors for %d queries", len(vectors), len(r.queries))
	}
	// A reranker sorts a pool of candidates for each query, which both
	// searches look deeper to fill, and never shallower than without one:
	// the candidates past those it is sent are what a query falls back on.
	depth := similarDepth
	reranker, rr := g.Rerankers.Reranker(file)
	if reranker != nil {
		depth = max(depth, rr.CandidateCount())
	}
	var hits, named []store.SimilarHit
	started := time.Now()
	// One snapshot for both searches: fuse joins their hits by path and line,
	// which a publication between them could give to two versions of a chunk.
	err = g.Store.WithAccountSnapshot(ctx, r.account.ID(), func(tx pgx.Tx) error {
		var err error
		if hits, err = store.SimilarChunks(ctx, tx, runID, vectors, r.exclude, depth); err != nil || !emb.LexicalSearch() || !lexicalReady {
			return err
		}
		named, err = g.searchByName(ctx, tx, r, runID, vectors, depth, logger)
		return err
	})
	searched := adapter.Outcome(err)
	if err != nil && ctx.Err() != nil {
		searched = "canceled"
	}
	g.Metrics.VectorSearch(r.account.Key(), searched, time.Since(started))
	if err != nil {
		return nil, similarError, err
	}
	kept, counts, best := gateCandidates(fuse(hits, named), emb.Floor(), emb.Margin())
	for _, s := range best {
		g.Metrics.SimilarBest(r.account.Key(), r.kind, s)
	}
	if reranker != nil {
		g.rerank(ctx, file, r, reranker, rr, kept, logger)
	}
	chunks = keepCandidates(kept, counts, r.limit)
	for k, n := range counts {
		g.Metrics.SimilarHits(r.account.Key(), k.outcome, k.source, n)
	}
	logger.Info("similar chunks", "queries", len(r.queries), "floor", emb.Floor(), "margin", emb.Margin(),
		"lexical", emb.LexicalSearch(), "kept", len(chunks), "tokens", tokens, "hits", countsByOutcome(counts))
	if len(chunks) == 0 {
		return chunks, similarEmpty, nil
	}
	return chunks, similarOK, nil
}

// lexicalTimeout bounds the search by name, which a request can do without.
// The search is one statement (store.LexicalChunks), so its statement timeout
// bounds the whole of it.
const lexicalTimeout = 5 * time.Second

// lexicalWithin is how long the search by name of a request due by answerBy
// can take, starting at now: lexicalTimeout, or what is left before
// answerBy when that is less, in whole milliseconds, which is what a
// statement timeout takes. Under a millisecond is none, and not a timeout of
// 0, which would not bound the search at all.
func lexicalWithin(now, answerBy time.Time) time.Duration {
	return max(min(lexicalTimeout, answerBy.Sub(now)).Truncate(time.Millisecond), 0)
}

// searchByName is lexicalHits for the request in a savepoint of tx, within
// lexicalWithin of the request's answerBy: a slow search by name ends while
// the runner still waits for the vector search's hits, and none is made
// once the answer is due. Its failure leaves the vector search's hits to
// answer the request alone: it is logged and counted, and no hit is
// returned. Only the request's own end, which the transaction cannot
// outlive, is an error.
func (g *Server) searchByName(
	ctx context.Context, tx pgx.Tx, r similarRequest, runID string, vectors [][]float32, depth int, logger *slog.Logger,
) ([]store.SimilarHit, error) {
	started := time.Now()
	within := lexicalWithin(started, r.answerBy)
	if within == 0 {
		logger.Info("similar code searched by vector alone: the answer is due", "answer_by", r.answerBy)
		return nil, nil
	}
	var named []store.SimilarHit
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		timeout := strconv.FormatInt(within.Milliseconds(), 10)
		if _, err := sp.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`, timeout); err != nil {
			return err
		}
		var err error
		named, err = lexicalHits(ctx, sp, runID, r.texts, vectors, r.exclude, depth)
		return err
	})
	outcome := adapter.Outcome(err)
	if err != nil && ctx.Err() != nil {
		outcome = "canceled"
	}
	g.Metrics.LexicalSearch(r.account.Key(), outcome, time.Since(started))
	if ctx.Err() != nil {
		return nil, err
	}
	if err != nil {
		logger.Warn("gateway: similar code searched by vector alone: the search by name failed", "error", err)
		return nil, nil
	}
	return named, nil
}

// lexicalTerms is how many words of a query are searched for, the longest
// first.
const lexicalTerms = 32

// lexicalCommon is how many of a generation's chunks a word can be in and
// still tell them apart: one in a hundred, at least 20 and at most
// store.LexicalCommonMax, 200, and never more than half of them. A word in
// more is left out of the search. The half bars, in a generation of fewer
// than 40 chunks, a word that nearly all of them hold: its weight is too
// small to say anything of a chunk, yet it would be its query's best by
// name, and every chunk holding it would clear lexicalShare.
var lexicalCommon = store.LexicalBar{Per: 100, Least: 20, Share: 2}

// lexicalAlone is the most of a generation's chunks that a word of a query
// can be in and still find a chunk alone, without another of the query's
// words or a compound identifier of it: no more than a query keeps.
const lexicalAlone = similarPerQuery

// lexicalHits finds, for each query text, the perQuery chunks that hold the
// most weight of its lexicalTerms longest words, outside the excluded paths.
// Each hit's Matched are the query's spellings of the words that the chunk
// holds.
func lexicalHits(
	ctx context.Context, tx pgx.Tx, runID string, texts []string, vectors [][]float32, exclude store.Exclusions, perQuery int,
) ([]store.SimilarHit, error) {
	words := make([][]lexical.Word, len(texts))
	queries := make([]store.LexicalQuery, len(texts))
	for i, text := range texts {
		w := lexical.Words(text)
		slices.SortStableFunc(w, func(a, b lexical.Word) int { return cmp.Compare(len(b.Lexeme), len(a.Lexeme)) })
		words[i] = w[:min(len(w), lexicalTerms)]
		queries[i].Vector = vectors[i]
		for _, w := range words[i] {
			queries[i].Terms = append(queries[i].Terms, store.LexicalTerm{Lexeme: w.Lexeme, Compound: w.Compound})
		}
	}
	hits, err := store.LexicalChunks(ctx, tx, runID, queries, exclude, perQuery, lexicalCommon, lexicalAlone)
	if err != nil {
		return nil, err
	}
	for i := range hits {
		hits[i].Matched = spellings(words[hits[i].Query], hits[i].Matched)
	}
	return hits, nil
}

// spellings is the query's spellings of matched, its lexemes: the whole
// identifiers, which say what their parts would, or the parts when no
// whole one matched.
func spellings(words []lexical.Word, matched []string) []string {
	var whole, parts []string
	for _, w := range words {
		switch {
		case !slices.Contains(matched, w.Lexeme):
		case w.Whole:
			whole = append(whole, w.Spelling)
		default:
			parts = append(parts, w.Spelling)
		}
	}
	if len(whole) > 0 {
		return whole
	}
	return parts
}

// Which searches found a hit, as kritika_similar_hits_total counts it.
const (
	sourceVector  = "vector"
	sourceLexical = "lexical"
	sourceBoth    = "both"
)

// hitKey is what became of the hits that a count counts, and which searches
// found them.
type hitKey struct{ outcome, source string }

// countsByOutcome adds up counts across sources, for the log.
func countsByOutcome(counts map[hitKey]int) map[string]int {
	out := map[string]int{}
	for k, n := range counts {
		out[k.outcome] += n
	}
	return out
}

// rrfK is reciprocal rank fusion's constant: a candidate's score in its
// query is the sum, over the searches that found it, of 1 / (rrfK + its rank
// in that search's answer), counted from 1. It is the value of the paper
// that introduced the fusion (Cormack, Clarke and Buettcher, 2009), which
// damps the weight of the very top ranks.
const rrfK = 60

// lexicalShare is the least share of its query's best lexical weight that a
// hit needs to count as similar code by its names alone.
const lexicalShare = 0.5

// candidate is a chunk found for a query, by one search or both.
type candidate struct {
	store.SimilarHit
	// vector and lexical say which searches found it, and close and named
	// which of them vouch for it: its similarity clears the floor and the
	// margin, or its lexical weight clears lexicalShare of its query's best.
	vector, lexical, close, named bool
	// score orders the query's candidates: reciprocal rank fusion of every
	// search that found it, whether or not that search vouches for it, or the
	// reranker's score.
	score float64
	// shared orders the candidates of different queries that hold the same
	// place in their own: the fusion's score, which every query has on one
	// scale, or the reranker's when it scored every query of the request.
	shared float64
	// past is a candidate of a query that the reranker scored, which was
	// below the candidates sent to it in the fused order and so keeps its
	// fused score: it ranks after every one of them.
	past bool
}

func (c candidate) source() string {
	switch {
	case c.vector && c.lexical:
		return sourceBoth
	case c.lexical:
		return sourceLexical
	}
	return sourceVector
}

// fuse merges each query's hits of the vector search and of the lexical
// search, named, into one candidate per chunk.
func fuse(hits, named []store.SimilarHit) []candidate {
	out := make([]candidate, 0, len(hits)+len(named))
	key := func(h store.SimilarHit) string {
		return fmt.Sprintf("%d:%s:%d", h.Query, h.Chunk.Path, h.Chunk.StartLine)
	}
	found := map[string]int{}
	for _, h := range hits {
		found[key(h)] = len(out)
		out = append(out, candidate{SimilarHit: h, vector: true})
	}
	for _, h := range named {
		if j, ok := found[key(h)]; ok && !out[j].lexical {
			out[j].lexical, out[j].Lexical, out[j].Matched = true, h.Lexical, h.Matched
			continue
		}
		out = append(out, candidate{SimilarHit: h, lexical: true})
	}
	return out
}

// gateCandidates is the candidates that count as similar code, each scored
// by reciprocal rank fusion, the count of those that do not, by why not, and
// the similarity of each query's best candidate. A candidate counts when its similarity is at or above floor and
// within margin of the best similarity of its query, or when it holds at
// least lexicalShare of the lexical weight that its query's best lexical hit
// holds: either search can vouch for it. Each query's scores sit at a height of their own,
// set by the code that it is near. So one floor cuts one query's good
// matches and keeps another's noise. The margin is measured from each
// query's best instead. The lexical weight vouches for a chunk that shares
// the change's rarer names, which an embedder can score low for code that
// does something else with them.
func gateCandidates(cands []candidate, floor, margin float64) ([]candidate, map[hitKey]int, map[int]float64) {
	counts := map[hitKey]int{}
	cands = slices.Clone(cands)
	addRanks(cands, func(c candidate) bool { return c.vector }, func(c candidate) float64 { return c.Similarity })
	addRanks(cands, func(c candidate) bool { return c.lexical }, func(c candidate) float64 { return c.Lexical })
	for i := range cands {
		cands[i].shared = cands[i].score
	}
	best, bestLexical := map[int]float64{}, map[int]float64{}
	for _, c := range cands {
		if s, ok := best[c.Query]; !ok || c.Similarity > s {
			best[c.Query] = c.Similarity
		}
		bestLexical[c.Query] = max(bestLexical[c.Query], c.Lexical)
	}
	kept := make([]candidate, 0, len(cands))
	for _, c := range cands {
		// The tolerance keeps a hit right at the margin, which the
		// subtraction's rounding can put a hair below it.
		c.close = c.Similarity >= floor && c.Similarity >= best[c.Query]-margin-1e-9
		c.named = c.lexical && c.Lexical >= lexicalShare*bestLexical[c.Query]
		switch {
		case c.close || c.named:
			kept = append(kept, c)
		case !c.vector:
			counts[hitKey{hitLexicalFloor, c.source()}]++
		case c.Similarity < floor:
			counts[hitKey{hitFloor, c.source()}]++
		default:
			counts[hitKey{hitMargin, c.source()}]++
		}
	}
	return kept, counts, best
}

// addRanks adds to the score of each candidate that found says a search
// found 1 / (rrfK + its rank), by by, among those of its query that it
// found. Of candidates that by ties, the one nearest the query ranks first.
func addRanks(cands []candidate, found func(candidate) bool, by func(candidate) float64) {
	var order []int
	for i, c := range cands {
		if found(c) {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Or(cmp.Compare(by(cands[b]), by(cands[a])), cmp.Compare(cands[b].Similarity, cands[a].Similarity))
	})
	rank := map[int]int{}
	for _, i := range order {
		rank[cands[i].Query]++
		cands[i].score += 1.0 / float64(rrfK+rank[cands[i].Query])
	}
}

// keepCandidates keeps each chunk once, at most similarPerQuery of one
// query, similarPerFile of one file and limit in all, and labels each with
// its similarity or the names it shares with its query. It takes every
// query's best, by its order among its query's (candidateOrder) and then by
// its shared score, before any query's second, and so on. So neither one
// query whose neighbours all score high nor one file's neighbouring
// declarations take every place. A query's turn is spent only on a chunk it
// keeps: one that a duplicate or the file cap turns away gives the turn to
// the query's next candidate, which competes for the same round's places as
// the other queries' first. It adds what it drops to counts.
func keepCandidates(cands []candidate, counts map[hitKey]int, limit int) []contextpack.Chunk {
	queues := map[int][]candidate{}
	for _, c := range cands {
		queues[c.Query] = append(queues[c.Query], c)
	}
	queries := slices.Sorted(maps.Keys(queues))
	for _, q := range queries {
		slices.SortStableFunc(queues[q], candidateOrder)
	}
	key := func(c candidate) string { return fmt.Sprintf("%s:%d", c.Chunk.Path, c.Chunk.StartLine) }
	seen := map[string]bool{}
	perFile, perQuery := map[string]int{}, map[int]int{}
	chunks := make([]contextpack.Chunk, 0, min(len(cands), limit))
	for len(chunks) < limit {
		pick := -1
		for _, q := range queries {
			queue := queues[q]
			for len(queue) > 0 && perQuery[q] < similarPerQuery {
				c := queue[0]
				if seen[key(c)] {
					counts[hitKey{hitDuplicate, c.source()}]++
				} else if perFile[c.Chunk.Path] == similarPerFile {
					counts[hitKey{hitFileCap, c.source()}]++
				} else {
					break
				}
				queue = queue[1:]
			}
			queues[q] = queue
			if len(queue) == 0 || perQuery[q] == similarPerQuery {
				continue
			}
			if pick < 0 || nextTurn(perQuery[q], queue[0], perQuery[pick], queues[pick][0]) {
				pick = q
			}
		}
		if pick < 0 {
			break
		}
		c := queues[pick][0]
		queues[pick] = queues[pick][1:]
		seen[key(c)] = true
		perFile[c.Chunk.Path]++
		perQuery[pick]++
		c.Chunk.Ref = similarRef(c)
		chunks = append(chunks, c.Chunk)
		counts[hitKey{hitKept, c.source()}]++
	}
	for _, q := range queries {
		for _, c := range queues[q] {
			counts[hitKey{hitLimit, c.source()}]++
		}
	}
	return chunks
}

// nextTurn reports whether a query that kept kept chunks, whose next
// candidate is c, takes its turn before one that kept other and whose next
// is o: the query that kept fewer, then the candidate the reranker scored
// over one past its candidates, then the higher shared score, then the
// nearer.
func nextTurn(kept int, c candidate, other int, o candidate) bool {
	return cmp.Or(cmp.Compare(kept, other), comparePast(c, o), cmp.Compare(o.shared, c.shared), cmp.Compare(o.Similarity, c.Similarity)) < 0
}

// candidateOrder orders the candidates of one query, the best first: by
// score, then by similarity, and, when the reranker scored the query, the
// candidates it scored before those past them.
func candidateOrder(a, b candidate) int {
	return cmp.Or(comparePast(a, b), cmp.Compare(b.score, a.score), cmp.Compare(b.Similarity, a.Similarity))
}

// comparePast orders a candidate that the reranker scored, or that it was
// not asked about, before one past its candidates.
func comparePast(a, b candidate) int {
	switch {
	case a.past == b.past:
		return 0
	case b.past:
		return -1
	}
	return 1
}

// similarNames is how many of the names a chunk shares with its query its
// label shows.
const similarNames = 3

// similarRef labels a kept chunk for the prompt: its similarity when that
// vouches for it, and the names it shares with its query when it holds any.
// A chunk that only its names vouch for shows no similarity, which sits
// under the floor and would read as a poor match.
func similarRef(c candidate) string {
	var parts []string
	if c.close {
		parts = append(parts, fmt.Sprintf("similarity %.2f", c.Similarity))
	}
	if len(c.Matched) > 0 {
		parts = append(parts, "shares "+strings.Join(c.Matched[:min(len(c.Matched), similarNames)], ", "))
	}
	return strings.Join(parts, "; ")
}
