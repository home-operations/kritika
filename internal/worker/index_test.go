package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/forge/forgejo"
	"github.com/home-operations/kritika/internal/forge/gitlab"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

func TestIndexIgnoreHash(t *testing.T) {
	tests := []struct {
		name        string
		left, right []string
		equal       bool
	}{
		{"empty", nil, []string{}, true},
		{"order", []string{"vendor/**", "docs/**"}, []string{"docs/**", "vendor/**"}, true},
		{"duplicates", []string{"vendor/**", "vendor/**"}, []string{"vendor/**"}, true},
		{"added", nil, []string{"vendor/**"}, false},
		{"removed", []string{"vendor/**"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := slices.Clone(tt.left)
			left, right := indexIgnoreHash(tt.left), indexIgnoreHash(tt.right)
			if left == "" || (left == right) != tt.equal {
				t.Fatalf("hashes %q / %q, want equal %v", left, right, tt.equal)
			}
			if !slices.Equal(original, tt.left) {
				t.Fatal("mutated input rules")
			}
		})
	}
}

func TestIndexResumeIdentity(t *testing.T) {
	emb := &configfile.Embedding{BaseURL: "https://embed.example", Model: "model", Dims: 8}
	key := indexResumeKey("head", store.IndexModeIncremental, "base", "base-run", emb, "ignore")
	for _, tc := range []struct {
		name string
		key  string
		same bool
	}{
		{"same", indexResumeKey("head", store.IndexModeIncremental, "base", "base-run", emb, "ignore"), true},
		{"new base commit", indexResumeKey("head", store.IndexModeIncremental, "other", "base-run", emb, "ignore"), false},
		{"replacement base", indexResumeKey("head", store.IndexModeIncremental, "base", "other", emb, "ignore"), false},
		{"batch limits", indexResumeKey("head", store.IndexModeIncremental, "base", "base-run", &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, MaxBatch: 1, MaxBatchChars: 100}, "ignore"), true},
		{"input truncation", indexResumeKey("head", store.IndexModeIncremental, "base", "base-run", &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, MaxItemChars: 12}, "ignore"), false},
		{"document prompt", indexResumeKey("head", store.IndexModeIncremental, "base", "base-run", &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, DocumentPrompt: "text: "}, "ignore"), false},
		{"query prompt", indexResumeKey("head", store.IndexModeIncremental, "base", "base-run", &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, QueryPrompt: "query: "}, "ignore"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.key == key) != tc.same {
				t.Fatalf("key match = %v, want %v", tc.key == key, tc.same)
			}
		})
	}
	// A full build takes vectors from the active generation, unless forced:
	// its progress is not resumed once that generation is replaced.
	active := &store.Generation{ID: "base-run"}
	full := func(active *store.Generation, forced bool) string {
		return indexResumeKey("head", store.IndexModeFull, "", vectorSource(active, forced), emb, "ignore")
	}
	if full(active, false) == full(&store.Generation{ID: "replacement"}, false) {
		t.Fatal("a full build resumes after the generation it took vectors from was replaced")
	}
	if full(active, true) != full(nil, true) {
		t.Fatal("a forced rebuild depends on the active generation, whose vectors it does not take")
	}
}

// TestIndexMatches: an index is kept, and stepped incrementally, only when
// the embedder that built it builds the same vectors. The query prompt
// changes no stored vector. A push steps an index of another format, or one
// that a step of another format advanced, which the onboarding feeder
// rebuilds in its turn.
func TestIndexMatches(t *testing.T) {
	emb := &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: "}
	built := store.Generation{Model: "model", Dims: 8, DocumentPrompt: "text: ", IgnoreHash: "ignore", Format: runspec.IndexFormat}
	for _, tc := range []struct {
		name    string
		edit    func(*store.Generation)
		trigger string
		want    bool
	}{
		{"same embedder", func(*store.Generation) {}, jobs.TriggerPush, true},
		{"another model", func(g *store.Generation) { g.Model = "other" }, jobs.TriggerPush, false},
		{"another dimension", func(g *store.Generation) { g.Dims = 16 }, jobs.TriggerPush, false},
		{"another document prompt", func(g *store.Generation) { g.DocumentPrompt = "" }, jobs.TriggerPush, false},
		{"other ignore rules", func(g *store.Generation) { g.IgnoreHash = "other" }, jobs.TriggerPush, false},
		{"same format onboarded", func(*store.Generation) {}, jobs.TriggerOnboard, true},
		{"older format pushed", func(g *store.Generation) { g.Format = runspec.IndexFormat - 1 }, jobs.TriggerPush, true},
		{"newer format pushed", func(g *store.Generation) { g.Format = runspec.IndexFormat + 1 }, jobs.TriggerPush, true},
		{"older format onboarded", func(g *store.Generation) { g.Format = runspec.IndexFormat - 1 }, jobs.TriggerOnboard, false},
		{"older format reindexed", func(g *store.Generation) { g.Format = runspec.IndexFormat - 1 }, jobs.TriggerReindex, false},
		{"stepped in another format pushed", func(g *store.Generation) { g.Mixed = true }, jobs.TriggerPush, true},
		{"stepped in another format onboarded", func(g *store.Generation) { g.Mixed = true }, jobs.TriggerOnboard, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := built
			tc.edit(&g)
			if got := indexMatches(&g, emb, "ignore", tc.trigger); got != tc.want {
				t.Fatalf("indexMatches = %v, want %v", got, tc.want)
			}
		})
	}
	if !indexMatches(&built, &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: ", QueryPrompt: "query: "}, "ignore", jobs.TriggerPush) {
		t.Fatal("a query prompt must not rebuild the index")
	}
	if indexMatches(nil, emb, "ignore", jobs.TriggerPush) {
		t.Fatal("no generation matches")
	}
}

// TestIndexContentHash: two chunks share a content hash only when the
// embedder gets the same text from them and embeds it the same way. Text
// past the embedder's item limit is cut before it is sent, so it changes
// nothing.
func TestIndexContentHash(t *testing.T) {
	emb := &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: ", MaxItemChars: 10}
	base := contentHash(emb, "main.go\nfunc f() {}")
	for _, tc := range []struct {
		name string
		emb  configfile.Embedding
		text string
		same bool
	}{
		{"same", *emb, "main.go\nfunc f() {}", true},
		{"past the item limit", *emb, "main.go\nfunc g() {}", true},
		{"other text", *emb, "util.go\nfunc f() {}", false},
		{"another model", configfile.Embedding{Model: "other", Dims: 8, DocumentPrompt: "text: ", MaxItemChars: 10}, "main.go\nfunc f() {}", false},
		{"another dimension", configfile.Embedding{Model: "model", Dims: 16, DocumentPrompt: "text: ", MaxItemChars: 10}, "main.go\nfunc f() {}", false},
		{"another document prompt", configfile.Embedding{Model: "model", Dims: 8, MaxItemChars: 10}, "main.go\nfunc f() {}", false},
		{"a longer item limit", configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: ", MaxItemChars: 20}, "main.go\nfunc f() {}", false},
		{"a query prompt", configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: ", QueryPrompt: "q: ", MaxItemChars: 10}, "main.go\nfunc f() {}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := contentHash(&tc.emb, tc.text)
			if len(got) != sha256.Size || bytes.Equal(got, base) != tc.same {
				t.Fatalf("hash %x against %x, want same %v", got, base, tc.same)
			}
		})
	}
}

// TestIndexContentHashSignature: two windows of the same text share a
// content hash, and so a vector, only when they carry the same signature.
func TestIndexContentHashSignature(t *testing.T) {
	emb := &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: "}
	window := store.StagedChunk{Path: "main.go", Symbol: "f", Kind: "function", Signature: "func f(a int) int {", Text: "\ta++\n\treturn a\n"}
	base := contentHash(emb, window.EmbedText(emb.DocumentPrompt))
	for _, tc := range []struct {
		name, signature string
		same            bool
	}{
		{"the same signature", "func f(a int) int {", true},
		{"another signature", "func f(a, b int) int {", false},
		{"no signature", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := window
			c.Signature = tc.signature
			if got := contentHash(emb, c.EmbedText(emb.DocumentPrompt)); bytes.Equal(got, base) != tc.same {
				t.Fatalf("hash %x against %x, want same %v", got, base, tc.same)
			}
		})
	}
}

// TestIndexContentHashPinned: the embedded text of a chunk, and so its
// content hash, stays byte for byte what the index has stored. A change to
// either finds none of the stored vectors, which every index then embeds
// again.
func TestIndexContentHashPinned(t *testing.T) {
	emb := &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: ", MaxItemChars: 1000}
	c := store.StagedChunk{Path: "a/b.py", Symbol: "bar", Kind: "function", Scope: "Foo", Signature: "def bar(self):",
		Text: "        total += 1\n        return total"}
	text := c.EmbedText(emb.DocumentPrompt)
	if want := "text: a/b.py function Foo.bar\ndef bar(self):\n        total += 1\n        return total"; text != want {
		t.Fatalf("embedded text %q, want %q", text, want)
	}
	if got, want := fmt.Sprintf("%x", contentHash(emb, text)), "fe1650022732986fb73043e01287b8c8cfd2e7d6bee51d3ee24ffdae430e9da3"; got != want {
		t.Fatalf("content hash %s, want %s", got, want)
	}
}

// TestForgeWait: an index job waits out a forge failing transiently for
// longer each time, up to tuning's Worker.ForgeWaits times, without spending an attempt.
// Then, or for any other failure, the failure spends one.
func TestForgeWait(t *testing.T) {
	transient := &url.Error{Op: "Get", URL: "https://api.github.com", Err: errors.New("connection reset")}
	for _, tc := range []struct {
		name      string
		err       error
		metadata  string
		wantWaits int
		min, max  time.Duration
	}{
		{name: "first", err: transient, metadata: `{}`, wantWaits: 1, min: tuning.Get().Worker.ForgeWaitMin / 2, max: tuning.Get().Worker.ForgeWaitMin},
		{name: "after snoozes for other reasons", err: transient, metadata: `{"snoozes": 9}`, wantWaits: 1, min: tuning.Get().Worker.ForgeWaitMin / 2, max: tuning.Get().Worker.ForgeWaitMin},
		{name: "third", err: transient, metadata: `{"forge_waits": 2}`, wantWaits: 3, min: 2 * tuning.Get().Worker.ForgeWaitMin, max: 4 * tuning.Get().Worker.ForgeWaitMin},
		{name: "last", err: transient, metadata: fmt.Sprintf(`{"forge_waits": %d}`, tuning.Get().Worker.ForgeWaits-1), wantWaits: tuning.Get().Worker.ForgeWaits,
			min: tuning.Get().Worker.ForgeWaitMax / 2, max: tuning.Get().Worker.ForgeWaitMax},
		{name: "spent", err: transient, metadata: fmt.Sprintf(`{"forge_waits": %d}`, tuning.Get().Worker.ForgeWaits)},
		{name: "permanent", err: errors.New("github: branch main of o/r: 404 Not Found"), metadata: `{}`},
		{name: "unreadable metadata", err: transient, metadata: `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, waits := forgeWait(tc.err, []byte(tc.metadata))
			if waits != tc.wantWaits || d < tc.min || d > tc.max {
				t.Fatalf("forgeWait = %s, %d, want %d waits for %s to %s", d, waits, tc.wantWaits, tc.min, tc.max)
			}
		})
	}
}

// TestForgeWaitsEndWithinAnHour: the waits of a job whose forge keeps failing
// transiently add up to about an hour at most, after which the failure spends
// an attempt. A forge down for longer than that drops the push rather than
// holding the repository's unique key for good.
func TestForgeWaitsEndWithinAnHour(t *testing.T) {
	transient := &url.Error{Op: "Get", URL: "https://api.github.com", Err: errors.New("connection reset")}
	var total time.Duration
	for waits := 0; ; waits++ {
		d, n := forgeWait(transient, []byte(fmt.Sprintf(`{"forge_waits": %d}`, waits)))
		if n == 0 {
			if waits != tuning.Get().Worker.ForgeWaits {
				t.Fatalf("waits ended after %d, want %d", waits, tuning.Get().Worker.ForgeWaits)
			}
			break
		}
		total += d
	}
	if total < 30*time.Minute || total > 61*time.Minute {
		t.Fatalf("waits add up to %s, want about an hour at most", total)
	}
}

// TestWaitOutGitLabRateLimit: an index job that GitLab refuses with a 429
// naming its reset snoozes until then, past the longest forge wait, and
// spends neither an attempt nor a forge wait, so even a job whose forge
// waits are used up snoozes.
func TestWaitOutGitLabRateLimit(t *testing.T) {
	h := http.Header{}
	h.Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10))
	h.Set("Retry-After", "60")
	limited := fmt.Errorf("gitlab: branch main of o/r: %w", &gl.ErrorResponse{
		StatusCode: http.StatusTooManyRequests,
		Response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: h,
			Request: &http.Request{URL: &url.URL{Scheme: "https", Host: "gitlab.example.com", Path: "/api/v4/projects/o%2Fr/repository/branches/main"}}},
	})
	for _, metadata := range []string{`{}`, fmt.Sprintf(`{"forge_waits": %d}`, tuning.Get().Worker.ForgeWaits)} {
		t.Run(metadata, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			w := &Index{Metrics: metrics.New(reg)}
			job := &river.Job[jobs.IndexArgs]{JobRow: &rivertype.JobRow{Metadata: []byte(metadata)}}
			err := w.waitOutForge(t.Context(), job, "acme", slog.New(slog.DiscardHandler), limited)
			snooze, ok := errors.AsType[*river.JobSnoozeError](err)
			if !ok || snooze.Duration <= tuning.Get().Worker.ForgeWaitMax || snooze.Duration > 2*time.Hour {
				t.Fatalf("waitOutForge = %v, want a snooze until the reset two hours away", err)
			}
			want := "# HELP kritika_index_snoozes_total Index jobs put back on the queue to wait, by reason.\n" +
				"# TYPE kritika_index_snoozes_total counter\n" +
				"kritika_index_snoozes_total{account=\"acme\",reason=\"forge\"} 1\n"
			if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kritika_index_snoozes_total"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// giteaRateLimited is the error of a Gitea client's BranchTip that the
// instance refused with a 429 naming retryAfter.
func giteaRateLimited(t *testing.T, retryAfter string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	c, err := forgejo.NewGiteaClient(srv.URL, "gitea-api", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.BranchTip(t.Context(), "acme", "widgets", "main")
	if forgejo.Status(err) != http.StatusTooManyRequests {
		t.Fatalf("BranchTip = %v, want a 429", err)
	}
	return err
}

// TestIndexWaitsOutForgejoRateLimit: an index job that Forgejo or Gitea
// refuses with a 429 whose Retry-After, in seconds or as a date, names a
// wait two hours away snoozes until then, past the longest forge wait, and
// spends no forge wait, as a GitLab one does. Its check of the branch tip
// after publication asks again then too, not at tuning's Worker.TipCheckWait.
func TestIndexWaitsOutForgejoRateLimit(t *testing.T) {
	for _, retryAfter := range []string{"7200", time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)} {
		t.Run(retryAfter, func(t *testing.T) {
			limited := giteaRateLimited(t, retryAfter)
			if d := tipCheckDelay(limited); d <= time.Hour || d > 2*time.Hour {
				t.Fatalf("tipCheckDelay = %s, want the two hours that Retry-After names", d)
			}
			for _, metadata := range []string{`{}`, fmt.Sprintf(`{"forge_waits": %d}`, tuning.Get().Worker.ForgeWaits)} {
				w := &Index{Metrics: metrics.New(prometheus.NewRegistry())}
				job := &river.Job[jobs.IndexArgs]{JobRow: &rivertype.JobRow{Metadata: []byte(metadata)}}
				err := w.waitOutForge(t.Context(), job, "acme", slog.New(slog.DiscardHandler), limited)
				snooze, ok := errors.AsType[*river.JobSnoozeError](err)
				if !ok || snooze.Duration <= tuning.Get().Worker.ForgeWaitMax || snooze.Duration > 2*time.Hour {
					t.Fatalf("waitOutForge with %s = %v, want a snooze until Retry-After two hours away", metadata, err)
				}
			}
		})
	}
}

// TestIndexRepoConfigWaitsOutGitLabRateLimit: GitLab serves the branch but
// rate-limits the read of .kritika.yaml until a reset hours away. The read
// answers the 429 at once, rather than the SDK sleeping on it past the
// job's deadline, so the job snoozes until the reset.
func TestIndexRepoConfigWaitsOutGitLabRateLimit(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{project}/repository/branches/main", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"name": "main", "commit": {"id": "abc"}}`)
	})
	var reads atomic.Int32
	mux.HandleFunc("GET /api/v4/projects/{project}/repository/files/{path}/raw", func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.Header().Set("Ratelimit-Reset", reset)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := gitlab.NewClient(srv.URL, "glpat-api", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A hang guard: the SDK's sleep until the reset lasts hours.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	commit, _, err := client.BranchTip(ctx, "acme", "widgets", "main")
	if err != nil {
		t.Fatal(err)
	}
	w := &Index{Metrics: metrics.New(prometheus.NewRegistry())}
	job := &river.Job[jobs.IndexArgs]{JobRow: &rivertype.JobRow{Metadata: []byte(`{}`)}}
	repo := store.IndexRepo{Name: "acme/widgets", DefaultBranch: "main", Enabled: true}
	_, _, err = w.repoConfig(ctx, job, "acme", client, repo, commit, 0, configfile.Settings{}, slog.New(slog.DiscardHandler))
	snooze, ok := errors.AsType[*river.JobSnoozeError](err)
	if !ok || snooze.Duration <= tuning.Get().Worker.ForgeWaitMax || reads.Load() != 1 {
		t.Fatalf("repoConfig past GitLab's rate limit = %v after %d reads, want a snooze until the reset after one", err, reads.Load())
	}
}

// TestAwaitAppliedBuild: an index job on a pod whose embedder or index format
// the leader did not apply is snoozed, counted on the metric, and logged as a
// warning once it has waited tuning's Worker.BuildWaitWarn times.
func TestAwaitAppliedBuild(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata string
		warn     bool
	}{
		{name: "first", metadata: `{}`},
		{name: "a rollout's", metadata: fmt.Sprintf(`{"build_waits": %d}`, tuning.Get().Worker.BuildWaitWarn-2)},
		{name: "long", metadata: fmt.Sprintf(`{"build_waits": %d}`, tuning.Get().Worker.BuildWaitWarn-1), warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			reg := prometheus.NewRegistry()
			w := &Index{Metrics: metrics.New(reg)}
			job := &river.Job[jobs.IndexArgs]{JobRow: &rivertype.JobRow{Metadata: []byte(tc.metadata)}}
			err := w.awaitAppliedBuild(t.Context(), job, "onedr0p", slog.New(slog.NewTextHandler(&logs, nil)))
			if snooze, ok := errors.AsType[*river.JobSnoozeError](err); !ok || snooze.Duration != tuning.Get().Worker.AppliedBuildWait {
				t.Fatalf("awaitAppliedBuild = %v, want a snooze of %s", err, tuning.Get().Worker.AppliedBuildWait)
			}
			if got := strings.Contains(logs.String(), "level=WARN msg=\"index has waited long"); got != tc.warn {
				t.Fatalf("warned = %v, want %v:\n%s", got, tc.warn, logs.String())
			}
			want := "# HELP kritika_index_snoozes_total Index jobs put back on the queue to wait, by reason.\n" +
				"# TYPE kritika_index_snoozes_total counter\n" +
				"kritika_index_snoozes_total{account=\"onedr0p\",reason=\"build\"} 1\n"
			if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kritika_index_snoozes_total"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestIndexCompletedTruncation: a run that publishes a truncated pack is
// logged at Warn and counted in kritika_index_truncated_total, and one that
// publishes a whole pack is neither.
func TestIndexCompletedTruncation(t *testing.T) {
	account := &configfile.Account{Forge: configfile.ForgeGitHub, Name: "onedr0p"}
	for _, tc := range []struct {
		name string
		pack store.IndexPack
		want int
	}{
		{"truncated full build", store.IndexPack{Mode: store.IndexModeFull, Truncated: true}, 1},
		{"whole full build", store.IndexPack{Mode: store.IndexModeFull}, 0},
		{"incremental step", store.IndexPack{Mode: store.IndexModeIncremental}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			reg := prometheus.NewRegistry()
			w := &Index{Metrics: metrics.New(reg)}
			w.completed(account, tc.pack, 3, slog.New(slog.NewTextHandler(&logs, nil)))
			want := ""
			if tc.want > 0 {
				want = "# HELP kritika_index_truncated_total Full index builds published truncated: stopped at the indexer's budget, leaving out the files past it.\n" +
					"# TYPE kritika_index_truncated_total counter\n" +
					fmt.Sprintf("kritika_index_truncated_total{account=%q} %d\n", account.Key(), tc.want)
			}
			if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "kritika_index_truncated_total"); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), `level=WARN msg="index published truncated`); got != (tc.want > 0) {
				t.Fatalf("warned = %v, want %v:\n%s", got, tc.want > 0, logs.String())
			}
		})
	}
}
