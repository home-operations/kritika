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
// built from that diff, the rules, skills and agent files of its paths, and
// the last review's and the dismissed findings on its files, the first part
// also those on files no part reviews, with a submit check that refuses a
// finding on another part's file. tools gets search_code when the
// repository has an index, and load_skill offers every part's skills. It
// returns the parts, and the context and the rules they were given
// together, which the pack keeps.
func partPrompts(
	ctx context.Context, p Spec, secrets Secrets, head, base *object.Tree, res *gitfetch.Result, files repoconfig.Files,
	found []repoconfig.Skill, split [][]string, scope review.Scope, tools *agentTools, logger *slog.Logger,
) ([]reviewPart, []contextpack.Chunk, []string, error) {
	diffs := make([]string, len(split))
	chunks := make([][]contextpack.Chunk, len(split))
	all := []contextpack.Chunk{}
	for i, paths := range split {
		diffs[i] = review.PartDiff(res.Diff, paths)
		var err error
		chunks[i], _, err = contextpack.Build(ctx, contextpack.Input{Head: head, Base: base, Diff: diffs[i], Changed: paths, Ignore: p.Ignore},
			contextpack.DefaultOptions)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("runner: context stages of part %d: %w", i+1, err)
		}
		if out, ok := similarCodeFor(ctx, p, secrets, diffs[i], res.Changed, logger); ok {
			chunks[i] = append(chunks[i], out.Chunks...)
			tools.search = newSearchTool(p, secrets, res.Changed)
		}
		all = append(all, chunks[i]...)
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
	}
	return parts, all, rules, nil
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

// lockedTool runs its tool holding lock, a channel of one the parts of a
// split review running at once share: their tools read the head through one
// go-git repository, which is not safe for concurrent use, and keep the
// counts the run records. A part that ends while it waits runs nothing.
type lockedTool struct {
	agent.Tool
	lock chan struct{}
}

func (t lockedTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	select {
	case t.lock <- struct{}{}:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
	defer func() { <-t.lock }()
	if ctx.Err() != nil {
		return "", context.Cause(ctx)
	}
	return t.Tool.Run(ctx, input)
}

// lockTools wraps each of tools to run holding lock.
func lockTools(tools []agent.Tool, lock chan struct{}) []agent.Tool {
	out := make([]agent.Tool, len(tools))
	for i, t := range tools {
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
	parts []reviewPart, logger *slog.Logger,
) error {
	results, timeline, err := runPartLoops(ctx, p, secrets, head, ignore, tools, parts, logger)
	if err != nil {
		return err
	}
	merged, records, err := mergeParts(parts, results, secrets)
	if err != nil {
		return err
	}
	rec, err := newAgentRecord(merged, timeline, toolSources(tools), secrets)
	if err != nil {
		return err
	}
	rec.useTools(tools)
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
// as p.Agent.Parallel allows and the rest as those end, and returns each
// part's result and the run's timeline, its steps numbered across the parts
// in part order. The parts share the agent time the worker sized the Job
// for, so a part late enough to pass it is left out, ending the run cleanly
// rather than at the Job's deadline.
func runPartLoops(
	ctx context.Context, p Spec, secrets Secrets, head *object.Tree, ignore []string, tools agentTools, parts []reviewPart,
	logger *slog.Logger,
) ([]agent.Result, []store.TimelineStep, error) {
	timeout := time.Duration(p.Agent.TimeoutSeconds) * time.Second
	parallel := max(p.Agent.Parallel, 1)
	rounds := (len(parts) + parallel - 1) / parallel
	deadline := time.Now().Add(min(time.Duration(rounds)*timeout, jobtimeout.MaxAgentTimeout))
	logger.Info("agent started in parts", "model", p.Model.Model, "parts", len(parts), "parallel", parallel, "commands", tools.commands(),
		"search", tools.search != nil)
	results := make([]agent.Result, len(parts))
	steps := make([][]store.TimelineStep, len(parts))
	toolLock := make(chan struct{}, 1)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for i, part := range parts {
		g.Go(func() error {
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
			offered := lockTools(offeredTools(p, head, ignore, tools.extra()), toolLock)
			res, partSteps := agentLoop(gctx, stepper, p, offered, part.prompt, min(timeout, left), plog)
			results[i], steps[i] = res, partSteps
			plog.Info("agent part stopped", "of", len(parts), "files", len(part.paths), "stop", res.Stop, "steps", res.Steps,
				"input_tokens", res.Usage.Prompt(), "output_tokens", res.Usage.Output, "cost_usd", res.CostUSD)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
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
