package gateway

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/lexical"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

// lexicalWithin is how long the search by name of a request due by answerBy
// can take, starting at now: tuning's Gateway.LexicalTimeout, which bounds
// its first statement (store.LexicalChunks) and its looks past full files
// together, or what is left before
// answerBy when that is less, in whole milliseconds, which is what a
// statement timeout takes. Under a millisecond is none, and not a timeout of
// 0, which would not bound the search at all.
func lexicalWithin(now, answerBy time.Time) time.Duration {
	return max(min(tuning.Get().Gateway.LexicalTimeout, answerBy.Sub(now)).Truncate(time.Millisecond), 0)
}

// searchByName is lexicalHits for the request in a savepoint of tx, within
// lexicalWithin of the request's answerBy: a slow search by name ends while
// the runner still waits for the vector search's hits, and none is made
// once the answer is due. Its failure leaves the vector search's hits to
// answer the request alone: it is logged and counted, and no hit is
// returned. Only the request's own end, which the transaction cannot
// outlive, is an error. A query whose window the file cap leaves short is
// searched again past the files that fill it, as pastFullFiles does for the
// vector search, each time in a savepoint of its own and within what is left
// of the same time: one that fails or runs out of it ends the looking, and
// the hits found until then are kept.
func (g *Server) searchByName(
	ctx context.Context, tx pgx.Tx, r similarRequest, runID string, vectors [][]float32, depth int, logger *slog.Logger,
) ([]store.SimilarHit, error) {
	started := time.Now()
	within := lexicalWithin(started, r.answerBy)
	if within == 0 {
		logger.Info("similar code searched by vector alone: the answer is due", "answer_by", r.answerBy)
		return nil, nil
	}
	search := namedWithin(ctx, tx, runID, depth, started.Add(within))
	named, err := search(r.texts, r.languages, vectors, r.exclude)
	outcome := adapter.Outcome(err)
	var past error
	if err == nil {
		named, past = pastFullNames(named, r.texts, r.languages, vectors, r.exclude, depth, r.perQuery, search)
	}
	if err != nil && ctx.Err() != nil {
		outcome = "canceled"
	}
	g.Metrics.LexicalSearch(r.account.Key(), outcome, time.Since(started))
	if ctx.Err() != nil {
		return nil, cmp.Or(err, past)
	}
	if err != nil {
		logger.Warn("gateway: similar code searched by vector alone: the search by name failed", "error", err)
		return nil, nil
	}
	if past != nil {
		logger.Warn("gateway: similar code searched by name without looking past every full window", "error", past)
	}
	return named, nil
}

// namedWithin is the search by name of runID, depth deep, that searchByName
// makes, its looks past full files included: lexicalHits of texts, of
// languages, and their vectors, outside exclude, each in a savepoint of tx
// (inSavepoint) within what is left before deadline.
func namedWithin(
	ctx context.Context, tx pgx.Tx, runID string, depth int, deadline time.Time,
) func(texts, languages []string, vectors [][]float32, exclude store.Exclusions) ([]store.SimilarHit, error) {
	return func(texts, languages []string, vectors [][]float32, exclude store.Exclusions) ([]store.SimilarHit, error) {
		var named []store.SimilarHit
		err := inSavepoint(ctx, tx, deadline, func(sp pgx.Tx) error {
			var err error
			named, err = lexicalHits(ctx, sp, runID, texts, languages, vectors, exclude, depth)
			return err
		})
		return named, err
	}
}

// pastFullNames adds to named, the search by name's window of each of texts,
// what search finds for a query past the files of which its hits hold
// Gateway.SimilarPerFile or more, while the file cap leaves the query short of
// target chunks, its window is full, and its query's names still vouch for
// the window's last hit (namedBy): pastFullFiles' looking, for the search by
// name. search is the search by name of texts, of languages, and their
// vectors, depth deep, outside exclude. A search's failure ends the looking, and pastFullNames
// returns what it found until then, with the error.
func pastFullNames(
	named []store.SimilarHit, texts, languages []string, vectors [][]float32, exclude store.Exclusions, depth, target int,
	search func(texts, languages []string, vectors [][]float32, exclude store.Exclusions) ([]store.SimilarHit, error),
) ([]store.SimilarHit, error) {
	qualifies := func(found []store.SimilarHit, last store.SimilarHit) bool {
		best := slices.MaxFunc(found, func(a, b store.SimilarHit) int { return cmp.Compare(a.Lexical, b.Lexical) })
		return namedBy(last.Lexical, best.Lexical)
	}
	return pastFull(named, len(vectors), target, func(found, window []store.SimilarHit) []string {
		return fullFiles(found, window, depth, target, qualifies)
	}, func(q int, full []string) ([]store.SimilarHit, error) {
		past := store.Exclusions{Run: exclude.Run, Paths: slices.Concat(exclude.Paths, full)}
		return search(texts[q:q+1], languages[q:q+1], vectors[q:q+1], past)
	})
}

// lexicalCommon bars the words too common to tell a generation's chunks
// apart, as tuning's Gateway.LexicalCommonPer, LexicalCommonLeast and
// LexicalCommonShare say.
func lexicalCommon() store.LexicalBar {
	g := tuning.Get().Gateway
	return store.LexicalBar{Per: g.LexicalCommonPer, Least: g.LexicalCommonLeast, Share: g.LexicalCommonShare}
}

// lexicalAlone is the most of a generation's chunks that a word of a query
// can be in and still find a chunk alone, without another of the query's
// words or a compound identifier of it: no more than a query keeps.
func lexicalAlone() int { return tuning.Get().Gateway.SimilarPerQuery }

// lexicalHits finds, for each query text, the perQuery chunks that hold the
// most weight of its Gateway.LexicalTerms longest words, outside the excluded paths:
// its words as lexical.CodeWords has them of its language in languages, or
// of none when languages is nil. Each hit's Matched are the query's
// spellings of the words that the chunk holds.
func lexicalHits(
	ctx context.Context, tx pgx.Tx, runID string, texts, languages []string, vectors [][]float32, exclude store.Exclusions, perQuery int,
) ([]store.SimilarHit, error) {
	words := make([][]lexical.Word, len(texts))
	queries := make([]store.LexicalQuery, len(texts))
	for i, text := range texts {
		var language string
		if languages != nil {
			language = languages[i]
		}
		w := lexical.CodeWords(language, text)
		slices.SortStableFunc(w, func(a, b lexical.Word) int { return cmp.Compare(len(b.Lexeme), len(a.Lexeme)) })
		words[i] = w[:min(len(w), tuning.Get().Gateway.LexicalTerms)]
		queries[i].Vector = vectors[i]
		for _, w := range words[i] {
			queries[i].Terms = append(queries[i].Terms, store.LexicalTerm{Lexeme: w.Lexeme, Compound: w.Compound})
		}
	}
	hits, err := store.LexicalChunks(ctx, tx, runID, queries, exclude, perQuery, lexicalCommon(), lexicalAlone())
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
