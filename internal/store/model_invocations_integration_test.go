//go:build integration

package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestModelInvocationRecovery(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listRepos(t, s, "alpha", "alpha/one", "alpha/two")
	account := accountID(t, s, "alpha")
	other := accountID(t, s, "beta")
	for _, tt := range []struct {
		name                                 string
		uncertain, failed, revoked, rollback bool
	}{
		{name: "concurrent settlement"},
		{name: "failed call refunds", failed: true},
		{name: "uncertain call retains reservation and accepts late outcome", uncertain: true},
		{name: "revoked token retains usage", revoked: true},
		{name: "failed settlement rolls back and reconciles", rollback: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, token := invocationFixture(t, s, account)
			expires := time.Now().Add(time.Hour)
			if tt.uncertain {
				expires = time.Now().Add(-time.Hour)
			}
			id, err := s.BeginModelInvocation(ctx, u, token, 50, expires)
			if err != nil {
				t.Fatal(err)
			}
			assertState := func(want string, spent int64, rows int) {
				t.Helper()
				assertInvocationState(t, s, id, token, want, spent, rows)
			}
			assertInvocationAdmission(t, s, u, token, id, other, expires)
			assertState("reserved", 50, 0)
			if tt.uncertain {
				result, err := s.ReconcileModelInvocations(ctx, 100)
				if err != nil || result.Uncertain != 1 {
					t.Fatalf("uncertain recovery=%+v: %v", result, err)
				}
				assertState("uncertain", 50, 0)
				result, err = s.ReconcileModelInvocations(ctx, 100)
				if err != nil || result.Uncertain != 0 {
					t.Fatalf("repeat recovery=%+v: %v", result, err)
				}
			}
			if tt.failed {
				u.Input, u.Output = 0, 0
			}
			if err := s.CompleteModelInvocation(ctx, id, u, !tt.failed); err != nil {
				t.Fatal(err)
			}
			changed := u
			changed.Input = 900
			if err := s.CompleteModelInvocation(ctx, id, changed, true); err != nil {
				t.Fatal(err)
			}
			if tt.revoked {
				if err := s.RevokeGatewayTokens(ctx, u.RunnerRunID); err != nil {
					t.Fatal(err)
				}
				token = ""
			}
			if tt.rollback {
				if _, err := s.owner.Exec(ctx, `ALTER TABLE usage ADD CONSTRAINT invocation_test_failure CHECK (id <> '`+id+`'::uuid) NOT VALID`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := s.owner.Exec(context.Background(), `ALTER TABLE usage DROP CONSTRAINT IF EXISTS invocation_test_failure`); err != nil {
						t.Error(err)
					}
				})
				if settled, err := s.SettleModelInvocation(ctx, account, id); err == nil || settled {
					t.Fatalf("failed settlement=%v: %v", settled, err)
				}
				assertState("completed", 50, 0)
				if _, err := s.owner.Exec(ctx, `ALTER TABLE usage DROP CONSTRAINT invocation_test_failure`); err != nil {
					t.Fatal(err)
				}
				result, err := s.ReconcileModelInvocations(ctx, 100)
				if err != nil || result.Settled != 1 {
					t.Fatalf("settlement recovery=%+v: %v", result, err)
				}
			} else {
				var wg sync.WaitGroup
				for range 4 {
					wg.Go(func() {
						if _, err := s.SettleModelInvocation(ctx, account, id); err != nil {
							t.Error(err)
						}
					})
				}
				wg.Wait()
			}
			wantRows, wantSpent := 1, int64(15)
			if tt.failed {
				wantRows, wantSpent = 0, 0
			}
			assertState("settled", wantSpent, wantRows)
			if wantRows != 0 {
				var tokens int64
				var sameTime bool
				if err := s.owner.QueryRow(ctx, `SELECT u.input_tokens + u.output_tokens, u.created_at = i.created_at FROM usage u JOIN model_invocations i USING (id) WHERE u.id = $1`, id).Scan(&tokens, &sameTime); err != nil {
					t.Fatal(err)
				}
				if tokens != 15 || !sameTime {
					t.Fatalf("tokens=%d same creation time=%v", tokens, sameTime)
				}
			}
		})
	}
}

func invocationFixture(t *testing.T, s *Store, account string) (Usage, string) {
	t.Helper()
	ctx := t.Context()
	u := Usage{AccountID: account, Role: RoleReview, Model: "test", Input: 10, Output: 5}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 LIMIT 1`, account).Scan(&u.RepositoryID); err != nil {
			return err
		}
		var err error
		u.RunnerRunID, err = InsertRunnerRun(ctx, tx, account, RunnerKindReview, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.RevokeGatewayTokens(context.Background(), u.RunnerRunID); err != nil {
			t.Error(err)
		}
		if err := s.WithAccount(context.Background(), account, func(tx pgx.Tx) error {
			return FailRunnerRun(context.Background(), tx, u.RunnerRunID, "test finished")
		}); err != nil {
			t.Error(err)
		}
	})
	token, err := s.MintGatewayToken(ctx, GatewayGrant{AccountID: account, RepositoryID: u.RepositoryID,
		RunID: u.RunnerRunID, Model: "test", Budget: 50}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	return u, token
}

func assertInvocationState(t *testing.T, s *Store, id, token, want string, spent int64, rows int) {
	ctx := t.Context()
	t.Helper()
	var state string
	var n int
	if err := s.owner.QueryRow(ctx, `SELECT state, (SELECT count(*) FROM usage WHERE id = $1) FROM model_invocations WHERE id = $1`, id).Scan(&state, &n); err != nil {
		t.Fatal(err)
	}
	if state != want || n != rows {
		t.Fatalf("state=%s usage rows=%d, want %s/%d", state, n, want, rows)
	}
	if token != "" {
		grant, err := s.LookupGatewayToken(ctx, token)
		if err != nil || grant.Spent != spent {
			t.Fatalf("spent=%d, want %d: %v", grant.Spent, spent, err)
		}
	}
}

func assertInvocationAdmission(t *testing.T, s *Store, u Usage, token, id, other string, expires time.Time) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.BeginModelInvocation(ctx, u, token, 50, expires); !errors.Is(err, ErrModelBudget) {
		t.Fatalf("second reservation: %v", err)
	}
	var invocations int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM model_invocations WHERE runner_run_id = $1`, u.RunnerRunID).Scan(&invocations); err != nil || invocations != 1 {
		t.Fatalf("invocations=%d: %v", invocations, err)
	}
	hidden := u
	hidden.AccountID = other
	if err := s.CompleteModelInvocation(ctx, id, hidden, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleModelInvocation(ctx, other, id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-account settlement: %v", err)
	}

}
