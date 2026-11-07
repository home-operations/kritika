package worker

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/textcut"
)

// What a re-review is shown of a finding's thread: its latest replies, each
// cut.
const (
	maxAnswers  = 3
	answerBytes = 600
)

// threadAnswers are the replies in the threads of the findings whose inline
// comments roots holds, by that comment's id, oldest first and the latest
// maxAnswers of each. Only the replies of people with write access count:
// on a public repository anyone can reply, and an answer can end a finding.
// A bot's reply, the bot's own among them, is none. A finding's comment can
// itself sit in a conversation that an older one opened, as Forgejo and
// Gitea group the comments on one line of a review, and then the replies to
// that conversation are its.
func threadAnswers(ctx context.Context, client forge.Client, owner, repo string, number int, roots map[int64]bool) (
	map[int64][]review.Answer, error,
) {
	if len(roots) == 0 {
		return nil, nil
	}
	comments, err := client.ListInline(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("worker: read finding threads: %w", err)
	}
	thread := make(map[int64]int64, len(roots))
	for id := range roots {
		thread[id] = id
	}
	for _, c := range comments {
		if roots[c.ID] {
			thread[c.ID] = cmp.Or(c.InReplyTo, c.ID)
		}
	}
	wanted := map[int64]bool{}
	for _, t := range thread {
		wanted[t] = true
	}
	writes := map[string]bool{}
	replies := map[int64][]review.Answer{}
	for _, c := range comments {
		if !wanted[c.InReplyTo] || c.AuthorIsBot {
			continue
		}
		can, known := writes[c.Author]
		if !known {
			perm, err := client.Permission(ctx, owner, repo, c.Author)
			if err != nil {
				return nil, fmt.Errorf("worker: look up the permission of %s: %w", c.Author, err)
			}
			can = forge.CanWrite(perm)
			writes[c.Author] = can
		}
		if can {
			replies[c.InReplyTo] = append(replies[c.InReplyTo], review.Answer{
				ID: c.ID, Author: c.Author, Text: textcut.Truncate(c.Body, answerBytes),
			})
		}
	}
	out := map[int64][]review.Answer{}
	for id, t := range thread {
		if rs := replies[t]; len(rs) > 0 {
			out[id] = rs[max(0, len(rs)-maxAnswers):]
		}
	}
	return out, nil
}

// answerRoots are the inline comments that the findings of prior opened.
func answerRoots(prior []priorFinding) map[int64]bool {
	roots := map[int64]bool{}
	for _, pf := range prior {
		if pf.commentID != 0 {
			roots[pf.commentID] = true
		}
	}
	return roots
}

// withAnswers is findings, the last review's as prior holds them, with the
// answers in their threads. It also returns those answers by each finding's
// id in the prompt, to keep as what the review was shown.
func withAnswers(findings []review.Finding, prior []priorFinding, answers map[int64][]review.Answer) (
	[]review.Finding, map[string][]review.Answer,
) {
	var shown map[string][]review.Answer
	for i, pf := range prior {
		if as := answers[pf.commentID]; pf.commentID != 0 && len(as) > 0 {
			findings[i].Answers = as
			if shown == nil {
				shown = map[string][]review.Answer{}
			}
			shown[review.PriorID(pf.Finding)] = as
		}
	}
	return findings, shown
}

// answeredPrior is a finding of the last review that this review accepted
// the answer to, with the reply that answered it last.
type answeredPrior struct {
	priorFinding
	answer review.Answer
}

// heldAnswers are, by the finding's id in the prompt, the last answer of each
// finding that this review listed as answered, where the thread holds the
// answers that the review was shown and no other. A thread that changed
// since, by a reply edited, removed or added, holds an answer the review did
// not weigh, so its finding stays open for the next review. A thread that
// cannot be read holds none.
func (p *publishPhase) heldAnswers(ctx context.Context, ids []string) map[string]review.Answer {
	if len(ids) == 0 {
		return nil
	}
	var shown map[string][]review.Answer
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		shown, err = store.ReviewAnswers(ctx, tx, p.reviewID)
		return err
	})
	if err != nil {
		p.logger.Warn("answered findings kept open", "error", err)
		return nil
	}
	roots := map[int64]bool{}
	for _, pf := range p.prior.findings {
		if id := review.PriorID(pf.Finding); pf.commentID != 0 && slices.Contains(ids, id) && len(shown[id]) > 0 {
			roots[pf.commentID] = true
		}
	}
	owner, repo := p.pr.ownerRepo()
	now, err := threadAnswers(ctx, p.client, owner, repo, p.pr.number, roots)
	if err != nil {
		p.logger.Warn("answered findings kept open", "error", err)
		return nil
	}
	return heldOf(p.prior.findings, ids, shown, now)
}

// heldOf is heldAnswers' answer from what the review was shown, by the
// finding's id in the prompt, and what the threads hold now, by the id of
// the comment that opened each.
func heldOf(prior []priorFinding, ids []string, shown map[string][]review.Answer, now map[int64][]review.Answer) map[string]review.Answer {
	held := map[string]review.Answer{}
	for _, pf := range prior {
		id := review.PriorID(pf.Finding)
		if was := shown[id]; pf.commentID != 0 && slices.Contains(ids, id) && len(was) > 0 && slices.Equal(was, now[pf.commentID]) {
			held[id] = was[len(was)-1]
		}
	}
	return held
}

// answeredDismissals are the dismissals that a published review records for
// the findings it settled as answered: kritika's concession to each answer,
// so that the finding stops counting and no review raises it again.
func answeredDismissals(accountID, pullRequestID, login string, answered []answeredPrior) []store.Dismissal {
	out := make([]store.Dismissal, 0, len(answered))
	for _, a := range answered {
		out = append(out, store.Dismissal{
			AccountID: accountID, PullRequestID: pullRequestID, Fingerprint: review.Fingerprint(a.Finding), Finding: a.Finding,
			Reason: "kritika accepted the answer of " + a.answer.Author + ": " + textcut.Truncate(a.answer.Text, 200),
			Author: login, CommentID: a.answer.ID, Source: store.DismissedByConcession,
		})
	}
	return out
}

// recheckAnswered checks again, as the review is delivered, that each
// finding it settled as answered still has the answers it was shown: the
// publication can be delivered long after it was prepared. One whose
// thread changed since, or whose answer's author lost write access, is no
// longer answered: recheckAnswered returns it, carried on, for the review to
// hold as the next one will judge it.
func (p *publishPhase) recheckAnswered(ctx context.Context) []review.Finding {
	if len(p.answered) == 0 {
		return nil
	}
	ids := make([]string, 0, len(p.answered))
	for _, d := range p.answered {
		ids = append(ids, review.PriorID(d.Finding))
	}
	var reopened []review.Finding
	p.answered, reopened = reopen(p.answered, p.heldAnswers(ctx, ids), p.prior.headSHA)
	if len(reopened) > 0 {
		p.logger.Info("answered findings reopened", "count", len(reopened))
	}
	return reopened
}

// reopen splits the pending dismissals of answered findings into those whose
// answer held still holds, the same reply, and the findings of the others,
// carried on from head.
func reopen(answered []store.Dismissal, held map[string]review.Answer, head string) (kept []store.Dismissal, reopened []review.Finding) {
	for _, d := range answered {
		if a, ok := held[review.PriorID(d.Finding)]; ok && a.ID == d.CommentID {
			kept = append(kept, d)
			continue
		}
		f := d.Finding
		f.CarriedFrom = cmp.Or(f.CarriedFrom, head)
		reopened = append(reopened, f)
	}
	return kept, reopened
}
