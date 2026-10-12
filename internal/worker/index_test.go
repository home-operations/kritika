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
		{"input truncation", indexResumeKey("head", store.IndexModeIncremental, "base", active, &configfile.Embedding{BaseURL: emb.BaseURL, Model: emb.Model, Dims: 8, MaxItemChars: 12}, "ignore"), false},
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
