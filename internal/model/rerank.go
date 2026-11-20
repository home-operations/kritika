package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/textcut"
)

// Reranker scores documents by how well each answers a query.
type Reranker interface {
	// Rerank returns a score for each document, in their order, the higher
	// the better, and the tokens that the provider reports it spent.
	Rerank(ctx context.Context, query string, documents []string) (scores []float64, tokens int64, err error)
}

// The rerank APIs that HTTPReranker speaks. Each posts to "{base}/rerank".
const (
	// RerankCohere is Cohere's v2 rerank: {model, query, documents, top_n}
	// answered by {results: [{index, relevance_score}]}.
	RerankCohere = "cohere"
	// RerankJina is Jina's, Cohere's shape with return_documents, which Jina
	// defaults to true, turned off.
	RerankJina = "jina"
	// RerankVoyage is Voyage AI's: top_k for top_n, answered by
	// {data: [{index, relevance_score}]}.
	RerankVoyage = "voyage"
	// RerankTEI is Hugging Face Text Embeddings Inference's: {query, texts}
	// answered by [{index, score}].
	RerankTEI = "tei"
)

// RerankAPIs are the APIs NewReranker takes.
var RerankAPIs = []string{RerankCohere, RerankJina, RerankVoyage, RerankTEI}

// maxRerankAnswer bounds a rerank answer, which carries a score, and with
// some APIs a document, for each document sent.
const maxRerankAnswer = 8 << 20

// HTTPReranker is a Reranker over one of the RerankAPIs.
type HTTPReranker struct {
	api, url, key, model string
	client               *http.Client
}

// NewReranker builds a reranker of api at baseURL, the API root that
// "/rerank" goes under, whose every request is bounded by timeout. An empty
// key sends none.
func NewReranker(api, baseURL, key, model string, timeout time.Duration) *HTTPReranker {
	return &HTTPReranker{
		api: api, url: strings.TrimSuffix(baseURL, "/") + "/rerank", key: key, model: model,
		client: &http.Client{Timeout: timeout},
	}
}

// Rerank implements Reranker. A document the answer leaves out scores -1,
// below every score the APIs give.
func (r *HTTPReranker) Rerank(ctx context.Context, query string, documents []string) ([]float64, int64, error) {
	body, err := json.Marshal(r.request(query, documents))
	if err != nil {
		return nil, 0, fmt.Errorf("model: rerank: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("model: rerank: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if r.key != "" {
		req.Header.Set("Authorization", "Bearer "+r.key)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("model: rerank: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxRerankAnswer))
	if err != nil {
		return nil, 0, fmt.Errorf("model: rerank: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("model: rerank: %s: %s", resp.Status, textcut.Prefix(string(bytes.TrimSpace(answer)), 512))
	}
	ranks, tokens, err := r.decode(answer)
	if err != nil {
		return nil, 0, fmt.Errorf("model: rerank: decode the answer: %w", err)
	}
	if tokens == 0 {
		// TEI reports what a request computed in a header of its answer
		// alone.
		tokens, _ = strconv.ParseInt(resp.Header.Get("X-Compute-Tokens"), 10, 64)
	}
	scores := make([]float64, len(documents))
	for i := range scores {
		scores[i] = -1
	}
	seen := make([]bool, len(documents))
	for _, rk := range ranks {
		if rk.Index < 0 || rk.Index >= len(documents) || seen[rk.Index] {
			return nil, tokens, fmt.Errorf("model: rerank: the answer has an invalid or repeated index %d", rk.Index)
		}
		seen[rk.Index] = true
		scores[rk.Index] = rk.Score
	}
	return scores, tokens, nil
}

type rerankRequest struct {
	Model           string   `json:"model,omitempty"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents,omitempty"`
	Texts           []string `json:"texts,omitempty"`
	TopN            int      `json:"top_n,omitempty"`
	TopK            int      `json:"top_k,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
	Truncation      *bool    `json:"truncation,omitempty"`
	Truncate        *bool    `json:"truncate,omitempty"`
}

func (r *HTTPReranker) request(query string, documents []string) rerankRequest {
	yes, no := true, false
	switch r.api {
	case RerankJina:
		return rerankRequest{Model: r.model, Query: query, Documents: documents, TopN: len(documents), ReturnDocuments: &no}
	case RerankVoyage:
		return rerankRequest{Model: r.model, Query: query, Documents: documents, TopK: len(documents), Truncation: &yes}
	case RerankTEI:
		return rerankRequest{Query: query, Texts: documents, Truncate: &yes}
	}
	return rerankRequest{Model: r.model, Query: query, Documents: documents, TopN: len(documents)}
}

type rerankRank struct {
	Index int
	Score float64
}

func (r *HTTPReranker) decode(answer []byte) ([]rerankRank, int64, error) {
	type ranked struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Score          float64 `json:"score"`
	}
	var ranks []ranked
	var tokens float64
	if r.api == RerankTEI {
		if err := json.Unmarshal(answer, &ranks); err != nil {
			return nil, 0, err
		}
	} else {
		var out struct {
			Results []ranked `json:"results"`
			Data    []ranked `json:"data"`
			Usage   struct {
				TotalTokens float64 `json:"total_tokens"`
			} `json:"usage"`
			Meta struct {
				BilledUnits struct {
					InputTokens float64 `json:"input_tokens"`
				} `json:"billed_units"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(answer, &out); err != nil {
			return nil, 0, err
		}
		ranks = append(out.Results, out.Data...)
		tokens = max(out.Usage.TotalTokens, out.Meta.BilledUnits.InputTokens)
	}
	out := make([]rerankRank, len(ranks))
	for i, rk := range ranks {
		out[i] = rerankRank{Index: rk.Index, Score: rk.RelevanceScore}
		if r.api == RerankTEI {
			out[i].Score = rk.Score
		}
	}
	return out, int64(tokens), nil
}
