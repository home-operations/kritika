package worker

import (
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
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
// the embedder that built it would build the same vectors; the query
// prompt changes no stored vector.
func TestIndexMatches(t *testing.T) {
	emb := &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: "}
	built := store.Generation{Model: "model", Dims: 8, DocumentPrompt: "text: ", IgnoreHash: "ignore"}
	for _, tc := range []struct {
		name string
		edit func(*store.Generation)
		want bool
	}{
		{"same embedder", func(*store.Generation) {}, true},
		{"another model", func(g *store.Generation) { g.Model = "other" }, false},
		{"another dimension", func(g *store.Generation) { g.Dims = 16 }, false},
		{"another document prompt", func(g *store.Generation) { g.DocumentPrompt = "" }, false},
		{"other ignore rules", func(g *store.Generation) { g.IgnoreHash = "other" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := built
			tc.edit(&g)
			if got := indexMatches(&g, emb, "ignore"); got != tc.want {
				t.Fatalf("indexMatches = %v, want %v", got, tc.want)
			}
		})
	}
	if !indexMatches(&built, &configfile.Embedding{Model: "model", Dims: 8, DocumentPrompt: "text: ", QueryPrompt: "query: "}, "ignore") {
		t.Fatal("a query prompt must not rebuild the index")
	}
	if indexMatches(nil, emb, "ignore") {
		t.Fatal("no generation matches")
	}
}
