//go:build integration

package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"testing"
)

// indexPath is account A's indexed repository's index.
const indexPath = "/api/v1/accounts/github/wa/repos/wa/one/index"

// testIndexContents reads what account A's indexed repository holds:
// its summary, its files a page at a time and filtered, and one file's
// chunks, whose text only an admin sees. Account B's chunks never show.
func testIndexContents(t *testing.T, e *apiEnv) {
	seedIndexChunks(t, e)
	get := indexGetter(t, e)
	checkIndexSummary(t, get)
	checkIndexFiles(t, get)
	checkIndexChunks(t, get)
	for _, tc := range []struct {
		who, path string
		status    int
	}{
		{"member-b", indexPath, 404},
		{"member-b", indexPath + "/files", 404},
		{"member-b", indexPath + "/chunks?path=a.go", 404},
		{"nobody", indexPath, 401},
		{"member-a", indexPath + "/chunks", 400},
		{"member-a", "/api/v1/accounts/github/wa/repos/wa/missing/index", 404},
	} {
		if status, body := e.getBody(tc.who, tc.path); status != tc.status {
			t.Errorf("%s %s: status = %d, want %d: %s", tc.who, tc.path, status, tc.status, body)
		}
	}
}

// seedIndexChunks creates the index table and gives account A's indexed
// repository two files' chunks and account B's one.
func seedIndexChunks(t *testing.T, e *apiEnv) {
	t.Helper()
	ctx := t.Context()
	if _, err := e.st.EnsureIndexSchema(ctx, "kritika_app", "embed", 8, ""); err != nil {
		t.Fatalf("EnsureIndexSchema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.owner.Exec(context.Background(), `DROP TABLE IF EXISTS index_chunks; DELETE FROM index_schema`)
	})
	chunk := func(s seeded, path string, start int, text string) {
		t.Helper()
		e.exec(`INSERT INTO index_chunks (account_id, repository_id, index_run_id, path, start_line, end_line, language, symbol, kind, text, embedding)
			SELECT r.account_id, r.id, r.active_index_run_id, $2, $3, $3 + 9, 'go', 'F', 'function', $4, '[1,0,0,0,0,0,0,0]'
			FROM repositories r WHERE r.id = $1`, s.repoID, path, start, text)
	}
	chunk(e.a, "a.go", 11, "func second() {}")
	chunk(e.a, "a.go", 1, "func first() {}")
	chunk(e.a, "pkg/b.go", 1, "func b() {}")
	chunk(e.b, "only-in-b.go", 1, "func secretOfB() {}")
}

// indexGetter reads an index endpoint as who into out. It fails on any status
// but 200 or a body with account B's index in it.
func indexGetter(t *testing.T, e *apiEnv) func(who, path string, out any) {
	return func(who, path string, out any) {
		t.Helper()
		status, body := e.getBody(who, path)
		if status != 200 {
			t.Fatalf("%s %s: status = %d: %s", who, path, status, body)
		}
		if bytes.Contains(body, []byte("only-in-b.go")) || bytes.Contains(body, []byte("secretOfB")) {
			t.Fatalf("%s %s: body leaks account B's index: %s", who, path, body)
		}
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatal(err)
		}
	}
}

func checkIndexSummary(t *testing.T, get func(who, path string, out any)) {
	t.Helper()
	var c IndexContents
	get("member-a", indexPath, &c)
	if !c.Indexed || c.Commit != "commit7" || c.EmbedModel != "embed" || c.EmbedDims != 8 || c.Chunks != 3 || c.Files != 2 || !c.Current {
		t.Fatalf("index = %+v", c)
	}
	var none IndexContents
	get("member-a", "/api/v1/accounts/github/wa/repos/wa/two/index", &none)
	if none.Indexed || none.Chunks != 0 {
		t.Fatalf("unindexed repository's index = %+v", none)
	}
}

func checkIndexFiles(t *testing.T, get func(who, path string, out any)) {
	t.Helper()
	var first, second Page[IndexFile]
	get("member-a", indexPath+"/files?limit=1", &first)
	if len(first.Items) != 1 || first.Items[0] != (IndexFile{Path: "a.go", Language: "go", Chunks: 2}) || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	get("member-a", indexPath+"/files?limit=1&cursor="+url.QueryEscape(*first.NextCursor), &second)
	if len(second.Items) != 1 || second.Items[0].Path != "pkg/b.go" || second.NextCursor != nil {
		t.Fatalf("second page = %+v", second)
	}
	var filtered Page[IndexFile]
	get("member-a", indexPath+"/files?q=PKG", &filtered)
	if len(filtered.Items) != 1 || filtered.Items[0].Path != "pkg/b.go" {
		t.Fatalf("filtered = %+v", filtered)
	}
	var empty Page[IndexFile]
	get("member-a", "/api/v1/accounts/github/wa/repos/wa/two/index/files", &empty)
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("unindexed repository's files = %+v", empty)
	}
}

func checkIndexChunks(t *testing.T, get func(who, path string, out any)) {
	t.Helper()
	var asMember, asAdmin IndexChunks
	get("member-a", indexPath+"/chunks?path=a.go", &asMember)
	get("admin", indexPath+"/chunks?path=a.go", &asAdmin)
	if len(asMember.Items) != 2 || asMember.Items[0].StartLine != 1 || asMember.Items[1].StartLine != 11 || asMember.Truncated {
		t.Fatalf("member's chunks = %+v", asMember)
	}
	for _, ch := range asMember.Items {
		if ch.Text != "" {
			t.Fatalf("a member sees chunk text: %+v", ch)
		}
	}
	if len(asAdmin.Items) != 2 || asAdmin.Items[0].Text != "func first() {}" {
		t.Fatalf("admin's chunks = %+v", asAdmin)
	}
}

// testIndexJobs shows a repository's queued index job on the repository
// list and on its page, and never another account's.
func testIndexJobs(t *testing.T, e *apiEnv) {
	two := e.scalar(`SELECT id::text FROM repositories WHERE name = 'wa/two'`)
	job := e.scalar(`INSERT INTO river_job (kind, queue, args, max_attempts, state)
		VALUES ('index', 'index', jsonb_build_object('account_id', $1::text, 'repository_id', $2::text, 'trigger', 'reindex', 'full', true), 3, 'available')
		RETURNING id::text`, e.a.accountID, two)
	t.Cleanup(func() { _, _ = e.owner.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1::bigint`, job) })

	var list Page[Repository]
	status, body := e.getBody("member-a", "/api/v1/accounts/github/wa/repos")
	if status != 200 {
		t.Fatalf("repos: status = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	queued := map[string]*IndexJob{}
	for _, r := range list.Items {
		queued[r.FullName] = r.Index.Job
	}
	if j := queued["wa/two"]; j == nil || j.State != "available" || j.Trigger != "reindex" || !j.Full {
		t.Fatalf("wa/two's job = %+v", j)
	}
	if j := queued["wa/one"]; j != nil {
		t.Fatalf("wa/one has no index job, got %+v", j)
	}

	var d RepoDetail
	status, body = e.getBody("member-a", "/api/v1/accounts/github/wa/repos/wa/two")
	if status != 200 {
		t.Fatalf("repo: status = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.IndexJobs) != 1 || d.IndexJobs[0].Trigger != "reindex" || d.Index.Job == nil {
		t.Fatalf("wa/two's index jobs = %+v, job %+v", d.IndexJobs, d.Index.Job)
	}
	status, body = e.getBody("member-b", "/api/v1/accounts/github/wb/repos")
	if status != 200 || bytes.Contains(body, []byte(`"trigger":"reindex"`)) {
		t.Fatalf("account B sees account A's index job: %d %s", status, body)
	}
}
