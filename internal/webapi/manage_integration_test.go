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
