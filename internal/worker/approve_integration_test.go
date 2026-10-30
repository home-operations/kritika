//go:build integration

package worker

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// TestJudgeApproval: a pull request held closed is out of approval's reach
// whether or not review.approve has conditions; an open one with none is in
// scope.
func TestJudgeApproval(t *testing.T) {
	ctx := t.Context()
	appStore := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	file := configfiletest.Load(t, configYAML)
	if err := appStore.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, appStore, file, "onedr0p/home-ops")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	const number = 8
	var repoID, prID string
	if err := appStore.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		var err error
		repoID, _, err = store.EnsureRepository(ctx, tx, account.ID(), store.ReachedRepository{FullName: "onedr0p/home-ops", DefaultBranch: "main"})
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha, state)
			VALUES ($1, $2, $3, 'h8', 'open') RETURNING id`, account.ID(), repoID, number).Scan(&prID)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	logger := slog.New(slog.DiscardHandler)
	unconditional, conditional := configfile.Review{}, configfile.Review{ApproveWhen: []configfile.When{{Expr: "pr.number == 8"}}}
	judge := func(r configfile.Review) approvalScope {
		return judgeApproval(ctx, appStore, account.ID(), prID, "synchronize", r, logger)
	}
	if got := judge(unconditional); got != approvalInScope {
		t.Fatalf("open, no conditions: %v, want in scope", got)
	}
	if got := judge(conditional); got != approvalInScope {
		t.Fatalf("open, conditions hold: %v, want in scope", got)
	}
	if err := appStore.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		return store.ClosePullRequest(ctx, tx, repoID, number, true, nil, time.Time{})
	}); err != nil {
		t.Fatal(err)
	}
	if got := judge(unconditional); got != approvalClosed {
		t.Fatalf("merged, no conditions: %v, want closed", got)
	}
	if got := judge(conditional); got != approvalClosed {
		t.Fatalf("merged, conditions hold: %v, want closed", got)
	}
}
