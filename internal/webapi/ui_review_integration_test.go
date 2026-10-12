//go:build integration

package webapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func testUnknownRepositorySearch(t *testing.T, e *apiEnv) {
	for _, endpoint := range []string{"pulls", "findings"} {
		for _, repo := range []string{"wa/on", "wa/typo", "wb/one"} {
			t.Run(endpoint+"/"+repo, func(t *testing.T) {
				status, body := e.getBody("member-a", "/api/v1/accounts/github/wa/"+endpoint+"?repo="+url.QueryEscape(repo))
				var page struct {
					Items      []json.RawMessage
					NextCursor *string
				}
				if status != http.StatusOK {
					t.Fatalf("status = %d: %s", status, body)
				}
				if err := json.Unmarshal(body, &page); err != nil {
					t.Fatal(err)
				}
				if page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
					t.Fatalf("unknown search = %s, want an empty page", body)
				}
			})
		}
	}
	if status, _ := e.getBody("member-a", "/api/v1/accounts/github/wa/repos/wa/typo"); status != http.StatusNotFound {
		t.Fatalf("missing repository detail = %d, want 404", status)
	}
}

func testDiffPreparation(t *testing.T, e *apiEnv) {
	for _, tt := range []struct {
		name, status              string
		run, pack, swept, pending bool
	}{
		{name: "before runner", status: "running", pending: true},
		{name: "before pack", status: "running", run: true, pending: true},
		{name: "prepared without pack", status: "prepared", run: true, pending: true},
		{name: "recorded empty diff", status: "running", run: true, pack: true},
		{name: "swept diff", status: "completed", run: true, pack: true, swept: true},
		{name: "failed without pack", status: "failed", run: true},
		{name: "skipped without runner", status: "skipped"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := e.scalar(`INSERT INTO reviews (account_id, pull_request_id, head_sha, status, trigger, model)
				VALUES ($1, $2, 'pending-diff', $3, 'push', 'acme/large') RETURNING id::text`, e.a.accountID, e.a.prID, tt.status)
			if tt.run {
				run := e.scalar(`INSERT INTO runner_runs (account_id, review_id, kind, phase) VALUES ($1, $2, 'review', 'done') RETURNING id::text`, e.a.accountID, id)
				if tt.pack {
					e.exec(`INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, swept_at)
						VALUES ($1, $2, 'head', 'base', 'patch', '', CASE WHEN $3 THEN now() END)`, run, e.a.accountID, tt.swept)
				}
			}
			status, body := e.getBody("member-a", "/api/v1/accounts/github/wa/reviews/"+id+"/diff")
			var diff ReviewDiff
			if status != http.StatusOK {
				t.Fatalf("status = %d: %s", status, body)
			}
			if err := json.Unmarshal(body, &diff); err != nil {
				t.Fatal(err)
			}
			if diff.Pending != tt.pending || diff.Swept != tt.swept || diff.Diff != "" {
				t.Fatalf("diff = %+v, pending = %v, swept = %v", diff, tt.pending, tt.swept)
			}
		})
	}
}
