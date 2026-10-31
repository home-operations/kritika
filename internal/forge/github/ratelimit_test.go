package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v93/github"
)

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		want    time.Duration
		limited bool
	}{
		{name: "secondary limit", status: 403, headers: map[string]string{"Retry-After": "30"}, want: 30 * time.Second, limited: true},
		{name: "secondary limit as 429", status: 429, headers: map[string]string{"Retry-After": "5"}, want: 5 * time.Second, limited: true},
		{name: "a zero Retry-After still waits a second", status: 403, headers: map[string]string{"Retry-After": "0"}, want: time.Second, limited: true},
		{name: "primary limit", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(40*time.Second).Unix(), 10)},
			want: 41 * time.Second, limited: true},
		{name: "a reset in the past still waits a second", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)},
			want: time.Second, limited: true},
		{name: "a 403 with quota left is not a limit", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "12"}},
		{name: "a plain 403 is not a limit", status: 403},
		{name: "a 404 is not a limit", status: 404, headers: map[string]string{"Retry-After": "30"}},
		{name: "an unparsable Retry-After is not a limit", status: 403, headers: map[string]string{"Retry-After": "soon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.status, Header: http.Header{}}
			for k, v := range tt.headers {
				resp.Header.Set(k, v)
			}
			got, limited := rateLimitWait(resp, now)
			if got != tt.want || limited != tt.limited {
				t.Errorf("rateLimitWait() = %s, %v; want %s, %v", got, limited, tt.want, tt.limited)
			}
		})
	}
}

// limitedServer refuses the first n requests with status and headers, then
// answers 200 with the request body echoed. It records every body it saw.
type limitedServer struct {
	mu      sync.Mutex
	refuse  int
	status  int
	headers map[string]string
	bodies  []string
}

func (s *limitedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, string(b))
	if len(s.bodies) <= s.refuse {
		for k, v := range s.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit"}`)
		return
	}
	_, _ = w.Write(b)
}

func TestRateLimitTransport(t *testing.T) {
	tests := []struct {
		name       string
		server     *limitedServer
		resetAfter time.Duration
		body       string
		noGetBody  bool
		cancel     bool
		wantStatus int
		wantCalls  int
		wantWait   time.Duration
		wantWaited bool
		wantNoObs  bool
		wantErr    bool
	}{
		{
			name: "a secondary limit is waited out and the body sent again", body: `{"body":"lgtm"}`,
			server:     &limitedServer{refuse: 1, status: 403, headers: map[string]string{"Retry-After": "2"}},
			wantStatus: 200, wantCalls: 2, wantWait: 2 * time.Second, wantWaited: true,
		},
		{
			name:       "a primary limit resetting soon is waited out",
			server:     &limitedServer{refuse: 1, status: 429, headers: map[string]string{"X-RateLimit-Remaining": "0"}},
			resetAfter: 3 * time.Second,
			wantStatus: 200, wantCalls: 2, wantWait: 4 * time.Second, wantWaited: true,
		},
		{
			name:       "a second refusal is handed back",
			server:     &limitedServer{refuse: 2, status: 403, headers: map[string]string{"Retry-After": "1"}},
			wantStatus: 403, wantCalls: 2, wantWait: time.Second, wantWaited: true,
		},
		{
			name:       "a wait past the cap is not taken",
			server:     &limitedServer{refuse: 1, status: 403, headers: map[string]string{"Retry-After": "120"}},
			wantStatus: 403, wantCalls: 1, wantWait: 120 * time.Second,
		},
		{
			name: "a body that cannot be read again is not resent", body: "stream", noGetBody: true,
			server:     &limitedServer{refuse: 1, status: 403, headers: map[string]string{"Retry-After": "1"}},
			wantStatus: 403, wantCalls: 1, wantWait: time.Second,
		},
		{
			name:       "a response that is not a limit passes through",
			server:     &limitedServer{refuse: 1, status: 403},
			wantStatus: 403, wantCalls: 1, wantNoObs: true,
		},
		{
			name:   "a request cancelled during the wait fails",
			server: &limitedServer{refuse: 1, status: 403, headers: map[string]string{"Retry-After": "1"}},
			cancel: true, wantCalls: 1, wantWait: time.Second, wantWaited: false, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if tt.resetAfter > 0 {
					tt.server.headers["X-RateLimit-Reset"] = strconv.FormatInt(time.Now().Add(tt.resetAfter).Unix(), 10)
				}
				var observed []time.Duration
				var waited []bool
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				tr := &rateLimitTransport{
					// Network I/O is outside synctest's fake clock.
					base: authRoundTripper(func(req *http.Request) (*http.Response, error) {
						rec := httptest.NewRecorder()
						tt.server.ServeHTTP(rec, req)
						return rec.Result(), nil
					}),
					maxWait: rateLimitMaxWait,
					observe: func(wait time.Duration, w bool) {
						observed = append(observed, wait)
						waited = append(waited, w)
					},
					sleep: func(ctx context.Context, _ time.Duration) error {
						if tt.cancel {
							cancel()
						}
						return ctx.Err()
					},
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/test", strings.NewReader(tt.body))
				if err != nil {
					t.Fatal(err)
				}
				if tt.noGetBody {
					req.GetBody = nil
				}
				resp, err := tr.RoundTrip(req)
				if (err != nil) != tt.wantErr {
					t.Fatalf("RoundTrip() error = %v, wantErr %v", err, tt.wantErr)
				}
				if err == nil {
					defer func() { _ = resp.Body.Close() }()
					if resp.StatusCode != tt.wantStatus {
						t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
					}
					if got, _ := io.ReadAll(resp.Body); tt.wantStatus == 200 && string(got) != tt.body {
						t.Errorf("body = %q, want %q echoed from the resent request", got, tt.body)
					}
				}
				tt.server.mu.Lock()
				calls := len(tt.server.bodies)
				bodies := tt.server.bodies
				tt.server.mu.Unlock()
				if calls != tt.wantCalls {
					t.Errorf("requests = %d, want %d", calls, tt.wantCalls)
				}
				for _, b := range bodies {
					if b != tt.body {
						t.Errorf("the server saw body %q, want %q every time", b, tt.body)
					}
				}
				switch {
				case tt.wantNoObs && len(observed) != 0:
					t.Errorf("observed %v, want nothing", observed)
				case !tt.wantNoObs && (len(observed) == 0 || observed[0] != tt.wantWait || waited[0] != tt.wantWaited):
					t.Errorf("observed %v %v, want %s, waited=%v", observed, waited, tt.wantWait, tt.wantWaited)
				}
				if tt.wantErr && !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want context.Canceled", err)
				}
			})
		})
	}
}

// TestRetryAfter: go-github's rate limit errors, wrapped as the client wraps
// them, say how long a job waits. Any other error is no limit.
func TestRetryAfter(t *testing.T) {
	now := time.Now()
	twoMinutes := 2 * time.Minute
	for _, tt := range []struct {
		name    string
		err     error
		want    time.Duration
		limited bool
	}{
		{"primary", fmt.Errorf("github: pull request: %w", &gh.RateLimitError{Rate: gh.Rate{Reset: gh.Timestamp{Time: now.Add(10 * time.Minute)}}}),
			10*time.Minute + time.Second, true},
		{"primary already reset", &gh.RateLimitError{Rate: gh.Rate{Reset: gh.Timestamp{Time: now.Add(-time.Minute)}}}, time.Second, true},
		{"secondary", fmt.Errorf("github: comment: %w", &gh.AbuseRateLimitError{RetryAfter: &twoMinutes}), 2 * time.Minute, true},
		{"secondary with no wait", &gh.AbuseRateLimitError{}, rateLimitMaxWait, true},
		{"other", errors.New("github: boom"), 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, limited := RetryAfter(tt.err, now); got != tt.want || limited != tt.limited {
				t.Fatalf("RetryAfter = %s, %v; want %s, %v", got, limited, tt.want, tt.limited)
			}
		})
	}
}

// TestRetryAfterOfAClient: GitHub refuses a request for its primary limit,
// and go-github refuses the next one itself until the reset. Both fail with
// an error that RetryAfter reads.
func TestRetryAfterOfAClient(t *testing.T) {
	f, c := newFakeAPI(t)
	reset := time.Now().Add(10 * time.Minute)
	f.mux.HandleFunc("GET /api/v3/repos/o/r/pulls/7", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})
	for range 2 {
		_, err := c.PullRequest(t.Context(), "o", "r", 7)
		if wait, limited := RetryAfter(err, time.Now()); !limited || wait < 9*time.Minute {
			t.Fatalf("PullRequest = %v: RetryAfter = %s, %v; want about ten minutes", err, wait, limited)
		}
	}
	if len(f.requests) != 1 {
		t.Fatalf("requests = %v, want one: go-github refuses the second itself", f.requests)
	}
}
