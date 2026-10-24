//go:build integration

package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

const manageConfig = `
auth:
  oidc:
    issuer: https://idp.example
    clientId: kritika
    clientSecret: { env: KRITIKA_TEST_TOKEN }
    roleMappingExpr: '"kritika-admin" in roles ? "admin" : ""'
apps:
  mgr-file-bot:
    accounts: [mf, md]
    clientId: Iv1.test
    privateKey: { env: KRITIKA_TEST_TOKEN }
    webhookSecret: { env: KRITIKA_TEST_TOKEN }
`

// fakeActions records each action with the account its transaction was
// scoped to.
type fakeActions struct {
	mu            sync.Mutex
	calls         []string
	notCancelable string
}

func (f *fakeActions) note(ctx context.Context, tx pgx.Tx, format string, args ...any) error {
	var scoped string
	if err := tx.QueryRow(ctx, `SELECT current_setting('app.account_id', true)`).Scan(&scoped); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...)+" in "+scoped)
	return nil
}

func (f *fakeActions) Rerun(ctx context.Context, tx pgx.Tx, accountID, repositoryID string, number int) (int64, error) {
	return 101, f.note(ctx, tx, "rerun %s %s %d", accountID, repositoryID, number)
}

func (f *fakeActions) Cancel(ctx context.Context, tx pgx.Tx, reviewID string) error {
	if reviewID == f.notCancelable {
		return jobs.ErrNotCancelable
	}
	return f.note(ctx, tx, "cancel %s", reviewID)
}

func (f *fakeActions) Reindex(ctx context.Context, tx pgx.Tx, accountID, repositoryID string) (int64, error) {
	return 202, f.note(ctx, tx, "reindex %s %s", accountID, repositoryID)
}

func (f *fakeActions) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

type manageEnv struct {
	t       *testing.T
	st      *store.Store
	owner   *pgxpool.Pool
	current *configfile.Current
	actions *fakeActions
	srv     *Server
	http    *httptest.Server
	cookie  map[string]*http.Cookie
	// users are the ids of the users signIn signed in, by name.
	users map[string]string
}

func newManageEnv(t *testing.T) *manageEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	logger := slog.New(slog.DiscardHandler)
	st := storetest.Open(t)
	owner, err := pgxpool.New(ctx, storetest.Env(t, "KRITIKA_TEST_OWNER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	e := &manageEnv{t: t, st: st, owner: owner, actions: &fakeActions{}, cookie: map[string]*http.Cookie{}, users: map[string]string{}}

	t.Setenv("KRITIKA_TEST_TOKEN", "tok")
	file := configfiletest.Load(t, manageConfig)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	storetest.Repositories(t, st, file, "md/one")

	webURL, _ := url.Parse("https://kritika.example")
	e.current = configfile.NewCurrent(file)
	h, err := auth.New(auth.Config{Store: st, Current: e.current, WebURL: webURL, Logger: logger})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	e.srv = New(Config{Store: st, Current: e.current, Auth: h, Actions: e.actions, WebURL: webURL, Logger: logger})
	e.http = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.http.Close)
	e.signIn("admin", "mgr-op", store.SessionGrant{Role: store.RoleAdmin})
	e.signIn("outsider", "mgr-outsider", memberOfAccounts("github/nobody"))
	return e
}

func (e *manageEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.owner.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *manageEnv) signIn(name, subject string, g store.SessionGrant) {
	e.t.Helper()
	ctx, now, origin := context.Background(), time.Now(), "oidc:https://idp.example"
	user, err := e.st.UpsertIdentity(ctx, store.SignInIdentity{
		Provider: "oidc", Origin: origin, Subject: subject, DisplayName: name, Email: name + "@example.com", EmailVerified: true,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	g.Key, _ = auth.GrantKey(e.current.Get(), "oidc")
	token, err := e.st.CreateSession(ctx, user.ID, "oidc", origin, g, now, now.Add(time.Hour))
	if err != nil {
		e.t.Fatal(err)
	}
	e.cookie[name] = &http.Cookie{Name: auth.SessionCookieName(&url.URL{Scheme: "https", Host: "kritika.example"}), Value: token}
	e.users[name] = user.ID
}

// do sends a request as who; a mutation carries the same-origin headers
// unless csrf is false.
func (e *manageEnv) do(who, method, path string, csrf ...bool) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.http.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if c := e.cookie[who]; c != nil {
		req.AddCookie(c)
	}
	if len(csrf) == 0 || csrf[0] {
		req.Header.Set("Origin", "https://kritika.example")
		req.Header.Set("X-Kritika", "1")
	}
	resp, err := e.http.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, out
}

// expect checks a response's status and, for an error, its code.
func (e *manageEnv) expect(status int, body []byte, wantStatus int, wantCode ErrorCode) {
	e.t.Helper()
	if status != wantStatus {
		e.t.Fatalf("status = %d, want %d: %s", status, wantStatus, body)
	}
	if wantCode == "" {
		return
	}
	var eb ErrorBody
	if err := json.Unmarshal(body, &eb); err != nil || eb.Code != wantCode {
		e.t.Fatalf("body = %s, want code %s", body, wantCode)
	}
}

func (e *manageEnv) audits(action AuditAction, target string) int {
	e.t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1 AND target = $2`,
		string(action), target).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

const mdPath = "/api/v1/accounts/github/md"

// TestManage walks the actions an admin takes and the audit log they
// leave; each step depends on the ones before it.
func TestManage(t *testing.T) {
	e := newManageEnv(t)
	md := configfile.AccountID(configfile.ForgeGitHub, "md")
	e.signIn("member", "mgr-member", memberOfAccounts("github/md"))
	t.Run("actions", func(t *testing.T) { testActions(t, e, md) })
	t.Run("audit log", func(t *testing.T) { testAuditLog(t, e, md) })
	t.Run("users", func(t *testing.T) { testUsers(t, e) })
	t.Run("tokens", func(t *testing.T) { testTokens(t, e) })
}

// send sends a request with a JSON body as who, with the same-origin
// headers.
func (e *manageEnv) send(who, method, path, body string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.http.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.AddCookie(e.cookie[who])
	req.Header.Set("Origin", "https://kritika.example")
	req.Header.Set("X-Kritika", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, out
}

// testTokens: a user creates tokens from what their session reads, lists
// and revokes their own, and an admin lists and revokes anyone's; a token's
// value is shown once, and creating and revoking are audited.
func testTokens(t *testing.T, e *manageEnv) {
	create := func(who, body string) CreatedToken {
		t.Helper()
		status, out := e.send(who, "POST", "/api/v1/me/tokens", body)
		e.expect(status, out, http.StatusCreated, "")
		var c CreatedToken
		if err := json.Unmarshal(out, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	list := func(who, path string) []APIToken {
		t.Helper()
		status, out := e.do(who, "GET", path)
		e.expect(status, out, http.StatusOK, "")
		var tokens []APIToken
		if err := json.Unmarshal(out, &tokens); err != nil {
			t.Fatal(err)
		}
		return tokens
	}

	laptop := create("member", `{"name":" laptop agent ","accounts":[],"expiresInDays":30}`)
	if !strings.HasPrefix(laptop.Token, store.APITokenPrefix) || laptop.Name != "laptop agent" || laptop.AllAccounts ||
		!slices.Equal(laptop.Accounts, []string{"github/md"}) || laptop.Status != TokenActive || laptop.Owner != nil ||
		laptop.ExpiresAt == nil || time.Until(*laptop.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("created = %+v; want a member's 30-day token reading github/md", laptop)
	}
	if every := create("admin", `{"name":"ci","accounts":[],"expiresInDays":0}`); !every.AllAccounts || every.ExpiresAt != nil {
		t.Fatalf("an admin's token = %+v; want every account, never expiring", every)
	}
	for _, tc := range []struct{ who, body, want string }{
		{"member", `{"name":"","accounts":[],"expiresInDays":30}`, "name must be"},
		{"member", `{"name":"` + strings.Repeat("x", 101) + `","accounts":[],"expiresInDays":30}`, "name must be"},
		{"member", `{"name":"x","accounts":[],"expiresInDays":10}`, "expiresInDays must be"},
		{"member", `{"name":"x","accounts":["github/mf"],"expiresInDays":7}`, "accounts must be accounts you read"},
		{"member", `{"name":"x","accounts":[],"expiresInDays":7,"admin":true}`, "not valid JSON"},
		{"outsider", `{"name":"x","accounts":[],"expiresInDays":7}`, "at least one account"},
	} {
		status, out := e.send(tc.who, "POST", "/api/v1/me/tokens", tc.body)
		if status != http.StatusBadRequest || !strings.Contains(string(out), tc.want) {
			t.Errorf("%s creating %s: status %d, %s; want 400 saying %q", tc.who, tc.body, status, out, tc.want)
		}
	}

	mine := list("member", "/api/v1/me/tokens")
	if len(mine) != 1 || mine[0].ID != laptop.ID || mine[0].Owner != nil {
		t.Fatalf("the member's tokens = %+v; want the laptop agent alone, with no owner", mine)
	}
	if _, out := e.do("member", "GET", "/api/v1/me/tokens"); strings.Contains(string(out), laptop.Token) {
		t.Fatalf("a token's value is listed: %s", out)
	}
	status, out := e.do("member", "GET", "/api/v1/admin/tokens")
	e.expect(status, out, http.StatusNotFound, CodeNotFound)
	all := list("admin", "/api/v1/admin/tokens")
	i := slices.IndexFunc(all, func(a APIToken) bool { return a.ID == laptop.ID })
	if i < 0 || all[i].Owner == nil || all[i].Owner.ID != e.users["member"] {
		t.Fatalf("an admin's list = %+v; want the laptop agent with its owner", all)
	}

	// A token goes stale when the sign-in configuration it came from changes,
	// and expires at its expiry.
	e.exec(`UPDATE api_tokens SET grant_key = 'before' WHERE id = $1`, laptop.ID)
	if got := list("member", "/api/v1/me/tokens"); got[0].Status != TokenStale {
		t.Errorf("after a sign-in change = %s, want stale", got[0].Status)
	}
	e.exec(`UPDATE api_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, laptop.ID)
	if got := list("member", "/api/v1/me/tokens"); got[0].Status != TokenExpired {
		t.Errorf("after its expiry = %s, want expired", got[0].Status)
	}

	path := "/api/v1/me/tokens/" + laptop.ID
	status, out = e.do("admin", "DELETE", path)
	e.expect(status, out, http.StatusNotFound, CodeNotFound)
	status, out = e.do("member", "DELETE", path, false)
	e.expect(status, out, http.StatusForbidden, "")
	status, out = e.do("member", "DELETE", "/api/v1/me/tokens/not-a-uuid")
	e.expect(status, out, http.StatusNotFound, CodeNotFound)
	status, out = e.do("member", "DELETE", path)
	e.expect(status, out, http.StatusNoContent, "")
	status, out = e.do("member", "DELETE", path)
	e.expect(status, out, http.StatusNotFound, CodeNotFound)

	agent := create("member", `{"name":"agent","accounts":["github/md"],"expiresInDays":7}`)
	status, out = e.do("member", "DELETE", "/api/v1/admin/tokens/"+agent.ID)
	e.expect(status, out, http.StatusNotFound, CodeNotFound)
	status, out = e.do("admin", "DELETE", "/api/v1/admin/tokens/"+agent.ID)
	e.expect(status, out, http.StatusNoContent, "")
	if got := list("member", "/api/v1/me/tokens"); len(got) != 0 {
		t.Errorf("after an admin revoked it, the member's tokens = %+v", got)
	}
	for _, name := range []string{"laptop agent", "agent"} {
		if c, r := e.audits(AuditTokenCreate, name), e.audits(AuditTokenRevoke, name); c != 1 || r != 1 {
			t.Errorf("%s: %d creations and %d revocations audited, want one each", name, c, r)
		}
	}
}

// testUsers: an admin lists every user with what their session reads, and
// signs one out everywhere, which ends their sessions and is audited; no
// one else sees the route.
func testUsers(t *testing.T, e *manageEnv) {
	e.signIn("leaver", "mgr-leaver", memberOfAccounts("github/md"))
	status, body := e.do("member", "GET", "/api/v1/admin/users")
	e.expect(status, body, http.StatusNotFound, CodeNotFound)
	users := func() map[string]AdminUser {
		t.Helper()
		status, body := e.do("admin", "GET", "/api/v1/admin/users")
		e.expect(status, body, http.StatusOK, "")
		var list []AdminUser
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		out := map[string]AdminUser{}
		for _, u := range list {
			out[u.ID] = u
		}
		return out
	}
	got := users()
	admin, leaver := got[e.users["admin"]], got[e.users["leaver"]]
	if admin.Access == nil || !admin.Access.Admin || admin.Sessions != 1 || admin.LastSignInAt == nil {
		t.Fatalf("admin = %+v; want an admin with one session", admin)
	}
	if leaver.Access == nil || leaver.Access.Admin || !slices.Equal(leaver.Access.Accounts, []string{"github/md"}) || leaver.Sessions != 1 ||
		len(leaver.SignIns) != 1 || leaver.SignIns[0].Provider != "oidc" {
		t.Fatalf("leaver = %+v; want a member of github/md with one session and their sign-in", leaver)
	}

	// Access is what the newest honoured session reads. A newer session whose
	// sign-in has changed since counts for nothing, not even as live.
	for _, accounts := range [][]string{{"github/md"}, {"github/mf"}, {"github/md", "github/mf"}} {
		e.signIn("juggler", "mgr-juggler", memberOfAccounts(accounts...))
	}
	juggler := e.users["juggler"]
	e.exec(`UPDATE sessions SET created_at = now() - interval '3 hours' WHERE user_id = $1 AND accounts = '{github/md}'`, juggler)
	e.exec(`UPDATE sessions SET created_at = now() - interval '2 hours' WHERE user_id = $1 AND accounts = '{github/mf}'`, juggler)
	e.exec(`UPDATE sessions SET created_at = now() - interval '1 hour', grant_key = 'before' WHERE user_id = $1 AND cardinality(accounts) = 2`, juggler)
	e.signIn("stale", "mgr-stale", memberOfAccounts("github/md"))
	e.exec(`UPDATE sessions SET grant_key = 'before' WHERE user_id = $1`, e.users["stale"])
	got = users()
	if j := got[juggler]; j.Sessions != 2 || j.Access == nil || !slices.Equal(j.Access.Accounts, []string{"github/mf"}) {
		t.Errorf("juggler = %+v; want two honoured sessions, reading what the newer of them reads", j)
	}
	if s, listed := got[e.users["stale"]]; !listed || s.Sessions != 0 || s.Access != nil {
		t.Errorf("stale = %+v, listed %v; want them listed, with no honoured session and no access", s, listed)
	}

	path := "/api/v1/admin/users/" + leaver.ID + "/sessions"
	status, body = e.do("member", "DELETE", path)
	e.expect(status, body, http.StatusNotFound, CodeNotFound)
	status, body = e.do("admin", "DELETE", path, false)
	e.expect(status, body, http.StatusForbidden, "")
	for _, missing := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		status, body = e.do("admin", "DELETE", "/api/v1/admin/users/"+missing+"/sessions")
		e.expect(status, body, http.StatusNotFound, CodeNotFound)
	}
	status, body = e.do("admin", "DELETE", path)
	e.expect(status, body, http.StatusNoContent, "")
	status, body = e.do("leaver", "GET", "/api/v1/me")
	e.expect(status, body, http.StatusUnauthorized, "")
	if n := e.audits(AuditUserSignOut, "leaver"); n != 1 {
		t.Errorf("sign-out audits = %d, want 1", n)
	}
	if gone := users()[e.users["leaver"]]; gone.Sessions != 0 || gone.Access != nil || gone.LastSignInAt == nil {
		t.Errorf("leaver signed out = %+v; want no session or access, and their last sign-in kept", gone)
	}
}

func testActions(t *testing.T, e *manageEnv, md string) {
	repoID := configfile.RepositoryID(md, "md/one")
	e.exec(`INSERT INTO pull_requests (account_id, repository_id, number, title, author, head_sha)
		VALUES ($1, $2, 3, 'x', 'ada', 'h3') ON CONFLICT DO NOTHING`, md, repoID)

	status, body := e.do("member", "POST", mdPath+"/pulls/md/one/3/rerun")
	e.expect(status, body, http.StatusForbidden, CodeForbidden)
	status, body = e.do("admin", "POST", mdPath+"/pulls/md/one/3/rerun", false)
	e.expect(status, body, http.StatusForbidden, "")
	status, body = e.do("admin", "POST", mdPath+"/pulls/md/one/99/rerun")
	e.expect(status, body, http.StatusNotFound, CodeNotFound)

	status, body = e.do("admin", "POST", mdPath+"/pulls/md/one/3/rerun")
	e.expect(status, body, http.StatusAccepted, "")
	if string(bytes.TrimSpace(body)) != `{"jobId":101}` || e.actions.last() != fmt.Sprintf("rerun %s %s 3 in %s", md, repoID, md) {
		t.Errorf("rerun = %s, call %q", body, e.actions.last())
	}
	if n := e.audits(AuditReviewRerun, "md/one#3"); n != 1 {
		t.Errorf("review.rerun audit rows = %d", n)
	}

	review := "00000000-0000-4000-8000-000000000001"
	e.actions.notCancelable = "00000000-0000-4000-8000-000000000002"
	status, body = e.do("admin", "POST", mdPath+"/reviews/"+e.actions.notCancelable+"/cancel")
	e.expect(status, body, http.StatusConflict, CodeNotCancelable)
	status, body = e.do("admin", "POST", mdPath+"/reviews/"+review+"/cancel")
	e.expect(status, body, http.StatusAccepted, "")
	if e.actions.last() != fmt.Sprintf("cancel %s in %s", review, md) {
		t.Errorf("cancel call %q", e.actions.last())
	}
	if e.audits(AuditReviewCancel, review) != 1 || e.audits(AuditReviewCancel, e.actions.notCancelable) != 0 {
		t.Errorf("review.cancel audit rows are wrong")
	}

	status, body = e.do("admin", "POST", mdPath+"/repos/md/one/reindex")
	e.expect(status, body, http.StatusAccepted, "")
	if e.actions.last() != fmt.Sprintf("reindex %s %s in %s", md, repoID, md) || e.audits(AuditRepoReindex, "md/one") != 1 {
		t.Errorf("reindex = %s, call %q", body, e.actions.last())
	}
}

func testAuditLog(t *testing.T, e *manageEnv, md string) {
	status, body := e.do("member", "GET", mdPath+"/audit")
	e.expect(status, body, http.StatusForbidden, CodeForbidden)
	status, body = e.do("member", "GET", "/api/v1/admin/audit")
	e.expect(status, body, http.StatusNotFound, CodeNotFound)

	var seen []AuditEvent
	path := mdPath + "/audit?limit=2"
	for range 20 {
		status, body = e.do("admin", "GET", path)
		e.expect(status, body, http.StatusOK, "")
		var page Page[AuditEvent]
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, page.Items...)
		if page.NextCursor == nil {
			break
		}
		path = mdPath + "/audit?limit=2&cursor=" + *page.NextCursor
	}
	var want int
	if err := e.owner.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE account_id = $1`, md).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if len(seen) != want || want < 3 {
		t.Fatalf("paged %d events, want %d", len(seen), want)
	}
	var prev int64
	for i, ev := range seen {
		id, err := strconv.ParseInt(ev.ID, 10, 64)
		if err != nil || ev.Account != "github/md" || ev.Actor == nil || (i > 0 && id >= prev) {
			t.Errorf("event %d = %+v, want newest first", i, ev)
		}
		prev = id
	}
	status, body = e.do("admin", "GET", "/api/v1/admin/audit?limit=200")
	e.expect(status, body, http.StatusOK, "")
	if !strings.Contains(string(body), `"account":"github/md","action":"repo.reindex","target":"md/one"`) {
		t.Errorf("admin audit lacks the account's reindex: %s", body)
	}
}
