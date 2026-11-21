package gateway

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/home-operations/kritika/internal/model"
)

func TestProxyToken(t *testing.T) {
	basic := func(userinfo string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(userinfo)) }
	for _, tt := range []struct{ name, header, token string }{
		{"the proxy URL's password", basic(":krk_token"), "krk_token"},
		{"a user named too", basic("run:krk_token"), "krk_token"},
		{"the scheme in any case", "basic " + strings.TrimPrefix(basic(":krk_token"), "Basic "), "krk_token"},
		{"a token as the user", basic("krk_token"), ""},
		{"a bearer token", "Bearer krk_token", ""},
		{"not base64", "Basic krk_token!", ""},
		{"no header", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if token := proxyToken(tt.header); token != tt.token {
				t.Fatalf("proxyToken(%q) = %q; want %q", tt.header, token, tt.token)
			}
		})
	}
}

func TestUpstreamStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"an overloaded provider", &openai.Error{StatusCode: http.StatusServiceUnavailable}, http.StatusBadGateway},
		{"a rate limit", &openai.Error{StatusCode: http.StatusTooManyRequests}, http.StatusBadGateway},
		{"a temporary budget refusal", &openai.Error{
			StatusCode: http.StatusPaymentRequired, Response: &http.Response{Header: http.Header{"Retry-After": {"120"}}},
		}, http.StatusBadGateway},
		{"an empty balance", &openai.Error{StatusCode: http.StatusPaymentRequired, Response: &http.Response{Header: http.Header{}}}, http.StatusUnprocessableEntity},
		{"a timeout", context.DeadlineExceeded, http.StatusBadGateway},
		{"a refused request", &openai.Error{StatusCode: http.StatusBadRequest}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := upstreamStatus(tt.err)
			// The runner's loop classifies the gateway's answer the same way.
			if status != tt.want || model.Transient(&openai.Error{StatusCode: status}) != model.Transient(tt.err) {
				t.Fatalf("status = %d, want %d, as transient as the provider's error", status, tt.want)
			}
		})
	}
}

func TestRefuseRetries(t *testing.T) {
	for status, retry := range map[int]bool{
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          false,
		http.StatusTooManyRequests:     false,
		http.StatusUnauthorized:        false,
		http.StatusBadRequest:          false,
	} {
		rec := httptest.NewRecorder()
		refuse(rec, status, "code", "message")
		if got := rec.Header().Get("X-Should-Retry") != "false"; got != retry || rec.Code != status {
			t.Fatalf("%d: retry = %v, want %v", status, got, retry)
		}
	}
}

func TestStepPart(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   int
		err    bool
	}{
		{header: "", want: 0},
		{header: "1", want: 1},
		{header: "12", want: 12},
		{header: "0", err: true},
		{header: "-2", err: true},
		{header: "two", err: true},
		{header: "99999999999", err: true},
	} {
		got, err := stepPart(tt.header)
		if got != tt.want || (err != nil) != tt.err {
			t.Fatalf("stepPart(%q) = %d, %v; want %d, error %v", tt.header, got, err, tt.want, tt.err)
		}
	}
}

// TestStepBudget: a step gets its call's budget, or what fits within the
// wait its runner names when that is less. A wait the gateway cannot settle
// a step within, or none, leaves the budget as it is.
func TestStepBudget(t *testing.T) {
	const own = 32 * time.Minute
	for _, tt := range []struct {
		header string
		want   time.Duration
		err    bool
	}{
		{header: "", want: own},
		{header: "2220", want: own},
		{header: "1020", want: 12 * time.Minute},
		{header: "300", want: own},
		{header: "0", err: true},
		{header: "-60", err: true},
		{header: "soon", err: true},
		{header: "99999999999", err: true},
	} {
		got, err := stepBudget(own, tt.header)
		if got != tt.want || (err != nil) != tt.err {
			t.Fatalf("stepBudget(%q) = %s, %v; want %s, error %v", tt.header, got, err, tt.want, tt.err)
		}
	}
}

func TestPromptEstimate(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"` + "0123456789abcdef0123456789abcdef" + `"}`
	const call = `{"type":"function_call","id":"fc_1","call_id":"c1","name":"read_file","namespace":"kritika","arguments":"{}"}`
	plain := `{"model":"kritika","messages":[{"role":"user","content":"review"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}]}`
	replayed := `{"model":"kritika","messages":[{"role":"user","content":"review"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}}],` +
		`"kritika_chatgpt_output":{"provider":"plan","items":[` + reasoning + `,` + call + `]}}]}`
	for _, tt := range []struct {
		name string
		body string
		want int
	}{
		{"a request without replayed output", plain, len(plain) / 4},
		{"replayed output left out", replayed, (len(replayed) - len(reasoning) - len(call)) / 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := model.DecodeChatRequest([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := promptEstimate([]byte(tt.body), req); got != int64(tt.want) {
				t.Fatalf("promptEstimate = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDecodeReport: a report with a field this build does not know is
// refused with encoding/json's "json: unknown field" in the answer. The
// runner sends such a report again, since during a rollout an older gateway
// replica refuses a newer runner's field and a newer replica takes it, so
// the wording is what the two builds agree on. A report that is not JSON is
// refused without it, and is not worth sending again; one cut short is
// answered 408, which the runner sends again too.
func TestDecodeReport(t *testing.T) {
	type report struct {
		Phase string `json:"phase"`
	}
	for _, tc := range []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		wantRetry  bool
	}{
		{name: "known fields", body: `{"phase":"fetching"}`, wantOK: true, wantStatus: http.StatusOK},
		{name: "a field of a newer build", body: `{"phase":"fetching","signature":"x"}`, wantStatus: http.StatusBadRequest, wantRetry: true},
		{name: "not JSON", body: `phase=fetching`, wantStatus: http.StatusBadRequest},
		{name: "cut short", body: `{"phase":`, wantStatus: http.StatusRequestTimeout},
		{name: "the wrong type", body: `{"phase":1}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/run/phase", strings.NewReader(tc.body))
			got, ok := decodeReport[report](w, r, 1<<10)
			if ok != tc.wantOK || w.Code != tc.wantStatus {
				t.Fatalf("decodeReport = %+v, %v with status %d, want %v with %d", got, ok, w.Code, tc.wantOK, tc.wantStatus)
			}
			if retry := strings.Contains(w.Body.String(), "json: unknown field "); retry != tc.wantRetry {
				t.Fatalf("answer %s names an unknown field = %v, want %v", w.Body.String(), retry, tc.wantRetry)
			}
		})
	}
}
