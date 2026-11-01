//go:build integration

package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestReadDependencyDrift: a dependency that more than one pull request
// moved in the window lists how each update was rated, oldest first, from
// the newest completed whole scored review of each pull request, a pull
// request counting once however many of its lines share the key, and its
// risk counting only where it moved that one version alone; the ones rated
// at the most levels come first, before the most updated. One update, a
// partial or unscored review, a bump with no reach, and a review outside
// the window count for nothing. Reviews and pull requests at one instant
// order by id.
func TestReadDependencyDrift(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("drift"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "drift", "drift/one")
	account := accountID(t, s, "drift")
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 30)
	at := func(d int) time.Time { return from.AddDate(0, 0, d) }
	const sonarr = "k.yaml image: ghcr.io/x/sonarr:*"
	const kps = "kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml tag: *"
	const cilium = "c.yaml tag: *"
	// verdict is a scored review of a change that moved one version alone,
	// and mixed one that did more, whose risk is not the dependency's.
	verdict := func(risk, stepped string, bumps ...string) string {
		return fmt.Sprintf(`{"score":5,"threshold":5,"risk":%q,"stepped":%q,"reason":"r","rubric":"x","one_bump":true,"bumps":[%s]}`, risk, stepped, joinJSON(bumps))
	}
	mixed := func(risk string, bumps ...string) string {
		return fmt.Sprintf(`{"score":5,"threshold":5,"risk":%q,"reason":"r","rubric":"x","bumps":[%s]}`, risk, joinJSON(bumps))
	}
	bump := func(key, step, reach string) string {
		path, _, _ := strings.Cut(key, " ")
		return fmt.Sprintf(`{"key":%q,"path":%q,"line":1,"step":%q,"reach":%q}`, key, path, step, reach)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repoID string
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'drift/one'`, account).Scan(&repoID); err != nil {
			return err
		}
		number := 0
		pull := func(reviews ...func(prID string) error) {
			number++
			var prID string
			if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha) VALUES ($1, $2, $3, 'h') RETURNING id`,
				account, repoID, number).Scan(&prID); err != nil {
				t.Fatal(err)
			}
			for _, r := range reviews {
				if err := r(prID); err != nil {
					t.Fatal(err)
				}
			}
		}
		// review inserts a review on day; ids 1 and 2 are ordered ids for
		// reviews at one instant, where a random id would decide the tie.
		review := func(day int, status string, partial bool, confidence string, ids ...int) func(string) error {
			return func(prID string) error {
				id := "gen_random_uuid()"
				if len(ids) > 0 {
					id = fmt.Sprintf("'00000000-0000-0000-0000-%012d'::uuid", ids[0])
				}
				_, err := tx.Exec(ctx, `INSERT INTO reviews (id, account_id, pull_request_id, head_sha, status, partial, confidence, created_at)
					VALUES (`+id+`, $1, $2, 'h', $3, $4, nullif($5, '')::jsonb, $6)`, account, prID, status, partial, confidence, at(day))
				return err
			}
		}
		pull(review(1, "completed", false, verdict("low", "", bump(sonarr, "patch", "standalone"))))
		pull(review(3, "completed", false, verdict("medium", "", bump(sonarr, "patch", "shared"))))
		// A change that did more than move sonarr's version counts its
		// reach, not its risk.
		pull(review(4, "completed", false, mixed("critical", bump(sonarr, "patch", "shared"))))
		// A newer review of the same pull request replaces the older.
		pull(review(5, "completed", false, verdict("critical", "", bump(sonarr, "major", "central"))),
			review(6, "completed", false, verdict("high", "medium", bump(sonarr, "patch", "shared"))))
		pull(review(8, "completed", false, verdict("medium", "", bump(kps, "minor", "central"))))
		// Two reviews at one instant resolve to the later id, and two lines
		// of that review that share the key count once, by the first.
		pull(review(9, "completed", false, verdict("low", "", bump(kps, "patch", "standalone")), 1),
			review(9, "completed", false, verdict("high", "", bump(kps, "major", "central"), bump(kps, "major", "standalone")), 2))
		// Fewer updates than sonarr, rated at more levels: the most drifted.
		pull(review(14, "completed", false, verdict("low", "", bump(cilium, "patch", "central"))))
		pull(review(15, "completed", false, verdict("medium", "", bump(cilium, "patch", "central"))))
		pull(review(16, "completed", false, verdict("high", "", bump(cilium, "minor", "central"))))
		// Alone, partial, unscored, without a reach, or outside the window.
		pull(review(10, "completed", false, verdict("low", "", bump("alone *", "patch", "standalone"))))
		pull(review(11, "completed", true, verdict("low", "", bump(sonarr, "patch", "standalone"))))
		pull(review(12, "completed", false, ""))
		pull(review(13, "completed", false, verdict("low", "", bump(sonarr, "patch", ""))))
		pull(review(40, "completed", false, verdict("low", "", bump(sonarr, "patch", "standalone"))))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got []DependencyDrift
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		got, err = ReadDependencyDrift(ctx, tx, from, to, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []DependencyDrift{
		{Repository: "drift/one", Key: cilium, Path: "c.yaml", Risks: []string{"low", "medium", "high"}, Reaches: []string{"central", "central", "central"},
			Steps: []string{"patch", "patch", "minor"}, LastAt: at(16)},
		{Repository: "drift/one", Key: sonarr, Path: "k.yaml", Risks: []string{"low", "medium", "", "medium"}, Reaches: []string{"standalone", "shared", "shared", "shared"},
			Steps: []string{"patch", "patch", "patch", "patch"}, LastAt: at(6)},
		{Repository: "drift/one", Key: kps, Path: "kubernetes/apps/o11y/kube-prometheus-stack/app/ocirepository.yaml", Risks: []string{"medium", "high"},
			Reaches: []string{"central", "central"}, Steps: []string{"minor", "major"}, LastAt: at(9)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadDependencyDrift = %+v, want %+v", got, want)
	}
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		rows, err := ReadDependencyDrift(ctx, tx, from, to, 1)
		if err == nil && (len(rows) != 1 || rows[0].Key != cilium) {
			t.Fatalf("ReadDependencyDrift limited = %+v, want the most drifted", rows)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// joinJSON joins JSON values for an array.
func joinJSON(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
