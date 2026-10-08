// Package runner is what a runner pod does: fetch the head and merge-base
// (and the last reviewed head when there is one), diff them, compute the
// patch id, decide what the review builds on and whether it is skipped,
// write a context pack under its own run id, and run the review's agent
// over it. A follow-up's run answers a comment with the same agent and
// tools, and an index run chunks a tree. It works from
// one versioned job document (Spec); its credentials, a git token for one
// repository and for a review a token for the worker's model gateway,
// arrive apart from it (Secrets). Its database role can only touch
// its own run.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/chunk"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// Run executes one run and reports success or failure in the run row,
// stamping a heartbeat while it works. The store must be opened with the
// runner role's DSN.
func Run(ctx context.Context, st *store.Store, spec Spec, secrets Secrets, logger *slog.Logger) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	hctx, stop := context.WithCancel(ctx)
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		heartbeat(hctx, HeartbeatInterval, func(ctx context.Context) error { return beat(ctx, st, spec.RunID) }, logger)
	}()
	defer func() {
		stop()
		<-beating
	}()
	run := runReview
	switch spec.Kind {
	case KindIndex:
		run = runIndex
	case KindFollowUp:
		run = runFollowUp
	}
	// The first thing either kind does is fetch through the gateway, which
	// a runner started with the service may reach before its Service
	// does.
	err := waitForGateway(ctx, logger)
	if err == nil {
		err = run(ctx, st, spec, secrets, logger)
	}
	if err != nil {
		// The worker still sees the Job fail; the row only loses why.
		if ferr := fail(ctx, st, spec.RunID, secrets, err); ferr != nil {
			logger.Warn("run failure not recorded", "error", ferr)
		}
	}
	return err
}

// maxPackDiffBytes is how much of a diff, and of a delta diff, a context
// pack keeps: whole files in order, a file that would pass it left out. A
// pull request that regenerates a large text file would otherwise put the
// whole of it in the service's memory. The prompt's own budget is far
// smaller, so the agent is sent no less for it. A variable for the tests.
var maxPackDiffBytes = 4 << 20

func runReview(ctx context.Context, st *store.Store, p Spec, secrets Secrets, logger *slog.Logger) error {
	if err := setPhase(ctx, st, p.RunID, "fetching"); err != nil {
		return err
	}
	res, err := gitfetch.Run(ctx, gitfetch.Fetch{
		CloneURL: p.CloneURL, Token: secrets.GitToken, Head: p.Head, Base: p.Base, Prior: p.PriorHead, PriorChanged: p.PriorChanged,
	})
	if err != nil {
		return err
	}
	defer func() { _ = res.Close() }()
	logger.Info("fetched", "head", p.Head[:7], "base", p.Base[:7], "changed_paths", len(res.Changed), "diff_bytes", len(res.Diff))

	if err := setPhase(ctx, st, p.RunID, "parsing"); err != nil {
		return err
	}
	headTree, err := res.Head.Tree()
	if err != nil {
		return fmt.Errorf("runner: head tree: %w", err)
	}
	baseTree, err := res.Base.Tree()
	if err != nil {
		return fmt.Errorf("runner: base tree: %w", err)
	}
	files, notes, err := repoFiles(baseTree, p.RepoFiles, res.Changed)
	if err != nil {
		return err
	}
	ignore := p.Ignore
	// A nil prior head tells the worker the delta is unknown, not empty.
	var priorHead *string
	deltaPaths := []string{}
	if res.Prior != nil {
		priorHead, deltaPaths = &p.PriorHead, notIgnored(res.DeltaChanged, ignore)
		logger.Info("fetched prior head", "prior", p.PriorHead[:7], "delta_paths", len(deltaPaths), "delta_bytes", len(res.DeltaDiff))
	} else if p.PriorHead != "" {
		// Best effort: the review goes on in full. A force-push is the
		// expected cause; the error tells it apart from auth or network.
		logger.Warn("prior head not fetched", "prior", p.PriorHead[:7], "error", res.PriorErr)
	}
	// Everything the worker reads back is decided here, before the pack is
	// written: whether the review is skipped, what it builds on, and what
	// the prompt was given. A skipped review spends nothing on a model.
	scope, scopeReason := review.DecideScope(p.PriorHead != "", p.PriorHead == p.Head, p.Prompt.PullRequest.Manual(), priorHead != nil,
		len(deltaPaths), p.Prompt.MaxDeltaFiles)
	skip, skipDetail, err := agentSkip(p, res.Changed, res.PatchID, res.Diff)
	switch {
	case skip == "" && err != nil:
		return err
	case err != nil:
		logger.Warn("filter failed to evaluate; the review is skipped", "filter", skipDetail, "error", err)
	}
	var split [][]string
	if skip == "" {
		split = splitReview(p, res, scope)
	}
	chunks, err := wholeContext(ctx, headTree, baseTree, res, ignore, split, logger)
	if err != nil {
		return err
	}
	var found []repoconfig.Skill
	if sk := p.Prompt.Skills; sk != nil {
		var skillNotes []string
		if found, skillNotes, err = discoverSkills(baseTree, sk.Paths); err != nil {
			return err
		}
		notes = append(notes, skillNotes...)
	}
	in := newPromptInputs(p, files, found, res.Changed)
	notes = append(notes, in.notes...)
	var tools agentTools
	var prompt agentPrompt
	var parts []reviewPart
	ruleIDs := in.ruleIDs()
	if skip == "" {
		tools.diff = newReadDiffTool(res.Diff, p.Agent.limits().MaxToolOutputBytes)
		if len(in.skills) > 0 {
			tools.skills = &skillTool{base: baseTree, skills: in.skills, maxBytes: p.Agent.limits().MaxToolOutputBytes}
		}
		var cleanup func()
		tools.run, tools.fetch, cleanup = commandTool(ctx, p, agent.NewTree(headTree, ignore), secrets.GitToken,
			p.Agent.limits().MaxToolOutputBytes, logger)
		defer cleanup()
		if split == nil {
			prompt, chunks = wholePrompt(ctx, p, secrets, headTree, ignore, res, in, scope, chunks, &tools, logger)
			notes = append(notes, prompt.notes()...)
		} else if parts, chunks, ruleIDs, err = partPrompts(
			ctx, p, secrets, headTree, baseTree, res, files, found, split, scope, &tools, logger,
		); err != nil {
			return err
		} else {
			notes = append(notes, splitNotes(parts)...)
		}
	}
	stagesJSON, err := json.Marshal(chunks)
	if err != nil {
		return fmt.Errorf("runner: encode context: %w", err)
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("runner: encode repository files: %w", err)
	}
	if notes == nil {
		notes = []string{}
	}

	// The pack is read whole by the service, on every publish and
	// follow-up, so what it keeps of the diffs is bounded here.
	packDiff, cut := review.FitDiff(res.Diff, maxPackDiffBytes)
	packDelta, _ := review.FitDiff(res.DeltaDiff, maxPackDiffBytes)
	if len(cut) > 0 {
		logger.Warn("diff too large to keep whole", "diff_bytes", len(res.Diff), "kept_bytes", len(packDiff), "files_cut", len(cut))
		notes = append(notes, fmt.Sprintf(noteDiffNotKept, len(cut), namePaths(cut)))
	}

	if err := setPhase(ctx, st, p.RunID, "writing"); err != nil {
		return err
	}
	err = st.WithRunnerJob(ctx, p.RunID, func(tx pgx.Tx) error {
		// account_id is copied from the run row: the runner never receives it
		// and cannot invent one, and the policy only opens its own run.
		_, err := tx.Exec(ctx, `
			INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, changed_paths, stages, repo_files, repo_notes,
				prior_head_sha, delta_diff, delta_paths, scope, scope_reason, skip_reason, rule_ids, skip_detail)
			SELECT id, account_id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17 FROM runner_runs WHERE id = $1`,
			p.RunID, p.Head, p.Base, res.PatchID, packDiff, res.Changed, stagesJSON, filesJSON, notes,
			priorHead, packDelta, deltaPaths, string(scope), scopeReason, skip, ruleIDs, skipDetail)
		if err != nil {
			return fmt.Errorf("runner: write context pack: %w", err)
		}
		if skip != "" {
			return setPhaseTx(ctx, tx, p.RunID, "done")
		}
		// A review is not done until its agent has run too.
		return setPhaseTx(ctx, tx, p.RunID, "reviewing")
	})
	if err != nil {
		return err
	}
	logger.Info("context pack written", "run", p.RunID, "patch_id", res.PatchID[:12], "scope", scope, "skip", skip)
	if skip != "" {
		logger.Info("agent not run", "reason", skip)
		return nil
	}
	if parts != nil {
		return runParts(ctx, st, p, secrets, headTree, ignore, tools, parts, logger)
	}
	return runAgentic(ctx, st, p, secrets, headTree, ignore, tools, prompt, scope, logger)
}

// wholeContext is the context stages over the whole diff, or none for a
// split review, whose parts build theirs from their own diffs.
func wholeContext(
	ctx context.Context, head, base *object.Tree, res *gitfetch.Result, ignore []string, split [][]string, logger *slog.Logger,
) ([]contextpack.Chunk, error) {
	if split != nil {
		return []contextpack.Chunk{}, nil
	}
	chunks, stats, err := stages(ctx, head, base, res, ignore)
	if err != nil {
		return nil, err
	}
	logger.Info("context built", "overlay", stats.Overlay, "definitions", stats.Definitions, "callers", stats.Callers,
		"identifiers", stats.Identifiers, "files_scanned", stats.FilesScanned, "files_parsed", stats.FilesParsed,
		"scan_truncated", stats.ScanTruncated, "elapsed", stats.Elapsed.Round(time.Millisecond))
	return chunks, nil
}

// wholePrompt writes the prompt of a review that is not split, from the
// whole diff and its context with the similar code that tools' search_code
// is offered over, carrying on the last review's conversation where it
// may. It returns the context the prompt was given.
func wholePrompt(
	ctx context.Context, p Spec, secrets Secrets, head *object.Tree, ignore []string, res *gitfetch.Result, in promptInputs,
	scope review.Scope, chunks []contextpack.Chunk, tools *agentTools, logger *slog.Logger,
) (agentPrompt, []contextpack.Chunk) {
	var similar []contextpack.Chunk
	similar, tools.search = similarContext(ctx, p, secrets, res, logger)
	chunks = append(chunks, similar...)
	prompt := newAgentPrompt(p, in, packView{
		Diff: res.Diff, Changed: res.Changed, Context: chunks, DeltaDiff: res.DeltaDiff, Scope: scope,
	}, tools.commands(), tools.fetch != nil, tools.search != nil)
	if p.Prompt.Continue != nil && scope == review.ScopeIncremental {
		prompt = carryOn(ctx, p, secrets, prompt, offeredTools(p, head, ignore, tools.extra()), res.DeltaDiff, logger)
	}
	return prompt, chunks
}

// similarContext is context stage 4, best effort: the code elsewhere in
// the repository that resembles the diff's hunks, and the search_code tool
// over the same index. An instance without an embedder, a repository
// without a completed index, or a refused or failed request leaves both
// out and the run goes on.
func similarContext(
	ctx context.Context, p Spec, secrets Secrets, res *gitfetch.Result, logger *slog.Logger,
) ([]contextpack.Chunk, *searchTool) {
	out, ok := similarCodeFor(ctx, p, secrets, res.Diff, res.Changed, logger)
	if !ok {
		return nil, nil
	}
	return out.Chunks, newSearchTool(p, secrets, res.Changed)
}

// similarCodeFor is the code elsewhere in the repository that resembles
// diff's hunks, best effort: false when diff has no hunk to ask about, the
// repository has no index, or the request failed.
func similarCodeFor(
	ctx context.Context, p Spec, secrets Secrets, diff string, changed []string, logger *slog.Logger,
) (contextpack.SimilarResponse, bool) {
	queries := hunkQueries(diff)
	if len(queries) == 0 {
		return contextpack.SimilarResponse{}, false
	}
	out, err := similarCode(ctx, p.Model.GatewayURL, secrets.GatewayToken, contextpack.SimilarRequest{Queries: queries, Exclude: changed})
	if err != nil {
		logger.Warn("similar-code retrieval skipped", "error", err)
		return contextpack.SimilarResponse{}, false
	}
	return out, out.Indexed
}

// newSearchTool is search_code over the repository's index, leaving out
// the changed paths, which the prompt shows.
func newSearchTool(p Spec, secrets Secrets, changed []string) *searchTool {
	return &searchTool{
		gatewayURL: p.Model.GatewayURL, token: secrets.GatewayToken, exclude: changed, maxBytes: p.Agent.limits().MaxToolOutputBytes,
	}
}

// notIgnored returns the paths no ignore glob matches, never nil.
func notIgnored(paths, ignore []string) []string {
	out := []string{}
	for _, p := range paths {
		if !chunk.Matches(ignore, p) {
			out = append(out, p)
		}
	}
	return out
}

// stages runs context stages 1 to 3 over the fetched trees. The chunk list
// is never nil so the column holds a JSON array even for an empty pack.
func stages(
	ctx context.Context, headTree, baseTree *object.Tree, res *gitfetch.Result, ignore []string,
) ([]contextpack.Chunk, contextpack.Stats, error) {
	chunks, stats, err := contextpack.Build(ctx, contextpack.Input{
		Head: headTree, Base: baseTree, Diff: res.Diff, Changed: res.Changed, Ignore: ignore,
	}, contextpack.DefaultOptions)
	if err != nil {
		return nil, stats, fmt.Errorf("runner: context stages: %w", err)
	}
	if chunks == nil {
		chunks = []contextpack.Chunk{}
	}
	return chunks, stats, nil
}

// heartbeat calls beat now and then every interval until ctx ends. A failed
// beat is logged and retried on the next tick: one lost write must not end
// a run the worker would otherwise see recover.
func heartbeat(ctx context.Context, interval time.Duration, beat func(context.Context) error, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := beat(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("heartbeat failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func beat(ctx context.Context, st *store.Store, runID string) error {
	return st.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runner_runs SET heartbeat_at = now() WHERE id = $1`, runID); err != nil {
			return fmt.Errorf("runner: heartbeat: %w", err)
		}
		return nil
	})
}

func setPhase(ctx context.Context, st *store.Store, runID, phase string) error {
	return st.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error { return setPhaseTx(ctx, tx, runID, phase) })
}

// setPhaseTx is setPhase in tx, alongside what the run wrote there.
func setPhaseTx(ctx context.Context, tx pgx.Tx, runID, phase string) error {
	tag, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = $2 WHERE id = $1`, runID, phase)
	if err != nil {
		return fmt.Errorf("runner: set phase: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("runner: run %s is not visible to this role", runID)
	}
	return nil
}

// fail records cause as the run's error, with its secrets masked: a git or
// provider error may carry a credential.
func fail(ctx context.Context, st *store.Store, runID string, secrets Secrets, cause error) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := st.WithRunnerJob(fctx, runID, func(tx pgx.Tx) error {
		_, err := tx.Exec(fctx, `UPDATE runner_runs SET phase = 'failed', error = left($2, 2000) WHERE id = $1`,
			runID, secrets.Mask(cause.Error()))
		return err
	})
	if err != nil {
		return fmt.Errorf("runner: record failure: %w", err)
	}
	return nil
}
