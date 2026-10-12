//go:build integration

package webapi

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"
)

func testInstancePulls(t *testing.T, e *apiEnv) {
	e.signIn("member-both", "both", memberOfAccounts("github/wa", "github/wb"))
	for _, id := range []string{e.a.prID, e.b.prID} {
		var updatedAt time.Time
		if err := e.owner.QueryRow(t.Context(), `SELECT updated_at FROM pull_requests WHERE id = $1`, id).Scan(&updatedAt); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.exec(`UPDATE pull_requests SET updated_at = $1 WHERE id = $2`, updatedAt, id) })
	}
	pullID := e.scalar(`INSERT INTO pull_requests (account_id, repository_id, number, title, author, head_sha)
  VALUES ($1, $2, 8, 'Second pull', 'grace', 'head8') RETURNING id::text`, e.a.accountID, e.a.repoID)
	t.Cleanup(func() { e.exec(`DELETE FROM pull_requests WHERE repository_id = $1 AND number = 8`, e.a.repoID) })
	e.exec(`UPDATE pull_requests SET updated_at = '2026-10-12T12:00:00Z' WHERE id IN ($1, $2, $3)`, e.a.prID, e.b.prID, pullID)
	for _, tc := range []struct {
		who      string
		accounts []string
		count    int
	}{
		{"member-a", []string{"github/wa"}, 2},
		{"member-b", []string{"github/wb"}, 1},
		{"member-both", []string{"github/wa", "github/wb"}, 3},
		{"admin", []string{"github/wa", "github/wb"}, 3},
	} {
		t.Run(tc.who, func(t *testing.T) {
			path := "/api/v1/pulls?limit=1"
			seen := map[string]bool{}
			for range tc.count {
				status, body := e.getBody(tc.who, path)
				var page Page[InstancePull]
				if err := json.Unmarshal(body, &page); err != nil || status != 200 || len(page.Items) != 1 {
					t.Fatalf("page: %d %s (%v)", status, body, err)
				}
				item := page.Items[0]
				if !slices.Contains(tc.accounts, item.Account) {
					t.Fatalf("unreadable account: %s", body)
				}
				key := item.Account + ":" + item.Title
				if seen[key] {
					t.Fatalf("duplicate: %s", key)
				}
				seen[key] = true
				if page.NextCursor == nil {
					if len(seen) != tc.count {
						t.Fatalf("early end: %s", body)
					}
				} else {
					if len(seen) == tc.count {
						t.Fatalf("extra page: %s", body)
					}
					path = "/api/v1/pulls?limit=1&cursor=" + url.QueryEscape(*page.NextCursor)
				}
			}
		})
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"repo=wa/one", 2}, {"repo=unknown/repo", 0}, {"author=GRACE", 1}, {"q=Second", 1}, {"outcome=completed", 2}, {"is=p0", 2}, {"state=closed", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			status, body := e.getBody("member-both", "/api/v1/pulls?"+tc.query)
			var page Page[InstancePull]
			if err := json.Unmarshal(body, &page); err != nil || status != 200 || len(page.Items) != tc.count {
				t.Fatalf("page: %d %s (%v)", status, body, err)
			}
		})
	}
	status, _ := e.getBody("nobody", "/api/v1/pulls")
	if status != 401 {
		t.Fatalf("anonymous status = %d", status)
	}
}
