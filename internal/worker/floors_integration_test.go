//go:build integration

package worker

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
)

// TestRepositoryFloorHoldsRenameEndToEnd: a floor that the merge-base
// .kritika.yaml sets, where the admin sets none, holds the risk of a change
// that renames a file out from under it. The runner names a rename by its
// head-side name, and the floor matches the base-side name as well.
func TestRepositoryFloorHoldsRenameEndToEnd(t *testing.T) {
	h := newAgenticHarness(t, 1546)
	fc := &fakeCompleter{}
	h.setSteppers(&adapter.Steppers{Build: func(configfile.Provider) (model.Stepper, error) { return fc, nil }})
	scored := *h.file
	scored.Defaults.Confidence.Model = new(configfile.ModelRef("gateway/judge"))
	h.review.Current.Set(&scored)
	t.Cleanup(func() { h.review.Current.Set(h.file) })

	migration := "CREATE TABLE widgets (\n" + strings.Repeat("  name text NOT NULL,\n", 8) + "  id bigint PRIMARY KEY\n);\n"
	base := h.commitTree(t, map[string]string{
		".kritika.yaml":       "confidence: { floors: { critical: ['db/migrations/**'] } }\n",
		"db/migrations/1.sql": migration,
	}, nil)
	h.lf.setBase(base)
	head := h.commitTree(t, map[string]string{"archive/1.sql": migration + "-- archived\n"}, []string{"db/migrations/1.sql"})

	h.sm.reset(scriptSubmit)
	h.dispatch(t, head)
	id, status, errText := h.waitReview(t, head)
	if status != "completed" {
		t.Fatalf("status = %s (%s)", status, errText)
	}
	var changed, renamed []string
	var c review.Confidence
	if err := h.st.WithAccount(h.ctx, h.account.ID(), func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT c.changed_paths, c.renamed_from, v.confidence FROM reviews v
			JOIN runner_runs rr ON rr.review_id = v.id JOIN context_packs c ON c.runner_run_id = rr.id WHERE v.id = $1`, id).
			Scan(&changed, &renamed, &c)
	}); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "archive/1.sql" || len(renamed) != 1 || renamed[0] != "db/migrations/1.sql" {
		t.Fatalf("changed = %q, renamed from %q; want the rename by its head-side name alone", changed, renamed)
	}
	if c.Risk != review.RiskCritical || c.Rated != review.RiskMedium || c.Held != "db/migrations/1.sql" {
		t.Fatalf("risk = %q, rated %q, held by %q; want the scorer's medium held to critical by the old name", c.Risk, c.Rated, c.Held)
	}
}
