package worker

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

// publishPhase writes a prepared review's answer back to the forge and the
// database. It returns the final review status and, for a failure the
// caller should surface, the error.
type publishPhase struct {
	w        *Review
	file     *configfile.File
	account  *configfile.Account
	settings configfile.Settings
	client   forge.Client
	pr       *pullRequest
	reviewID string
	runID    string
	jobID    int64
	// lease is the job's slot on the review model, nil when it holds none.
	lease *store.Lease
	// trigger is why the review ran; one a human asked for does not
	// count towards the pull request's automatic reviews.
	trigger string
	logger  *slog.Logger
	// parse and templates are the repository's contract settings; the zero
	// values are kritika's defaults.
	parse     review.ParseOptions
	templates review.Templates
	// repoNotes are what the summary states about the repository's
	// configuration files.
	repoNotes []string
	// prior is the last completed review, whose inline comments are not
	// posted again; scope says how much of the change this review covers.
	prior priorReview
	scope review.Scope
	// agent is the review's agent run, whose usage the gateway recorded
	// step by step.
	agent *store.AgentRunRow
	// statusReported is set once incomplete has written the head's commit
	// status with its reason, which the job's ending then leaves alone.
	statusReported bool
	invalidAnswer  bool
	pending        json.RawMessage
	// confidence is the score judge got, nil when the repository asks for
	// none or, with unscored set, when the scorer did not answer.
	confidence *review.Confidence
	unscored   bool
	// heldBack are the findings an incremental re-review held back, which
	// the summary lists apart from its own.
	heldBack []review.Finding
	// unfinished counts the parts of a split review that ended before
	// they submitted, whose files went unreviewed: the review is then
	// incomplete for the commit status and approves nothing.
	unfinished int
	// resolved are the last review's findings this review
	// listed as no longer holding, which the summary lists and whose
	// threads it resolves.
	resolved []priorFinding
}

// run publishes what the runner's agent submitted; the run's usage is
// already recorded.
func (p *publishPhase) run(job context.Context) (store.ReviewStatus, error) {
	if cause := context.Cause(job); cause != nil {
		return store.ReviewPrepared, cause
	}
	answer, err := p.prepare(job)
	if errors.Is(err, errSuperseded) {
		_, endErr := p.w.endReview(job, p.account.ID(), p.reviewID,
			store.ReviewEnd{Status: store.ReviewSuperseded, OnlyUnfinished: true})
		return store.ReviewSuperseded, endErr
	}
	if err != nil {
		if p.invalidAnswer && job.Err() == nil {
			if !p.statusReported {
				owner, repo := p.pr.ownerRepo()
				if statusErr := p.client.SetStatus(job, owner, repo, p.pr.headSHA, forge.StatusError, reviewFailedDescription); statusErr != nil {
					p.logger.Warn("commit status not set", "error", statusErr)
				}
			}
			_, endErr := p.w.endReview(job, p.account.ID(), p.reviewID,
				store.ReviewEnd{Status: store.ReviewFailed, Error: err.Error(), OnlyUnfinished: true})
			return store.ReviewFailed, endErr
		}
		return store.ReviewFailed, err
	}
	if cause := context.Cause(job); cause != nil {
		return store.ReviewPrepared, cause
	}
	if err := p.checkpoint(job, answer); err != nil {
		return store.ReviewFailed, err
	}
	return p.deliver(job, answer)
}

// prepare settles the findings, summary and confidence before delivery.
func (p *publishPhase) prepare(ctx context.Context) (*publicationAnswer, error) {
	if err := p.preparationCurrent(ctx); err != nil {
		return nil, err
	}
	if p.agent == nil {
		return nil, errors.New("worker: the runner wrote no agent run")
	}
	run := *p.agent
	var changes store.ContextPackChanges
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		changes, err = store.ContextPackDiffs(ctx, tx, p.runID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("worker: read context pack: %w", err)
	}
	diff, delta := changes.Diff, changes.Delta
	var deltaPaths []string
	if p.scope == review.ScopeIncremental {
		deltaPaths = changes.DeltaPaths
	}
	p.logger.Info("agent answered", "stop", run.StopReason, "steps", run.Steps, "model", run.Model, "input_tokens", run.Usage.Prompt(),
		"cached_tokens", run.Usage.CacheRead, "output_tokens", run.Usage.Output, "cost_usd", run.CostUSD)
	if stopErr := stopError(run); stopErr != nil {
		p.invalidAnswer = true
		return nil, errors.Join(stopErr, p.incomplete(ctx, "agent stopped: "+run.StopReason, run.Model))
	}
	res, dropped, err := review.Parse(string(run.Result), review.Anchors(diff), p.parse)
	if err != nil {
		p.invalidAnswer = true
		return nil, errors.Join(err, p.incomplete(ctx, "the submitted review was invalid", run.Model))
	}
	for _, d := range dropped {
		p.logger.Debug("finding dropped", "reason", d.Reason, "path", d.Finding.Path, "line", d.Finding.Line, "title", d.Finding.Title)
	}
	unanchored, notes := splitDropped(dropped)
	partNotes := unfinishedParts(run.Parts)
	p.unfinished = len(partNotes)
	notes = append(notes, partNotes...)
	var dismissed, dismissedOff int
	res.Findings, dismissed = dropDismissed(res.Findings, p.prior.dismissed)
	unanchored, dismissedOff = dropDismissed(unanchored, p.prior.dismissed)
	if dismissed += dismissedOff; dismissed > 0 {
		notes = append(notes, fmt.Sprintf("%d finding(s) a maintainer dismissed were left out", dismissed))
	}
	// Findings outside the diff are never held back: they are listed apart
	// and never posted inline, so there is nothing to spare the reader.
	if p.scope == review.ScopeIncremental {
		held := review.HoldBack(reviewFindings(p.prior.findings), slices.Concat(res.Findings, unanchored), delta, deltaPaths)
		if res.Findings, p.heldBack = splitHeld(res.Findings, held); len(p.heldBack) > 0 {
			p.logger.Info("findings held back", "count", len(p.heldBack))
		}
	}
	answer := run.Result
	if len(run.Parts) > 1 {
		var note string
		answer, note = p.mergeSummary(ctx, run.Parts, answer, &res, slices.Concat(res.Findings, unanchored))
		if note != "" {
			notes = append(notes, note)
		}
	}
	if p.parse.Diagram {
		res.Summary.Diagram = carriedDiagram(answer, res.Summary.Diagram, p.prior.diagram, p.scope == review.ScopeIncremental)
	}
	// Carried findings follow the review's own, which it vouches for.
	if len(p.prior.findings) > 0 {
		var carried []review.Finding
		p.resolved, carried = settlePrior(p.prior.findings, p.prior.headSHA, slices.Concat(res.Findings, unanchored), res.Resolved)
		if len(carried) > 0 {
			p.logger.Info("earlier findings carried on unchecked", "count", len(carried))
			res.Findings = append(res.Findings, carried...)
		}
	}
	if err := p.preparationCurrent(ctx); err != nil {
		return nil, err
	}
	// A finding on a line the diff does not show has no inline comment, but
	// weighs on the score, the counts and the approval all the same.
	if note := p.judge(ctx, review.Result{Summary: res.Summary, Findings: slices.Concat(res.Findings, unanchored)}, diff); note != "" {
		notes = append(notes, note)
	}
	if note := skillsNote(run.SkillsOffered, run.SkillsOpened); note != "" {
		notes = append(notes, note)
	}
	if note := commandsNote(run.CommandsOffered, run.CommandsRun); note != "" {
		notes = append(notes, note)
	}
	notes = append(notes, run.CommandErrors...)
	return &publicationAnswer{Result: res, Unanchored: unanchored, Notes: notes, Model: run.Model}, nil
}

// autoReviewPauses returns the note the summary states when this review
// is the last automatic one the repository allows the pull request, ""
// otherwise; a review a human asked for is not counted.
func (p *publishPhase) autoReviewPauses(ctx context.Context) (string, error) {
	if p.trigger == jobs.TriggerManual {
		return "", nil
	}
	var pauses bool
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		pauses, err = store.AutoReviewPauses(ctx, tx, p.pr.id, p.settings.MaxAutoReviews)
		return err
	})
	if err != nil || !pauses {
		return "", err
	}
	login, err := p.client.BotLogin(ctx)
	if err != nil {
		return "", err
	}
	return review.AutoPausedNote(strings.TrimSuffix(login, "[bot]"), p.settings.MaxAutoReviews), nil
}

// unfinishedParts is what the summary states for each part of a split
// review that ended before it submitted, whose files went unreviewed.
func unfinishedParts(parts []store.AgentPart) []string {
	var notes []string
	for i, part := range parts {
		if part.Stop == string(agent.StopSubmitted) {
			continue
		}
		files := part.Paths
		if n := len(files); n > maxNamedFiles {
			files = append(slices.Clone(files[:maxNamedFiles]), fmt.Sprintf("%d more", n-maxNamedFiles))
		}
		notes = append(notes, fmt.Sprintf("Part %d of %d ended before it submitted, so its files went unreviewed: %s",
			i+1, len(parts), strings.Join(files, ", ")))
	}
	return notes
}

// maxNamedFiles is how many files a note names before it counts the rest.
const maxNamedFiles = 5

// skillsNote is what the summary states about the repository's skills: the
// ones the review was offered and, of those, the ones it read. "" when it
// was offered none.
func skillsNote(offered, opened []string) string {
	if len(offered) == 0 {
		return ""
	}
	read := "none read"
	if len(opened) > 0 {
		read = "read: " + strings.Join(opened, ", ")
	}
	return fmt.Sprintf("Skills offered: %s; %s", strings.Join(offered, ", "), read)
}

// commandsNote is the summary's note of the commands the run tool offered
// and the ones the agent ran, "" when it offered none.
func commandsNote(offered, ran []string) string {
	if len(offered) == 0 {
		return ""
	}
	used := "none run"
	if len(ran) > 0 {
		used = "run: " + strings.Join(ran, ", ")
	}
	return fmt.Sprintf("Commands offered: %s; %s", strings.Join(offered, ", "), used)
}

// skipDescription is how the commit status states a skip: a repository's
// own reason or a bot's unchanged patch, and for a filtered one the name of
// the condition that decided, "" for none.
func skipDescription(reason, filter string) string {
	if reason == runner.SkipUnchangedPatch {
		return "patch unchanged since the last review"
	}
	desc := repoconfig.SkipReason(reason).Description()
	if filter != "" {
		desc += ": " + filter
	}
	return desc
}

// incomplete replaces the sticky comment with one saying why this head was
// not fully reviewed, in kritika's own template, and records the model.
func (p *publishPhase) incomplete(ctx context.Context, reason, modelName string) error {
	owner, repo := p.pr.ownerRepo()
	web, pull := p.dashboard(owner, repo)
	body, _ := review.RenderSummary(ctx, review.Templates{}, review.RenderData{
		Number: p.pr.number, HeadSHA: p.pr.headSHA, HeadURL: p.client.CommitURL(owner, repo, p.pr.headSHA), Model: modelName,
		Effort: string(p.settings.Models.Effort), HeadSubject: p.headSubject(ctx, owner, repo), Reviews: p.reviews(ctx), Cost: p.cost(ctx),
		Incomplete: reason, Notes: slices.Concat(p.repoNotes, p.agent.CommandErrors), WebURL: web, PullURL: pull,
	})
	commentID, err := p.upsertSticky(ctx, body)
	if err != nil {
		return err
	}
	if err := p.client.SetStatus(ctx, owner, repo, p.pr.headSHA, forge.StatusError, "kritika: review incomplete ("+reason+")"); err != nil {
		p.logger.Warn("commit status not set", "error", err)
	}
	p.statusReported = true
	return p.persist(ctx, review.Result{}, nil, nil, modelName, commentID)
}

func (p *publishPhase) countFindings(res review.Result) {
	byKind := map[[2]string]int{}
	for _, f := range res.Findings {
		if f.CarriedFrom != "" {
			continue
		}
		byKind[[2]string{string(f.Severity), string(f.Category)}]++
	}
	for kind, n := range byKind {
		p.w.Metrics.Findings(p.account.Key(), kind[0], kind[1], n)
	}
}

// splitDropped sorts what Parse dropped into the findings on lines the
// diff does not show, which the summary lists since no inline comment can
// carry them, and a note counting the rest.
func splitDropped(dropped []review.Dropped) (unanchored []review.Finding, notes []string) {
	byReason := map[review.DropReason]int{}
	total := 0
	for _, d := range dropped {
		if d.Reason == review.DropUnanchored {
			unanchored = append(unanchored, d.Finding)
			continue
		}
		byReason[d.Reason]++
		total++
	}
	if total == 0 {
		return unanchored, nil
	}
	reasons := make([]string, 0, len(byReason))
	for r, n := range byReason {
		reasons = append(reasons, fmt.Sprintf("%s: %d", r, n))
	}
	slices.Sort(reasons)
	return unanchored, []string{fmt.Sprintf("%d finding(s) were dropped (%s)", total, strings.Join(reasons, ", "))}
}

// splitHeld splits off the findings held reports held back.
func splitHeld(findings []review.Finding, held func(review.Finding) bool) (kept, back []review.Finding) {
	kept = make([]review.Finding, 0, len(findings))
	for _, f := range findings {
		if held(f) {
			back = append(back, f)
		} else {
			kept = append(kept, f)
		}
	}
	return kept, back
}

// writeBack posts the sticky comment (created once, edited after), the
// inline review, and the commit status. Only the sticky comment is
// required: the other two are best effort and logged when they fail, so a
// forge quirk cannot turn a finished review into a retry storm. A finding
// the last review already posted inline, or any finding when inline
// comments are off, is listed in the summary only. Once the inline review
// is posted, the sticky comment is edited again to link each finding to
// its thread. The returned comments say, per finding, those on the diff
// then those off it, whether an inline comment for it is on the forge, and
// its id there: a finding off the diff is never posted, but carries the
// thread an earlier report of it opened.
func (p *publishPhase) writeBack(
	ctx context.Context, res review.Result, unanchored []review.Finding, modelName string, notes []string,
) (int64, []store.InlinePosted, error) {
	owner, repo := p.pr.ownerRepo()
	// A finding reported again outside the diff is reported again all the
	// same: it is neither resolved nor listed so, and keeps its thread.
	reported := slices.Concat(res.Findings, unanchored)
	onForge := alreadyInline(reported, p.prior.findings)
	if err := p.markOnForge(ctx, reported, onForge); err != nil {
		return 0, nil, err
	}
	for i := range res.Findings {
		f := &res.Findings[i]
		f.URL = p.client.FileURL(owner, repo, cmp.Or(f.CarriedFrom, p.pr.headSHA), f.Path, f.Line, f.EndLine)
		if onForge[i].ID != 0 {
			f.ThreadURL = p.client.ThreadURL(owner, repo, p.pr.number, onForge[i].ID)
		}
	}
	for i := range unanchored {
		if id := onForge[len(res.Findings)+i].ID; id != 0 {
			unanchored[i].ThreadURL = p.client.ThreadURL(owner, repo, p.pr.number, id)
		}
	}
	for i := range p.heldBack {
		f := &p.heldBack[i]
		f.URL = p.client.FileURL(owner, repo, p.pr.headSHA, f.Path, f.Line, f.EndLine)
	}
	// Inline comments render first so a failing inline template is noted
	// in the summary. After one failure the rest use the default, so a
	// template that times out costs one deadline, not one per finding.
	web, pull := p.dashboard(owner, repo)
	templates := p.templates
	inline := make([]forge.InlineComment, 0, len(res.Findings))
	var posted []int
	for i, f := range res.Findings {
		// A carried finding's line numbers are an earlier head's, which
		// the diff may no longer show.
		if onForge[i].Posted || f.CarriedFrom != "" || !p.settings.Review.InlineComments {
			continue
		}
		body, inlineNotes := review.RenderInline(ctx, templates, review.InlineData{Finding: f, WebURL: web})
		if len(inlineNotes) > 0 {
			templates.Inline = ""
			notes = append(notes, inlineNotes...)
		}
		c := forge.InlineComment{Path: f.Path, Line: f.Line, Body: body}
		if f.EndLine > 0 {
			c.StartLine, c.Line = f.Line, f.EndLine
		}
		inline = append(inline, c)
		posted = append(posted, i)
	}
	var sources []string
	if p.agent != nil {
		sources = review.SourceLinks(p.agent.Sources)
	}
	counts := review.Result{Findings: reported}.Counts()
	data := review.RenderData{
		Number: p.pr.number, HeadSHA: p.pr.headSHA, HeadURL: p.client.CommitURL(owner, repo, p.pr.headSHA), Model: modelName,
		Effort: string(p.settings.Models.Effort), HeadSubject: p.headSubject(ctx, owner, repo), Reviews: p.reviews(ctx), Cost: p.cost(ctx),
		AuthorIsBot: p.pr.authorIsBot, Result: res, Counts: counts, Notes: notes, Unanchored: unanchored, HeldBack: p.heldBack,
		Incremental: p.scope == review.ScopeIncremental, PriorHeadSHA: p.prior.headSHA, Sources: sources,
		WebURL: web, PullURL: pull, Confidence: p.confidence,
	}
	if p.prior.id != "" {
		data.PriorHeadURL = p.client.CommitURL(owner, repo, p.prior.headSHA)
		data.Prior = p.priorFindings()
		p.resolveThreads(ctx, resolvedThreads(p.resolved))
	}
	body, renderNotes := review.RenderSummary(ctx, p.templates, data)
	for _, n := range renderNotes {
		p.logger.Warn("template fell back to the default", "note", n)
	}

	commentID, err := p.upsertSticky(ctx, body)
	if err != nil {
		return 0, nil, err
	}

	ids, err := p.client.CreateReview(ctx, owner, repo, p.pr.number, p.pr.headSHA, inline)
	switch {
	case ids == nil:
		p.logger.Warn("inline review not posted", "error", err)
	case err != nil:
		p.logger.Warn("inline review posted, but its comments' ids not read back", "error", err)
	}
	linked := false
	for j, i := range posted {
		if ids != nil {
			onForge[i] = store.InlinePosted{Posted: true, ID: ids[j]}
		}
		if ids != nil && ids[j] != 0 {
			res.Findings[i].ThreadURL = p.client.ThreadURL(owner, repo, p.pr.number, ids[j])
			linked = true
		}
	}
	state, desc := p.verdict(counts.Total())
	if err := p.client.SetStatus(ctx, owner, repo, p.pr.headSHA, state, "kritika: "+desc); err != nil {
		p.logger.Warn("commit status not set", "error", err)
	}
	// A merged or closed pull request's head merges no more, so it is
	// neither approved nor its approval withdrawn.
	if p.settings.Review.Approve && p.pr.closed == "" {
		data.Approval = p.approve(ctx, counts, p.headCurrent(ctx))
	}
	if linked || data.Approval != nil {
		// The threads and the approval exist only now, so the summary is
		// written a second time with the links and the verdict; losing
		// them is not worth failing the review.
		data.Result = res
		body, _ = review.RenderSummary(ctx, p.templates, data)
		if err := p.client.UpdateComment(ctx, owner, repo, commentID, body); err != nil {
			p.logger.Warn("summary not rewritten with its threads and approval", "error", err)
		}
	}
	return commentID, onForge, nil
}

// markOnForge marks the findings whose inline comment the bot has already
// posted on the pull request, by the FindingMarker each carries, which the
// database may not know: an attempt that posted and then died before
// recording them, or a review whose record failed to write. A forge that
// cannot be read leaves delivery pending rather than risking duplicates.
func (p *publishPhase) markOnForge(ctx context.Context, findings []review.Finding, onForge []store.InlinePosted) error {
	if !p.settings.Review.InlineComments || len(findings) == 0 {
		return nil
	}
	owner, repo := p.pr.ownerRepo()
	login, err := p.client.BotLogin(ctx)
	if err != nil {
		return fmt.Errorf("worker: reconcile inline comments: %w", err)
	}
	comments, err := p.client.ListInline(ctx, owner, repo, p.pr.number)
	if err != nil {
		return fmt.Errorf("worker: reconcile inline comments: %w", err)
	}
	posted := markedInline(comments, login)
	for i, f := range findings {
		if id, ok := posted[review.Fingerprint(f)]; ok && !onForge[i].Posted {
			onForge[i] = store.InlinePosted{Posted: true, ID: id}
		}
	}
	return nil
}

// markedInline maps the fingerprint each of the bot's root inline comments
// carries a FindingMarker for to the comment's id; a later comment for the
// same finding wins, as the one a reader sees as current. An outdated
// comment is passed over: a person may have resolved its thread as fixed,
// and a finding raised again after the fix was lost needs a thread of its
// own on the current lines.
func markedInline(comments []forge.Comment, login string) map[string]int64 {
	posted := map[string]int64{}
	for _, c := range comments {
		if c.InReplyTo != 0 || c.Outdated || !strings.EqualFold(c.Author, login) {
			continue
		}
		if fp, ok := review.MarkedFinding(c.Body); ok {
			posted[fp] = c.ID
		}
	}
	return posted
}

// settlePrior sorts the last review's findings, made at head, that this
// review did not report again, reported being its findings
// on and off the diff: those whose ids it listed as resolved, and the rest,
// which stay open, carried on unchecked from the head they were last
// reported at. A finding is gone only when a review says so, so one the
// model passed over, one a split review's unfinished part had, or one the
// prompt had no room for is never taken as fixed.
func settlePrior(prior []priorFinding, head string, reported []review.Finding, resolvedIDs []string) (
	resolved []priorFinding, carried []review.Finding,
) {
	again := reportedSet(reported)
	gone := make(map[string]bool, len(resolvedIDs))
	for _, id := range resolvedIDs {
		gone[strings.TrimSpace(id)] = true
	}
	for _, pf := range prior {
		switch {
		case again[review.Fingerprint(pf.Finding)]:
		case gone[review.PriorID(pf.Finding)]:
			resolved = append(resolved, pf)
		default:
			f := pf.Finding
			f.CarriedFrom = cmp.Or(f.CarriedFrom, head)
			carried = append(carried, f)
		}
	}
	return resolved, carried
}

// priorFindings is the last review's findings as this review's summary
// lists them: those it listed as resolved, linked to their threads where
// they have one, then the findings maintainers dismissed, each linked to
// the comment that dismissed it.
func (p *publishPhase) priorFindings() []review.PriorFinding {
	owner, repo := p.pr.ownerRepo()
	out := make([]review.PriorFinding, 0, len(p.resolved)+len(p.prior.dismissed))
	for _, pf := range p.resolved {
		f := pf.Finding
		f.URL = p.client.FileURL(owner, repo, cmp.Or(f.CarriedFrom, p.prior.headSHA), f.Path, f.Line, f.EndLine)
		if pf.commentID != 0 {
			f.ThreadURL = p.client.ThreadURL(owner, repo, p.pr.number, pf.commentID)
		}
		out = append(out, review.PriorFinding{Finding: f, Resolved: true})
	}
	for _, d := range p.prior.dismissed {
		f := d.Finding
		f.URL = p.client.FileURL(owner, repo, p.prior.headSHA, f.Path, f.Line, f.EndLine)
		f.ThreadURL = p.client.ThreadURL(owner, repo, p.pr.number, d.CommentID)
		out = append(out, review.PriorFinding{Finding: f, Dismissed: true, DismissReason: d.Reason})
	}
	return out
}

// resolvedThreads is the inline comment id of each of resolved, the last
// review's findings this review listed as no longer holding: the threads
// the summary lists as resolved.
func resolvedThreads(resolved []priorFinding) []int64 {
	var ids []int64
	for _, pf := range resolved {
		if pf.commentID != 0 {
			ids = append(ids, pf.commentID)
		}
	}
	return ids
}

// reportedSet is the fingerprints of findings.
func reportedSet(findings []review.Finding) map[string]bool {
	set := make(map[string]bool, len(findings))
	for _, f := range findings {
		set[review.Fingerprint(f)] = true
	}
	return set
}

// resolveThreads resolves the threads of the findings this review found
// gone, so the forge agrees with the summary that lists them resolved. The
// forge client leaves a thread someone else wrote in open. Best effort,
// like the inline review: the summary is the record either way.
func (p *publishPhase) resolveThreads(ctx context.Context, ids []int64) {
	owner, repo := p.pr.ownerRepo()
	for _, id := range ids {
		resolved, err := p.client.ResolveThread(ctx, owner, repo, p.pr.number, id, true)
		switch {
		case err != nil:
			p.logger.Warn("finding's thread not resolved", "comment", id, "error", err)
		case resolved:
			p.logger.Info("finding's thread resolved", "comment", id)
		}
	}
}

// upsertSticky edits the pull request's sticky comment to body, creating
// it the first time, and returns its id.
func (p *publishPhase) upsertSticky(ctx context.Context, body string) (int64, error) {
	var stored int64
	// The row is a shortcut: no row, or a read that failed, leaves the
	// comment to be found on the forge by its marker.
	_ = p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		stored, err = store.StickyCommentID(ctx, tx, p.pr.id)
		return err
	})
	return p.writeSticky(ctx, stored, body)
}

// writeSticky edits the sticky comment stored, the one the database
// remembers, to body, and returns its id. With none stored, or one the
// forge no longer has, the comment is found by its marker instead and
// created when there is none: a sticky comment someone deleted must not
// fail every later review, and the id returned replaces the stored one.
func (p *publishPhase) writeSticky(ctx context.Context, stored int64, body string) (int64, error) {
	owner, repo := p.pr.ownerRepo()
	if stored != 0 {
		err := p.client.UpdateComment(ctx, owner, repo, stored, body)
		if err == nil {
			return stored, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		p.logger.Warn("sticky comment no longer on the forge", "comment", stored)
	}
	login, err := p.client.BotLogin(ctx)
	if err != nil {
		return 0, err
	}
	commentID, err := p.client.FindComment(ctx, owner, repo, p.pr.number, login, review.Marker(p.pr.number))
	if err != nil {
		return 0, err
	}
	if commentID != 0 {
		err = p.client.UpdateComment(ctx, owner, repo, commentID, body)
	} else {
		commentID, err = p.client.CreateComment(ctx, owner, repo, p.pr.number, body)
	}
	if err != nil {
		return 0, err
	}
	return commentID, nil
}

// persist records the review: its summary and its findings on the diff
// then off it, with the inline state of each as writeBack returned them,
// so the next review is told of every finding and resolves the thread of
// one found gone, wherever it was reported.
func (p *publishPhase) persist(
	ctx context.Context, res review.Result, unanchored []review.Finding, inline []store.InlinePosted, modelName string, commentID int64,
) error {
	recorded := review.Result{Summary: res.Summary, Findings: slices.Concat(res.Findings, unanchored)}
	return p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		return store.RecordReviewResult(ctx, tx, store.ReviewResult{
			AccountID: p.account.ID(), ReviewID: p.reviewID, PullRequestID: p.pr.id, Result: recorded, Inline: inline, Model: modelName,
			CommentID: commentID, Confidence: p.confidence, Partial: p.unfinished > 0,
		})
	})
}

// headSubject is the head commit's subject line for the sticky comment's
// footer, cut and escaped for Markdown, "" when the forge would not say:
// the comment then names the commit by its hash.
func (p *publishPhase) headSubject(ctx context.Context, owner, repo string) string {
	subject, err := p.client.CommitSubject(ctx, owner, repo, p.pr.headSHA)
	if err != nil {
		p.logger.Warn("head commit subject not read", "error", err)
	}
	return review.FooterSubject(subject)
}

// reviews is how many reviews of the pull request this one makes, counted
// now rather than when it started, since another head's review may have
// completed in between.
func (p *publishPhase) reviews(ctx context.Context) int {
	var completed int
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		completed, err = store.CompletedReviewCount(ctx, tx, p.pr.id)
		return err
	})
	if err != nil {
		p.logger.Warn("completed reviews not counted", "error", err)
	}
	return completed + 1
}

// cost is what the pull request's reviews have cost together, this one's
// calls included, formatted for the footer; "" where the repository does
// not show it, or it could not be read.
func (p *publishPhase) cost(ctx context.Context) string {
	if !p.settings.Review.Cost {
		return ""
	}
	var cost float64
	var unpriced int64
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		cost, unpriced, err = store.PullCost(ctx, tx, p.pr.id)
		return err
	})
	if err != nil {
		p.logger.Warn("pull request cost not read", "error", err)
		return ""
	}
	return review.FormatCost(cost, unpriced)
}

// dashboard is the dashboard's origin and the pull request's page on it,
// both "" when the worker was given no web URL.
func (p *publishPhase) dashboard(owner, repo string) (web, pull string) {
	if p.w.WebURL == nil {
		return "", ""
	}
	return p.w.WebURL.String(), review.PullPageURL(p.w.WebURL, string(p.account.Forge), p.account.Name, owner, repo, p.pr.number)
}
