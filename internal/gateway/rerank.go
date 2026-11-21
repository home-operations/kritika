package gateway

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/store"
)

// rerankDocument gives the reranker a chunk's path and qualified symbol,
// followed by its stored text, without the embedding prompt or signature.
func rerankDocument(c contextpack.Chunk) string {
	head := c.Path
	if c.Symbol != "" {
		head += " " + c.Kind + " " + strings.TrimPrefix(c.Scope+"."+c.Symbol, ".")
	}
	return head + "\n" + c.Text
}

// rerankDeadline is when reranking that starts at now ends, the wait for a
// slot included: once the reranker's timeout has passed, or at answerBy, the
// request's own deadline, when that comes first.
func rerankDeadline(now time.Time, rr *configfile.Rerank, answerBy time.Time) time.Time {
	if d := now.Add(rr.Deadline()); d.Before(answerBy) {
		return d
	}
	return answerBy
}

// rerankBatches is, for each query of cands, the indices of its n best
// candidates by their fused order, the best first: what the reranker is
// sent of it.
func rerankBatches(cands []candidate, n int) map[int][]int {
	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return candidateOrder(cands[a], cands[b]) })
	batches := map[int][]int{}
	for _, i := range order {
		if q := cands[i].Query; len(batches[q]) < n {
			batches[q] = append(batches[q], i)
		}
	}
	return batches
}

// rescore gives the candidates of query q at idx the reranker's scores, in
// their order, and puts the query's others, which the reranker was not sent,
// past them.
func rescore(cands []candidate, q int, idx []int, scores []float64) {
	for i := range cands {
		if cands[i].Query == q {
			cands[i].past = true
		}
	}
	for j, i := range idx {
		cands[i].score, cands[i].past = scores[j], false
	}
}

// rerank scores each query's candidates by the reranker, in place of their
// fused score, one query at a time under one of the reranker's slots, all
// within its deadline, the wait for the slot included. The deadline is the
// reranker's timeout, or the request's r.answerBy when that comes first:
// reranking that starts late in the request does not keep the fused order
// from the runner. Each query's rr.CandidateCount() best by the fused order
// are sent, and its others rank after them, in that order. It fails open: a
// query that the reranker does not answer, or that the deadline leaves out,
// keeps the fused order of all of its candidates, and a failure is logged
// and counted as the reranker's call. What it sends is reserved against the
// run's budget first, at four characters a token of each query beside each
// of its documents, as a reranker reads them in pairs, and only if it leaves
// some of the budget: the reranking is optional, and must not take what the
// review's own steps need, nor bring the run to its budget, which refuses
// them. A run with less left keeps the fused order. The reservation is
// settled at the tokens that the provider reports. Nor does it rerank for
// an account at its tokensPerMonth, which the request's embedding can have
// reached since the request was admitted.
// A request that reranked, or that the provider billed tokens or search units
// for, records a usage row of the review at those tokens. A request with only
// search units reported, as Cohere can, still records its row, at no tokens,
// whether or not its ranks were refused: the row records the call, and search
// units count toward neither the usage nor its caps.
func (g *Server) rerank(
	ctx context.Context, file *configfile.File, r similarRequest, reranker model.Reranker, rr *configfile.Rerank,
	cands []candidate, logger *slog.Logger,
) {
	byQuery := rerankBatches(cands, rr.CandidateCount())
	if len(byQuery) == 0 {
		return
	}
	// One query's one candidate sent leads its order whatever it scores,
	// ahead of its others, and there is no other query to rank it against.
	if len(byQuery) == 1 {
		for _, idx := range byQuery {
			if len(idx) == 1 {
				return
			}
		}
	}
	deadline := rerankDeadline(time.Now(), rr, r.answerBy)
	if time.Until(deadline) <= 0 {
		logger.Info("similar code not reranked: the answer is due", "answer_by", r.answerBy)
		return
	}
	docs := map[int][]string{}
	var chars int
	for q, idx := range byQuery {
		for _, i := range idx {
			d := rerankDocument(cands[i].Chunk)
			docs[q] = append(docs[q], d)
			chars += len(r.texts[q]) + len(d)
		}
	}
	// The wait for a slot counts against the deadline, so a reranker whose
	// slots are all held delays the answer no longer than its timeout.
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// The month's cap was read as the request was admitted, before its
	// embedding, whose usage can have reached it since.
	capped, err := g.Store.CapReached(dctx, r.account.ID(), configfile.Limits{TokensPerMonth: file.Settings(r.account).Limits.TokensPerMonth})
	if err != nil {
		logger.Warn("gateway: similar code not reranked: the fused order stands", "error", err)
		return
	}
	if capped != "" {
		logger.Info("similar code not reranked: the account is at its cap", "reason", capped)
		return
	}
	reserved := int64(chars)/4 + 1
	fits, err := g.Store.ReserveGatewayTokensWithin(ctx, r.token, reserved)
	if err != nil {
		logger.Warn("gateway: similar code not reranked: the fused order stands", "error", err)
		return
	}
	if !fits {
		logger.Info("similar code not reranked: the run's budget has less left than the reranking would read", "tokens", reserved)
		return
	}
	var spent model.RerankUsage
	defer func() { g.chargeRun(ctx, r.token, spent.Tokens-reserved, logger) }()
	key := "rerank:" + rr.Model
	waitStarted := time.Now()
	lease, err := g.Store.AcquireLease(dctx, r.account.ID(), key, file.Slots(r.account, rr.Ref), r.jobID)
	logger.Info("similar rerank slot", "elapsed", time.Since(waitStarted), "outcome", adapter.Outcome(err))
	if err != nil {
		logger.Warn("gateway: similar code not reranked: no slot", "error", err)
		return
	}
	// Released once the reranker has answered, and here only when it panics.
	release := sync.OnceFunc(func() {
		rctx, cancel := detach(ctx)
		defer cancel()
		if _, err := lease.Release(rctx); err != nil {
			logger.Warn("lease not released", "key", key, "error", err)
		}
	})
	defer release()
	started := time.Now()
	reranked := 0
	for _, q := range slices.Sorted(maps.Keys(byQuery)) {
		if dctx.Err() != nil {
			break
		}
		idx := byQuery[q]
		scores, used, err := reranker.Rerank(dctx, r.texts[q], docs[q])
		spent.Tokens += used.Tokens
		spent.SearchUnits += used.SearchUnits
		g.Metrics.ModelCall(r.account.Key(), rr.Model, store.RoleRerank, adapter.Outcome(err), used.Tokens, 0, 0, 0, false)
		if err != nil {
			// The error names neither the endpoint nor what the reranker
			// answered: either can carry a key, a document or the query.
			logger.Warn("gateway: similar code not reranked: the fused order stands", "query", q, "error", err)
			continue
		}
		rescore(cands, q, idx, scores)
		reranked++
	}
	// The slot first: the usage and the charge can wait for Postgres, and the
	// reranker's other callers must not wait with them.
	release()
	if reranked == len(byQuery) {
		rerankedAll(cands)
	}
	logger.Info("similar candidates reranked", "queries", len(byQuery), "reranked", reranked, "candidates", len(cands),
		"elapsed", time.Since(started), "tokens", spent.Tokens, "search_units", spent.SearchUnits)
	// A provider that bills by the call, as Cohere does by search unit, can
	// report no tokens, and can bill a call whose ranks are refused: the row
	// still records the request.
	if reranked == 0 && spent.Tokens == 0 && spent.SearchUnits == 0 {
		return
	}
	uctx, ucancel := detach(ctx)
	defer ucancel()
	if err := g.Store.WithAccount(uctx, r.account.ID(), func(tx pgx.Tx) error {
		return store.InsertUsage(uctx, tx, store.Usage{
			AccountID: r.account.ID(), RepositoryID: r.repositoryID, ReviewID: r.reviewID,
			Role: store.RoleRerank, Model: rr.Model, Input: spent.Tokens,
		})
	}); err != nil {
		logger.Error("gateway: rerank usage not recorded", "error", err)
	}
}

// rerankedAll compares the candidates of different queries by the
// reranker's scores, once it scored every query of the request: they are
// then on its one scale.
func rerankedAll(cands []candidate) {
	for i := range cands {
		cands[i].shared = cands[i].score
	}
}
