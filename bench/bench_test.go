//go:build bench

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/gitfetch"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
)

// Modes are the ablations: what the prompt carries beyond the diff.
var modes = map[string]func([]contextpack.Chunk) []contextpack.Chunk{
	// diff: the diff alone.
	"diff": func([]contextpack.Chunk) []contextpack.Chunk { return nil },
	// overlay: whole declarations the diff touches.
	"overlay": func(cs []contextpack.Chunk) []contextpack.Chunk { return keep(cs, contextpack.StageOverlay) },
	// context: stages 1 to 3 as the runner builds them.
	"context": func(cs []contextpack.Chunk) []contextpack.Chunk { return cs },
}

func keep(cs []contextpack.Chunk, stage string) []contextpack.Chunk {
	var out []contextpack.Chunk
	for _, c := range cs {
		if c.Stage == stage {
			out = append(out, c)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestBench runs the corpus. Environment:
//
//	OPENROUTER_API_KEY     model key (required unless KRITIKA_BENCH_DRY=1)
//	KRITIKA_BENCH_MODEL     model id on OpenRouter (default openai/gpt-6-sol)
//	KRITIKA_BENCH_MODES     comma list of modes (default diff,context)
//	KRITIKA_BENCH_CASES     corpus glob (default bench/cases/*.yaml)
//	KRITIKA_BENCH_LIMIT     at most this many cases (default all)
//	KRITIKA_BENCH_ONLY      run one case id
//	KRITIKA_BENCH_DRY       1: fetch, build context and prompts, call no model
//	KRITIKA_BENCH_SCRIPT    1: answer every model call from the corpus itself, with no key (the wiring smoke)
//	KRITIKA_BENCH_JUDGE     model id on OpenRouter that judges whether a finding off an expected range
//	                        identifies its defect (default none: lines alone match)
//	KRITIKA_BENCH_JUDGE_EFFORT  the judge's reasoning effort
//	KRITIKA_BENCH_FLOORS    floors file (default bench/floors.json; absent: no floor); a run under one fails
//	KRITIKA_BENCH_OUT       results directory (default bench/results)
func TestBench(t *testing.T) {
	ctx := t.Context()
	opts, err := readOptions(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	dry := os.Getenv("KRITIKA_BENCH_DRY") == "1"
	script := os.Getenv("KRITIKA_BENCH_SCRIPT") == "1"
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" && !dry && !script {
		t.Skip("OPENROUTER_API_KEY not set (use KRITIKA_BENCH_DRY=1 to run without a model, or KRITIKA_BENCH_SCRIPT=1 for the wiring smoke)")
	}
	cases, err := Load(envOr("KRITIKA_BENCH_CASES", "cases/*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if only := os.Getenv("KRITIKA_BENCH_ONLY"); only != "" {
		var sel []Case
		for _, c := range cases {
			if c.ID == only {
				sel = append(sel, c)
			}
		}
		cases = sel
	}
	if lim, _ := strconv.Atoi(os.Getenv("KRITIKA_BENCH_LIMIT")); lim > 0 && lim < len(cases) {
		cases = cases[:lim]
	}
	if len(cases) == 0 {
		t.Fatal("no cases selected")
	}
	modeNames := strings.Split(envOr("KRITIKA_BENCH_MODES", "diff,context"), ",")
	for _, m := range modeNames {
		if _, ok := modes[m]; !ok {
			t.Fatalf("unknown mode %q; modes are diff, overlay, context", m)
		}
	}
	modelID := envOr("KRITIKA_BENCH_MODEL", "openai/gpt-6-sol")
	stepper, raw, scripts, modelID := benchStepper(t, opts, key, modelID, dry, script)
	j := newJudge(raw, opts.Judge, opts.JudgeEffort)
	floors, gated, err := LoadFloors(envOr("KRITIKA_BENCH_FLOORS", "floors.json"))
	if err != nil {
		t.Fatal(err)
	}

	rep := report{Options: opts, Model: modelID, Judge: j.Name(), When: time.Now().UTC(), Dry: dry, Scripted: script, Cases: len(cases),
		Modes: map[string]modeTotals{}}
	for _, c := range cases {
		if scripts != nil {
			scripts.use(c)
		}
		t.Logf("%s: fetching %s..%s", c.ID, c.Base[:7], c.Head[:7])
		res, err := gitfetch.Run(ctx, gitfetch.Fetch{CloneURL: c.CloneURL, Token: os.Getenv("GITHUB_TOKEN"), Head: c.Head, Base: c.Base})
		if err != nil {
			t.Logf("%s: fetch failed: %v", c.ID, err)
			for _, m := range modeNames {
				rep.Results = append(rep.Results, caseResult{ID: c.ID, Mode: m, Error: "fetch: " + err.Error()})
			}
			continue
		}
		headTree, _ := res.Head.Tree()
		baseTree, _ := res.Base.Tree()
		chunks, stats, err := contextpack.Build(ctx, contextpack.Input{Head: headTree, Base: baseTree, Diff: res.Diff, Changed: res.Changed}, contextpack.DefaultOptions)
		if err != nil {
			t.Logf("%s: context failed: %v", c.ID, err)
		}
		anchors := review.Anchors(res.Diff)
		for _, m := range modeNames {
			cr := caseResult{ID: c.ID, Mode: m, Context: map[string]int{}}
			selected := modes[m](chunks)
			for _, ch := range selected {
				cr.Context[ch.Stage]++
			}
			// Stage 4 needs an index, which the bench has none of.
			system := review.SystemPrompt(nil, nil, nil, false, false, false, false)
			msg, _, _, _ := review.Build(review.Input{
				Repository: c.Repository, Number: c.PR, Title: c.Title, Author: "author", BaseRef: "main",
				Changed: res.Changed, Diff: res.Diff, Context: selected, BudgetTokens: review.UserBudget(system, 0),
			})
			cr.PromptChars = len(system) + len(msg)
			for _, e := range c.Expected {
				if e.Must {
					cr.MustTotal++
				} else {
					cr.NiceTotal++
				}
			}
			if !dry {
				runCase(ctx, t, &cr, c, opts, run{stepper: stepper, model: modelID, system: system, user: msg, head: headTree, anchors: anchors, judge: j})
			}
			t.Logf("%s [%s]: must %d/%d nice %d/%d (%d by the judge) findings %d matched %d cost $%.4f context %v %s",
				c.ID, m, cr.MustHit, cr.MustTotal, cr.NiceHit, cr.NiceTotal, cr.JudgeHit, len(cr.Findings), cr.Matched, cr.CostUSD, cr.Context,
				errText(cr.Error))
			rep.Results = append(rep.Results, cr)
		}
		_ = res.Close()
		_ = stats
	}
	for _, m := range modeNames {
		rep.Modes[m] = totals(rep.Results, m)
	}
	printSummary(t, rep)
	writeReport(t, rep)
	switch {
	case script:
		checkScripted(t, rep)
	case gated && !dry:
		gate(t, floors, rep)
	}
}

// benchStepper returns four values. The first is the model that a run answers
// with: the corpus's own script, none for a dry run, or otherwise the
// OpenRouter model at opts's effort. The second is the same model with no
// effort of its own, so that the judge can set its effort. The third is the
// script, when the script is the model. The fourth is the model's name for
// the report.
func benchStepper(t *testing.T, opts benchOptions, key, modelID string, dry, script bool) (stepper, raw model.Stepper, scripts *scripted, name string) {
	t.Helper()
	switch {
	case script:
		s := &scripted{}
		return s, s, s, "scripted"
	case dry:
		return nil, nil, nil, modelID
	}
	stepper, err := model.NewStepper(model.ProviderOpenRouter, "", key, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return effortStepper{Stepper: stepper, effort: opts.Effort}, stepper, nil, modelID
}

// checkScripted fails a scripted run that did not go through: the script
// lands a finding on every expected range, so any case that errored or
// missed one is the harness broken, not the model.
func checkScripted(t *testing.T, rep report) {
	t.Helper()
	for _, r := range rep.Results {
		switch {
		case r.Error != "":
			t.Errorf("%s [%s]: %s", r.ID, r.Mode, r.Error)
		case r.MustHit != r.MustTotal || r.NiceHit != r.NiceTotal:
			t.Errorf("%s [%s]: the script's findings hit %d of %d must and %d of %d nice ranges", r.ID, r.Mode, r.MustHit, r.MustTotal,
				r.NiceHit, r.NiceTotal)
		}
	}
}

// maxErrorShare is the share of a mode's cases that can fail to run while
// the bench still judges the mode against its floors. Past this share, the
// measures are of too few cases to mean anything. Kodus also leaves such a
// run unmeasured rather than passed.
const maxErrorShare = 0.05

// gate fails the test for what rep measures under floors, when the floors
// apply to a run scored as rep was. It also fails the test for a mode in
// which too many cases did not run to be measured.
func gate(t *testing.T, floors Floors, rep report) {
	t.Helper()
	under, held := floors.Check(rep)
	if !held {
		t.Logf("floors not applied: they were set with judge %q and this run was scored by %q", floors.Judge, rep.Judge)
		return
	}
	for _, u := range under {
		t.Error(u)
	}
	for _, m := range slices.Sorted(maps.Keys(floors.Modes)) {
		if x, ran := rep.Modes[m]; ran && float64(x.Errors) > maxErrorShare*float64(x.Cases) {
			t.Errorf("%s: %d of %d cases did not run, too many for the floors to judge the rest", m, x.Errors, x.Cases)
		}
	}
}

// run is what a case's agent runs on: the model, the prompt, the head
// tree its tools read, the diff's anchors and the judge.
type run struct {
	stepper      model.Stepper
	model        string
	system, user string
	head         *object.Tree
	anchors      map[string]map[int]string
	judge        *judge
}

// runCase runs the agent on c under one mode and records on cr what it
// spent, what it submitted and what of c it found.
func runCase(ctx context.Context, t *testing.T, cr *caseResult, c Case, opts benchOptions, r run) {
	t.Helper()
	tree := agent.NewTree(r.head, nil)
	limits := opts.limits()
	started := time.Now()
	out := agent.Run{
		Stepper: r.stepper, Model: r.model, System: r.system, User: r.user,
		Tools: []agent.Tool{
			agent.ReadFileTool(tree, limits.MaxToolOutputBytes),
			agent.GrepTool(tree, limits.MaxToolOutputBytes),
			agent.ListFilesTool(tree, limits.MaxToolOutputBytes),
		},
		Submit: runner.SubmitTool(false, false), Limits: limits,
	}.Do(ctx)
	cr.Latency = time.Since(started)
	cr.Steps, cr.ToolCalls = out.Steps, out.ToolCalls
	cr.Input, cr.Cached, cr.Output, cr.CostUSD = out.Usage.Prompt(), out.Usage.CacheRead, out.Usage.Output, out.CostUSD
	switch {
	case out.Stop == agent.StopError:
		cr.Error = out.Err
		t.Logf("%s [%s]: model error: %s", c.ID, cr.Mode, out.Err)
	case out.Stop != agent.StopSubmitted:
		cr.Error = "agent stopped: " + string(out.Stop)
	default:
		parsed, dropped, err := review.Parse(string(out.Submitted), r.anchors, review.ParseOptions{})
		if err != nil {
			cr.Error = err.Error()
		} else {
			cr.Findings, cr.Dropped = parsed.Findings, len(dropped)
			score(ctx, cr, c, r.judge)
		}
	}
}

// score counts the expected ranges that cr's findings hit. A finding hits a
// range on its lines. When j is set, a finding in the file that landed on no
// range can also hit it by j's verdict. j judges only such findings, because
// a finding identifies one defect. score also counts the findings that land
// on a range.
func score(ctx context.Context, cr *caseResult, c Case, j *judge) {
	landed := func(f review.Finding) bool {
		return slices.ContainsFunc(c.Expected, func(e Expected) bool { return e.Matches(f.Path, f.Line) })
	}
	for _, e := range c.Expected {
		hit := slices.ContainsFunc(cr.Findings, func(f review.Finding) bool { return e.Matches(f.Path, f.Line) })
		if !hit && j != nil {
			for _, f := range cr.Findings {
				if f.Path != e.Path || landed(f) {
					continue
				}
				v := j.Matches(ctx, e, f)
				cr.Judged = append(cr.Judged, v)
				cr.JudgeCost += v.CostUSD
				if v.Match {
					hit = true
					cr.JudgeHit++
					break
				}
			}
		}
		if hit && e.Must {
			cr.MustHit++
		} else if hit {
			cr.NiceHit++
		}
	}
	for _, f := range cr.Findings {
		for _, e := range c.Expected {
			if e.Matches(f.Path, f.Line) {
				cr.Matched++
				break
			}
		}
	}
}

func totals(results []caseResult, mode string) modeTotals {
	var tot modeTotals
	var must, mustHit, nice, niceHit, calls int
	var latency time.Duration
	for _, r := range results {
		if r.Mode != mode {
			continue
		}
		tot.Cases++
		// A failed case spent its tokens all the same: a budget that cuts
		// off a submission must not look cheaper for it.
		tot.CostUSD += r.CostUSD
		tot.JudgeCost += r.JudgeCost
		tot.InputTokens += r.Input
		tot.OutputTokens += r.Output
		if r.Error != "" {
			tot.Errors++
			continue
		}
		must += r.MustTotal
		mustHit += r.MustHit
		nice += r.NiceTotal
		niceHit += r.NiceHit
		tot.Findings += len(r.Findings)
		tot.Matched += r.Matched
		tot.JudgeHit += r.JudgeHit
		for name, n := range r.ToolCalls {
			if name != "submit_review" {
				calls += n
			}
		}
		latency += r.Latency
	}
	if must > 0 {
		tot.MustRecall = float64(mustHit) / float64(must)
	}
	if nice > 0 {
		tot.NiceRecall = float64(niceHit) / float64(nice)
	}
	if tot.Findings > 0 {
		tot.MatchRate = float64(tot.Matched) / float64(tot.Findings)
	}
	if n := tot.Cases - tot.Errors; n > 0 {
		tot.MeanLatency = latency.Seconds() / float64(n)
		tot.MeanFindings = float64(tot.Findings) / float64(n)
		tot.MeanToolCalls = float64(calls) / float64(n)
	}
	return tot
}

func printSummary(t *testing.T, rep report) {
	t.Helper()
	names := slices.Sorted(maps.Keys(rep.Modes))
	var b strings.Builder
	fmt.Fprintf(&b, "\nmodel %s, %d cases%s%s\n", rep.Model, rep.Cases, map[bool]string{true: " (dry: no model calls)", false: ""}[rep.Dry],
		map[bool]string{true: ", judge " + rep.Judge, false: ", lines alone match"}[rep.Judge != ""])
	fmt.Fprintf(&b, "%-8s %6s %6s %11s %11s %9s %9s %9s %9s %8s\n", "mode", "cases", "errors", "must recall", "nice recall", "findings",
		"matched", "judged", "cost usd", "mean s")
	for _, m := range names {
		x := rep.Modes[m]
		fmt.Fprintf(&b, "%-8s %6d %6d %10.0f%% %10.0f%% %9d %9d %9d %9.4f %8.1f\n",
			m, x.Cases, x.Errors, x.MustRecall*100, x.NiceRecall*100, x.Findings, x.Matched, x.JudgeHit, x.CostUSD+x.JudgeCost, x.MeanLatency)
		fmt.Fprintf(&b, "%-8s mean findings %.2f, mean tool calls %.2f per case\n", "", x.MeanFindings, x.MeanToolCalls)
	}
	b.WriteString("matched = findings that land on an expected range; judged = expected ranges the judge alone credited; " +
		"the rest need a human label for precision.\n")
	t.Log(b.String())
}

func writeReport(t *testing.T, rep report) {
	t.Helper()
	dir := envOr("KRITIKA_BENCH_OUT", "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, rep.When.Format("20060102-150405.000000000")+".json")
	raw, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report written to %s", name)
}

func errText(s string) string {
	if s == "" {
		return ""
	}
	return "error: " + s
}
