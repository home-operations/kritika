package model

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Reranker scores documents by how well each answers a query.
type Reranker interface {
	// Rerank returns a score for each document, in their order, the higher
	// the better, and what the provider reports it spent.
	Rerank(ctx context.Context, query string, documents []string) (scores []float64, usage RerankUsage, err error)
}

// RerankUsage is what a rerank call spent, as its provider reports it:
// tokens, and the search units that Cohere bills a call by, a query of up
// to a hundred documents each, instead of or beside its tokens.
type RerankUsage struct {
	Tokens, SearchUnits int64
}

// The rerank APIs that HTTPReranker speaks. Each posts to "{base}/rerank".
const (
	// RerankCohere is Cohere's v2 rerank: {model, query, documents, top_n}
	// answered by {results: [{index, relevance_score}]}, and what it billed
	// in meta.billed_units, search_units or input_tokens, with the tokens
	// it read in meta.tokens.
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

// maxRerankUsage bounds a count of what one rerank call spent, as its
// provider reports it: far past what one call of the gateway's reads, and
// small enough that a request's calls cannot overflow their sum. The gateway
// settles the run's budget at the count, so one that is negative, not a whole
// number or past the bound is taken for none reported.
const maxRerankUsage = math.MaxUint32

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
// key sends none. A redirect is not followed: a 307 or 308 would send the
// query and the repository's code on to wherever it points, and one to
// plain HTTP on the same host the key too.
func NewReranker(api, baseURL, key, model string, timeout time.Duration) *HTTPReranker {
	return &HTTPReranker{
		api: api, url: strings.TrimSuffix(baseURL, "/") + "/rerank", key: key, model: model,
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Rerank implements Reranker. A document the answer leaves out scores -1,
// below every score the APIs give.
func (r *HTTPReranker) Rerank(ctx context.Context, query string, documents []string) ([]float64, RerankUsage, error) {
	body, err := json.Marshal(r.request(query, documents))
	if err != nil {
		return nil, RerankUsage{}, fmt.Errorf("model: rerank: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, RerankUsage{}, fmt.Errorf("model: rerank: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if r.key != "" {
		req.Header.Set("Authorization", "Bearer "+r.key)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, RerankUsage{}, fmt.Errorf("model: rerank: send the request: %w", transportError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	// A refusal's body can echo the query, a document or the key, and the
	// gateway logs the error, so it says the status code alone. resp.Status
	// is not that: it keeps the reason phrase that an HTTP/1.x server sent,
	// which can echo them as well.
	if resp.StatusCode != http.StatusOK {
		return nil, RerankUsage{}, fmt.Errorf("model: rerank: HTTP status %d", resp.StatusCode)
	}
	// TEI reports what a request computed in a header of its answer alone,
	// which stands when the body is cut short.
	var computed RerankUsage
	if n, err := strconv.ParseUint(resp.Header.Get("X-Compute-Tokens"), 10, 32); err == nil {
		computed.Tokens = int64(n)
	}
	// One byte past the bound tells an answer cut at it from one that ends
	// there: the part within the bound can be whole JSON of its own.
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxRerankAnswer+1))
	if err != nil {
		return nil, computed, fmt.Errorf("model: rerank: read the answer: %w", transportError(err))
	}
	if len(answer) > maxRerankAnswer {
		return nil, computed, fmt.Errorf("model: rerank: the answer is longer than %d bytes", maxRerankAnswer)
	}
	// An answer refused for its ranks still reports what the provider spent
	// on it, which the gateway charges the run.
	ranks, used, err := r.decode(answer)
	if used.Tokens == 0 {
		used.Tokens = computed.Tokens
	}
	if err != nil {
		return nil, used, fmt.Errorf("model: rerank: decode the answer: %w", err)
	}
	scores := make([]float64, len(documents))
	for i := range scores {
		scores[i] = -1
	}
	seen := make([]bool, len(documents))
	for _, rk := range ranks {
		if rk.Index < 0 || rk.Index >= len(documents) || seen[rk.Index] {
			return nil, used, fmt.Errorf("model: rerank: the answer has an invalid or repeated index %d", rk.Index)
		}
		seen[rk.Index] = true
		scores[rk.Index] = rk.Score
	}
	return scores, used, nil
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

// decode reads the ranks of the API's answer, each with the index and the
// score that the API names. An answer that ranks no document, or a rank
// without its index or score, is an error rather than a document that the
// answer leaves out or one that scores 0, so that the fused order stands.
func (r *HTTPReranker) decode(answer []byte) ([]rerankRank, RerankUsage, error) {
	type ranked struct {
		Index          *int     `json:"index"`
		RelevanceScore *float64 `json:"relevance_score"`
		Score          *float64 `json:"score"`
	}
	var ranks []ranked
	var used RerankUsage
	if r.api == RerankTEI {
		if err := json.Unmarshal(answer, &ranks); err != nil {
			return nil, used, decodeError(err)
		}
	} else {
		// The ranks and each usage count are decoded apart, so that a value of
		// the wrong type among them leaves the others reported.
		var out struct {
			Results json.RawMessage `json:"results"`
			Data    json.RawMessage `json:"data"`
			Usage   json.RawMessage `json:"usage"`
			Meta    json.RawMessage `json:"meta"`
		}
		if err := json.Unmarshal(answer, &out); err != nil {
			return nil, used, decodeError(err)
		}
		total, totalErr := usageAt(out.Usage, "total_tokens")
		billed, billedErr := usageAt(out.Meta, "billed_units", "input_tokens")
		units, unitsErr := usageAt(out.Meta, "billed_units", "search_units")
		tokens, tokensErr := usageAt(out.Meta, "tokens", "input_tokens")
		used = RerankUsage{Tokens: max(total, billed, tokens), SearchUnits: units}
		if errors.Join(totalErr, billedErr, unitsErr, tokensErr) != nil {
			return nil, used, errors.New("the answer's usage has a value of the wrong type")
		}
		field := out.Results
		if r.api == RerankVoyage {
			field = out.Data
		}
		// Absent, field is empty, and so are the ranks. The answer parsed, so
		// a failure is a value of the wrong type, at an offset into field
		// rather than the answer: none is given.
		if len(field) > 0 && json.Unmarshal(field, &ranks) != nil {
			return nil, used, errors.New("the answer's ranks have a value of the wrong type")
		}
	}
	if len(ranks) == 0 {
		return nil, used, errors.New("the answer ranks no document")
	}
	out := make([]rerankRank, len(ranks))
	for i, rk := range ranks {
		score := rk.RelevanceScore
		if r.api == RerankTEI {
			score = rk.Score
		}
		switch {
		case rk.Index == nil:
			return nil, used, fmt.Errorf("rank %d is without an index", i)
		case score == nil:
			return nil, used, fmt.Errorf("rank %d is without a score", i)
		}
		out[i] = rerankRank{Index: *rk.Index, Score: *score}
	}
	return out, used, nil
}

// usageAt is the count at path in raw, an object of the answer: 0 when
// it is absent or null, and an error when a value on the way to it, or the
// count itself, is of the wrong type. Its keys match exactly, as every
// provider names them.
func usageAt(raw json.RawMessage, path ...string) (int64, error) {
	for _, key := range path {
		if len(raw) == 0 {
			return 0, nil
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return 0, err
		}
		raw = obj[key]
	}
	if len(raw) == 0 {
		return 0, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return usageCount(n), nil
}

// usageCount is n, a count of what a rerank call spent, when it is a whole
// number from 0 to maxRerankUsage, and 0, none reported, otherwise, absent
// included.
func usageCount(n json.Number) int64 {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || f < 0 || f > maxRerankUsage || f != math.Trunc(f) {
		return 0
	}
	return int64(f)
}

// transportError is err, from sending a rerank request or reading its
// answer, as one that names neither the endpoint nor anything the provider
// sent: net/http quotes both, a Location header that does not parse, say,
// or a malformed header line, and a provider can echo the query, a document
// or a key into them, and the gateway logs the error. A cancellation or a
// deadline stays one, a client timeout included.
func transportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return errors.New("the endpoint's certificate does not verify")
	}
	if oe, ok := errors.AsType[*net.OpError](err); ok && oe.Op == "dial" {
		return errors.New("the endpoint cannot be reached")
	}
	return errors.New("the endpoint's answer failed in transport")
}

// decodeError is err, from decoding a rerank answer, without what
// encoding/json quotes of the answer in it, a value or a character: a
// provider can echo the query, a document or a key into any field of its
// answer, and the gateway logs the error.
func decodeError(err error) error {
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Errorf("the answer is not JSON at byte %d", se.Offset)
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Offset > 0 {
		return fmt.Errorf("the answer has a value of the wrong type at byte %d", te.Offset)
	}
	return errors.New("the answer has a value of the wrong type")
}
