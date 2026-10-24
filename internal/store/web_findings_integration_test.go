//go:build integration

package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/review"
)

// TestListAccountFindings checks that a finding is listed once per pull
// request and fingerprint, as its latest completed review reported it, is
// addressed once a later completed review at another head drops it, and
// is dismissed, not addressed, once a maintainer dismisses it.
func TestListAccountFindings(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("findings"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "findings", "findings/one")
	account := accountID(t, s, "findings")
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	var first, second, earliest string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repoID string
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'findings/one'`, account).Scan(&repoID); err != nil {
			return err
		}
		pull := func(number int, title string) string {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, title, head_sha, url)
				VALUES ($1, $2, $3, $4, 'h', 'https://git.example/pr') RETURNING id`, account, repoID, number, title).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		reviewAt := func(pr, head, status string, at time.Time, findings ...[3]string) string {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at)
				VALUES ($1, $2, $3, $4, $5) RETURNING id`, account, pr, head, status, at).Scan(&id); err != nil {
				t.Fatal(err)
			}
			for _, f := range findings {
				if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation, fingerprint)
					VALUES ($1, $2, 'a.go', 3, $3, $4, 'why', $5)`, account, id, f[0], f[1], f[2]); err != nil {
					t.Fatal(err)
				}
			}
			return id
		}
		first, second = pull(7, "Add widgets"), pull(8, "More widgets")
		earliest = reviewAt(first, "h1", "completed", t0, [3]string{"p0", "nil deref", "fp-a"}, [3]string{"p2", "naming", "fp-b"})
		// The same head again: a re-run that leaves naming out did not address it.
		reviewAt(first, "h1", "completed", t0.Add(time.Minute), [3]string{"p0", "nil deref", "fp-a"})
		reviewAt(first, "h2", "completed", t0.Add(2*time.Minute), [3]string{"p1", "Nil deref", "fp-a"})
		reviewAt(first, "h3", "running", t0.Add(3*time.Minute))
		reviewAt(second, "h1", "completed", t0.Add(4*time.Minute), [3]string{"p2", "nil deref", "fp-a"})
		// Pull 7's latest report of fp-a cites a rule its earlier ones did not,
		// and the security agent raised it.
		_, err := tx.Exec(ctx, `UPDATE findings SET rules = CASE title WHEN 'Nil deref' THEN '{wrap-errors}'::text[]
			ELSE '{wrap-errors,no-tokens}' END, agents = CASE title WHEN 'Nil deref' THEN '{security}'::text[] ELSE '{}' END
			WHERE title IN ('Nil deref', 'naming')`)
		if err != nil {
			return err
		}
		// Naming drew a 👎, and pull 8's report of fp-a is deeper in the tree.
		_, err = tx.Exec(ctx, `UPDATE findings SET category = CASE title WHEN 'naming' THEN 'maintainability' ELSE 'correctness' END,
			reactions_down = CASE title WHEN 'naming' THEN 1 ELSE 0 END,
			path = CASE review_id WHEN (SELECT id FROM reviews WHERE pull_request_id = $1) THEN 'internal/store/x.go' ELSE path END`, second)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	list := func(f FindingFilter, p Page) ([]AccountFinding, *Cursor) {
		t.Helper()
		var out []AccountFinding
		var next *Cursor
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			out, next, err = ListAccountFindings(ctx, tx, f, p)
			return err
		}); err != nil {
			t.Fatalf("ListAccountFindings(%+v): %v", f, err)
		}
		return out, next
	}
	type row struct {
		number   int
		title    string
		severity review.Severity
		status   FindingStatus
	}
	rows := func(items []AccountFinding) []row {
		out := make([]row, len(items))
		for i, a := range items {
			out[i] = row{a.PullRequest.Number, a.Title, a.Severity, a.Status}
		}
		return out
	}
	check := func(name string, got []AccountFinding, want ...row) {
		t.Helper()
		g := rows(got)
		if len(g) != len(want) {
			t.Fatalf("%s: got %+v, want %+v", name, g, want)
		}
		for i := range want {
			if g[i] != want[i] {
				t.Fatalf("%s: got %+v, want %+v", name, g, want)
			}
		}
	}

	all, next := list(FindingFilter{}, Page{Limit: 10})
	check("all", all,
		row{8, "nil deref", review.SeverityP2, FindingOpen},
		row{7, "Nil deref", review.SeverityP1, FindingOpen},
		row{7, "naming", review.SeverityP2, FindingAddressed},
	)
	if next != nil {
		t.Fatalf("next = %+v, want the end of the list", next)
	}
	if a := all[1]; !a.FirstSeenAt.Equal(t0) || !a.LastSeenAt.Equal(t0.Add(2*time.Minute)) || a.PullRequest.Title != "Add widgets" {
		t.Fatalf("pull 7's finding = %+v, want first seen at t0 and last two minutes later", a)
	}

	// The earliest review's own findings say what became of each since.
	var own []FindingRow
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		own, err = ListFindings(ctx, tx, earliest)
		return err
	}); err != nil {
		t.Fatalf("ListFindings: %v", err)
	}
	if len(own) != 2 || own[0].Title != "nil deref" || own[0].Status != FindingOpen || own[1].Title != "naming" ||
		own[1].Status != FindingAddressed {
		t.Fatalf("the earliest review's findings = %+v, want nil deref open and naming addressed", own)
	}

	addressed, _ := list(FindingFilter{Status: FindingAddressed}, Page{Limit: 10})
	check("addressed", addressed, row{7, "naming", review.SeverityP2, FindingAddressed})
	important, _ := list(FindingFilter{Severity: review.SeverityP1}, Page{Limit: 10})
	check("p1", important, row{7, "Nil deref", review.SeverityP1, FindingOpen})
	byPull, _ := list(FindingFilter{Query: "More"}, Page{Limit: 10})
	check("pull title", byPull, row{8, "nil deref", review.SeverityP2, FindingOpen})
	byNumber, _ := list(FindingFilter{Query: "#7"}, Page{Limit: 10})
	check("number", byNumber, row{7, "Nil deref", review.SeverityP1, FindingOpen}, row{7, "naming", review.SeverityP2, FindingAddressed})

	wraps, _ := list(FindingFilter{Rule: "wrap-errors"}, Page{Limit: 10})
	check("rule", wraps, row{7, "Nil deref", review.SeverityP1, FindingOpen}, row{7, "naming", review.SeverityP2, FindingAddressed})
	if got := wraps[1].Rules; len(got) != 2 || got[0] != "wrap-errors" || got[1] != "no-tokens" {
		t.Fatalf("naming's rules = %q", got)
	}
	var cited []RuleCitation
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var err error
		cited, err = RuleCitations(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []RuleCitation{
		{"findings/one", "no-tokens", FindingCounts{Findings: 1, Addressed: 1, Downvoted: 1}},
		{"findings/one", "wrap-errors", FindingCounts{Findings: 2, Addressed: 1, Downvoted: 1}},
	}
	if !slices.Equal(cited, want) {
		t.Fatalf("RuleCitations = %+v, want %+v", cited, want)
	}
	checkNarrowed(t, s, account)
	checkSummary(t, s, account)

	page1, next := list(FindingFilter{}, Page{Limit: 2})
	if next == nil {
		t.Fatal("a page of two of three has no next cursor")
	}
	page2, _ := list(FindingFilter{}, Page{Limit: 2, After: *next})
	check("paged", append(page1, page2...), rows(all)[0], rows(all)[1], rows(all)[2])
}

// checkNarrowed checks that TestListAccountFindings's findings narrow to
// the one whose comment drew a 👎, to the one under a directory or at a
// path, and to the one an agent raised.
func checkNarrowed(t *testing.T, s *Store, account string) {
	for _, tc := range []struct {
		name string
		f    FindingFilter
		want string
	}{
		{"downvoted", FindingFilter{Downvoted: true}, "7 naming"},
		{"directory", FindingFilter{Path: "internal/"}, "8 nil deref"},
		{"file", FindingFilter{Path: "internal/store/x.go"}, "8 nil deref"},
		{"agent", FindingFilter{Reviewer: "security"}, "7 Nil deref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			if err := s.WithAccount(t.Context(), account, func(tx pgx.Tx) error {
				items, _, err := ListAccountFindings(t.Context(), tx, tc.f, Page{Limit: 10})
				for _, a := range items {
					got = append(got, fmt.Sprintf("%d %s", a.PullRequest.Number, a.Title))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []string{tc.want}) {
				t.Errorf("ListAccountFindings(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

// checkSummary checks TestListAccountFindings's findings summarized: each
// counted once per pull request, a finding citing two rules in each of
// their groups, the default pass naming a finding no agent raised, and a
// file at the root in its repository's "" directory.
func checkSummary(t *testing.T, s *Store, account string) {
	t.Helper()
	ctx := t.Context()
	summarize := func(f FindingFilter) FindingSummary {
		t.Helper()
		var out FindingSummary
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			var err error
			out, err = SummarizeFindings(ctx, tx, f)
			return err
		}); err != nil {
			t.Fatalf("SummarizeFindings(%+v): %v", f, err)
		}
		return out
	}
	group := func(repo, key string, findings, addressed, downvoted int) FindingGroup {
		return FindingGroup{Repository: repo, Key: key, Findings: findings, Addressed: addressed, Downvoted: downvoted}
	}
	got := summarize(FindingFilter{})
	if want := (FindingCounts{Findings: 3, Addressed: 1, Downvoted: 1}); got.FindingCounts != want {
		t.Errorf("total = %+v, want %+v", got.FindingCounts, want)
	}
	for _, tc := range []struct {
		kind      string
		got, want []FindingGroup
	}{
		{"categories", got.Categories, []FindingGroup{group("", "correctness", 2, 0, 0), group("", "maintainability", 1, 1, 1)}},
		{"rules", got.Rules, []FindingGroup{group("", "wrap-errors", 2, 1, 1), group("", "no-tokens", 1, 1, 1)}},
		{"agents", got.Agents, []FindingGroup{group("", review.DefaultReviewer, 2, 1, 1), group("", "security", 1, 0, 0)}},
		{"directories", got.Directories, []FindingGroup{group("findings/one", "", 2, 1, 1), group("findings/one", "internal/", 1, 0, 0)}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s = %+v, want %+v", tc.kind, tc.got, tc.want)
		}
	}
	// The summary keeps to the list's filters: equal counts tie by key.
	cites := summarize(FindingFilter{Rule: "no-tokens"})
	if want := []FindingGroup{group("", "no-tokens", 1, 1, 1), group("", "wrap-errors", 1, 1, 1)}; cites.Findings != 1 || !slices.Equal(cites.Rules, want) {
		t.Errorf("summary of no-tokens = %+v, want one finding and rules %+v", cites, want)
	}
	// Under a path, directories are the ones a level below it.
	for path, want := range map[string]string{"internal/": "internal/store/", "internal": "internal/", "internal/store/x.go": "internal/store/x.go"} {
		if under := summarize(FindingFilter{Path: path}); !slices.Equal(under.Directories, []FindingGroup{group("findings/one", want, 1, 0, 0)}) {
			t.Errorf("directories under %q = %+v, want %q", path, under.Directories, want)
		}
	}
	if none := summarize(FindingFilter{Query: "nothing like it"}); none.Findings != 0 || none.Categories != nil || none.Directories != nil {
		t.Errorf("summary of no finding = %+v", none)
	}
}

// TestDismissedFindings checks that a finding a maintainer dismissed is
// listed dismissed with its reason, never addressed, whatever the reviews
// after it report.
func TestDismissedFindings(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("dismissed"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "dismissed", "dismissed/one")
	account := accountID(t, s, "dismissed")
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var pull, earliest string
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var repoID string
		if err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE account_id = $1 AND name = 'dismissed/one'`, account).Scan(&repoID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, title, head_sha, url)
			VALUES ($1, $2, 7, 'Add widgets', 'h', 'https://git.example/pr') RETURNING id`, account, repoID).Scan(&pull); err != nil {
			return err
		}
		for i, head := range []string{"h1", "h2"} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO reviews (account_id, pull_request_id, head_sha, status, created_at)
				VALUES ($1, $2, $3, 'completed', $4) RETURNING id`, account, pull, head, t0.Add(time.Duration(i)*time.Minute)).Scan(&id); err != nil {
				return err
			}
			if i == 0 {
				earliest = id
			}
			// The second review drops naming, which would address it.
			findings := [][2]string{{"nil deref", "fp-a"}, {"naming", "fp-b"}}[:2-i]
			for _, f := range findings {
				if _, err := tx.Exec(ctx, `INSERT INTO findings (account_id, review_id, path, line, severity, title, explanation, fingerprint)
					VALUES ($1, $2, 'a.go', 3, 'p2', $3, 'why', $4)`, account, id, f[0], f[1]); err != nil {
					return err
				}
			}
		}
		finding, found, err := LatestFinding(ctx, tx, pull, "fp-b")
		if err != nil || !found || finding.Title != "naming" || finding.Severity != review.SeverityP2 {
			t.Fatalf("LatestFinding = %+v, %v, %v", finding, found, err)
		}
		if _, found, err := LatestFinding(ctx, tx, pull, "fp-none"); err != nil || found {
			t.Fatalf("LatestFinding of an unknown fingerprint = %v, %v", found, err)
		}
		return RecordDismissal(ctx, tx, Dismissal{AccountID: account, PullRequestID: pull, Fingerprint: "fp-b", Finding: finding,
			Reason: "house style", Author: "devin", CommentID: 99, Source: DismissedByCommand})
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	statuses := map[FindingStatus][]string{}
	var reason string
	var source DismissalSource
	var ds []Dismissal
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		for _, status := range []FindingStatus{FindingOpen, FindingAddressed, FindingDismissed} {
			items, _, err := ListAccountFindings(ctx, tx, FindingFilter{Status: status}, Page{Limit: 10})
			if err != nil {
				return err
			}
			for _, a := range items {
				statuses[status] = append(statuses[status], a.Title)
				reason += a.DismissReason
				source += a.DismissSource
			}
		}
		if err := checkDismissedOwn(ctx, t, tx, earliest); err != nil {
			return err
		}
		var err error
		ds, err = Dismissals(ctx, tx, pull)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(statuses[FindingOpen], []string{"nil deref"}) || len(statuses[FindingAddressed]) != 0 ||
		!slices.Equal(statuses[FindingDismissed], []string{"naming"}) || reason != "house style" || source != DismissedByCommand {
		t.Fatalf("statuses = %v with reason %q by %q; want naming dismissed by command, not addressed", statuses, reason, source)
	}
	checkDismissedBy(t, s, account, pull)
	if len(ds) != 1 || ds[0].Fingerprint != "fp-b" || ds[0].Finding.Title != "naming" || ds[0].CommentID != 99 {
		t.Fatalf("Dismissals = %+v", ds)
	}
}

// checkDismissedOwn checks that a review's own findings say naming was
// dismissed, with its reason, and the other is still open.
func checkDismissedOwn(ctx context.Context, t *testing.T, tx pgx.Tx, reviewID string) error {
	t.Helper()
	own, err := ListFindings(ctx, tx, reviewID)
	if err != nil {
		return err
	}
	if len(own) != 2 {
		t.Errorf("the review has %d findings, want 2", len(own))
	}
	for _, f := range own {
		want, why := FindingOpen, ""
		if f.Title == "naming" {
			want, why = FindingDismissed, "house style"
		}
		var by DismissalSource
		if f.Title == "naming" {
			by = DismissedByCommand
		}
		if f.Status != want || f.DismissReason != why || f.DismissSource != by {
			t.Errorf("the review's %s is %s (%q by %q), want %s (%q by %q)", f.Title, f.Status, f.DismissReason, f.DismissSource, want, why, by)
		}
	}
	return nil
}

// checkDismissedBy checks that TestDismissedFindings's dismissal is found
// by its source alone, counted in the summary, and found by another
// source once the finding is dismissed again by it.
func checkDismissedBy(t *testing.T, s *Store, account, pull string) {
	t.Helper()
	ctx := t.Context()
	by := func(source DismissalSource) []string {
		t.Helper()
		var titles []string
		if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
			items, _, err := ListAccountFindings(ctx, tx, FindingFilter{DismissedBy: source}, Page{Limit: 10})
			for _, a := range items {
				titles = append(titles, a.Title)
			}
			return err
		}); err != nil {
			t.Fatalf("ListAccountFindings(dismissed by %s): %v", source, err)
		}
		return titles
	}
	if got, none := by(DismissedByCommand), by(DismissedByConcession); !slices.Equal(got, []string{"naming"}) || none != nil {
		t.Errorf("dismissed by command = %q and by concession = %q, want naming and none", got, none)
	}
	var sum FindingSummary
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		finding, _, err := LatestFinding(ctx, tx, pull, "fp-b")
		if err != nil {
			return err
		}
		if sum, err = SummarizeFindings(ctx, tx, FindingFilter{}); err != nil {
			return err
		}
		return RecordDismissal(ctx, tx, Dismissal{AccountID: account, PullRequestID: pull, Fingerprint: "fp-b", Finding: finding,
			Reason: "kritika conceded: fair", Author: "kritika", CommentID: 100, Source: DismissedByConcession})
	}); err != nil {
		t.Fatal(err)
	}
	if want := (FindingCounts{Findings: 2, Dismissed: 1}); sum.FindingCounts != want {
		t.Errorf("summary = %+v, want %+v", sum.FindingCounts, want)
	}
	if got, none := by(DismissedByConcession), by(DismissedByCommand); !slices.Equal(got, []string{"naming"}) || none != nil {
		t.Errorf("dismissed again by concession = %q and by command = %q, want naming and none", got, none)
	}
}

// TestDeleteDismissal: a dismissal taken back is gone, and taking back one
// that is not there says so.
func TestDeleteDismissal(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	if err := s.ApplyConfig(ctx, parse(t, soloAccount("undismissed"))); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	listRepos(t, s, "undismissed", "undismissed/one")
	account := accountID(t, s, "undismissed")
	var ds []Dismissal
	if err := s.WithAccount(ctx, account, func(tx pgx.Tx) error {
		var pull string
		if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
			SELECT $1, id, 7, 'h' FROM repositories WHERE account_id = $1 AND name = 'undismissed/one' RETURNING id`, account).Scan(&pull); err != nil {
			return err
		}
		d := Dismissal{AccountID: account, PullRequestID: pull, Fingerprint: "fp-a", Reason: "intended", Author: "devin", CommentID: 99,
			Source: DismissedByCommand}
		if err := RecordDismissal(ctx, tx, d); err != nil {
			return err
		}
		if deleted, err := DeleteDismissal(ctx, tx, pull, "fp-a"); err != nil || !deleted {
			t.Fatalf("DeleteDismissal = %v, %v", deleted, err)
		}
		if deleted, err := DeleteDismissal(ctx, tx, pull, "fp-a"); err != nil || deleted {
			t.Fatalf("DeleteDismissal again = %v, %v", deleted, err)
		}
		var err error
		ds, err = Dismissals(ctx, tx, pull)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(ds) != 0 {
		t.Fatalf("Dismissals after the delete = %+v", ds)
	}
}

// soloAccount serves one account of its own, with one repository, so a
// test's rows are the only ones its account reads in the shared database.
func soloAccount(name string) string {
	return fmt.Sprintf(`
apps:
  %[1]s-bot:
    accounts: [%[1]s]
    clientId: Iv1.test
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
`, name)
}
