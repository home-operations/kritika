package runner

import (
	"fmt"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/textcut"
)

// SkipNoReviewer is the skip reason of a review none of whose reviewers
// applies to the change: the default one is off, and no other one's
// paths match a changed file.
const SkipNoReviewer = "no_reviewer"

// partShare is what one part of a split review reviews: its files, its
// place among its reviewer's parts, counted from 1, and the scope it
// reviews them at. reviewer is the reviewer it is for in a review with
// several, nil in a review with the default reviewer alone, and scoped
// the changed files that reviewer reviews, every one for the default
// reviewer.
type partShare struct {
	reviewer     *Reviewer
	scoped       []string
	paths        []string
	index, count int
	scope        review.Scope
}

// name is the share's reviewer's name, "" in a review with one.
func (s partShare) name() string {
	if s.reviewer == nil {
		return ""
	}
	return s.reviewer.Name
}

// full reports whether the share reviews the whole change of a review that
// builds on the last one, its reviewer not run there.
func (s partShare) full(p Spec) bool {
	return s.reviewer != nil && s.reviewer.Full && p.PriorHead != ""
}

// specialist reports whether the share is a reviewer's other than the
// default one.
func (s partShare) specialist() bool {
	return s.reviewer != nil && s.reviewer.Name != review.DefaultReviewer
}

// spec is p as the share's reviewer reviews by it: a reviewer other than
// the default one with its own rules and skills, in place of the
// default's.
func (s partShare) spec(p Spec) Spec {
	if !s.specialist() {
		return p
	}
	prompt := *p.Prompt
	prompt.Rules, prompt.Skills = s.reviewer.Rules, s.reviewer.Skills
	p.Prompt = &prompt
	return p
}

// role is the share's reviewer as its system prompt gives it, its
// instructions read from files when they are a file's, nil for the
// default reviewer.
func (s partShare) role(files repoconfig.Files) *review.Reviewer {
	if !s.specialist() {
		return nil
	}
	r := s.reviewer
	instructions := r.Instructions
	if r.File != "" {
		instructions = textcut.Prefix(strings.TrimSpace(files[r.File]), repoconfig.MaxRuleFileBytes)
	}
	return &review.Reviewer{Name: r.Name, Instructions: instructions, Categories: r.Categories}
}

// limits are the run's agent bounds with the share's reviewer's in place
// of them, which the worker has kept within the run's.
func (s partShare) limits(a *AgentLimits) *AgentLimits {
	if s.reviewer == nil || s.reviewer.Limits == nil {
		return a
	}
	out, l := *a, s.reviewer.Limits
	if l.MaxSteps > 0 {
		out.MaxSteps = l.MaxSteps
	}
	if l.MaxTokens > 0 {
		out.MaxTokens = l.MaxTokens
	}
	if l.TimeoutSeconds > 0 {
		out.TimeoutSeconds = l.TimeoutSeconds
	}
	return &out
}

// reviewerPlan is how a review with reviewers is run: in shares, one or
// more for each reviewer that applies, the default one's first; or, when
// shares is nil, as a review with the default reviewer alone always has
// been, at full scope when full is set. none says no reviewer applies.
type reviewerPlan struct {
	shares []partShare
	full   bool
	none   bool
}

// planReviewers decides which of the spec's reviewers review the change
// and how, scope being what the review builds on and deltaPaths the files
// changed since the last review, less the ignored ones. A reviewer
// applies when a file the change touches matches its paths, or it has
// none, judged over the whole change as its when conditions were, so the
// reviewers a pull request gets do not shift from push to push. A review
// whose reviewers are the default one alone, or none, is not planned in
// shares. Each reviewer's files are cut into parts as a review's are,
// within its share of the parts the run is granted, the diff since the
// last review cut for a reviewer the last review ran.
func planReviewers(p Spec, res *gitfetch.Result, scope review.Scope, deltaPaths []string) reviewerPlan {
	reviewers := p.Prompt.Reviewers
	if reviewers == nil {
		return reviewerPlan{none: p.Prompt.DefaultOff}
	}
	changed := notIgnored(res.Changed, p.Ignore)
	type applied struct {
		reviewer *Reviewer
		files    []string
		scope    review.Scope
	}
	var apply []applied
	// What changed since the last review counts the files the change
	// touched there, which a revert has since dropped from changed.
	since := slices.Concat(changed, slices.DeleteFunc(slices.Clone(deltaPaths), func(d string) bool { return slices.Contains(changed, d) }))
	for i := range reviewers {
		r := &reviewers[i]
		rscope := scope
		if r.Full {
			rscope = review.ScopeFull
		}
		files := changed
		if rscope == review.ScopeIncremental {
			files = since
		}
		if len(r.Paths) > 0 {
			files = slices.DeleteFunc(slices.Clone(files), func(c string) bool { return !chunk.Matches(r.Paths, c) })
		}
		if len(files) == 0 {
			continue
		}
		apply = append(apply, applied{reviewer: r, files: files, scope: rscope})
	}
	switch {
	case len(apply) == 0:
		return reviewerPlan{none: true}
	case len(apply) == 1 && apply[0].reviewer.Name == review.DefaultReviewer:
		return reviewerPlan{full: apply[0].reviewer.Full}
	}
	perReviewer := 1
	if p.Agent != nil {
		perReviewer = max(1, p.Agent.Parts/len(reviewers))
	}
	var shares []partShare
	for _, a := range apply {
		diff := res.Diff
		if a.scope == review.ScopeIncremental {
			diff = res.DeltaDiff
		}
		split := review.SplitDiff(review.PartDiff(diff, a.files), p.Ignore, review.PartBytes, perReviewer)
		if split == nil {
			split = [][]string{a.files}
		}
		for i, paths := range split {
			shares = append(shares, partShare{
				reviewer: a.reviewer, scoped: a.files, paths: paths, index: i + 1, count: len(split), scope: a.scope,
			})
		}
	}
	return reviewerPlan{shares: shares}
}

// splitShares are the shares of a review with the default reviewer alone
// that is split into parts.
func splitShares(split [][]string, scope review.Scope) []partShare {
	shares := make([]partShare, len(split))
	for i, paths := range split {
		shares[i] = partShare{paths: paths, index: i + 1, count: len(split), scope: scope}
	}
	return shares
}

// siblings is the files of the other shares of shares[i]'s reviewer.
func siblings(shares []partShare, i int) [][]string {
	var out [][]string
	for j, s := range shares {
		if j != i && s.reviewer == shares[i].reviewer {
			out = append(out, s.paths)
		}
	}
	return out
}

// others are the files shares[i]'s submission may not report on: its
// reviewer's other parts' files, and the changed files its reviewer does
// not review.
func others(shares []partShare, i int, changed []string) []string {
	out := slices.Concat(siblings(shares, i)...)
	if s := shares[i]; s.reviewer != nil {
		for _, c := range changed {
			if !slices.Contains(s.scoped, c) && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// reviewerNotes are what the summary states of a review with several
// reviewers: each reviewer split into parts, with how many.
func reviewerNotes(shares []partShare) []string {
	var notes []string
	for i, s := range shares {
		if s.reviewer != nil && s.index == 1 && s.count > 1 && !slices.ContainsFunc(shares[:i], func(o partShare) bool {
			return o.reviewer == s.reviewer
		}) {
			notes = append(notes, fmt.Sprintf(noteReviewerSplit, s.reviewer.Name, s.count))
		}
	}
	return notes
}

// noteReviewerSplit is noteSplit for one reviewer of several.
const noteReviewerSplit = "The %s reviewer reviewed in %d parts, each by an agent of its own"

// followUpReviewer is the reviewer a follow-up answers for, the one its
// spec carries, "" for the default reviewer and for a review's run, whose
// parts name their own.
func followUpReviewer(p Spec) string {
	if p.Kind != KindFollowUp || p.Prompt == nil || len(p.Prompt.Reviewers) == 0 {
		return ""
	}
	return p.Prompt.Reviewers[0].Name
}

// followUpRole is the reviewer a follow-up answers for as its system
// prompt gives it, with no categories, since a reply reports no findings;
// nil for the default reviewer.
func followUpRole(p Spec, files repoconfig.Files) *review.Reviewer {
	if followUpReviewer(p) == "" {
		return nil
	}
	role := partShare{reviewer: &p.Prompt.Reviewers[0]}.role(files)
	if role != nil {
		role.Categories = nil
	}
	return role
}
