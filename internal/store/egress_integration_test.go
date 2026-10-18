//go:build integration

package store

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestEgressRefusals(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, twoAccounts)); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	alpha, beta := accountID(t, s, "alpha"), accountID(t, s, "beta")
	var repo, run, other string
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 ORDER BY name LIMIT 1`, alpha).Scan(&repo); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, alpha).Scan(&run); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO runner_runs (account_id, kind) VALUES ($1, 'review') RETURNING id`, alpha).Scan(&other)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM egress_refusals WHERE runner_run_id IN ($1, $2)`, run, other)
		_, _ = s.owner.Exec(context.Background(), `DELETE FROM runner_runs WHERE id IN ($1, $2)`, run, other)
	})
	limit := refusalsPerRun
	refusalsPerRun = 3
	t.Cleanup(func() { refusalsPerRun = limit })

	record := func(runID, repoID, kind, host, reason string) bool {
		t.Helper()
		var ok bool
		if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
			var err error
			ok, err = RecordEgressRefusal(ctx, tx, EgressRefusal{RunID: runID, AccountID: alpha, RepositoryID: repoID, Kind: kind, Host: host, Reason: reason})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// Two refusals of one host count on one row, and one for another
	// reason on a row of its own; a third host is past the run's bound and
	// goes unrecorded, while the first host still counts.
	for i, want := range []struct {
		runID, repoID, kind, host, reason string
		ok                                bool
	}{
		{run, repo, "http", "registry.example.com", "host not allowed", true},
		{run, repo, "http", "registry.example.com", "host not allowed", true},
		{run, repo, "connect", "docs.example.com", "host not allowed", true},
		{run, repo, "connect", "registry.example.com", "only port 443 is served", true},
		{run, repo, "http", "third.example.com", "host not allowed", false},
		{run, repo, "http", "registry.example.com", "host not allowed", true},
		{other, "", "http", "registry.example.com", "host denied", true},
	} {
		if got := record(want.runID, want.repoID, want.kind, want.host, want.reason); got != want.ok {
			t.Fatalf("refusal %d (%s): recorded = %v, want %v", i, want.host, got, want.ok)
		}
	}

	read := func(account string, from, to time.Time) []EgressRefusalRow {
		t.Helper()
		var rows []EgressRefusalRow
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			rows, err = ReadEgressRefusals(ctx, tx, from, to, 10)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	// A host refused for two reasons has one row, with the run counted once.
	now := time.Now()
	rows := read(alpha, now.Add(-time.Hour), now.Add(time.Hour))
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s %s %d/%d %v", r.Repository, r.Host, r.Count, r.Runs, r.Reasons))
		if r.LastSeenAt.Before(now.Add(-time.Minute)) {
			t.Fatalf("last seen %s, want about now", r.LastSeenAt)
		}
	}
	want := []string{"alpha/one registry.example.com 4/1 map[host not allowed:3 only port 443 is served:1]",
		"alpha/one docs.example.com 1/1 map[host not allowed:1]", " registry.example.com 1/1 map[host denied:1]"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	// The limit is on hosts, counted over every repository: with one host
	// allowed, registry.example.com's two rows come and docs.example.com's
	// does not.
	if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
		rows, err := ReadEgressRefusals(ctx, tx, now.Add(-time.Hour), now.Add(time.Hour), 1)
		if err != nil {
			return err
		}
		if len(rows) != 2 || rows[0].Host != "registry.example.com" || rows[1].Host != "registry.example.com" {
			t.Fatalf("one host: rows = %+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rows := read(alpha, now.Add(-2*time.Hour), now.Add(-time.Hour)); len(rows) != 0 {
		t.Fatalf("rows outside the window = %+v", rows)
	}
	if rows := read(beta, now.Add(-time.Hour), now.Add(time.Hour)); len(rows) != 0 {
		t.Fatalf("another account reads %+v", rows)
	}

	// Refusals at once cannot both pass the bound: the run has three rows,
	// so of ten new hosts at once none is recorded.
	var wg sync.WaitGroup
	recorded := make([]bool, 10)
	for i := range recorded {
		wg.Go(func() {
			if err := s.WithAccount(ctx, alpha, func(tx pgx.Tx) error {
				var err error
				recorded[i], err = RecordEgressRefusal(ctx, tx, EgressRefusal{
					RunID: run, AccountID: alpha, RepositoryID: repo, Kind: "http", Host: fmt.Sprintf("host-%d.example.com", i), Reason: "host not allowed",
				})
				return err
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var rowsOfRun int
	if err := s.owner.QueryRow(ctx, `SELECT count(*) FROM egress_refusals WHERE runner_run_id = $1`, run).Scan(&rowsOfRun); err != nil {
		t.Fatal(err)
	}
	if rowsOfRun != 3 || slices.Contains(recorded, true) {
		t.Fatalf("rows of the run = %d, recorded = %v; want the three and none", rowsOfRun, recorded)
	}

	// The sweep deletes by when a host was last refused.
	if _, err := s.owner.Exec(ctx, `UPDATE egress_refusals SET last_seen_at = now() - interval '2 days' WHERE runner_run_id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepEgressRefusals(ctx, 0); err == nil {
		t.Fatal("a zero retention was accepted")
	}
	if n, err := s.SweepEgressRefusals(ctx, 24*time.Hour); err != nil || n < 1 {
		t.Fatalf("swept %d, %v; want the other run's refusal", n, err)
	}
	if rows := read(alpha, now.Add(-time.Hour), now.Add(time.Hour)); len(rows) != 2 {
		t.Fatalf("rows after the sweep = %+v, want the run's two", rows)
	}
}
