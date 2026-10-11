package worker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// mergeTimeout bounds writing a split review's summary, inside the time
// publishing reads the agent's run in, and mergeMaxOutputTokens its answer:
// a summary, with room for the review model's reasoning at its effort.
const (
	mergeTimeout         = 100 * time.Second
	mergeMaxOutputTokens = 16384
)

// mergeFailedNote is what the summary states when it joins the parts' own
// because the call that writes one from them failed.
const mergeFailedNote = "The summary joins the parts' own: writing one from them failed"

// mergeSummary writes a split review's summary from its parts' with one
// call to the review model at its effort, recorded and charged as the
// review's, and the findings they reported; the parts' checked notes, which
// res already carries, are kept, and with one part's summary there is
// nothing to merge. It returns the answer carriedDiagram reads: the merge
// call's, shaped as the agent's, when it wrote the summary, answer
// otherwise; and the note the summary states when the call failed and res
// keeps the parts' summaries joined, "" otherwise.
func (p *publishPhase) mergeSummary(ctx context.Context, parts []store.AgentPart, answer json.RawMessage, res *review.Result,
	findings []review.Finding,
) (json.RawMessage, string) {
	merge := make([]review.MergePart, 0, len(parts))
	for _, part := range parts {
		var s review.Summary
		if len(part.Summary) == 0 || json.Unmarshal(part.Summary, &s) != nil {
			continue
		}
		merge = append(merge, review.MergePart{Paths: part.Paths, Summary: s})
	}
	if len(merge) < 2 {
		return answer, ""
	}
	ctx, cancel := context.WithTimeout(ctx, mergeTimeout)
	defer cancel()
	raw, err := p.merge(ctx, merge, findings)
	var s review.Summary
	if err == nil {
		s, err = review.ParseMerge(raw, p.parse)
	}
	if err != nil {
		p.logger.Warn("split review's summary not written from its parts'", "error", err)
		return answer, mergeFailedNote
	}
	s.Checked = res.Summary.Checked
	res.Summary = s
	return mergedAnswer(raw), ""
}

// mergedAnswer is the merge call's answer, a summary, shaped as the agent's.
func mergedAnswer(raw string) json.RawMessage { return json.RawMessage(`{"summary":` + raw + `}`) }

// merge asks the review model for the summary of a split review's parts,
// recording the call against the review and charging it to the account,
// and returns its answer.
func (p *publishPhase) merge(ctx context.Context, parts []review.MergePart, findings []review.Finding) (string, error) {
	if err := p.checkMonthCap(ctx); err != nil {
		return "", err
	}
	ref := p.settings.Models.Review
	// The review model's attempts may take all of the call's time: that is
	// about one slow answer at the review's effort, which half of it would
	// cut, and a merge that fails only joins the parts' summaries.
	call, err := p.structured(ctx, ref, p.settings.Models.Fallback, store.ModelCallMerge, store.RoleReview, "merge", "review", false)
	if err != nil {
		return "", err
	}
	body, err := p.pullBody(ctx)
	if err != nil {
		return "", err
	}
	resp, err := call.completer.Complete(ctx, model.CompletionRequest{
		System: review.MergeSystemPrompt(p.parse.Diagram),
		User:   review.BuildMerge(p.pr.title, body, parts, findings, p.settings.Agent.MaxPromptTokens),
		Model:  ref.Model(), Fallbacks: call.fallbacks, Session: "merge-" + p.reviewID, Effort: p.settings.Models.Effort,
		Schema: review.MergeSchema(p.parse.Diagram), SchemaName: "summary", MaxTokens: mergeMaxOutputTokens,
	})
	p.w.Metrics.ModelCall(p.account.Key(), adapter.ServedRef(call.served.Ref, resp.Model), store.RoleReview, adapter.Outcome(err),
		resp.InputTokens, resp.CachedTokens, resp.OutputTokens, resp.CostUSD, resp.Unpriced)
	return resp.Raw, err
}
