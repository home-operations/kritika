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
// file, and chunks kept. The floor and the margin below a query's best
// match, under which a neighbour is noise, are the embedder's.
const (
	similarPerQuery = 4
	similarPerFile  = 2
	similarMax      = 10
)

// similarDepth is how many chunks each search finds for a query, without a
// reranker, for the fusion to rank.
const similarDepth = similarPerQuery

// maxSimilarBody bounds a similar-code request: its queries, each cut to
// contextpack.SimilarQueryChars, with room for their encoding. The paths a
// search leaves out are the run's report, not the request's, so the bound
// holds however many files a change touches.
const maxSimilarBody = 256 << 10

// maxSimilarExclude is the most paths a run's similar-code searches leave
// out. The run's report is read back for every search and carried by each of
// its statements, a dozen in one request. A request's own exclusions, under
// maxSimilarBody, stopped at a few thousand paths. A change of more is mostly
// renamed, vendored or generated files, which similar code helps little, so
// its review goes without, as it did then.
const maxSimilarExclude = 5000

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
	texts := make([]string, len(req.Queries))
	for i, q := range req.Queries {
		if strings.TrimSpace(q) == "" {
			refuse(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("query %d is empty", i))
			return
		}
		texts[i] = textcut.Prefix(q, contextpack.SimilarQueryChars)
		req.Queries[i] = prompt + texts[i]
		chars += len(req.Queries[i])
	}
	outcome = similarError
	var jobID int64
	var excluded []string
	// A request without queries searches nothing, so it reads nothing of the
	// run: it costs what any request that the run token admits does, and one
	// read of the repository's generation.
	if len(req.Queries) > 0 {
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
		queries: req.Queries, texts: texts, exclude: slices.Concat(excluded, req.Exclude),
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
	// then a query cut to contextpack.SimilarQueryChars, which texts holds
	// without the prompt. exclude are the paths left out of the answer.
	queries []string
	texts   []string
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
	if len(vectors) != len(r.queries) {
		return nil, tokens, similarError, fmt.Errorf("gateway: %d vectors for %d queries", len(vectors), len(r.queries))
	}
	// A reranker sorts a pool of candidates for each query, which both
	// searches look deeper to fill.
	depth := similarDepth
	reranker, rr := g.Rerankers.Reranker(file)
	if reranker != nil {
		depth = rr.CandidateCount()
	}
	var hits, named []store.SimilarHit
	started := time.Now()
	err = g.Store.WithAccount(ctx, r.account.ID(), func(tx pgx.Tx) error {
		var err error
		if hits, err = store.SimilarChunks(ctx, tx, runID, vectors, r.exclude, depth); err != nil || !emb.LexicalSearch() {
			return err
		}
		named, err = lexicalHits(ctx, tx, runID, r.texts, vectors, r.exclude, depth)
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
	kept, counts := gateCandidates(fuse(hits, named), emb.Floor(), emb.Margin())
	if reranker != nil {
		kept = topPerQuery(kept, rr.CandidateCount(), counts)
		g.rerank(ctx, file, r, reranker, rr, kept, logger)
	}
	chunks = keepCandidates(topPerQuery(kept, similarPerQuery, counts), counts)
	for k, n := range counts {
		g.Metrics.SimilarHits(r.account.Key(), k.outcome, k.source, n)
	}
	logger.Info("similar chunks", "queries", len(r.queries), "floor", emb.Floor(), "margin", emb.Margin(),
		"lexical", emb.LexicalSearch(), "kept", len(chunks), "tokens", tokens, "hits", countsByOutcome(counts))
	if len(chunks) == 0 {
		return chunks, tokens, similarEmpty, nil
	}
	return chunks, tokens, similarOK, nil
}

// lexicalTerms is how many words of a query are searched for, the longest
// first.
const lexicalTerms = 32

// lexicalCommon is how many of a generation's chunks a word can be in and
// still tell them apart: one in a hundred, at least 20 and at most 200. A
// word in more is left out of the search.
func lexicalCommon(chunks int) int { return min(max(chunks/100, 20), 200) }

// lexicalHits finds, for each query text, the perQuery chunks that hold the
// most weight of its lexicalTerms longest words, outside the excluded paths.
// Each hit's Matched are the query's spellings of the words that the chunk
// holds.
func lexicalHits(
	ctx context.Context, tx pgx.Tx, runID string, texts []string, vectors [][]float32, exclude []string, perQuery int,
) ([]store.SimilarHit, error) {
	words := make([][]lexical.Word, len(texts))
	queries := make([]store.LexicalQuery, len(texts))
	for i, text := range texts {
		w := lexical.Words(text)
		slices.SortStableFunc(w, func(a, b lexical.Word) int { return cmp.Compare(len(b.Lexeme), len(a.Lexeme)) })
		words[i] = w[:min(len(w), lexicalTerms)]
		queries[i].Vector = vectors[i]
		for _, w := range words[i] {
			queries[i].Terms = append(queries[i].Terms, w.Lexeme)
		}
	}
	hits, err := store.LexicalChunks(ctx, tx, runID, queries, exclude, perQuery, lexicalCommon)
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
	// score orders the query's candidates: reciprocal rank fusion of the
	// searches that vouch for it, or the reranker's score.
	score float64
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
// by reciprocal rank fusion, and the count of those that do not, by why
// not. A candidate counts when its similarity is at or above floor and
// within margin of the best similarity of its query, or when it holds at
// least lexicalShare of the lexical weight that its query's best lexical hit
// holds: either search can vouch for it. Each query's scores sit at a height of their own,
// set by the code that it is near. So one floor cuts one query's good
// matches and keeps another's noise. The margin is measured from each
// query's best instead. The lexical weight vouches for a chunk that shares
// the change's rarer names, which an embedder can score low for code that
// does something else with them.
func gateCandidates(cands []candidate, floor, margin float64) ([]candidate, map[hitKey]int) {
	counts := map[hitKey]int{}
	cands = slices.Clone(cands)
	addRanks(cands, func(c candidate) bool { return c.vector }, func(c candidate) float64 { return c.Similarity })
	addRanks(cands, func(c candidate) bool { return c.lexical }, func(c candidate) float64 { return c.Lexical })
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
		case c.vector && c.Similarity < floor:
			counts[hitKey{hitFloor, c.source()}]++
		default:
			counts[hitKey{hitMargin, c.source()}]++
		}
	}
	return kept, counts
}

// addRanks adds to the score of each candidate that found says a search
// found 1 / (rrfK + its rank), by by, among those of its query that it
// found.
func addRanks(cands []candidate, found func(candidate) bool, by func(candidate) float64) {
	var order []int
	for i, c := range cands {
		if found(c) {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(by(cands[b]), by(cands[a])) })
	rank := map[int]int{}
	for _, i := range order {
		rank[cands[i].Query]++
		cands[i].score += 1.0 / float64(rrfK+rank[cands[i].Query])
	}
}

// keepCandidates keeps each chunk once, at most similarPerFile of one file
// and at most similarMax in all, and labels each with its similarity or the
// names it shares with its query. It takes every query's best, the highest score first, before any
// query's second, and so on. So neither one query whose neighbours all score
// high nor one file's neighbouring declarations take every place. It adds
// what it drops to counts.
func keepCandidates(cands []candidate, counts map[hitKey]int) []contextpack.Chunk {
	cands = slices.Clone(cands)
	slices.SortStableFunc(cands, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(b.Similarity, a.Similarity))
	})
	// A candidate's rank is its place among its own query's.
	ranked := make([]rankedHit, len(cands))
	next := map[int]int{}
	for i, c := range cands {
		ranked[i] = rankedHit{hit: c, rank: next[c.Query]}
		next[c.Query]++
	}
	slices.SortStableFunc(ranked, func(a, b rankedHit) int { return cmp.Compare(a.rank, b.rank) })
	seen := map[string]bool{}
	perFile := map[string]int{}
	chunks := make([]contextpack.Chunk, 0, min(len(cands), similarMax))
	for _, r := range ranked {
		c := r.hit
		key := fmt.Sprintf("%s:%d", c.Chunk.Path, c.Chunk.StartLine)
		switch {
		case len(chunks) == similarMax:
			counts[hitKey{hitLimit, c.source()}]++
			continue
		case seen[key]:
			counts[hitKey{hitDuplicate, c.source()}]++
			continue
		case perFile[c.Chunk.Path] == similarPerFile:
			counts[hitKey{hitFileCap, c.source()}]++
			continue
		}
		seen[key] = true
		perFile[c.Chunk.Path]++
		c.Chunk.Ref = similarRef(c)
		chunks = append(chunks, c.Chunk)
		counts[hitKey{hitKept, c.source()}]++
	}
	return chunks
}

// topPerQuery is the n candidates of each query that score highest, in
// their order. It counts the others as past the limit.
func topPerQuery(cands []candidate, n int, counts map[hitKey]int) []candidate {
	cands = slices.Clone(cands)
	slices.SortStableFunc(cands, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(b.Similarity, a.Similarity))
	})
	taken := map[int]int{}
	return slices.DeleteFunc(cands, func(c candidate) bool {
		if taken[c.Query] == n {
			counts[hitKey{hitLimit, c.source()}]++
			return true
		}
		taken[c.Query]++
		return false
	})
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

// rankedHit is a candidate and its place among its query's candidates, 0
// for the best.
type rankedHit struct {
	hit  candidate
	rank int
}

// rerankDocument is what the reranker reads of a chunk: what the index
// embeds of it, its path and declaration, then its text.
func rerankDocument(c contextpack.Chunk) string {
	head := c.Path
	if c.Symbol != "" {
		head += " " + c.Kind + " " + strings.TrimPrefix(c.Scope+"."+c.Symbol, ".")
	}
	return head + "\n" + c.Text
}

// rerank scores each query's candidates by the reranker, in place of their
// fused score, one query at a time under one of the reranker's slots and
// within its deadline. It fails open: a query that the reranker does not
// answer, or that the deadline leaves out, keeps the fused order, and a
// failure is logged and counted as the reranker's call. The tokens that the
// provider reports are recorded as the review's usage.
func (g *Server) rerank(
	ctx context.Context, file *configfile.File, r similarRequest, reranker model.Reranker, rr *configfile.Rerank,
	cands []candidate, logger *slog.Logger,
) {
	byQuery := map[int][]int{}
	for i, c := range cands {
		byQuery[c.Query] = append(byQuery[c.Query], i)
	}
	if len(byQuery) == 0 {
		return
	}
	key := "rerank:" + rr.Model
	waitStarted := time.Now()
	lease, err := g.Store.AcquireLease(ctx, r.account.ID(), key, file.Slots(r.account, rr.Ref), r.jobID)
	logger.Info("similar rerank slot", "elapsed", time.Since(waitStarted), "outcome", adapter.Outcome(err))
	if err != nil {
		logger.Warn("gateway: similar code not reranked: no slot", "error", err)
		return
	}
	defer func() {
		rctx, cancel := detach(ctx)
		defer cancel()
		if _, err := lease.Release(rctx); err != nil {
			logger.Warn("lease not released", "key", key, "error", err)
		}
	}()
	dctx, cancel := context.WithTimeout(ctx, rr.Deadline())
	defer cancel()
	started := time.Now()
	var tokens int64
	reranked := 0
	for _, q := range slices.Sorted(maps.Keys(byQuery)) {
		if dctx.Err() != nil {
			break
		}
		idx := byQuery[q]
		docs := make([]string, len(idx))
		for j, i := range idx {
			docs[j] = rerankDocument(cands[i].Chunk)
		}
		scores, used, err := reranker.Rerank(dctx, r.texts[q], docs)
		tokens += used
		g.Metrics.ModelCall(r.account.Key(), rr.Model, store.RoleRerank, adapter.Outcome(err), used, 0, 0, 0, false)
		if err != nil {
			// The provider's error can carry its URL.
			logger.Warn("gateway: similar code not reranked: the fused order stands", "query", q, "error", err)
			continue
		}
		for j, i := range idx {
			cands[i].score = scores[j]
		}
		reranked++
	}
	logger.Info("similar candidates reranked", "queries", len(byQuery), "reranked", reranked, "candidates", len(cands),
		"elapsed", time.Since(started), "tokens", tokens)
	if tokens == 0 {
		return
	}
	uctx, ucancel := detach(ctx)
	defer ucancel()
	if err := g.Store.WithAccount(uctx, r.account.ID(), func(tx pgx.Tx) error {
		return store.InsertUsage(uctx, tx, store.Usage{
			AccountID: r.account.ID(), RepositoryID: r.repositoryID, ReviewID: r.reviewID,
			Role: store.RoleRerank, Model: rr.Model, Input: tokens,
		})
	}); err != nil {
		logger.Error("gateway: rerank usage not recorded", "error", err)
	}
}
