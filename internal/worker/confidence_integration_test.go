//go:build integration

package worker

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/ingest"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store/storetest"
	"github.com/home-operations/kritika/internal/webhook"
)

// TestCarriedConfidence: an unchanged patch keeps the score that its last
// completed review got, judged by the threshold and held to the risk floors
// asked for now. It can be skipped only where there is a score to keep or
// none is asked for.
func TestCarriedConfidence(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	insertOnly, err := river.NewClient(riverpgxv5.New(st.App()), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	out, err := ingest.NewService(st, insertOnly).Dispatch(ctx, ingest.Request{File: file, Account: account, Event: webhook.Event{
		Kind: webhook.KindPullRequest, Action: "opened", Account: "onedr0p",
		Repository:  &webhook.Repository{FullName: "onedr0p/home-ops", DefaultBranch: "main"},
		PullRequest: &webhook.PullRequest{Number: 4243, Title: "t", Author: "a", State: "open", HeadRef: "f", HeadSHA: "abc4243", BaseRef: "main"},
	}})
	if err != nil || out.Status != ingest.Enqueued {
		t.Fatalf("dispatch = %+v, %v", out, err)
	}
	pr, err := loadPullRequest(ctx, st, account.ID(), configfile.RepositoryID(account.ID(), "onedr0p/home-ops"), 4243)
	if err != nil {
		t.Fatal(err)
	}
	scored := configfile.Confidence{Model: "test/judge", Threshold: 3}
	floored := configfile.Confidence{Model: "test/judge", Threshold: 3, Floors: configfile.RiskFloors{review.RiskCritical: {"db/migrations/**"}}}
	errRollback := errors.New("roll back")
	type row struct{ status, confidence string }
	tests := []struct {
		name string
		// rows are the pull request's reviews, oldest first. The last one's
		// id is excluded when exclude is set. changed is the paths that its
		// context pack records.
		rows          []row
		changed       []string
		exclude       bool
		want          configfile.Confidence
		wantScore     int
		wantRisk      review.Risk
		wantHeld      string
		wantSkippable bool
	}{
		{name: "no score asked for", rows: []row{{"completed", ""}}, wantScore: -1, wantSkippable: true},
		{name: "a prepared score cannot be carried", want: scored, wantScore: -1, rows: []row{{"prepared", `{"score":5}`}}},
		{name: "a prepared review cannot hide the last completed score", want: scored, wantScore: 4, wantSkippable: true,
			rows: []row{{"completed", `{"score":4}`}, {"prepared", `{"score":5}`}}},
		{name: "JSON null is unscored", want: scored, wantScore: -1, rows: []row{{"completed", "null"}}},
		{name: "no review yet", want: scored, wantScore: -1},
		{name: "the last review's score, by the threshold asked for now", want: scored, wantScore: 3, wantSkippable: true,
			rows: []row{{"completed", `{"score":5,"threshold":5}`}, {"completed", `{"score":3,"threshold":5}`}}},
		{name: "a last review left unscored", want: scored, wantScore: -1,
			rows: []row{{"completed", `{"score":5,"threshold":5}`}, {"completed", ""}}},
		{name: "a failed review does not count", want: scored, wantScore: 4, wantSkippable: true,
			rows: []row{{"completed", `{"score":4,"threshold":5}`}, {"failed", ""}}},
		{name: "the review asking is left out", want: scored, wantScore: 4, wantSkippable: true, exclude: true,
			rows: []row{{"completed", `{"score":4,"threshold":5}`}, {"prepared", ""}}},
		{name: "a floor set since holds the risk", want: floored, wantScore: 4, wantRisk: review.RiskCritical, wantHeld: "db/migrations/1.sql", wantSkippable: true,
			rows: []row{{"completed", `{"score":4,"threshold":3,"risk":"low","reason":"Docs only."}`}}, changed: []string{"db/migrations/1.sql", "README.md"}},
		{name: "a floor the change is not under", want: floored, wantScore: 4, wantRisk: review.RiskLow, wantSkippable: true,
			rows: []row{{"completed", `{"score":4,"threshold":3,"risk":"low"}`}}, changed: []string{"README.md"}},
		{name: "a floor with no paths recorded", want: floored, wantScore: 4, wantRisk: review.RiskLow, wantSkippable: true,
			rows: []row{{"completed", `{"score":4,"threshold":3,"risk":"low"}`}}},
		{name: "a floor cleared since lets the rating go", want: scored, wantScore: 4, wantRisk: review.RiskLow, wantSkippable: true,
			rows: []row{{"completed", `{"score":4,"threshold":3,"risk":"critical","rated":"low","held":"db/migrations/1.sql"}`}}, changed: []string{"db/migrations/1.sql"}},
		{name: "a risk never rated stays none under a floor", want: floored, wantScore: 4, wantSkippable: true,
			rows: []row{{"completed", `{"score":4,"threshold":3}`}}, changed: []string{"db/migrations/1.sql"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *review.Confidence
			var skippable bool
			err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
				var last string
				for i, r := range tt.rows {
					if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, confidence, created_at)
						VALUES ($1, $2, 'abc4243', $3, nullif($4, '')::jsonb, now() + make_interval(secs => $5)) RETURNING id::text`,
						account.ID(), pr.id, r.status, r.confidence, i).Scan(&last); err != nil {
						return err
					}
				}
				if tt.changed != nil {
					var runID string
					if err := tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind, review_id) VALUES ($1, 'review', $2) RETURNING id`,
						account.ID(), last).Scan(&runID); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, changed_paths, stages, repo_files)
						VALUES ($1, $2, 'abc4243', 'base', 'p', '', $3, '[]', '{}')`, runID, account.ID(), tt.changed); err != nil {
						return err
					}
				}
				if !tt.exclude {
					last = ""
				}
				var err error
				if got, skippable, err = carriedConfidence(ctx, tx, pr.id, last, tt.want); err != nil {
					return err
				}
				return errRollback
			})
			if !errors.Is(err, errRollback) {
				t.Fatal(err)
			}
			score := -1
			if got != nil {
				score = got.Score
				if got.Threshold != tt.want.Threshold {
					t.Errorf("threshold = %d, want the one asked for now, %d", got.Threshold, tt.want.Threshold)
				}
				if got.Risk != tt.wantRisk || got.Held != tt.wantHeld {
					t.Errorf("risk = %q held by %q, want %q and %q under the floors asked for now", got.Risk, got.Held, tt.wantRisk, tt.wantHeld)
				}
			}
			if score != tt.wantScore || skippable != tt.wantSkippable {
				t.Fatalf("carriedConfidence = score %d, skippable %v; want %d, %v", score, skippable, tt.wantScore, tt.wantSkippable)
			}
		})
	}
}
