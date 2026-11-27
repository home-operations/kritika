package runner

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/tuning"
)

// fastRetries keeps the test's waits short, and its window as asked.
func fastRetries(t *testing.T, window time.Duration) {
	t.Helper()
	tuning.Override(t, func(v *tuning.Values) {
		v.Runner.ReportBackoff, v.Runner.ReportMaxBackoff, v.RunSpec.ReportRetryWindow = time.Millisecond, 5*time.Millisecond, window
	})
}

func TestReportRetries(t *testing.T) {
	// What a gateway of a build before a field that the report carries
	// answers.
	unknownField := `{"error":{"message":"decoding the report: json: unknown field \"signature\"","type":"invalid_request"}}`
	for _, tt := range []struct {
		name  string
		first int
		body  string
		sends int
		ok    bool
	}{
		{"the gateway's trouble with its database", http.StatusInternalServerError, "", 2, true},
		{"a replica going away", http.StatusBadGateway, "", 2, true},
		{"a replica older than the run's image", http.StatusNotFound, "", 2, true},
		{"a replica older than a field of the report", http.StatusBadRequest, unknownField, 2, true},
		{"another upload overtaking it", http.StatusConflict, "", 2, true},
		{"a body cut short", http.StatusRequestTimeout, "", 2, true},
		{"a token that is not valid", http.StatusUnauthorized, "", 1, false},
		{"a report the database refuses", http.StatusBadRequest, "", 1, false},
		{"a report too large", http.StatusRequestEntityTooLarge, "", 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fastRetries(t, time.Minute)
			var sends atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if sends.Add(1) == 1 {
					http.Error(w, cmp.Or(tt.body, "refused"), tt.first)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			err := (&reporter{gatewayURL: srv.URL, token: "krr_token"}).phase(t.Context(), "fetching")
			if (err == nil) != tt.ok || int(sends.Load()) != tt.sends {
				t.Fatalf("err = %v after %d sends; want ok = %v after %d", err, sends.Load(), tt.ok, tt.sends)
			}
			if err != nil && strings.Contains(err.Error(), "krr_token") {
				t.Fatalf("the error quotes the token: %v", err)
			}
		})
	}
}

func TestReportRetriesAGatewayOutOfReach(t *testing.T) {
	fastRetries(t, time.Minute)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	srv := &http.Server{Addr: addr, ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	defer func() { _ = srv.Close() }()
	go func() {
		time.Sleep(20 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = srv.Serve(ln)
		}
	}()
	if err := (&reporter{gatewayURL: "http://" + addr}).phase(t.Context(), "fetching"); err != nil {
		t.Fatalf("phase = %v", err)
	}
}

func TestReportGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Run("when the window passes", func(t *testing.T) {
		fastRetries(t, 20*time.Millisecond)
		if err := (&reporter{gatewayURL: srv.URL}).phase(t.Context(), "fetching"); err == nil || !strings.Contains(err.Error(), "503") {
			t.Fatalf("phase = %v, want the gateway's answer", err)
		}
	})
	t.Run("when its context ends", func(t *testing.T) {
		fastRetries(t, time.Minute)
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		if err := (&reporter{gatewayURL: srv.URL}).phase(ctx, "fetching"); err == nil {
			t.Fatal("phase = nil, want a failure")
		}
	})
}

// TestReportWindowBoundsRetriesAlone: an index upload runs for far longer than
// the retries are given, and a failure at the end of one is still worth a retry.
func TestReportWindowBoundsRetriesAlone(t *testing.T) {
	const window, long = 10 * time.Millisecond, 50 * time.Millisecond
	t.Run("an attempt longer than the window", func(t *testing.T) {
		fastRetries(t, window)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(long)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		if err := (&reporter{gatewayURL: srv.URL}).phase(t.Context(), "fetching"); err != nil {
			t.Fatalf("phase = %v", err)
		}
	})
	t.Run("a retry after one", func(t *testing.T) {
		fastRetries(t, window)
		var sends atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if sends.Add(1) == 1 {
				time.Sleep(long)
				http.Error(w, "going away", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		if err := (&reporter{gatewayURL: srv.URL}).phase(t.Context(), "fetching"); err != nil || sends.Load() != 2 {
			t.Fatalf("phase = %v after %d sends", err, sends.Load())
		}
	})
}

// TestReportWindowGivesUpOnSlowFailures: a gateway that answers every
// attempt with a retryable failure, each after longer than the window, is
// given a second window once and then given up on, well before the
// report's context ends.
func TestReportWindowGivesUpOnSlowFailures(t *testing.T) {
	const window, long = 10 * time.Millisecond, 30 * time.Millisecond
	fastRetries(t, window)
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		time.Sleep(long)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := (&reporter{gatewayURL: srv.URL}).phase(ctx, "fetching"); err == nil || ctx.Err() != nil || sends.Load() != 2 {
		t.Fatalf("phase = %v after %d sends (context %v), want a failure after 2", err, sends.Load(), ctx.Err())
	}
}

// TestReportWindowHoldsTheBackoff: a retry whose backoff would end past the
// window is not waited for, nor sent.
func TestReportWindowHoldsTheBackoff(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) {
		v.Runner.ReportBackoff, v.Runner.ReportMaxBackoff, v.RunSpec.ReportRetryWindow = time.Minute, time.Minute, 20*time.Millisecond
	})
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	started := time.Now()
	err := (&reporter{gatewayURL: srv.URL}).phase(t.Context(), "fetching")
	if err == nil || sends.Load() != 1 || time.Since(started) > 10*time.Second {
		t.Fatalf("phase = %v after %d sends and %s, want a failure after 1 at once", err, sends.Load(), time.Since(started))
	}
}

func TestReportHeartbeatTriesOnce(t *testing.T) {
	fastRetries(t, time.Minute)
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := (&reporter{gatewayURL: srv.URL}).heartbeat(t.Context()); err == nil || sends.Load() != 1 {
		t.Fatalf("heartbeat = %v after %d sends, want a failure after one", err, sends.Load())
	}
}

// TestReportIndexSendsAgainWhole: a failed upload is sent from its first line.
func TestReportIndexSendsAgainWhole(t *testing.T) {
	fastRetries(t, time.Minute)
	chunks := make([]runspec.IndexChunk, 3)
	for i := range chunks {
		chunks[i] = runspec.IndexChunk{Path: strconv.Itoa(i) + ".go", Text: "<&>\n", StartLine: 1, EndLine: 2}
	}
	var uploads atomic.Int32
	var opened runspec.IndexReport
	var got []runspec.IndexChunk
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != runspec.ReportIndexPath || r.Header.Get("Authorization") != "Bearer krr_token" {
			t.Errorf("request = %s %s with %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if uploads.Add(1) == 1 {
			// Read one line and fail, as a replica going away does.
			_, _ = bufio.NewReader(r.Body).ReadString('\n')
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		lines := bufio.NewScanner(r.Body)
		lines.Scan()
		if err := json.Unmarshal(lines.Bytes(), &opened); err != nil {
			t.Error(err)
		}
		for lines.Scan() {
			var c runspec.IndexChunk
			if err := json.Unmarshal(lines.Bytes(), &c); err != nil {
				t.Error(err)
			}
			got = append(got, c)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	pack := runspec.IndexPack{Mode: runspec.IndexModeFull, ChangedPaths: []string{"a.go"}}
	if err := (&reporter{gatewayURL: srv.URL, token: "krr_token"}).index(t.Context(), pack, chunks); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 2 || opened.Chunks != 3 || opened.Pack.Mode != runspec.IndexModeFull || len(got) != 3 || got[2].Text != "<&>\n" {
		t.Fatalf("uploads = %d, opened = %+v, chunks = %+v", uploads.Load(), opened, got)
	}
}

// TestReportAgentEncoding: a run with no result reports none, rather than a
// JSON null the database would keep as one, and a conversation arrives byte for
// byte, as a provider's cache needs it.
func TestReportAgentEncoding(t *testing.T) {
	const conversation = `{ "system": "<&>" }`
	var got runspec.AgentReport
	var raw struct {
		Record map[string]json.RawMessage `json:"record"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			err = json.Unmarshal(body, &got)
		}
		if err == nil {
			err = json.Unmarshal(body, &raw)
		}
		if err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	rec := runspec.AgentRecord{Stop: "error", Conversation: []byte(conversation), SkillsOffered: []string{}}
	if err := (&reporter{gatewayURL: srv.URL}).agent(t.Context(), rec, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, present := raw.Record["result"]; present || got.Record.Result != nil {
		t.Fatalf("result = %s, want none", got.Record.Result)
	}
	if string(got.Record.Conversation) != conversation || got.Phase != "failed" {
		t.Fatalf("conversation = %q, phase = %q", got.Record.Conversation, got.Phase)
	}
	if got.Record.SkillsOffered == nil || got.Record.SkillsOpened != nil {
		t.Fatalf("an empty list and a nil one must stay what they are: %#v, %#v", got.Record.SkillsOffered, got.Record.SkillsOpened)
	}
}

// TestFitPackDiffsToTheEncodedReport: a diff within runner.maxPackDiffBytes
// whose JSON escaping would take its context report past
// gateway.maxReportBody loses whole files until the report fits, and its
// notes name them.
func TestFitPackDiffsToTheEncodedReport(t *testing.T) {
	section := func(name string) string {
		return "diff --git a/" + name + " b/" + name + "\n--- a/" + name + "\n+++ b/" + name + "\n@@ -1 +1 @@\n+" +
			strings.Repeat("<", 1000) + "\n"
	}
	diff := section("a.go") + section("b.go")
	base := runspec.ContextPack{Head: "head", Stages: json.RawMessage("[]"), Files: json.RawMessage("[]"), Notes: []string{}}
	fitted := base
	fitted.Diff, fitted.Notes = section("a.go"), []string{fmt.Sprintf(noteDiffNotKept, 1, namePaths([]string{"b.go"}))}
	want, err := json.Marshal(runspec.ContextReport{Pack: fitted, Phase: "reviewing"})
	if err != nil {
		t.Fatal(err)
	}
	tuning.Override(t, func(v *tuning.Values) {
		v.Runner.MaxPackDiffBytes, v.Gateway.MaxReportBody = 1<<20, tuning.ByteSize(len(want))
	})
	pack, cut := fitPackDiffs(base, "reviewing", diff, "")
	got, err := json.Marshal(runspec.ContextReport{Pack: pack, Phase: "reviewing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > len(want) || pack.Diff != section("a.go") || len(cut) != 1 || cut[0] != "b.go" ||
		len(pack.Notes) != 1 || !strings.Contains(pack.Notes[0], "b.go") {
		t.Fatalf("report of %d bytes (limit %d), diff %q, cut %q, notes %q", len(got), len(want), pack.Diff, cut, pack.Notes)
	}
	tuning.Override(t, func(v *tuning.Values) { v.Gateway.MaxReportBody = 1 << 20 })
	if pack, cut := fitPackDiffs(base, "reviewing", diff, ""); pack.Diff != diff || len(cut) != 0 || len(pack.Notes) != 0 {
		t.Fatalf("under the limit, diff %q, cut %q, notes %q; want it whole", pack.Diff, cut, pack.Notes)
	}
}

// TestReportIndexRefusesWhatTheGatewayWould: an upload with a line over
// gateway.maxIndexLine, or over gateway.maxIndexBody in all, as it encodes
// with its control bytes escaped, is not sent, and its error names the
// limit.
func TestReportIndexRefusesWhatTheGatewayWould(t *testing.T) {
	chunks := []runspec.IndexChunk{{Path: "a.go", Text: strings.Repeat("\x01", 100), StartLine: 1, EndLine: 1}}
	var want strings.Builder
	if err := encodeIndex(&want, runspec.IndexPack{ChangedPaths: []string{}}, chunks); err != nil {
		t.Fatal(err)
	}
	longest := 0
	for l := range strings.SplitAfterSeq(want.String(), "\n") {
		longest = max(longest, len(l))
	}
	for _, tt := range []struct {
		name       string
		line, body tuning.ByteSize
		says       string
	}{
		{"a line over its limit", tuning.ByteSize(longest - 1), 1 << 20, "tuning.gateway.maxIndexLine"},
		{"an upload over its limit", tuning.ByteSize(longest), tuning.ByteSize(want.Len() - 1), "tuning.gateway.maxIndexBody"},
		{"an upload at both limits", tuning.ByteSize(longest), tuning.ByteSize(want.Len()), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fastRetries(t, time.Minute)
			tuning.Override(t, func(v *tuning.Values) { v.Gateway.MaxIndexLine, v.Gateway.MaxIndexBody = tt.line, tt.body })
			var sends atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			err := (&reporter{gatewayURL: srv.URL}).index(t.Context(), runspec.IndexPack{ChangedPaths: []string{}}, chunks)
			if tt.says == "" {
				if err != nil || sends.Load() != 1 {
					t.Fatalf("index = %v after %d sends, want it sent", err, sends.Load())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.says) || sends.Load() != 0 {
				t.Fatalf("index = %v after %d sends, want it refused naming %q, unsent", err, sends.Load(), tt.says)
			}
		})
	}
}
