//go:build integration

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestRunnerTokenAuthority is what a report token opens: its own run and
// nothing else. A run's and an account's ID are public metadata that a token
// names, so naming them must open nothing.
func TestRunnerTokenAuthority(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	id, token := runnerWriteFixture(t, s, alpha, RunnerKindIndex)
	foreign, foreignToken := runnerWriteFixture(t, s, beta, RunnerKindIndex)
	secret := token[strings.LastIndex(token, ".")+1:]
	for _, tt := range []struct {
		name, token string
		want        string
	}{
		{"own", token, id},
		{"another account's own", foreignToken, foreign},
		{"no token", "", ""},
		{"a gateway token", gatewayTokenPrefix + secret, ""},
		{"a bare secret", secret, ""},
		{"another run's id with this secret", runnerTokenPrefix + alpha + "." + foreign + "." + secret, ""},
		{"another account with this secret", runnerTokenPrefix + beta + "." + id + "." + secret, ""},
		{"a random secret", runnerTokenPrefix + alpha + "." + id + "." + RandomToken(), ""},
		{"an empty secret", runnerTokenPrefix + alpha + "." + id + ".", ""},
		{"an id that is not a uuid", runnerTokenPrefix + "'; DROP TABLE runner_runs; --." + id + "." + secret, ""},
		{"an id in another spelling", runnerTokenPrefix + strings.ToUpper(alpha) + "." + id + "." + secret, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			err := s.WithRunner(ctx, tt.token, func(_ pgx.Tx, g RunnerGrant) error {
				got = g.RunID
				return nil
			})
			if tt.want == "" {
				if !errors.Is(err, ErrRunnerToken) {
					t.Fatalf("WithRunner = %v, want ErrRunnerToken", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("WithRunner = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	t.Run("a token stops working when revoked", func(t *testing.T) {
		revoked, token := runnerWriteFixture(t, s, alpha, RunnerKindIndex)
		if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runner_runs SET access_token_hash = NULL WHERE id = $1`, revoked)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.WithRunner(ctx, token, func(pgx.Tx, RunnerGrant) error { return nil }); !errors.Is(err, ErrRunnerToken) {
			t.Fatalf("WithRunner = %v, want ErrRunnerToken", err)
		}
	})
	t.Run("a token stops working when its run ends", func(t *testing.T) {
		ended, token := runnerWriteFixture(t, s, alpha, RunnerKindIndex)
		if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error { return FailRunnerRun(ctx, tx, ended, "over") }); err != nil {
			t.Fatal(err)
		}
		if err := s.WithRunner(ctx, token, func(pgx.Tx, RunnerGrant) error { return nil }); !errors.Is(err, ErrRunnerToken) {
			t.Fatalf("WithRunner = %v, want ErrRunnerToken", err)
		}
	})
	t.Run("a revocation waits for a write in flight and refuses the next", func(t *testing.T) {
		held, token := runnerWriteFixture(t, s, alpha, RunnerKindIndex)
		revoked := make(chan error, 1)
		waited := true
		err := s.WithRunner(ctx, token, func(pgx.Tx, RunnerGrant) error {
			go func() {
				revoked <- s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE runner_runs SET access_token_hash = NULL WHERE id = $1`, held)
					return err
				})
			}()
			select {
			case err := <-revoked:
				waited = false
				t.Errorf("the revocation did not wait for the write: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if waited {
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
		}
		if err := s.WithRunner(ctx, token, func(pgx.Tx, RunnerGrant) error { return nil }); !errors.Is(err, ErrRunnerToken) {
			t.Fatalf("WithRunner after the revocation = %v, want ErrRunnerToken", err)
		}
	})
}

func TestRunnerNotificationsConveyNoAuthority(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatal(err)
	}
	listener, err := s.app.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, `LISTEN kritika_events`); err != nil {
		t.Fatal(err)
	}
	account := accountID(t, s, "alpha")
	id, token := runnerWriteFixture(t, s, account, RunnerKindIndex)
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		n, err := listener.Conn().WaitForNotification(wait)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(n.Payload, token) {
			t.Fatal("notification leaked a report token")
		}
		event, err := parseEvent(n.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == EventRunnerRun && event.ID == id && event.AccountID == account {
			return
		}
	}
}
