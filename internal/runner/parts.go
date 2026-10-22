package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"
	"golang.org/x/sync/errgroup"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/jobtimeout"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// splitReview is the parts a review is split into, the paths of each, nil
// when it is reviewed whole: the diff it reads, the pull request's or, on
// an incremental re-review, the diff since the last review, cut where it is
// over review.PartBytes into no more parts than its grant covers.
func splitReview(p Spec, res *gitfetch.Result, scope review.Scope) [][]string {
	if p.Agent == nil {
		return nil
	}
	diff := res.Diff
	if scope == review.ScopeIncremental {
		diff = res.DeltaDiff
	}
	return review.SplitDiff(diff, p.Ignore, review.PartBytes, p.Agent.Parts)
}

// reviewPart is one part of a split review: the files it reviews and its
// prompt.
type reviewPart struct {
	paths  []string
	prompt agentPrompt
}

// partPrompts writes each part's prompt: its own files' diff, the context
// built from that diff, the rules, skills and context references of its
// paths, and the last review's and the dismissed findings on its files.
// The first part also gets those on files no part reviews. Every part has
// a submit check that refuses a finding on another part's file.
// tools gets search_code when the
// repository has an index, and load_skill offers every part's skills. It
// returns the parts, and the context and the rules they were given
// together, which the pack keeps.
func partPrompts(
	ctx context.Context, p Spec, secrets Secrets, head, base *object.Tree, res *gitfetch.Result, files repoconfig.Files,
	found []repoconfig.Skill, split [][]string, scope review.Scope, tools *agentTools, logger *slog.Logger,
) ([]reviewPart, []contextpack.Chunk, []string, error) {
	diffs := make([]string, len(split))
	inputs := make([]contextpack.Part, len(split))
	for i, paths := range split {
		diffs[i] = review.PartDiff(res.Diff, paths)
		inputs[i] = contextpack.Part{Diff: diffs[i], Changed: paths}
	}
	chunks, stats, err := contextpack.BuildParts(ctx, head, base, p.Ignore, inputs, contextpack.DefaultOptions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("runner: context stages of parts: %w", err)
	}
	logger.Info("parts context built", "parts", len(split), "files_scanned", stats.FilesScanned, "files_parsed", stats.FilesParsed,
		"bytes_scanned", stats.BytesScanned, "truncated", stats.ScanTruncated, "elapsed", stats.Elapsed)
	similar, err := partSimilarCode(ctx, p, secrets, diffs, res.Changed, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("runner: similar code of parts: %w", err)
	}
	for i, out := range similar {
		if out.Indexed {
			chunks[i] = append(chunks[i], out.Chunks...)
			tools.search = newSearchTool(p, secrets, res.Changed)
		}
	}
	ins := make([]promptInputs, len(split))
	var skills []repoconfig.Skill
	var loaded []string
	rules := []string{}
	for i, paths := range split {
		ins[i] = newPromptInputs(p, files, found, paths)
		for _, s := range slices.Concat(ins[i].skills, ins[i].loaded) {
			if !slices.ContainsFunc(skills, func(o repoconfig.Skill) bool { return o.Name == s.Name }) {
				skills = append(skills, s)
			}
		}
		for _, s := range ins[i].loaded {
			if !slices.Contains(loaded, s.Name) {
				loaded = append(loaded, s.Name)
			}
		}
		for _, id := range ins[i].ruleIDs() {
			if !slices.Contains(rules, id) {
				rules = append(rules, id)
			}
		}
	}
	tools.skills = nil
	if len(skills) > 0 {
		tools.skills = &skillTool{base: base, skills: skills, maxBytes: p.Agent.limits().MaxToolOutputBytes, opened: loaded}
	}
	parts := make([]reviewPart, len(split))
	all := []contextpack.Chunk{}
	for i, paths := range split {
		pack := packView{
			Diff: diffs[i], Changed: res.Changed, Context: chunks[i], DeltaDiff: review.PartDiff(res.DeltaDiff, paths), Scope: scope,
			Part: &review.PartInput{Index: i + 1, Count: len(split), Paths: paths},
		}
		checks := paths
		if i == 0 {
			checks = append(slices.Clone(paths), unsplitPaths(p, split)...)
		}
		prompt := newAgentPrompt(partSpec(p, checks), ins[i], pack, tools.commands(), tools.fetch != nil, tools.search != nil)
		prompt.validate = review.CheckPart(paths, slices.Concat(slices.Concat(split[:i]...), slices.Concat(split[i+1:]...)))
		prompt.fallback = review.Lenient(prompt.validate)
		parts[i] = reviewPart{paths: paths, prompt: prompt}
		all = append(all, prompt.context...)
	}
	return parts, all, rules, nil
}

// partSimilarCode overlaps remote retrieval within the review's granted
// parallelism. It reads no git objects; those belong to the serial context pass.
func partSimilarCode(
	ctx context.Context, p Spec, secrets Secrets, diffs, changed []string, logger *slog.Logger,
) ([]contextpack.SimilarResponse, error) {
	out := make([]contextpack.SimilarResponse, len(diffs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, p.Agent.Parallel))
	for i, diff := range diffs {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			out[i], _ = similarCodeFor(gctx, p, secrets, diff, changed, logger.With("part", i+1))
			return gctx.Err()
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// unsplitPaths are the files of the last review's and the dismissed
// findings that no part of split reviews, which the first part checks
// again, as a review not split would.
func unsplitPaths(p Spec, split [][]string) []string {
	var out []string
	add := func(path string) {
		if !slices.ContainsFunc(split, func(paths []string) bool { return slices.Contains(paths, path) }) && !slices.Contains(out, path) {
			out = append(out, path)
		}
	}
	for _, f := range p.Prompt.Prior {
		add(f.Path)
	}
	for _, d := range p.Prompt.Dismissed {
		add(d.Path)
	}
	return out
}

// partSpec is p for one part of a split review: the last review's and the
// dismissed findings on paths alone, and no diagram to carry forward, since
// the review's summary is written from all the parts'.
func partSpec(p Spec, paths []string) Spec {
	prompt := *p.Prompt
	theirs := func(path string) bool { return !slices.Contains(paths, path) }
	prompt.Prior = slices.DeleteFunc(slices.Clone(prompt.Prior), func(f review.Finding) bool { return theirs(f.Path) })
	prompt.Dismissed = slices.DeleteFunc(slices.Clone(prompt.Dismissed), func(d review.DismissedFinding) bool {
		return theirs(d.Path)
	})
	prompt.PriorDiagram = ""
	p.Prompt = &prompt
	return p
}

// splitNotes are what the summary states of a split review: its parts,
// and what their prompts' cuts left out, stated as one prompt's would be.
func splitNotes(parts []reviewPart) []string {
	var whole agentPrompt
	for _, part := range parts {
		whole.omitted = append(whole.omitted, part.prompt.omitted...)
		whole.contextOmitted += part.prompt.contextOmitted
	}
	return append([]string{fmt.Sprintf(noteSplit, len(parts))}, whole.notes()...)
}

// minPartTime is the least time a part of a split review starts with, or
// half its timeout when that is less: less is not enough for an agent to
// read its files and answer. Its whole timeout would be too much, since the
// run's deadline leaves the last round no more than that, less what the run
// took to reach it.
const minPartTime = 2 * time.Minute

// partLocks are what the parts of a split review running at once share,
// each a channel of one, so that a part's slow command, fetch or search
// holds up another part's read of a file no more than it must. head
// serializes what reads the head or the base through the run's one go-git
// repository, which is not safe for concurrent use: read_file, grep,
// list_files, load_skill and the checkout the run tool writes on its first
// command. writes serializes the run tool and fetch_repo, which share the
// directory beside the checkout and its budget, and read_file when it reads
// there. search and oci keep the counts those tools record. A holder of
// more than one takes them in the order writes, head, oci: the run tool
// holds writes while its checkout takes head, and a part's checkpoint
// reads the run tool, fetch_repo and fetch_oci.
type partLocks struct {
	head, writes, search, oci chan struct{}
}

func newPartLocks() *partLocks {
	return &partLocks{
		head: make(chan struct{}, 1), writes: make(chan struct{}, 1), search: make(chan struct{}, 1), oci: make(chan struct{}, 1),
	}
}

// acquire takes lock, a channel of one, and returns what releases it; a
// nil lock is nothing to take. A part that ends while it waits takes
// nothing.
func acquire(ctx context.Context, lock chan struct{}) (func(), error) {
	if lock == nil {
		return func() {}, nil
	}
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	if ctx.Err() != nil {
		<-lock
		return nil, context.Cause(ctx)
	}
	return func() { <-lock }, nil
}

// checkpoint runs f holding writes and oci, unless ctx ends first.
func (l *partLocks) checkpoint(ctx context.Context, f func() error) error {
	for _, lock := range []chan struct{}{l.writes, l.oci} {
		release, err := acquire(ctx, lock)
		if err != nil {
			return err
		}
		defer release()
	}
	return f()
}

// lockedTool runs its tool holding lock.
type lockedTool struct {
	agent.Tool
	lock chan struct{}
}

func (t lockedTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	release, err := acquire(ctx, t.lock)
	if err != nil {
		return "", err
	}
	defer release()
	return t.Tool.Run(ctx, input)
}

// lockTools wraps each of tools to run holding the lock of locks for what
// it touches; read_file over fetched files takes head or writes itself, by
// the path, and read_diff reads what it was built with and takes none. A
// tool of any other type takes head: one that writes beside the checkout
// must be listed with the run tool.
func lockTools(tools []agent.Tool, locks *partLocks) []agent.Tool {
	out := make([]agent.Tool, len(tools))
	for i, t := range tools {
		var lock chan struct{}
		switch t := t.(type) {
		case fetchedReadFile:
			t.head, t.writes = locks.head, locks.writes
			out[i] = t
			continue
		case *readDiffTool:
			out[i] = t
			continue
		case *agent.RunTool, *fetchRepoTool:
			lock = locks.writes
		case *fetchOCITool:
			lock = locks.oci
		case *searchTool:
			lock = locks.search
		default:
			lock = locks.head
		}
		out[i] = lockedTool{Tool: t, lock: lock}
	}
	return out
}

// runParts runs a split review's parts, each an agent of its own with the
// run's limits over the same tools, and writes their run as one row: the
// findings every part submitted under a joined summary, and how each part
// ended.
func runParts(
	ctx context.Context, st *store.Store, p Spec, secrets Secrets, head *object.Tree, ignore []string, tools agentTools,
	locks *partLocks, parts []reviewPart, logger *slog.Logger,
) error {
	hash, err := partInputHash(p, parts, tools)
	if err != nil {
		return err
	}
	saved, err := loadParts(ctx, st, p, hash, parts, logger)
	if err != nil {
		return err
	}
	save := func(ctx context.Context, part int, result agent.Result) error {
		return savePart(ctx, st, p, secrets, hash, part, result, tools)
	}
	results, timeline, err := runPartLoops(ctx, p, secrets, head, ignore, tools, locks, parts, logger, saved, save,
		runnerPartSlots(st, p, logger))
	if err != nil {
		return err
	}
	merged, records, err := mergeParts(parts, results, secrets)
	if err != nil {
		return err
	}
	sources := toolSources(tools)
	for i, part := range saved {
		records[i].ReusedFrom = part.SourceRun
		sources = checkpointNames(sources, part.Sources)
	}
	rec, err := newAgentRecord(merged, timeline, sources, secrets)
	if err != nil {
		return err
	}
	rec.useTools(tools)
	for _, part := range saved {
		rec.skillsOpened = checkpointNames(rec.skillsOpened, part.SkillsOpened)
		rec.commandsRun = checkpointNames(rec.commandsRun, part.CommandsRun)
		rec.commandErrors = checkpointNames(rec.commandErrors, part.CommandErrors)
	}
	if rec.parts, err = json.Marshal(records); err != nil {
		return fmt.Errorf("runner: encode parts: %w", err)
	}
	logger.Info("agent stopped", "stop", merged.Stop, "parts", len(parts), "steps", merged.Steps, "input_tokens", merged.Usage.Prompt(),
		"output_tokens", merged.Usage.Output, "cost_usd", merged.CostUSD, "error", rec.err)
	if cerr := ctx.Err(); cerr != nil {
		// What the parts spent is spent: the row is written on a context of
		// its own, as a cancelled agent's is.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), canceledWriteTimeout)
		defer cancel()
		return errors.Join(fmt.Errorf("runner: agent: %w", cerr), writeAgentRun(wctx, st, p, rec, "failed"))
	}
	return writeAgentRun(ctx, st, p, rec, "done")
}

// runPartLoops runs the agents of a split review's parts, as many at once
// as the worker grants slots for, and returns each
// part's result and the run's timeline, its steps numbered across the parts
// in part order. The parts share the agent time the worker sized the Job
// for, so a part late enough to pass it is left out, ending the run cleanly
// rather than at the Job's deadline.
func runPartLoops(
	ctx context.Context, p Spec, secrets Secrets, head *object.Tree, ignore []string, tools agentTools, locks *partLocks,
	parts []reviewPart, logger *slog.Logger,
	saved []partCheckpoint, save func(context.Context, int, agent.Result) error, report func(context.Context, int) int,
) ([]agent.Result, []store.TimelineStep, error) {
	timeout := time.Duration(p.Agent.TimeoutSeconds) * time.Second
	parallel := max(p.Agent.Parallel, 1)
	rounds := (len(parts) + parallel - 1) / parallel
	deadline := time.Now().Add(min(time.Duration(rounds)*timeout, jobtimeout.MaxAgentTimeout))
	logger.Info("agent started in parts", "model", p.Model.Model, "parts", len(parts), "parallel", parallel, "commands", tools.commands(),
		"search", tools.search != nil)
	results := make([]agent.Result, len(parts))
	steps := make([][]store.TimelineStep, len(parts))
	pending := make([]int, 0, len(parts))
	for i := range parts {
		if len(saved) > i && saved[i].Result.Stop == agent.StopSubmitted {
			results[i] = saved[i].Result
			logger.Info("agent part reused", "part", i+1, "source_run", saved[i].SourceRun)
			continue
		}
		results[i] = agent.Result{Stop: agent.StopCanceled, Err: "part not started"}
		pending = append(pending, i)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	err := scheduleParts(ctx, pending, parallel, ticker.C, report, func(gctx context.Context, i int) error {
		part := parts[i]
		left := time.Until(deadline)
		switch {
		case gctx.Err() != nil:
			results[i] = agent.Result{Stop: agent.StopCanceled, Err: context.Cause(gctx).Error()}
			return nil
		case left < min(minPartTime, timeout/2):
			results[i] = agent.Result{Stop: agent.StopCanceled, Err: "no agent time left"}
			return nil
		}
		stepper, err := gatewayStepper(p, secrets, i+1)
		if err != nil {
			return err
		}
		plog := logger.With("part", i+1)
		offered := lockTools(offeredTools(p, head, ignore, tools.extra()), locks)
		res, partSteps := agentLoop(gctx, stepper, p, offered, part.prompt, min(timeout, left), plog)
		results[i], steps[i] = res, partSteps
		// The checkpoint is a retry's shortcut: an answer it fails to
		// keep, or a cancellation finds it waiting to keep, is still
		// this run's to merge. It reads what the run tool, fetch_repo and
		// fetch_oci recorded.
		if res.Stop == agent.StopSubmitted && save != nil {
			if err := locks.checkpoint(gctx, func() error { return save(gctx, i+1, res) }); err != nil {
				plog.Warn("agent part not checkpointed", "error", err)
			}
		}
		plog.Info("agent part stopped", "of", len(parts), "files", len(part.paths), "stop", res.Stop, "steps", res.Steps,
			"input_tokens", res.Usage.Prompt(), "output_tokens", res.Usage.Output, "cost_usd", res.CostUSD)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var timeline []store.TimelineStep
	for i, partSteps := range steps {
		for j := range partSteps {
			partSteps[j].Part, partSteps[j].Index = i+1, len(timeline)+j
		}
		timeline = append(timeline, partSteps...)
	}
	return results, timeline, nil
}

// mergeParts is a split review's run as one agent.Result: the reviews its
// parts submitted joined (review.MergeParts), what they spent together, and
// why the others stopped; and how each part ended, its summary once it
// submitted. A run no part submitted for stops as its first part did.
func mergeParts(parts []reviewPart, results []agent.Result, secrets Secrets) (agent.Result, []store.AgentPart, error) {
	merged := agent.Result{ToolCalls: map[string]int{}}
	var submitted []review.Result
	var stops []string
	records := make([]store.AgentPart, len(parts))
	for i, r := range results {
		merged.Steps += r.Steps
		merged.Usage = merged.Usage.Add(r.Usage)
		merged.CostUSD += r.CostUSD
		merged.UnpricedSteps += r.UnpricedSteps
		merged.Model = cmp.Or(r.Model, merged.Model)
		for name, n := range r.ToolCalls {
			merged.ToolCalls[name] += n
		}
		records[i] = store.AgentPart{Paths: parts[i].paths, Stop: string(r.Stop), Error: secrets.Mask(r.Err), Steps: r.Steps}
		if r.Stop != agent.StopSubmitted {
			stops = append(stops, fmt.Sprintf("part %d of %d: %s", i+1, len(parts), cmp.Or(r.Err, string(r.Stop))))
			continue
		}
		var res review.Result
		if err := json.Unmarshal(r.Submitted, &res); err != nil {
			return agent.Result{}, nil, fmt.Errorf("runner: part %d's review: %w", i+1, err)
		}
		summary, err := json.Marshal(res.Summary)
		if err != nil {
			return agent.Result{}, nil, fmt.Errorf("runner: part %d's summary: %w", i+1, err)
		}
		submitted, records[i].Summary = append(submitted, res), json.RawMessage(secrets.Mask(string(summary)))
	}
	merged.Err = strings.Join(stops, "; ")
	if len(submitted) == 0 {
		merged.Stop = results[0].Stop
		return merged, records, nil
	}
	b, err := json.Marshal(review.MergeParts(submitted))
	if err != nil {
		return agent.Result{}, nil, fmt.Errorf("runner: merge parts: %w", err)
	}
	merged.Stop, merged.Submitted = agent.StopSubmitted, b
	return merged, records, nil
}
