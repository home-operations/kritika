package worker

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
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
	active := &store.Generation{ID: "base-run"}
	key := indexResumeKey("head", store.IndexModeIncremental, "base", active, emb, "ignore")
	for _, tc := range []struct {
		name string
		key  string
		same bool
	}{
		{"same", indexResumeKey("head", store.IndexModeIncremental, "base", active, emb, "ignore"), true},
		{"new base commit", indexResumeKey("head", store.IndexModeIncremental, "other", active, emb, "ignore"), false},
		{"replacement base", indexResumeKey("head", store.IndexModeIncremental, "base", &store.Generation{ID: "other"}, emb, "ignore"), false},
		{"batch limits", indexResumeKey("head", store.IndexModeIncremental, "base", active, &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, MaxBatch: 1, MaxBatchChars: 100}, "ignore"), true},
		{"input truncation", indexResumeKey("head", store.IndexModeIncremental, "base", active, &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, MaxItemChars: 12}, "ignore"), false},
		{"document prompt", indexResumeKey("head", store.IndexModeIncremental, "base", active, &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, DocumentPrompt: "text: "}, "ignore"), false},
		{"query prompt", indexResumeKey("head", store.IndexModeIncremental, "base", active, &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, QueryPrompt: "query: "}, "ignore"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.key == key) != tc.same {
				t.Fatalf("key match = %v, want %v", tc.key == key, tc.same)
			}
		})
	}
	if indexResumeKey("head", store.IndexModeFull, "", active, emb, "ignore") != indexResumeKey("head", store.IndexModeFull, "", nil, emb, "ignore") {
		t.Fatal("full build depends on the active generation")
	}
}

// TestIndexMatches: an index is kept, and stepped incrementally, only when
// the embedder that built it builds the same vectors. The query prompt
// changes no stored vector. A push steps an index of another format, which
// the onboarding feeder rebuilds in its turn.
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

// TestForgeWait: an index job waits out a forge failing transiently for
// longer each time, up to forgeWaits times, without spending an attempt.
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
		{name: "first", err: transient, metadata: `{}`, wantWaits: 1, min: forgeWaitMin / 2, max: forgeWaitMin},
		{name: "after snoozes for other reasons", err: transient, metadata: `{"snoozes": 9}`, wantWaits: 1, min: forgeWaitMin / 2, max: forgeWaitMin},
		{name: "third", err: transient, metadata: `{"forge_waits": 2}`, wantWaits: 3, min: 2 * forgeWaitMin, max: 4 * forgeWaitMin},
		{name: "last", err: transient, metadata: fmt.Sprintf(`{"forge_waits": %d}`, forgeWaits-1), wantWaits: forgeWaits,
			min: forgeWaitMax / 2, max: forgeWaitMax},
		{name: "spent", err: transient, metadata: fmt.Sprintf(`{"forge_waits": %d}`, forgeWaits)},
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
