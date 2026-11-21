package configfile

import (
	"cmp"
	"time"

	"github.com/home-operations/kritika/internal/model"
)

// Embedding is the instance's embedder, which builds the similar-code
// index: a model of one of the instance's openrouter or openai providers,
// on its endpoint and key. There is one per instance because the index has
// one vector dimension. Unset, indexing is off and reviews run without
// vector retrieval.
type Embedding struct {
	// Ref is the model as the configuration names it, "<provider>/<model>".
	Ref ModelRef `yaml:"model"`
	// Dims is the vector dimension, which shapes the index table: a change
	// of it or of the model rebuilds every repository's index.
	Dims int `yaml:"dims"`
	// MaxBatch, MaxBatchChars and MaxItemChars bound one request: inputs,
	// characters, and characters per input, beyond which an input is cut.
	// OpenAI-compatible servers differ widely in what they accept. Unset,
	// each is a default conservative enough for the common ones.
	MaxBatch      int `yaml:"maxBatch,omitempty"`
	MaxBatchChars int `yaml:"maxBatchChars,omitempty"`
	MaxItemChars  int `yaml:"maxItemChars,omitempty"`
	// SimilarFloor is the cosine similarity an index chunk needs to count
	// as similar code, for the review's similar-code stage and the agent's
	// search_code tool alike. Where useful matches part from noise depends
	// on the model, its prompts and the repository. Unset is
	// DefaultSimilarFloor, which only fits inputs without prompts.
	SimilarFloor float64 `yaml:"similarFloor,omitempty"`
	// SimilarMargin is how far below the best match of its query, in cosine
	// similarity, a chunk can score and still count as similar code. A
	// query's scores sit at a height set by the code it is near. That height
	// differs between repositories and between the hunks of one diff. So the
	// margin follows each query's best where a floor cannot. Unset is
	// DefaultSimilarMargin, and 0 keeps only each query's best.
	SimilarMargin *float64 `yaml:"similarMargin,omitempty"`
	// QueryPrompt and DocumentPrompt go, exactly as written, in front of
	// each similar-code query and each indexed chunk: the task prompts many
	// embedding models are trained with. Unset, inputs go as they are.
	// DocumentPrompt shapes the stored vectors, so a change of it rebuilds
	// every repository's index, as a change of the model does.
	QueryPrompt    string `yaml:"queryPrompt,omitempty"`
	DocumentPrompt string `yaml:"documentPrompt,omitempty"`
	// SearchPrompt goes in front of each search_code query in place of
	// QueryPrompt: such a query mostly describes code, where a review's is
	// code, and instruction-tuned embedders want a task for each. Unset, it
	// is QueryPrompt.
	SearchPrompt string `yaml:"searchPrompt,omitempty"`
	// Lexical searches the index by name beside the vector search: the
	// identifiers of a review's hunk, or the words of a search_code query,
	// against the identifiers of each chunk, the rarer ones weighing more.
	// The two searches' rankings are fused. A change's code is full of
	// names, which an embedder can score low for code that does something
	// else with them. Unset is DefaultLexicalSearch.
	Lexical *bool `yaml:"lexical,omitempty"`
	// Rerank reorders each query's candidates with a reranking model
	// before the answer is picked from them. Unset, nothing is reranked.
	Rerank *Rerank `yaml:"rerank,omitempty"`

	// BaseURL, Model and the key are the provider's, resolved at load:
	// its endpoint, the model's id on it, and its key.
	BaseURL string `yaml:"-"`
	Model   string `yaml:"-"`
	apiKey  Secret
}

// APIKeyValue returns the resolved API key.
func (e *Embedding) APIKeyValue() Secret { return e.apiKey }

// PromptFor is the prompt of a search_code query when search is true, and
// of a review's hunk query otherwise.
func (e *Embedding) PromptFor(search bool) string {
	if search {
		return cmp.Or(e.SearchPrompt, e.QueryPrompt)
	}
	return e.QueryPrompt
}

// MaxEmbedDims is the largest dimension the index's halfvec column takes.
const MaxEmbedDims = 4000

// Bounds returns MaxBatch, MaxBatchChars and MaxItemChars, each the
// embedder's default when unset.
func (e *Embedding) Bounds() (batch, batchChars, itemChars int) {
	return cmp.Or(e.MaxBatch, model.DefaultEmbedMaxBatch), cmp.Or(e.MaxBatchChars, model.DefaultEmbedMaxBatchChars),
		cmp.Or(e.MaxItemChars, model.DefaultEmbedMaxItemChars)
}

// DefaultSimilarFloor is the similar-code floor when the embedder sets
// none.
const DefaultSimilarFloor = 0.5

// Floor returns SimilarFloor, DefaultSimilarFloor when unset.
func (e *Embedding) Floor() float64 { return cmp.Or(e.SimilarFloor, DefaultSimilarFloor) }

// DefaultSimilarMargin is the similar-code margin when the embedder sets
// none: wide enough to keep the matches close to a query's best and drop
// only those well below it.
const DefaultSimilarMargin = 0.1

// Margin returns SimilarMargin, DefaultSimilarMargin when unset.
func (e *Embedding) Margin() float64 {
	if e.SimilarMargin == nil {
		return DefaultSimilarMargin
	}
	return *e.SimilarMargin
}

// Rerank is the similar-code search's reranker: a model of an openai
// provider of the instance, at that provider's baseUrl and with its key,
// over one of the rerank APIs (model.RerankAPIs). The provider stands for
// any endpoint that takes a bearer key, a self-hosted one included, and
// sets baseUrl, as no rerank API is at the openai type's default.
type Rerank struct {
	// Ref is the model as the configuration names it, "<provider>/<model>".
	Ref ModelRef `yaml:"model"`
	// API is the shape of the endpoint's requests and answers, one of
	// model.RerankAPIs.
	API string `yaml:"api"`
	// Candidates is how many of each query's candidates, the best fused
	// first, are sent to be reranked, at most MaxRerankCandidates. The two
	// searches each look for that many. Unset is DefaultRerankCandidates, or
	// DefaultTEIRerankCandidates for the tei API.
	Candidates int `yaml:"candidates,omitempty"`
	// Timeout bounds the reranking of one similar-code request, all of its
	// queries, at most MaxRerankTimeout. A query not reranked by then keeps
	// the fused order. Unset is DefaultRerankTimeout.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// BaseURL, Model and the key are the provider's, resolved at load.
	BaseURL string `yaml:"-"`
	Model   string `yaml:"-"`
	apiKey  Secret
}

// APIKeyValue returns the resolved API key.
func (r *Rerank) APIKeyValue() Secret { return r.apiKey }

// Reranker bounds and defaults. A request reranks each of its queries in
// turn, after the wait for a slot, so the timeout covers them all. A run
// waits a minute for its answer, of which the timeout leaves at least half
// to the embedding, its slot and the searches.
const (
	DefaultRerankCandidates = 50
	MaxRerankCandidates     = 100
	DefaultRerankTimeout    = 15 * time.Second
	MaxRerankTimeout        = 30 * time.Second
)

// DefaultTEIRerankCandidates is the candidates sent to a Text Embeddings
// Inference reranker when unset: the most that its --max-client-batch-size
// takes by default, which refuses a request of more with a 422.
const DefaultTEIRerankCandidates = 32

// CandidateCount returns Candidates when set, DefaultTEIRerankCandidates for
// the tei API, and DefaultRerankCandidates otherwise.
func (r *Rerank) CandidateCount() int {
	if r.Candidates == 0 && r.API == model.RerankTEI {
		return DefaultTEIRerankCandidates
	}
	return cmp.Or(r.Candidates, DefaultRerankCandidates)
}

// Deadline returns Timeout, DefaultRerankTimeout when unset.
func (r *Rerank) Deadline() time.Duration { return cmp.Or(r.Timeout, DefaultRerankTimeout) }

// DefaultLexicalSearch is whether the similar-code search also searches by
// name when the embedder does not say.
const DefaultLexicalSearch = true

// LexicalSearch returns Lexical, DefaultLexicalSearch when unset.
func (e *Embedding) LexicalSearch() bool {
	if e.Lexical == nil {
		return DefaultLexicalSearch
	}
	return *e.Lexical
}
