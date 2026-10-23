package ingest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/webhook"
)

const configYAML = `
apps:
  bot-ross:
    accounts: [onedr0p, home-operations]
    clientId: Iv1.x
    privateKey: { env: TEST_PEM }
    webhookSecret: { env: TEST_SECRET }
`

type fakeDispatcher struct {
	got       []Request
	out       Outcome
	err       error
	delivered []string
	unsigned  []string
	recordErr error
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req Request) (Outcome, error) {
	f.got = append(f.got, req)
	return f.out, f.err
}

func (f *fakeDispatcher) RecordDelivery(_ context.Context, connectionID string) error {
	f.delivered = append(f.delivered, connectionID)
	return f.recordErr
}

func (f *fakeDispatcher) RecordUnsigned(_ context.Context, connectionID string) error {
	f.unsigned = append(f.unsigned, connectionID)
	return f.recordErr
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

const prBody = `{"action":"opened","pull_request":{"number":1,"user":{"login":"x"},
  "head":{"ref":"f","sha":"1","repo":{"full_name":"onedr0p/home-ops"}},
  "base":{"ref":"main","sha":"2","repo":{"full_name":"onedr0p/home-ops"}}},
  "repository":{"full_name":"onedr0p/home-ops","default_branch":"main","owner":{"login":"onedr0p"}}}`

func setup(t *testing.T, disp Dispatcher) *httptest.Server {
	t.Helper()
	return setupLogged(t, disp, slog.New(slog.DiscardHandler))
}

func setupLogged(t *testing.T, disp Dispatcher, logger *slog.Logger) *httptest.Server {
	t.Helper()
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	f := configfiletest.Load(t, configYAML)
	h := NewHandler(configfile.NewCurrent(f), disp, logger)
	if r, ok := disp.(DeliveryRecorder); ok {
		h.Deliveries = r
	}
	mux := http.NewServeMux()
	mux.Handle("POST /hooks/{connection}", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, srv *httptest.Server, path, event, secret, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "d-1")
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", sign(secret, []byte(body)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		event      string
		secret     string
		body       string
		out        Outcome
		err        error
		want       int
		dispatched bool
		// recorded: any delivery that passes the signature check counts.
		recorded bool
	}{
		{"unknown connection", "/hooks/nope", "pull_request", "s3cret", prBody, Outcome{}, nil, http.StatusNotFound, false, false},
		{"bad signature", "/hooks/bot-ross", "pull_request", "wrong", prBody, Outcome{}, nil, http.StatusUnauthorized, false, false},
		{"missing signature", "/hooks/bot-ross", "pull_request", "", prBody, Outcome{}, nil, http.StatusUnauthorized, false, false},
		{"unparsable", "/hooks/bot-ross", "pull_request", "s3cret", "{nope", Outcome{}, nil, http.StatusBadRequest, false, true},
		{"ping", "/hooks/bot-ross", "ping", "s3cret", `{"zen":"x"}`, Outcome{}, nil, http.StatusNoContent, false, true},
		{"unknown event ignored", "/hooks/bot-ross", "workflow_run", "s3cret", `{}`, Outcome{}, nil, http.StatusAccepted, false, true},
		{"undeclared account ignored", "/hooks/bot-ross", "pull_request", "s3cret",
			strings.ReplaceAll(prBody, `"owner":{"login":"onedr0p"}`, `"owner":{"login":"stranger"}`), Outcome{}, nil, http.StatusAccepted, false, true},
		{"dispatched", "/hooks/bot-ross", "pull_request", "s3cret", prBody, Outcome{Status: Enqueued}, nil, http.StatusAccepted, true, true},
		{"another declared account dispatched", "/hooks/bot-ross", "pull_request", "s3cret",
			strings.ReplaceAll(prBody, "onedr0p", "Home-Operations"), Outcome{Status: Enqueued}, nil, http.StatusAccepted, true, true},
		{"dispatcher error", "/hooks/bot-ross", "pull_request", "s3cret", prBody, Outcome{}, errors.New("db down"), http.StatusInternalServerError, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp := &fakeDispatcher{out: tt.out, err: tt.err}
			srv := setup(t, disp)
			resp := post(t, srv, tt.path, tt.event, tt.secret, tt.body)
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if (len(disp.got) > 0) != tt.dispatched {
				t.Fatalf("dispatched = %v, want %v", len(disp.got) > 0, tt.dispatched)
			}
			if (len(disp.delivered) > 0) != tt.recorded {
				t.Fatalf("recorded = %v, want %v", len(disp.delivered) > 0, tt.recorded)
			}
			if tt.dispatched {
				req := disp.got[0]
				if !strings.EqualFold(req.Account.Name, req.Event.Account) || req.Event.Kind != webhook.KindPullRequest {
					t.Fatalf("request = %+v", req)
				}
			}
		})
	}
}

// TestHandlerLogsRepositoryAndPullRequest: a handled delivery's line
// names the repository it was for and, when the event concerns one, the
// pull request, so a skip can be traced from the logs alone.
func TestHandlerLogsRepositoryAndPullRequest(t *testing.T) {
	const repo = `"repository":{"full_name":"onedr0p/home-ops","default_branch":"main","owner":{"login":"onedr0p"}}`
	for _, tt := range []struct {
		name, event, body string
		pr                float64
	}{
		{"pull request", "pull_request", prBody, 1},
		{"comment", "issue_comment", `{"action":"created","issue":{"number":7,"pull_request":{"url":"x"}},
		  "comment":{"id":99,"body":"@bot look","user":{"login":"devin","type":"User"}},` + repo + `}`, 7},
		{"review thread", "pull_request_review_thread", `{"action":"resolved","pull_request":{"number":9},
		  "sender":{"login":"devin"},"thread":{"comments":[{"id":5}]},` + repo + `}`, 9},
		{"push", "push", `{"ref":"refs/heads/main","after":"2",` + repo + `}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			srv := setupLogged(t, &fakeDispatcher{out: Outcome{Status: Skipped, Reason: "disabled"}}, slog.New(slog.NewJSONHandler(&logs, nil)))
			if resp := post(t, srv, "/hooks/bot-ross", tt.event, "s3cret", tt.body); resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			line := loggedLine(t, &logs, "webhook handled")
			pr, hasPR := line["pr"]
			if line["repository"] != "onedr0p/home-ops" || line["reason"] != "disabled" || hasPR != (tt.pr != 0) || (hasPR && pr != tt.pr) {
				t.Fatalf("webhook handled = %v, want repository onedr0p/home-ops and pr %v", line, tt.pr)
			}
		})
	}
}

// TestHandlerLogsRepositoryOfServedAccounts: a failed dispatch's line
// names the repository and pull request too, and the line for an account
// the instance does not serve names neither.
func TestHandlerLogsRepositoryOfServedAccounts(t *testing.T) {
	var logs bytes.Buffer
	srv := setupLogged(t, &fakeDispatcher{err: errors.New("db down")}, slog.New(slog.NewJSONHandler(&logs, nil)))
	post(t, srv, "/hooks/bot-ross", "pull_request", "s3cret", prBody)
	post(t, srv, "/hooks/bot-ross", "pull_request", "s3cret",
		strings.ReplaceAll(prBody, `"owner":{"login":"onedr0p"}`, `"owner":{"login":"stranger"}`))
	if failed := loggedLine(t, &logs, "webhook dispatch failed"); failed["repository"] != "onedr0p/home-ops" || failed["pr"] != float64(1) {
		t.Fatalf("webhook dispatch failed = %v, want repository onedr0p/home-ops and pr 1", failed)
	}
	stranger := loggedLine(t, &logs, "webhook for an undeclared account ignored")
	if _, named := stranger["repository"]; named || stranger["pr"] != nil {
		t.Fatalf("undeclared account line = %v, want neither repository nor pr", stranger)
	}
}

// loggedLine is the JSON line in logs whose message is msg.
func loggedLine(t *testing.T, logs *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for raw := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		if line["msg"] == msg {
			return line
		}
	}
	t.Fatalf("no %q line in:\n%s", msg, logs.String())
	return nil
}

// TestHandlerRecordsUnsigned: a delivery with no signature, which an App
// with no webhook secret sends, is recorded as unsigned; a wrong signature
// is not, and neither is an unsigned request without GitHub's delivery
// headers, which anyone can send to the public hook path.
func TestHandlerRecordsUnsigned(t *testing.T) {
	for _, tt := range []struct {
		name     string
		secret   string
		bare     bool
		unsigned bool
	}{
		{"no signature", "", false, true},
		{"a wrong signature", "wrong", false, false},
		{"no signature and no delivery headers", "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			disp := &fakeDispatcher{}
			srv := setup(t, disp)
			var resp *http.Response
			if tt.bare {
				req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hooks/bot-ross", strings.NewReader(prBody))
				req.Header.Set("Content-Type", "application/json")
				var err error
				if resp, err = http.DefaultClient.Do(req); err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			} else {
				resp = post(t, srv, "/hooks/bot-ross", "pull_request", tt.secret, prBody)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if (len(disp.unsigned) == 1) != tt.unsigned || len(disp.delivered) != 0 {
				t.Fatalf("unsigned = %v, delivered = %v", disp.unsigned, disp.delivered)
			}
		})
	}
}

func TestHandlerServesWhenTheDeliveryIsNotRecorded(t *testing.T) {
	disp := &fakeDispatcher{out: Outcome{Status: Enqueued}, recordErr: errors.New("db down")}
	srv := setup(t, disp)
	if resp := post(t, srv, "/hooks/bot-ross", "pull_request", "s3cret", prBody); resp.StatusCode != http.StatusAccepted || len(disp.got) != 1 {
		t.Fatalf("status = %d, dispatched %d times; want 202 and once", resp.StatusCode, len(disp.got))
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	srv := setup(t, &fakeDispatcher{})
	body := `{"pad":"` + strings.Repeat("x", webhook.MaxBody) + `"}`
	if resp := post(t, srv, "/hooks/bot-ross", "push", "s3cret", body); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// dispatcherFunc is a Dispatcher from a function.
type dispatcherFunc func(context.Context, Request) (Outcome, error)

func (f dispatcherFunc) Dispatch(ctx context.Context, req Request) (Outcome, error) {
	return f(ctx, req)
}

// TestHandlerDispatchOutlivesTheRequest: a delivery the forge gave up
// waiting on is still dispatched to the end.
func TestHandlerDispatchOutlivesTheRequest(t *testing.T) {
	entered, release, ended := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	srv := setup(t, dispatcherFunc(func(ctx context.Context, _ Request) (Outcome, error) {
		close(entered)
		<-release
		ended <- ctx.Err()
		return Outcome{Status: Enqueued}, nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/hooks/bot-ross", strings.NewReader(prBody))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "d-1")
	req.Header.Set("X-Hub-Signature-256", sign("s3cret", []byte(prBody)))
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	<-gone
	// Long enough for the server to notice the client left.
	time.Sleep(200 * time.Millisecond)
	close(release)
	if err := <-ended; err != nil {
		t.Fatalf("the dispatch's context ended with the request: %v", err)
	}
}

// fakeAuthors answers every pull request with one author.
type fakeAuthors struct {
	login string
	err   error
	asked []string
}

func (f *fakeAuthors) PullRequestAuthor(_ context.Context, in *configfile.Connection, repo string, number int) (string, bool, error) {
	f.asked = append(f.asked, fmt.Sprintf("%s %s#%d", in.Name, repo, number))
	return f.login, true, f.err
}

// TestHandlerReadsGitLabAuthor: a GitLab merge request event names the
// author by id alone, so one another user's action sends is completed
// from the forge before it is dispatched, or fails.
func TestHandlerReadsGitLabAuthor(t *testing.T) {
	t.Setenv("TEST_GL_TOKEN", "glpat")
	t.Setenv("TEST_GL_HOOK", "whsec_c2lnbmluZy1rZXk=")
	f := configfiletest.Load(t, `
gitlab:
  lab:
    host: gitlab.example.com
    accounts: [acme]
    token: { env: TEST_GL_TOKEN }
    webhookSecret: { env: TEST_GL_HOOK }
`)
	body := `{"user": {"id": 4, "username": "ann"}, "project": {"path_with_namespace": "acme/sub/widgets"},
	  "object_attributes": {"iid": 7, "action": "open", "state": "opened", "author_id": 3}}`
	send := func(authors *fakeAuthors) (*fakeDispatcher, int) {
		disp := &fakeDispatcher{out: Outcome{Status: Enqueued}}
		h := NewHandler(configfile.NewCurrent(f), disp, slog.New(slog.DiscardHandler))
		if authors != nil {
			h.Authors = authors
		}
		key, _ := base64.StdEncoding.DecodeString("c2lnbmluZy1rZXk=")
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("m-1." + ts + "." + body))
		req := httptest.NewRequest(http.MethodPost, "/hooks/lab", strings.NewReader(body))
		req.SetPathValue("connection", "lab")
		req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
		req.Header.Set("webhook-id", "m-1")
		req.Header.Set("webhook-timestamp", ts)
		req.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return disp, rec.Code
	}
	authors := &fakeAuthors{login: "renovate"}
	disp, code := send(authors)
	if code != http.StatusAccepted || len(disp.got) != 1 || len(authors.asked) != 1 || authors.asked[0] != "lab acme/sub/widgets#7" {
		t.Fatalf("code = %d, asked = %q, dispatched = %d", code, authors.asked, len(disp.got))
	}
	if pr := disp.got[0].Event.PullRequest; pr.Author != "renovate" || !pr.AuthorIsBot {
		t.Fatalf("pr = %+v", pr)
	}
	for name, authors := range map[string]*fakeAuthors{"no lookup": nil, "a failed lookup": {err: errors.New("boom")}, "no author": {}} {
		if disp, code := send(authors); code != http.StatusInternalServerError || len(disp.got) != 0 {
			t.Errorf("%s: code = %d, dispatched = %d", name, code, len(disp.got))
		}
	}
}

// TestHandlerMarksListedBots: a pull request or comment by a login a
// Forgejo or Gitea connection lists as a bot is dispatched as a bot's.
func TestHandlerMarksListedBots(t *testing.T) {
	t.Setenv("TEST_FJ_KEY", "key")
	t.Setenv("TEST_FJ_HOOK", "hook-secret")
	f := configfiletest.Load(t, `
forgejo:
  home: { host: forge.example.com, accounts: [acme], bots: [bot-owl], privateKey: { env: TEST_FJ_KEY }, webhookSecret: { env: TEST_FJ_HOOK } }
gitea:
  tea: { host: gitea.example.com, accounts: [acme], bots: [bot-owl], token: { env: TEST_FJ_KEY }, webhookSecret: { env: TEST_FJ_HOOK } }
`)
	repo := `"repository": {"full_name": "acme/widgets", "owner": {"login": "acme"}}`
	pull := `{"action": "opened", "pull_request": {"number": 7, "user": {"login": "Bot-Owl"}, "head": {"sha": "h"}}, ` + repo + `}`
	comment := `{"action": "created", "is_pull": true, "issue": {"number": 7}, "comment": {"id": 5, "body": "@kritika hi", "user": {"login": "bot-owl"}}, ` + repo + `}`
	for _, tt := range []struct {
		name, conn, prefix, event, body string
		kind                            webhook.Kind
	}{
		{"forgejo pull request", "home", "X-Forgejo-", "pull_request", pull, webhook.KindPullRequest},
		{"forgejo comment", "home", "X-Forgejo-", "issue_comment", comment, webhook.KindComment},
		{"gitea pull request", "tea", "X-Gitea-", "pull_request", pull, webhook.KindPullRequest},
		{"gitea comment", "tea", "X-Gitea-", "issue_comment", comment, webhook.KindComment},
	} {
		t.Run(tt.name, func(t *testing.T) {
			disp := &fakeDispatcher{out: Outcome{Status: Enqueued}}
			h := NewHandler(configfile.NewCurrent(f), disp, slog.New(slog.DiscardHandler))
			mac := hmac.New(sha256.New, []byte("hook-secret"))
			mac.Write([]byte(tt.body))
			req := httptest.NewRequest(http.MethodPost, "/hooks/"+tt.conn, strings.NewReader(tt.body))
			req.SetPathValue("connection", tt.conn)
			req.Header.Set(tt.prefix+"Event", tt.event)
			req.Header.Set(tt.prefix+"Delivery", "d-1")
			req.Header.Set(tt.prefix+"Signature", hex.EncodeToString(mac.Sum(nil)))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted || len(disp.got) != 1 || disp.got[0].Event.Kind != tt.kind {
				t.Fatalf("code = %d, dispatched = %+v", rec.Code, disp.got)
			}
			ev := disp.got[0].Event
			switch tt.kind {
			case webhook.KindPullRequest:
				if ev.PullRequest == nil || !ev.PullRequest.AuthorIsBot {
					t.Fatalf("pr = %+v, want a bot's", ev.PullRequest)
				}
			default:
				if ev.Comment == nil || !ev.Comment.AuthorIsBot {
					t.Fatalf("comment = %+v, want a bot's", ev.Comment)
				}
			}
		})
	}
}

type fakeUninstaller struct {
	got []string
	err error
	// ctxErr and deadline are the last call's context's.
	ctxErr   error
	deadline bool
}

func (f *fakeUninstaller) Uninstall(ctx context.Context, in *configfile.Connection, account string, installation int64) error {
	f.got = append(f.got, fmt.Sprintf("%s/%s#%d", in.Name, account, installation))
	f.ctxErr = ctx.Err()
	_, f.deadline = ctx.Deadline()
	return f.err
}

// fakeEvents records pod Events as "Type Reason: note".
type fakeEvents struct{ got []string }

func (f *fakeEvents) PodEventAsync(_ context.Context, warning bool, reason, _, note string) {
	kind := "Normal"
	if warning {
		kind = "Warning"
	}
	f.got = append(f.got, kind+" "+reason+": "+note)
}

// installationBody is an installation event of action on account, made
// now, with the installation's id when it is not 0.
func installationBody(action, account string, id int64) string {
	return installationBodyAt(action, account, id, strconv.FormatInt(time.Now().Unix(), 10))
}

// installationBodyAt is installationBody with createdAt, a JSON value, as
// the installation's created_at, none when it is "".
func installationBodyAt(action, account string, id int64, createdAt string) string {
	inst := `"account":{"login":"` + account + `","type":"Organization"}`
	if id != 0 {
		inst = `"id":` + strconv.FormatInt(id, 10) + `,` + inst
	}
	if createdAt != "" {
		inst += `,"created_at":` + createdAt
	}
	return `{"action":"` + action + `","installation":{` + inst + `},"repositories":[{"full_name":"` + account + `/a"}]}`
}

// sendHook delivers a signed GitHub event to h's bot-ross hook.
func sendHook(t *testing.T, h *Handler, event, body string) int {
	t.Helper()
	return sendHookCtx(t.Context(), h, event, body)
}

// sendHookCtx is sendHook with the request's context ctx.
func sendHookCtx(ctx context.Context, h *Handler, event, body string) int {
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/hooks/bot-ross", strings.NewReader(body))
	req.SetPathValue("connection", "bot-ross")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "d-1")
	req.Header.Set("X-Hub-Signature-256", sign("s3cret", []byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// webhookOutcomes sums kritika_webhooks_total by outcome.
func webhookOutcomes(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range families {
		if mf.GetName() != "kritika_webhooks_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" {
					out[l.GetValue()] += m.GetCounter().GetValue()
				}
			}
		}
	}
	return out
}

// TestHandlerUninstallsFromUndeclaredAccounts: the App is removed from an
// account no connection serves as it is installed there, and from no
// other: a served account's installation is dispatched, and another
// action on an undeclared account's, as any of its events, is ignored.
func TestHandlerUninstallsFromUndeclaredAccounts(t *testing.T) {
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	f := configfiletest.Load(t, configYAML)
	for _, tt := range []struct {
		name, event, body string
		noUninstaller     bool
		err               error
		uninstalled       []string
		dispatched        bool
		outcome           string
	}{
		{name: "installed on an undeclared account", event: "installation", body: installationBody("created", "stranger", 7),
			uninstalled: []string{"bot-ross/stranger#7"}, outcome: "uninstalled"},
		{name: "installed at an RFC 3339 time", event: "installation",
			body:        installationBodyAt("created", "stranger", 7, `"`+time.Now().Format(time.RFC3339)+`"`),
			uninstalled: []string{"bot-ross/stranger#7"}, outcome: "uninstalled"},
		{name: "the forge refuses", event: "installation", body: installationBody("created", "stranger", 7), err: errors.New("boom"),
			uninstalled: []string{"bot-ross/stranger#7"}, outcome: "error"},
		{name: "redelivered after its account was dropped", event: "installation",
			body:    installationBodyAt("created", "stranger", 7, strconv.FormatInt(time.Now().Add(-2*24*time.Hour).Unix(), 10)),
			outcome: "undeclared_account"},
		{name: "no installation time", event: "installation", body: installationBodyAt("created", "stranger", 7, ""),
			outcome: "undeclared_account"},
		{name: "installed on a served account", event: "installation", body: installationBody("created", "Home-Operations", 7),
			dispatched: true, outcome: Recorded},
		{name: "uninstalled from an undeclared account", event: "installation", body: installationBody("deleted", "stranger", 7),
			outcome: "undeclared_account"},
		{name: "repositories added on an undeclared account", event: "installation_repositories",
			body: installationBody("added", "stranger", 7), outcome: "undeclared_account"},
		{name: "no installation id", event: "installation", body: installationBody("created", "stranger", 0),
			outcome: "undeclared_account"},
		{name: "no uninstaller", event: "installation", body: installationBody("created", "stranger", 7), noUninstaller: true,
			outcome: "undeclared_account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			disp := &fakeDispatcher{out: Outcome{Status: Recorded}}
			reg := prometheus.NewRegistry()
			h := NewHandler(configfile.NewCurrent(f), disp, slog.New(slog.DiscardHandler))
			h.Metrics = metrics.New(reg)
			u := &fakeUninstaller{err: tt.err}
			if !tt.noUninstaller {
				h.Uninstaller = u
			}
			events := &fakeEvents{}
			h.Events = events
			if code := sendHook(t, h, tt.event, tt.body); code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", code)
			}
			if !slices.Equal(u.got, tt.uninstalled) {
				t.Fatalf("uninstalled = %q, want %q", u.got, tt.uninstalled)
			}
			if (len(disp.got) > 0) != tt.dispatched {
				t.Fatalf("dispatched = %v, want %v", len(disp.got) > 0, tt.dispatched)
			}
			if got := webhookOutcomes(t, reg); got[tt.outcome] != 1 || len(got) != 1 {
				t.Fatalf("outcomes = %v, want one %s", got, tt.outcome)
			}
			// Only a removal is recorded, naming the account and where to list it.
			if tt.outcome != "uninstalled" {
				if len(events.got) != 0 {
					t.Fatalf("events = %q, want none", events.got)
				}
				return
			}
			if len(events.got) != 1 || !strings.HasPrefix(events.got[0], "Warning AppUninstalled: ") ||
				!strings.Contains(events.got[0], "uninstalled from stranger (installation 7)") ||
				!strings.Contains(events.got[0], "apps.bot-ross.accounts") {
				t.Fatalf("events = %q", events.got)
			}
		})
	}
}

// TestHandlerLimitsUninstalls: past its burst a replica uninstalls no
// more, and ignores the installation as it would any other webhook for an
// undeclared account.
func TestHandlerLimitsUninstalls(t *testing.T) {
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	reg := prometheus.NewRegistry()
	h := NewHandler(configfile.NewCurrent(configfiletest.Load(t, configYAML)), &fakeDispatcher{}, slog.New(slog.DiscardHandler))
	h.Metrics = metrics.New(reg)
	u := &fakeUninstaller{}
	h.Uninstaller = u
	for i := range uninstallBurst + 2 {
		if code := sendHook(t, h, "installation", installationBody("created", "stranger", int64(i+1))); code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", code)
		}
	}
	got := webhookOutcomes(t, reg)
	if len(u.got) != uninstallBurst || got["uninstalled"] != uninstallBurst || got["uninstall_limited"] != 2 {
		t.Fatalf("uninstalled %d, outcomes %v; want %d uninstalled and 2 limited", len(u.got), got, uninstallBurst)
	}
}

// TestHandlerUninstallOutlivesItsRequest: a removal the forge was asked
// for finishes, bounded, when GitHub hangs up, so its audit row is not
// rolled back after the installation is gone.
func TestHandlerUninstallOutlivesItsRequest(t *testing.T) {
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "s3cret")
	h := NewHandler(configfile.NewCurrent(configfiletest.Load(t, configYAML)), &fakeDispatcher{}, slog.New(slog.DiscardHandler))
	u := &fakeUninstaller{}
	h.Uninstaller = u
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := sendHookCtx(ctx, h, "installation", installationBody("created", "stranger", 7)); code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if len(u.got) != 1 || u.ctxErr != nil || !u.deadline {
		t.Fatalf("uninstalled %q with context error %v, deadline %v; want one, live and bounded", u.got, u.ctxErr, u.deadline)
	}
}
