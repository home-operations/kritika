//go:build integration

package webapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/store"
)

// token creates a token with grant g for a new user named subject, which
// expires at expires, nil for never; a grant without a key gets the
// current configuration's.
func (e *apiEnv) token(subject string, g store.SessionGrant, expires *time.Time) string {
	e.t.Helper()
	ctx, origin := context.Background(), "oidc:https://idp.example"
	user, err := e.st.UpsertIdentity(ctx, store.SignInIdentity{Provider: "oidc", Origin: origin, Subject: subject, DisplayName: subject})
	if err != nil {
		e.t.Fatal(err)
	}
	if g.Key == "" {
		g.Key, _ = auth.GrantKey(e.file, "oidc")
	}
	var token string
	err = e.st.WithAccount(ctx, "", func(tx pgx.Tx) error {
		var err error
		token, _, err = store.CreateAPIToken(ctx, tx, store.APIToken{
			User: user, Name: subject, Identity: store.SignInIdentity{Provider: "oidc", Origin: origin}, Grant: g,
			CreatedAt: time.Now(), ExpiresAt: expires,
		})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return token
}

// bearer sends every request with a bearer token.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession connects an MCP client to the endpoint with token.
func (e *apiEnv) mcpSession(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "kritika-test", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: e.http.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool calls the tool name with args. It decodes what the tool
// reports into out and returns it raw, or, when out is nil, wants an error
// and returns its text.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if out == nil {
		if !res.IsError {
			t.Fatalf("%s(%v) = %s, want an error", name, args, text.String())
		}
		return text.String()
	}
	if res.IsError {
		t.Fatalf("%s(%v): %s", name, args, text.String())
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v: %s", name, err, raw)
	}
	return string(raw)
}

// testMCP reads through the MCP endpoint with tokens: the tools report
// what the dashboard does without cost or runner data, a token reads only
// its own accounts, and nothing but a token that stands gets in.
func testMCP(t *testing.T, e *apiEnv) {
	t.Run("refusals", func(t *testing.T) { testMCPRefusals(t, e) })
	member := e.mcpSession(t, e.token("mcp-alice", memberOfAccounts("github/wa"), nil))
	t.Run("tools", func(t *testing.T) { testMCPToolList(t, member) })
	t.Run("a pull request and its review", func(t *testing.T) { testMCPPull(t, e, member) })
	t.Run("lists", func(t *testing.T) { testMCPLists(t, e, member) })
	t.Run("tool errors", func(t *testing.T) { testMCPToolErrors(t, e, member) })
	t.Run("every account", func(t *testing.T) {
		cs := e.mcpSession(t, e.token("mcp-everyone", store.SessionGrant{Role: store.RoleMember, AllAccounts: true}, nil))
		var p toolPull
		callTool(t, cs, "get_pull", map[string]any{"repository": "wb/one", "number": 7}, &p)
		if p.Title != "PR of webapi-b" {
			t.Errorf("title = %q, want account B's", p.Title)
		}
		var d toolReviewDetail
		callTool(t, cs, "get_review", map[string]any{"repository": "wb/one", "reviewId": e.b.reviewID}, &d)
		if d.ID != e.b.reviewID {
			t.Errorf("review = %s, want %s", d.ID, e.b.reviewID)
		}
	})
}

func testMCPRefusals(t *testing.T, e *apiEnv) {
	past := time.Now().Add(-time.Minute)
	valid := e.token("mcp-valid", memberOfAccounts("github/wa"), nil)
	expired := e.token("mcp-expired", memberOfAccounts("github/wa"), &past)
	stale := e.token("mcp-stale", store.SessionGrant{Role: store.RoleMember, Accounts: []string{"github/wa"}, Key: "an older sign-in"}, nil)
	revoked := e.token("mcp-revoked", memberOfAccounts("github/wa"), nil)
	e.exec(`DELETE FROM api_tokens WHERE name = 'mcp-revoked'`)
	post := func(t *testing.T, path, authorization string, cookie *http.Cookie) (int, http.Header) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, e.http.URL+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := e.http.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	if status, header := post(t, "/mcp", "Bearer "+valid, nil); status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("a valid token: status = %d, Cache-Control = %q, want 200 and no-store", status, header.Get("Cache-Control"))
	}
	tests := []struct {
		name, authorization string
		cookie              *http.Cookie
	}{
		{"no token", "", nil},
		{"a session cookie", "", e.cookie["member-a"]},
		{"an unknown token", "Bearer kritika_unknown", nil},
		{"an expired token", "Bearer " + expired, nil},
		{"a token whose sign-in changed", "Bearer " + stale, nil},
		{"a revoked token", "Bearer " + revoked, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, header := post(t, "/mcp", tt.authorization, tt.cookie)
			if status != http.StatusUnauthorized || !strings.HasPrefix(header.Get("WWW-Authenticate"), `Bearer realm="kritika"`) {
				t.Fatalf("status = %d, WWW-Authenticate = %q, want a 401 bearer challenge", status, header.Get("WWW-Authenticate"))
			}
		})
	}
	t.Run("a token on the API", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, e.http.URL+"/api/v1/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+valid)
		resp, err := e.http.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
}

// noLeaks fails for what a tool reported, raw, holding cost, usage or
// runner data, or account B's rows.
func (e *apiEnv) noLeaks(t *testing.T, raw string) {
	t.Helper()
	for _, leak := range append([]string{`"costUsd"`, `"tokens"`, `"usage"`, `"logTail"`, `"runnerRun"`, `"agentRun"`, `"contextPack"`}, e.bMarkers()...) {
		if strings.Contains(raw, leak) {
			t.Errorf("a tool reports %s: %s", leak, raw)
		}
	}
}

func testMCPToolList(t *testing.T, cs *mcp.ClientSession) {
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
	}
	slices.Sort(names)
	if want := []string{"common_findings", "get_pull", "get_review", "list_findings", "list_followups", "list_rules"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

// testMCPPull reads account A's pull request and its seeded review as a
// token of its member, cs.
func testMCPPull(t *testing.T, e *apiEnv, cs *mcp.ClientSession) {
	var p toolPull
	e.noLeaks(t, callTool(t, cs, "get_pull", map[string]any{"repository": "wa/one", "number": 7}, &p))
	seeded := slices.IndexFunc(p.Reviews, func(v toolReview) bool { return v.ID == e.a.reviewID })
	if p.Title != "PR of webapi-a" || p.HeadSHA != "head7" || seeded < 0 || p.Reviews[seeded].Status != store.ReviewCompleted ||
		p.Reviews[seeded].HeadSHA != "head7" {
		t.Errorf("pull = %+v", p)
	}
	if p.Job == nil || p.Job.State != "available" || p.Job.HeadSHA != "head7" {
		t.Errorf("job = %+v, want the queued review of head7", p.Job)
	}
	if len(p.Followups) != 1 || p.Followups[0].CommentID != followupComment {
		t.Errorf("followups = %+v", p.Followups)
	}
	var d toolReviewDetail
	e.noLeaks(t, callTool(t, cs, "get_review", map[string]any{"repository": "wa/one", "reviewId": e.a.reviewID}, &d))
	if d.ID != e.a.reviewID || d.Pull.Number != 7 || d.Summary == nil || d.Summary.Take != "ok" || len(d.Findings) != 2 {
		t.Errorf("review = %+v", d)
	}
}

// testMCPLists reads account A's lists as a token of its member, cs.
func testMCPLists(t *testing.T, e *apiEnv, cs *mcp.ClientSession) {
	var first, second Page[AccountFinding]
	e.noLeaks(t, callTool(t, cs, "list_findings", map[string]any{"account": "wa", "limit": 1}, &first))
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first page = %+v, want one finding and a cursor", first)
	}
	callTool(t, cs, "list_findings", map[string]any{"account": "wa", "limit": 1, "cursor": *first.NextCursor}, &second)
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.NextCursor != nil {
		t.Errorf("second page = %+v, want the other finding and no cursor", second)
	}
	var p0 Page[AccountFinding]
	callTool(t, cs, "list_findings", map[string]any{"repository": "wa/one", "severity": "p0", "status": "open", "pr": "open"}, &p0)
	if len(p0.Items) != 1 || p0.Items[0].Title != "nil deref" || p0.Items[0].ReviewID != e.a.reviewID {
		t.Errorf("p0 findings = %+v", p0.Items)
	}
	var merged Page[AccountFinding]
	callTool(t, cs, "list_findings", map[string]any{"repository": "wa/one", "pr": "merged"}, &merged)
	if len(merged.Items) != 0 {
		t.Errorf("findings of merged pull requests = %+v, want none: every pull request is open", merged.Items)
	}
	var sum FindingSummary
	e.noLeaks(t, callTool(t, cs, "common_findings", map[string]any{"repository": "wa/one"}, &sum))
	if sum.Findings != 2 || len(sum.Directories) != 1 {
		t.Errorf("summary = %+v", sum)
	}
	var rules toolRules
	e.noLeaks(t, callTool(t, cs, "list_rules", map[string]any{"repository": "wa/one"}, &rules))
	if !slices.ContainsFunc(rules.Rules, func(r Rule) bool { return r.Path == "docs/rules-of-webapi-a.md" }) {
		t.Errorf("rules = %+v, want the repository's own", rules.Rules)
	}
	var followups Page[Followup]
	e.noLeaks(t, callTool(t, cs, "list_followups", map[string]any{"account": "wa"}, &followups))
	if len(followups.Items) != 1 || followups.Items[0].CommentID != followupComment {
		t.Errorf("followups = %+v", followups.Items)
	}
}

// testMCPToolErrors: as a token of account A's member, cs, account B is
// missing, and so is what account A does not have.
func testMCPToolErrors(t *testing.T, e *apiEnv, cs *mcp.ClientSession) {
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_pull", map[string]any{"repository": "wb/one", "number": 7}, "account not found"},
		{"list_findings", map[string]any{"account": "wb"}, "account not found"},
		{"get_review", map[string]any{"repository": "wa/one", "reviewId": e.b.reviewID}, "review not found"},
		{"get_review", map[string]any{"repository": "wa/two", "reviewId": e.a.reviewID}, "review not found"},
		{"get_pull", map[string]any{"repository": "wa/missing", "number": 7}, "repository not found"},
		{"get_pull", map[string]any{"repository": "wa/one", "number": 8}, "pull request not found"},
		{"get_pull", map[string]any{"repository": "wa/one", "number": 1 << 40}, "pull request not found"},
		{"list_rules", map[string]any{"repository": "wa/missing"}, "repository not found"},
		{"list_followups", map[string]any{"repository": "wa/missing"}, "repository not found"},
		{"list_findings", map[string]any{"account": "wa", "severity": "p9"}, "severity must be p0, p1 or p2"},
		{"list_findings", map[string]any{"account": "wa", "cursor": "nope"}, "cursor is not valid"},
		{"list_findings", map[string]any{"forge": "github"}, "name a repository or an account"},
		{"get_pull", map[string]any{"repository": "wa/one"}, "number"},
		{"get_pull", map[string]any{"repository": "wa/one", "number": 7, "admin": true}, "admin"},
	}
	for _, tt := range tests {
		if got := callTool(t, cs, tt.tool, tt.args, nil); !strings.Contains(got, tt.want) {
			t.Errorf("%s(%v) = %q, want %q", tt.tool, tt.args, got, tt.want)
		}
	}
}
