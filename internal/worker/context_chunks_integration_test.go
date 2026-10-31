//go:build integration

package worker

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/contextpack"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/store"
)

// TestContextChunksGiven: the context pack keeps, and
// kritika_context_chunks_total counts, the context chunks a review's
// opening request holds: not the similar code a smaller prompt budget
// leaves out, which the summary's note counts instead, nor the context of
// a review that is skipped.
func TestContextChunksGiven(t *testing.T) {
	h := newAgenticHarness(t, 1027)
	configure := func(config string) *configfile.File {
		t.Helper()
		f, err := configfiletest.Parse(t, config+"embedding: { model: gateway/fake-embed, dims: 8 }\n")
		if err != nil {
			t.Fatal(err)
		}
		h.review.Current.Set(f)
		return f
	}
	// Similar code that fits the default budget, not one of 8,000 tokens.
	seedIndex(t, h, configure(h.config).Embedding, configfile.RepositoryID(h.account.ID(), "acme/widgets"),
		strings.Repeat("func b() { helper() }\n", 2000))
	reg := prometheus.NewRegistry()
	h.setMetrics(metrics.New(reg))
	counted := func() map[string]int {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, f := range families {
			if f.GetName() != "kritika_context_chunks_total" {
				continue
			}
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "stage" {
						got[l.GetValue()] += int(m.GetCounter().GetValue())
					}
				}
			}
		}
		return got
	}
	// reviewed has review start a review and wait for it to end. It makes
	// sure that the review ended with want. It returns the review's pack and
	// the context sections of its opening request, which the pack must match.
	reviewed := func(want string, review func() (id, status string)) (store.ContextPackRecord, map[string]int) {
		t.Helper()
		h.sm.reset(scriptSubmit)
		h.sm.mu.Lock()
		first := len(h.sm.bodies)
		h.sm.mu.Unlock()
		reviewID, status := review()
		if status != want {
			t.Fatalf("status = %s, want %s", status, want)
		}
		var rec store.ContextPackRecord
		if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
			var runID string
			if err := tx.QueryRow(h.ctx, `SELECT id FROM runner_runs WHERE review_id = $1`, reviewID).Scan(&runID); err != nil {
				return err
			}
			var err error
			rec, err = store.ReadContextPack(h.ctx, tx, runID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		sections := map[string]int{}
		h.sm.mu.Lock()
		if first < len(h.sm.bodies) {
			for _, stage := range []string{contextpack.StageOverlay, contextpack.StageDefinition, contextpack.StageCaller, contextpack.StageSimilar} {
				if n := strings.Count(string(h.sm.bodies[first]), "### "+stage+": "); n > 0 {
					sections[stage] = n
				}
			}
		}
		h.sm.mu.Unlock()
		if !maps.Equal(rec.StageCounts, sections) {
			t.Fatalf("the pack holds %v, the opening request's sections %v", rec.StageCounts, sections)
		}
		return rec, sections
	}
	const cutNote = "1 context chunk(s) left out of the prompt to fit its budget"

	// helper is on the base, so stage 2 has its definition to give.
	h.lf.setBase(h.commit(t, "helper.go", "package main\n\nfunc helper() {}\n"))
	head := h.commit(t, "main.go", "package main\n\nfunc b() { helper() }\n")
	whole, given := reviewed("completed", func() (string, string) {
		h.dispatch(t, head)
		id, status, _ := h.waitReview(t, head)
		return id, status
	})
	if given[contextpack.StageSimilar] != 1 || given[contextpack.StageDefinition] == 0 || slices.Contains(whole.Notes, cutNote) {
		t.Fatalf("a review within the default budget was given %v, notes %q; want the similar code and helper's definition", given, whole.Notes)
	}
	if got := counted(); !maps.Equal(got, given) {
		t.Fatalf("counted %v, want the prompt's %v", got, given)
	}

	const timeout = "  commandTimeout: 5s\n"
	if !strings.Contains(h.config, timeout) {
		t.Fatalf("the harness configuration has no %q", timeout)
	}
	configure(strings.Replace(h.config, timeout, timeout+"  prompt: 8000\n", 1))
	cut, cutGiven := reviewed("completed", func() (string, string) {
		// A re-run at the reviewed head starts afresh rather than carrying
		// on the conversation, so what cuts its context is the budget.
		h.rerun(t, head)
		id, status, _ := h.waitReview(t, head)
		return id, status
	})
	if cutGiven[contextpack.StageSimilar] != 0 || cutGiven[contextpack.StageDefinition] == 0 || !slices.Contains(cut.Notes, cutNote) {
		t.Fatalf("a review within 8,000 tokens was given %v, notes %q; want helper's definition alone and the cut noted", cutGiven, cut.Notes)
	}
	want := maps.Clone(given)
	want[contextpack.StageDefinition] += cutGiven[contextpack.StageDefinition]
	if got := counted(); !maps.Equal(got, want) {
		t.Fatalf("counted %v, want the prompts' %v", got, want)
	}

	configure(h.config + "trigger: { exclude: [{ name: too-large, expr: pr.lines > 0 }] }\n")
	next := h.commit(t, "main.go", "package main\n\nfunc b() { helper() }\n\nfunc d() { helper() }\n")
	if skipped, _ := reviewed("skipped", func() (string, string) {
		h.dispatch(t, next)
		id, status, _ := h.waitReview(t, next)
		return id, status
	}); len(skipped.StageCounts) != 0 {
		t.Fatalf("a skipped review's pack holds %v", skipped.StageCounts)
	}
	if got := counted(); !maps.Equal(got, want) {
		t.Fatalf("counted %v after a skipped review, want the prompts' %v", got, want)
	}
}
