package configfile

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/tuning"
)

// fileApp is an app named name serving accounts, its secrets read from
// TEST_PRIVATE_KEY and TEST_WEBHOOK_SECRET.
func fileApp(name string, accounts ...string) string {
	return "  " + name + ": { accounts: [" + strings.Join(accounts, ", ") + "], clientId: Iv1." + name +
		", privateKey: { env: TEST_PRIVATE_KEY }, webhookSecret: { env: TEST_WEBHOOK_SECRET } }\n"
}

func TestParseEmpty(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("  \n")} {
		f, err := Parse(raw)
		if err != nil || len(f.Connections) != 0 || f.Hash() == "" {
			t.Fatalf("Parse(%q) = %+v, %v", raw, f, err)
		}
	}
}

// TestParse: the file holds the whole configuration, and runs the accounts
// that its apps serve. It keeps aside the entries that none serves.
func TestParse(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_KEY", "sk")
	f, err := Parse([]byte(`providers:
  p: { type: openai, apiKey: { env: TEST_KEY } }
review: { model: p/big }
trigger: { limit: 2 }
apps:
` + fileApp("acme-bot", "acme") + fileApp("org-bot", "org-2", "Org-3") + `accounts:
  ORG-2: { limits: { reviewsPerDay: 5 } }
  gone: { limits: { reviewsPerDay: 3 } }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Providers["p"].APIKeyValue().Value() != "sk" {
		t.Fatalf("providers = %+v", f.Providers)
	}
	accounts := make([]string, 0, len(f.Accounts))
	for _, a := range f.Accounts {
		accounts = append(accounts, a.Slug())
	}
	if !slices.Equal(accounts, []string{"github/acme", "github/ORG-2", "github/Org-3"}) {
		t.Fatalf("accounts = %v", accounts)
	}
	org2, ok := f.Account(ForgeGitHub, "org-2")
	if !ok || org2.Limits.ReviewsPerDay == nil || f.Settings(org2).MaxAutoReviews != 2 || f.Settings(org2).Limits.ReviewsPerDay != 5 {
		t.Fatalf("org-2 = %+v", org2)
	}
	if in := f.ConnectionFor(org2); in == nil || in.Name != "org-bot" {
		t.Fatalf("ConnectionFor(org-2) = %v", in)
	}
	if u := f.Unserved(); len(u) != 1 || u[0].Name != "gone" {
		t.Fatalf("unserved = %+v", u)
	}
	if _, ok := f.Account(ForgeGitHub, "gone"); ok {
		t.Fatal("an unserved account runs")
	}
}

func TestParseRejectsEntries(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_KEY", "ek")
	apps := func(c ...string) string { return "apps:\n" + strings.Join(c, "") }
	for _, tt := range []struct{ name, yaml, want string }{
		{"an unknown key", "tenants: []\n", "field tenants not found"},
		{"apps as a list", "apps:\n  - { name: a, accounts: [x] }\n", "!!seq"},
		{"a sealed reference", apps(strings.Replace(fileApp("a", "x"), "{ env: TEST_PRIVATE_KEY }", "{ sealed: abc }", 1)),
			"field sealed not found"},
		{"a clientId from a file", apps(strings.Replace(fileApp("a", "x"), "clientId: Iv1.a", "clientId: { file: /x }", 1)),
			"field file not found"},
		{"a repositories map", "repositories: { acme/*: { review: { model: nope/x } } }\n", "field repositories not found"},
		{"a duplicate app", apps(fileApp("a", "x"), fileApp("a", "y")), "already defined"},
		{"an account two apps serve", apps(fileApp("a", "x"), fileApp("b", "X")), "an account is served by one connection"},
		{"an App two entries declare", apps(fileApp("a", "x"), strings.Replace(fileApp("b", "y"), "clientId: Iv1.b", "clientId: Iv1.a", 1)),
			`apps.b.clientId is "a"'s App already`},
		{"two documents", "egress: {}\n---\negress: {}\n", "one document"},
		{"an embedder without a provider", embeddingDoc("model: voyage-code-3"), `embedding.model must be "<provider>/<model>"`},
		{"an embedder of an undeclared provider", embeddingDoc("model: other/voyage-code-3"), `references provider "other"`},
		{"an embedder of an anthropic provider", "providers:\n  a: { type: anthropic, apiKey: { env: TEST_KEY } }\nembedding: { model: a/x, dims: 8 }\n",
			"embeddings need an openrouter or openai one"},
		{"an embedder alias", "providers:\n  o: { type: openai, apiKey: { env: TEST_KEY } }\nembedding: { model: o/~embed-latest, dims: 8 }\n",
			`embedding.model takes no floating alias, got "o/~embed-latest"`},
		{"an OpenRouter embedder alias", embeddingDoc("model: or/~vendor/embed-latest"),
			`embedding.model takes no floating alias, got "or/~vendor/embed-latest"`},
		{"an embedder too wide for the index", embeddingDoc("dims: 4096"), "embedding.dims must be between 1 and 4000"},
		{"an embedder with a negative bound", embeddingDoc("maxBatch: -1"), "must not be negative"},
		{"an embedder whose batch cannot hold a rune", embeddingDoc("maxBatchChars: 2"), "must be at least 4, a whole rune"},
		{"an embedder whose item cannot hold a rune", embeddingDoc("maxItemChars: 3"), "must be at least 4, a whole rune"},
		{"an embedder with a floor above 1", embeddingDoc("similarFloor: 1.5"), "embedding.similarFloor must be between 0 and 1"},
		{"an embedder with a margin above 1", embeddingDoc("similarMargin: 1.5"), "embedding.similarMargin must be between 0 and 1"},
		{"an embedder with a negative margin", embeddingDoc("similarMargin: -0.1"), "embedding.similarMargin must be between 0 and 1"},
		{"an embedder with a margin that is not a number", embeddingDoc("similarMargin: .nan"), "embedding.similarMargin must be between 0 and 1"},
		{"an embedder with a floor that is not a number", embeddingDoc("similarFloor: .nan"), "embedding.similarFloor must be between 0 and 1"},
		{"a query prompt without a floor", embeddingDoc(`queryPrompt: "query: "`), "need embedding.similarFloor set"},
		{"a document prompt without a floor", embeddingDoc(`documentPrompt: "passage: "`), "need embedding.similarFloor set"},
		{"a search prompt without a floor", embeddingDoc(`searchPrompt: "query: "`), "need embedding.similarFloor set"},
		{"an embedder with an endpoint of its own", embeddingDoc("baseUrl: https://embed.example/v1"), "field baseUrl not found"},
		{"a reranker without a provider", rerankDoc("{ model: rerank-v3.5, api: cohere }"), `embedding.rerank.model must be "<provider>/<model>"`},
		{"a reranker of an undeclared provider", rerankDoc("{ model: other/rerank-v3.5, api: cohere }"), `references provider "other"`},
		{"a reranker of an openrouter provider", rerankDoc("{ model: or/rerank-v3.5, api: cohere }"), "a reranker needs an openai one"},
		{"a reranker without an endpoint", rerankDoc("{ model: bare/rerank-v3.5, api: cohere }"), `provider "bare", which sets no baseUrl`},
		{"a reranker of no API", rerankDoc("{ model: rr/rerank-v3.5 }"), "embedding.rerank.api must be one of cohere, jina, voyage, tei"},
		{"a reranker of another API", rerankDoc("{ model: rr/rerank-v3.5, api: openai }"), "embedding.rerank.api must be one of"},
		{"too many rerank candidates", rerankDoc("{ model: rr/rerank-v3.5, api: cohere, candidates: 101 }"),
			"embedding.rerank.candidates must be between 0 and 100"},
		{"a rerank timeout too long", rerankDoc("{ model: rr/rerank-v3.5, api: cohere, timeout: 1m }"),
			"embedding.rerank.timeout must be between 0 and 30s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.yaml)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// embeddingDoc is a file with an openrouter provider and an embedder of it.
// The file is valid but for override, a field that replaces the one of its
// name.
func embeddingDoc(override string) string {
	fields := map[string]string{"model": "model: or/voyage-code-3", "dims": "dims: 1024"}
	key, _, _ := strings.Cut(override, ":")
	fields[key] = override
	return "providers:\n  or: { type: openrouter, apiKey: { env: TEST_KEY } }\n" +
		"embedding:\n  " + strings.Join(slices.Sorted(maps.Values(fields)), "\n  ") + "\n"
}

// rerankDoc is a file with an embedder and the reranker rerank, and three
// providers: openrouter, an openai one with an endpoint, rr, and one
// without, bare.
func rerankDoc(rerank string) string {
	return "providers:\n  or: { type: openrouter, apiKey: { env: TEST_KEY } }\n" +
		"  rr: { type: openai, baseUrl: https://api.cohere.com/v2, apiKey: { env: TEST_KEY } }\n" +
		"  bare: { type: openai, apiKey: { env: TEST_KEY } }\n" +
		"embedding:\n  model: or/voyage-code-3\n  dims: 1024\n  rerank: " + rerank + "\n"
}

// TestParseRerank: the reranker takes its provider's endpoint and key, and
// the defaults of what it leaves out. Lexical search is on unless turned
// off.
func TestParseRerank(t *testing.T) {
	t.Setenv("TEST_KEY", "rk")
	f, err := Parse([]byte(rerankDoc("{ model: rr/rerank-v3.5, api: cohere }")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, cf := f.Embedding.Rerank, tuning.Get().ConfigFile
	if r.BaseURL != "https://api.cohere.com/v2" || r.Model != "rerank-v3.5" || r.APIKeyValue().Value() != "rk" ||
		r.CandidateCount() != cf.DefaultRerankCandidates || r.Deadline() != cf.DefaultRerankTimeout {
		t.Fatalf("rerank = %+v", r)
	}
	if !f.Embedding.LexicalSearch() {
		t.Fatal("lexical search is off by default")
	}
	f, err = Parse([]byte(rerankDoc("{ model: rr/m, api: tei, candidates: 20, timeout: 5s }") + "  lexical: false\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r := f.Embedding.Rerank; r.CandidateCount() != 20 || r.Deadline() != 5*time.Second || f.Embedding.LexicalSearch() {
		t.Fatalf("embedding = %+v, rerank %+v", f.Embedding, r)
	}
}

// TestParseEmbedding: the embedder takes its provider's endpoint, or the
// type's, and its key, and the model's id on it.
func TestParseEmbedding(t *testing.T) {
	t.Setenv("TEST_KEY", "ek")
	f, err := Parse([]byte(embeddingDoc("maxBatch: 8")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e := f.Embedding
	if e == nil || e.Model != "voyage-code-3" || e.BaseURL != model.OpenRouterBaseURL || e.Dims != 1024 || e.APIKeyValue().Value() != "ek" {
		t.Fatalf("embedding = %+v", e)
	}
	mt, cf := tuning.Get().Model, tuning.Get().ConfigFile
	if batch, chars, item := e.Bounds(); batch != 8 || chars != mt.DefaultEmbedMaxBatchChars || item != mt.DefaultEmbedMaxItemChars {
		t.Fatalf("Bounds = %d, %d, %d", batch, chars, item)
	}
	if e.Floor() != cf.DefaultSimilarFloor {
		t.Fatalf("Floor = %g, want the default %g", e.Floor(), cf.DefaultSimilarFloor)
	}
	if f, err := Parse([]byte(embeddingDoc("similarFloor: 0.7"))); err != nil || f.Embedding.Floor() != 0.7 {
		t.Fatalf("Parse(similarFloor: 0.7) = %v, floor %v", err, f)
	}
	if e.Margin() != cf.DefaultSimilarMargin {
		t.Fatalf("Margin = %g, want the default %g", e.Margin(), cf.DefaultSimilarMargin)
	}
	for _, margin := range []float64{0.05, 0} {
		doc := embeddingDoc(fmt.Sprintf("similarMargin: %g", margin))
		if f, err := Parse([]byte(doc)); err != nil || f.Embedding.Margin() != margin {
			t.Fatalf("Parse(similarMargin: %g) = %v, margin %v", margin, err, f)
		}
	}
	if e.QueryPrompt != "" || e.DocumentPrompt != "" || e.SearchPrompt != "" {
		t.Fatalf("prompts = %q, %q, %q; want none unless set", e.QueryPrompt, e.DocumentPrompt, e.SearchPrompt)
	}
	f, err = Parse([]byte(strings.Replace(embeddingDoc("similarFloor: 0.3"), "embedding:\n",
		"embedding:\n  queryPrompt: \"Instruct: find similar code\\nQuery:\"\n  documentPrompt: \"text: \"\n", 1)))
	if err != nil || f.Embedding.QueryPrompt != "Instruct: find similar code\nQuery:" || f.Embedding.DocumentPrompt != "text: " {
		t.Fatalf("prompts = %+v, %v; want them exactly as written", f.Embedding, err)
	}
	for _, tt := range []struct {
		name, searchPrompt, diff, search string
	}{
		{name: "a search without a prompt of its own takes the query prompt", diff: "Instruct: find similar code\nQuery:",
			search: "Instruct: find similar code\nQuery:"},
		{name: "a search prompt of its own", searchPrompt: "Instruct: find the code described\nQuery:",
			diff: "Instruct: find similar code\nQuery:", search: "Instruct: find the code described\nQuery:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := *f.Embedding
			e.SearchPrompt = tt.searchPrompt
			if diff, search := e.PromptFor(false), e.PromptFor(true); diff != tt.diff || search != tt.search {
				t.Fatalf("PromptFor = %q for a hunk, %q for a search; want %q, %q", diff, search, tt.diff, tt.search)
			}
		})
	}
	f, err = Parse([]byte("providers:\n  gw: { type: openai, baseUrl: https://gw.example/v1, apiKey: { env: TEST_KEY } }\n" +
		"embedding: { model: gw/embed-large, dims: 8 }\n"))
	if err != nil || f.Embedding.BaseURL != "https://gw.example/v1" || f.Embedding.Model != "embed-large" {
		t.Fatalf("embedding = %+v, %v", f.Embedding, err)
	}
}

func TestParseAccountProviders(t *testing.T) {
	t.Setenv("TEST_PRIVATE_KEY", "tok")
	t.Setenv("TEST_WEBHOOK_SECRET", "whsec")
	t.Setenv("TEST_KEY", "sk-acme")
	f, err := Parse([]byte("apps:\n" + fileApp("acme-bot", "acme") + `accounts:
  acme:
    providers: { own: { type: anthropic, apiKey: { env: TEST_KEY } } }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p, ok := f.Provider(&f.Accounts[0], "own"); !ok || p.APIKeyValue().Value() != "sk-acme" {
		t.Fatalf("Provider(acme, own) = %+v, %v", p, ok)
	}
}

// TestRerankCandidates: a reranker of Text Embeddings Inference is sent no
// more candidates than its default client batch takes, unless they are set.
func TestRerankCandidates(t *testing.T) {
	t.Setenv("TEST_KEY", "rk")
	for _, tt := range []struct {
		rerank string
		want   int
	}{
		{"{ model: rr/m, api: tei }", 32},
		{"{ model: rr/m, api: tei, candidates: 64 }", 64},
		{"{ model: rr/m, api: cohere }", 50},
	} {
		t.Run(tt.rerank, func(t *testing.T) {
			f, err := Parse([]byte(rerankDoc(tt.rerank)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := f.Embedding.Rerank.CandidateCount(); got != tt.want {
				t.Fatalf("CandidateCount = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestParseRerankBaseURL: a reranker's provider has an HTTP API root that
// "/rerank" goes under, or the file is refused, without the URL in the error.
// A query or a fragment would take the suffix out of the path.
func TestParseRerankBaseURL(t *testing.T) {
	t.Setenv("TEST_KEY", "rk")
	for _, tt := range []struct {
		name, url string
		ok        bool
	}{
		{"a root", "https://api.cohere.com/v2", true},
		{"a root with a slash", "http://tei.ai.svc:8080/", true},
		{"a query", "https://api.cohere.com/v2?secret=s3cr3t", false},
		{"an empty query", "https://api.cohere.com/v2?", false},
		{"a fragment", "https://api.cohere.com/v2#s3cr3t", false},
		{"an empty fragment", "https://api.cohere.com/v2#", false},
		{"another scheme", "ftp://api.cohere.com/v2", false},
		{"a port alone", "http://:8080/", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("providers:\n  or: { type: openrouter, apiKey: { env: TEST_KEY } }\n" +
				"  rr: { type: openai, baseUrl: \"" + tt.url + "\", apiKey: { env: TEST_KEY } }\n" +
				"embedding:\n  model: or/voyage-code-3\n  dims: 1024\n  rerank: { model: rr/m, api: cohere }\n"))
			switch {
			case tt.ok && err != nil:
				t.Fatalf("Parse = %v", err)
			case !tt.ok && (err == nil || !strings.Contains(err.Error(), `embedding.rerank.model names provider "rr"`)):
				t.Fatalf("Parse = %v, want the reranker's provider refused", err)
			case !tt.ok && strings.Contains(err.Error(), "s3cr3t"):
				t.Fatalf("Parse = %v, want the URL left out", err)
			}
		})
	}
}
