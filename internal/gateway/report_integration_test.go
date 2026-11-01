//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// reportEnv is a gateway and what a run holds to call it.
type reportEnv struct {
	t       *testing.T
	st      *store.Store
	url     string
	account string
}

func newReportEnv(t *testing.T) *reportEnv {
	t.Helper()
	st := storetest.Open(t)
	t.Setenv("TEST_SECRET", "test-key")
	file := configfiletest.Load(t, `apps:
  test:
    accounts: [report-alpha]
    clientId: Iv1.test
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
  other:
    accounts: [report-beta]
    clientId: Iv1.other
    privateKey: { env: TEST_SECRET }
    webhookSecret: { env: TEST_SECRET }
`)
	if err := st.ApplyConfig(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "report-alpha/repo")
	account, _ := file.Account(configfile.ForgeGitHub, "report-alpha")
	srv := httptest.NewServer(&Server{Store: st, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(srv.Close)
	return &reportEnv{t: t, st: st, url: srv.URL, account: account.ID()}
}

// run is a new run and its report token.
func (e *reportEnv) run(kind store.RunnerKind) (string, string) {
	e.t.Helper()
	ctx := e.t.Context()
	var id string
	if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error {
		var err error
		id, err = store.InsertRunnerRun(ctx, tx, e.account, kind, "", 0)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	// The packages share a database, where a run left open counts as orphaned.
	e.t.Cleanup(func() {
		ctx := context.Background()
		if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error { return store.FailRunnerRun(ctx, tx, id, "test finished") }); err != nil {
			e.t.Error(err)
		}
	})
	token, err := e.st.MintRunnerToken(ctx, e.account, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, token
}

func (e *reportEnv) send(method, path, token string, body []byte) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.url+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	if _, err := out.ReadFrom(resp.Body); err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, out.Bytes()
}

func (e *reportEnv) post(path, token string, v any) int {
	e.t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	status, _ := e.send(http.MethodPost, path, token, body)
	return status
}

func (e *reportEnv) phase(id string) string {
	e.t.Helper()
	ctx := e.t.Context()
	var phase string
	if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT phase FROM runner_runs WHERE id = $1`, id).Scan(&phase)
	}); err != nil {
		e.t.Fatal(err)
	}
	return phase
}

func (e *reportEnv) count(table, id string) int {
	e.t.Helper()
	ctx := e.t.Context()
	var n int
	if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` WHERE runner_run_id = $1`, id).Scan(&n)
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// everyReport is a valid report to each path that takes one.
func everyReport(t *testing.T) map[string][]byte {
	t.Helper()
	bodies := map[string]any{
		runspec.ReportPhasePath:   runspec.PhaseReport{Phase: "failed"},
		runspec.ReportFailurePath: runspec.FailureReport{Cause: "forged"},
		runspec.ReportContextPath: runspec.ContextReport{Pack: contextPack(), Phase: "done"},
		runspec.ReportAgentPath:   runspec.AgentReport{Record: agentRecord(), Phase: "done"},
		runspec.ReportCheckpointsPath: runspec.CheckpointReport{
			Input: "inputs", Part: 1, Payload: json.RawMessage(`{"forged":true}`),
		},
		runspec.ReportPartsPath: runspec.PartsReport{Remaining: 9},
	}
	out := map[string][]byte{runspec.ReportHeartbeatPath: nil, runspec.ReportIndexPath: []byte("{\"pack\":{\"mode\":\"full\"},\"chunks\":0}\n")}
	for path, v := range bodies {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out[path] = b
	}
	return out
}

func contextPack() runspec.ContextPack {
	return runspec.ContextPack{
		Head: "head", Base: "base", PatchID: "patch", Diff: "diff", ChangedPaths: []string{"a.go"}, Stages: []byte(`[]`), Files: []byte(`{}`),
		Notes: []string{}, DeltaPaths: []string{}, Scope: "full", RuleIDs: []string{},
	}
}

func agentRecord() runspec.AgentRecord {
	return runspec.AgentRecord{
		Stop: "error", ToolCalls: []byte(`{}`), Timeline: []byte(`[]`), Sources: []byte(`[]`),
		SkillsOffered: []string{}, SkillsOpened: []string{}, CommandsOffered: []string{}, CommandsRun: []string{}, CommandErrors: []string{},
	}
}

// TestReportRefusesAnythingButItsToken is the boundary this API rests on. A
// command that a run starts holds the run's gateway token as its proxy
// credential. If that token could report, the command could forge what the run
// found, and a pull request that steers the agent could approve itself.
func TestReportRefusesAnythingButItsToken(t *testing.T) {
	e := newReportEnv(t)
	ctx := t.Context()
	id, token := e.run(store.RunnerKindReview)
	other, otherToken := e.run(store.RunnerKindReview)
	gatewayToken, err := e.st.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: id, AccountID: e.account, RepositoryID: configfile.RepositoryID(e.account, "report-alpha/repo"),
	}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.st.RevokeGatewayTokens(context.Background(), id); err != nil {
			t.Error(err)
		}
	})
	for _, tt := range []struct{ name, token string }{
		{"no token", ""},
		{"the run's gateway token, which its commands hold", gatewayToken},
		{"a made up token", "krr_" + e.account + "." + id + ".made-up"},
		{"another run's id on this token's secret", strings.Replace(token, id, other, 1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for path, body := range everyReport(t) {
				if status, _ := e.send(http.MethodPost, path, tt.token, body); status != http.StatusUnauthorized {
					t.Errorf("POST %s = %d, want 401", path, status)
				}
			}
			for _, path := range []string{runspec.ReportAuthorizePath, runspec.ReportCheckpointsPath + "?input=inputs"} {
				if status, _ := e.send(http.MethodGet, path, tt.token, nil); status != http.StatusUnauthorized {
					t.Errorf("GET %s = %d, want 401", path, status)
				}
			}
			if e.phase(id) != "created" || e.count("context_packs", id) != 0 || e.count("agent_runs", id) != 0 {
				t.Fatalf("a refused report wrote: phase = %s", e.phase(id))
			}
		})
	}
	t.Run("a report token admits no model call", func(t *testing.T) {
		if status, _ := e.send(http.MethodPost, "/v1/chat/completions", token, []byte(`{}`)); status != http.StatusUnauthorized {
			t.Fatalf("chat = %d, want 401", status)
		}
	})
	t.Run("a token writes its own run alone", func(t *testing.T) {
		if status := e.post(runspec.ReportPhasePath, otherToken, runspec.PhaseReport{Phase: "fetching"}); status != http.StatusNoContent {
			t.Fatalf("phase = %d", status)
		}
		if e.phase(other) != "fetching" || e.phase(id) != "created" {
			t.Fatalf("phases = %s, %s", e.phase(other), e.phase(id))
		}
	})
	t.Run("a report cannot say more than its fields", func(t *testing.T) {
		for _, body := range []string{
			`{"phase":"done","account_id":"` + e.account + `"}`,
			`{"phase":"done","run_id":"` + id + `"}`,
			`{"phase":"done","finished_at":"2026-01-01T00:00:00Z"}`,
		} {
			if status, _ := e.send(http.MethodPost, runspec.ReportPhasePath, otherToken, []byte(body)); status != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", body, status)
			}
		}
	})
	t.Run("nothing grants a run slots", func(t *testing.T) {
		status, answer := e.send(http.MethodPost, runspec.ReportPartsPath, otherToken, []byte(`{"remaining":4,"slots":99}`))
		if status != http.StatusBadRequest {
			t.Fatalf("parts = %d: %s", status, answer)
		}
	})
	t.Run("a token stops working when its run ends", func(t *testing.T) {
		ended, token := e.run(store.RunnerKindReview)
		if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error { return store.FailRunnerRun(ctx, tx, ended, "over") }); err != nil {
			t.Fatal(err)
		}
		if status := e.post(runspec.ReportPhasePath, token, runspec.PhaseReport{Phase: "done"}); status != http.StatusUnauthorized {
			t.Fatalf("phase = %d, want 401", status)
		}
	})
	t.Run("an unknown report is not found", func(t *testing.T) {
		if status, _ := e.send(http.MethodGet, runspec.ReportPrefix+"nope", otherToken, nil); status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		if status, _ := e.send(http.MethodGet, runspec.ReportPrefix+"nope", "", nil); status != http.StatusUnauthorized {
			t.Fatalf("without a token = %d, want 401", status)
		}
	})
}

func TestReportRoundTrips(t *testing.T) {
	e := newReportEnv(t)
	ctx := t.Context()
	t.Run("authorize and heartbeat", func(t *testing.T) {
		id, token := e.run(store.RunnerKindReview)
		if status, _ := e.send(http.MethodGet, runspec.ReportAuthorizePath, token, nil); status != http.StatusNoContent {
			t.Fatalf("authorize = %d", status)
		}
		if status, _ := e.send(http.MethodPost, runspec.ReportHeartbeatPath, token, nil); status != http.StatusNoContent {
			t.Fatalf("heartbeat = %d", status)
		}
		if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error {
			_, started, err := store.RunnerHeartbeatAge(ctx, tx, id)
			if err == nil && !started {
				t.Error("no heartbeat stamped")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a failure", func(t *testing.T) {
		id, token := e.run(store.RunnerKindReview)
		if status := e.post(runspec.ReportFailurePath, token, runspec.FailureReport{Cause: "boom"}); status != http.StatusNoContent {
			t.Fatalf("failure = %d", status)
		}
		if e.phase(id) != "failed" {
			t.Fatalf("phase = %s", e.phase(id))
		}
	})
	t.Run("a context sent twice lands once", func(t *testing.T) {
		id, token := e.run(store.RunnerKindReview)
		for range 2 {
			if status := e.post(runspec.ReportContextPath, token, runspec.ContextReport{Pack: contextPack(), Phase: "reviewing"}); status != http.StatusNoContent {
				t.Fatalf("context = %d", status)
			}
		}
		if e.count("context_packs", id) != 1 || e.phase(id) != "reviewing" {
			t.Fatalf("packs = %d, phase = %s", e.count("context_packs", id), e.phase(id))
		}
	})
	t.Run("a phase the database refuses is final", func(t *testing.T) {
		id, token := e.run(store.RunnerKindReview)
		if status := e.post(runspec.ReportContextPath, token, runspec.ContextReport{Pack: contextPack(), Phase: "nonsense"}); status != http.StatusBadRequest {
			t.Fatalf("context = %d, want 400", status)
		}
		if e.count("context_packs", id) != 0 {
			t.Fatal("the pack outlived the phase that it commits with")
		}
	})
	t.Run("an agent's result and conversation", func(t *testing.T) {
		id, token := e.run(store.RunnerKindReview)
		const conversation = `{ "system": "<&>" }`
		rec := agentRecord()
		rec.Conversation, rec.Session, rec.GrantedModel, rec.Tokens = []byte(conversation), id, "test/model", 10
		for range 2 {
			if status := e.post(runspec.ReportAgentPath, token, runspec.AgentReport{Record: rec, Phase: "done"}); status != http.StatusNoContent {
				t.Fatalf("agent = %d", status)
			}
		}
		var noResult bool
		var kept string
		if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT result IS NULL FROM agent_runs WHERE runner_run_id = $1`, id).Scan(&noResult); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT conversation FROM agent_conversations WHERE runner_run_id = $1`, id).Scan(&kept)
		}); err != nil {
			t.Fatal(err)
		}
		if !noResult || kept != conversation || e.phase(id) != "done" {
			t.Fatalf("no result = %v, kept = %q, phase = %s", noResult, kept, e.phase(id))
		}
	})
}

func TestReportPartCheckpointsAndSlots(t *testing.T) {
	e := newReportEnv(t)
	ctx := t.Context()
	id, token := e.run(store.RunnerKindReview)
	if err := e.st.WithAccount(ctx, e.account, func(tx pgx.Tx) error { return store.SetPartSlots(ctx, tx, id, 3) }); err != nil {
		t.Fatal(err)
	}
	if status := e.post(runspec.ReportCheckpointsPath, token, runspec.CheckpointReport{
		Input: "inputs", Part: 1, Payload: json.RawMessage(`{"a":1}`),
	}); status != http.StatusNoContent {
		t.Fatalf("save = %d", status)
	}
	status, answer := e.send(http.MethodGet, runspec.ReportCheckpointsPath+"?input=inputs", token, nil)
	var saved []runspec.PartCheckpoint
	if err := json.Unmarshal(answer, &saved); err != nil || status != http.StatusOK || len(saved) != 1 || saved[0].SourceRun != id {
		t.Fatalf("read = %d %s, %v", status, answer, err)
	}
	// One past the bound only costs a retry that part's run again.
	if status := e.post(runspec.ReportCheckpointsPath, token, runspec.CheckpointReport{
		Input: "inputs", Part: 2, Payload: json.RawMessage(strconv.Quote(strings.Repeat("x", maxCheckpointBody))),
	}); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized checkpoint = %d, want 413", status)
	}
	if status, _ := e.send(http.MethodPost, runspec.ReportCheckpointsPath, token, []byte(`{"input":"missing","part":1}`)); status != http.StatusBadRequest {
		t.Fatalf("a checkpoint with no payload = %d, want 400", status)
	}
	if status, answer := e.send(http.MethodGet, runspec.ReportCheckpointsPath+"?input=missing", token, nil); status != http.StatusOK || string(answer) != "null" {
		t.Fatalf("read after a refused save = %d %s", status, answer)
	}
	// A number that is short to send reads back as short as it was sent.
	const huge = `[1e65535,1e65535]`
	if status := e.post(runspec.ReportCheckpointsPath, token, runspec.CheckpointReport{
		Input: "numbers", Part: 1, Payload: json.RawMessage(huge),
	}); status != http.StatusNoContent {
		t.Fatalf("save = %d", status)
	}
	status, answer = e.send(http.MethodGet, runspec.ReportCheckpointsPath+"?input=numbers", token, nil)
	var numbers []runspec.PartCheckpoint
	if err := json.Unmarshal(answer, &numbers); err != nil || status != http.StatusOK || len(numbers) != 1 || string(numbers[0].Payload) != huge {
		t.Fatalf("read = %d %.200s, %v", status, answer, err)
	}
	status, answer = e.send(http.MethodPost, runspec.ReportPartsPath, token, []byte(`{"remaining":2}`))
	var slots runspec.PartSlots
	if err := json.Unmarshal(answer, &slots); err != nil || status != http.StatusOK || slots.Slots != 3 {
		t.Fatalf("parts = %d %s, %v", status, answer, err)
	}
}

func indexBody(t *testing.T, claimed, chunks int) []byte {
	t.Helper()
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	if err := enc.Encode(runspec.IndexReport{Pack: runspec.IndexPack{Mode: runspec.IndexModeFull, ChangedPaths: []string{}}, Chunks: claimed}); err != nil {
		t.Fatal(err)
	}
	for i := range chunks {
		if err := enc.Encode(runspec.IndexChunk{Path: strconv.Itoa(i) + ".go", StartLine: 1, EndLine: 1, Text: "content"}); err != nil {
			t.Fatal(err)
		}
	}
	return body.Bytes()
}

func TestReportIndexUpload(t *testing.T) {
	e := newReportEnv(t)
	upload := func(token string, body []byte) int {
		t.Helper()
		status, _ := e.send(http.MethodPost, runspec.ReportIndexPath, token, body)
		return status
	}
	t.Run("more chunks than one batch, sent twice", func(t *testing.T) {
		id, token := e.run(store.RunnerKindIndex)
		chunks := indexBatchChunks + 1
		body := indexBody(t, chunks, chunks)
		for range 2 {
			if status := upload(token, body); status != http.StatusNoContent {
				t.Fatalf("upload = %d", status)
			}
		}
		if e.count("index_staging", id) != chunks || e.count("index_packs", id) != 1 || e.phase(id) != "done" {
			t.Fatalf("staged = %d, packs = %d, phase = %s", e.count("index_staging", id), e.count("index_packs", id), e.phase(id))
		}
	})
	t.Run("slower than the listener's read timeout", func(t *testing.T) {
		const readTimeout = 100 * time.Millisecond
		srv := httptest.NewUnstartedServer(&Server{Store: e.st, Logger: slog.New(slog.DiscardHandler)})
		srv.Config.ReadTimeout = readTimeout
		srv.Start()
		defer srv.Close()
		id, token := e.run(store.RunnerKindIndex)
		pr, pw := io.Pipe()
		go func() {
			_, _ = pw.Write(indexBody(t, 2, 1))
			time.Sleep(3 * readTimeout)
			_, _ = pw.Write(indexBody(t, 1, 1)[len(indexBody(t, 1, 0)):])
			_ = pw.Close()
		}()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+runspec.ReportIndexPath, pr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNoContent || e.count("index_staging", id) != 2 {
			t.Fatalf("upload = %d, staged = %d", resp.StatusCode, e.count("index_staging", id))
		}
	})
	t.Run("no chunks", func(t *testing.T) {
		id, token := e.run(store.RunnerKindIndex)
		if status := upload(token, indexBody(t, 0, 0)); status != http.StatusNoContent {
			t.Fatalf("upload = %d", status)
		}
		if e.count("index_packs", id) != 1 || e.phase(id) != "done" {
			t.Fatalf("packs = %d, phase = %s", e.count("index_packs", id), e.phase(id))
		}
	})
	for _, tt := range []struct {
		name string
		body func(*testing.T) []byte
		want int
	}{
		{"fewer chunks than it said", func(t *testing.T) []byte { return indexBody(t, 3, 2) }, http.StatusBadRequest},
		{"more chunks than it said", func(t *testing.T) []byte { return indexBody(t, 1, 2) }, http.StatusBadRequest},
		{"a line that is not JSON", func(t *testing.T) []byte { return append(indexBody(t, 2, 1), "nope\n"...) }, http.StatusBadRequest},
		{"a chunk with a field more", func(t *testing.T) []byte { return append(indexBody(t, 1, 0), `{"path":"a","account":"b"}`+"\n"...) }, http.StatusBadRequest},
		{"no opening line", func(*testing.T) []byte { return nil }, http.StatusRequestTimeout},
		{"a line past the limit", func(t *testing.T) []byte {
			return append(indexBody(t, 1, 0), fmt.Sprintf(`{"text":%q}`+"\n", strings.Repeat("x", maxIndexLine))...)
		}, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, token := e.run(store.RunnerKindIndex)
			if status := upload(token, tt.body(t)); status != tt.want {
				t.Fatalf("upload = %d, want %d", status, tt.want)
			}
			if e.count("index_packs", id) != 0 || e.phase(id) != "created" {
				t.Fatalf("a refused upload sealed: packs = %d, phase = %s", e.count("index_packs", id), e.phase(id))
			}
			// The upload after it starts clean.
			if status := upload(token, indexBody(t, 2, 2)); status != http.StatusNoContent {
				t.Fatalf("second upload = %d", status)
			}
			if e.count("index_staging", id) != 2 {
				t.Fatalf("staged = %d, want 2", e.count("index_staging", id))
			}
		})
	}
}
