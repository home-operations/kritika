// Package gateway is the worker's listener for runner pods: the egress
// proxy they reach the outside through, and the model, similar-code and
// conversation endpoints a review's runner calls with its run token. No
// provider key enters a runner pod: the gateway reserves each call
// against the run's budget and checks the account's monthly cap, answers
// it through the account's provider, and records what it spent where the
// caps see it.
package gateway

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// ModelName is the name a runner calls its run's model by; the gateway maps
// it to the provider model the run was granted.
const ModelName = "review"

// Drain is how long a stopping gateway lets model steps in flight finish.
// A step it cuts is paid for and not recorded, and the runner's retry is
// paid for again, so it covers a long step rather than the usual few
// seconds; the chart's grace period outlasts it.
const Drain = 2 * time.Minute

// Server serves the gateway listener.
type Server struct {
	Store   *store.Store
	Current *configfile.Current
	Logger  *slog.Logger
	// Metrics may be nil.
	Metrics *metrics.Metrics
	// Proxy serves CONNECT and absolute-URI requests.
	Proxy    http.Handler
	Steppers *adapter.Steppers
	// Embedders resolves the instance's embedder for similar code; nil
	// serves none.
	Embedders *adapter.Embedders
}

// maxBody bounds one step's request: the whole conversation so far, every
// tool output in it capped.
const maxBody = 16 << 20

// MaxStepOutput caps the answer to one step, whatever the runner asks: the
// agent loop's own cap.
var MaxStepOutput = agent.DefaultLimits.MaxOutputTokensPerStep

// detachTimeout bounds the writes that settle a step once the provider has
// answered, which must land even if the request's ctx ends.
const detachTimeout = 2 * time.Minute

// detach is ctx without its cancellation, bounded by detachTimeout.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachTimeout)
}

// ServeHTTP implements http.Handler.
func (g *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect || r.URL.IsAbs():
		g.Proxy.ServeHTTP(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		g.chat(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/similar":
		g.similarCode(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/conversation":
		g.conversation(w, r)
	default:
		http.NotFound(w, r)
	}
}

// stepPart is the part of a split review a step is for, as its runner
// names it in model.PartHeader: 0 when it names none.
func stepPart(h string) (int, error) {
	if h == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(h, 10, 32)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s %q is not a part number", model.PartHeader, h)
	}
	return int(n), nil
}

// promptEstimate is a step's prompt at four characters a token of its
// request, leaving out replayed ChatGPT output: it repeats its turn's text
// and calls, and its encrypted reasoning is no measure of tokens.
func promptEstimate(body []byte, req model.StepRequest) int64 {
	size := len(body)
	for _, m := range req.Messages {
		if m.ChatGPTOutput != nil {
			for _, item := range m.ChatGPTOutput.Items {
				size -= len(item)
			}
		}
	}
	return int64(max(size, 0)) / 4
}

// refuse answers a step with an error. Only a 500, the gateway's own
// trouble reaching its database, is worth the runner's retry; every other
// refusal is final: the provider was already retried, the budget is spent,
// or the request is wrong.
func refuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusInternalServerError {
		w.Header().Set("X-Should-Retry", "false")
	}
	w.WriteHeader(status)
	_, _ = w.Write(model.EncodeChatError(code, message))
}

// runCall is a runner's request to the gateway, authenticated by its run
// token.
type runCall struct {
	token   string
	grant   store.GatewayGrant
	file    *configfile.File
	account *configfile.Account
	logger  *slog.Logger
}

// role is the usage role c's run spends as.
func (c runCall) role() string {
	if c.grant.FollowupCommentID != 0 {
		return store.RoleFollowUp
	}
	return store.RoleReview
}

// usageReview is the review c's spend is charged to: a follow-up's is
// charged to none.
func (c runCall) usageReview() string {
	if c.grant.FollowupCommentID != 0 {
		return ""
	}
	return c.grant.ReviewID
}

// admit authenticates r by its run token, finds the run's account in the
// configuration and checks the account's monthly cap, or refuses r and
// reports false.
func (g *Server) admit(w http.ResponseWriter, r *http.Request) (runCall, bool) {
	ctx := r.Context()
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	grant, err := g.Store.LookupGatewayToken(ctx, token)
	if errors.Is(err, store.ErrGatewayToken) {
		refuse(w, http.StatusUnauthorized, "invalid_token", "the run token is not valid")
		return runCall{}, false
	}
	if err != nil {
		g.Logger.Error("gateway: token lookup failed", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the run token could not be checked")
		return runCall{}, false
	}
	file := g.Current.Get()
	account, found := file.AccountByID(grant.AccountID)
	if !found {
		refuse(w, http.StatusForbidden, "invalid_token", "the run's account is not in the configuration")
		return runCall{}, false
	}
	logger := g.Logger.With("account", account.Key(), "run", review.ShortSHA(grant.RunID))
	// A step spends tokens, not a review, so only the month's cap applies.
	limits := file.Settings(account, "").Limits
	capped, err := g.Store.CapReached(ctx, account.ID(), configfile.Limits{TokensPerMonth: limits.TokensPerMonth})
	if err != nil {
		logger.Error("gateway: caps not read", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the caps could not be checked")
		return runCall{}, false
	}
	if capped != "" {
		logger.Info("gateway: call refused", "reason", capped)
		refuse(w, http.StatusTooManyRequests, model.BudgetCode, capped)
		return runCall{}, false
	}
	return runCall{token: token, grant: grant, file: file, account: account, logger: logger}, true
}

// reserve reserves tokens against c's run budget before the call spends
// them, or refuses the request and reports false. Each reservation is
// visible to the next at once, so concurrent calls cannot all pass a
// budget one of them spends.
func (g *Server) reserve(ctx context.Context, w http.ResponseWriter, c runCall, tokens int64) bool {
	ok, err := g.Store.ReserveGatewayTokens(ctx, c.token, tokens)
	if err != nil {
		c.logger.Error("gateway: call not reserved", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the run's budget could not be checked")
		return false
	}
	if !ok {
		reason := fmt.Sprintf("the run's budget of %d tokens is spent", c.grant.Budget)
		c.logger.Info("gateway: call refused", "reason", reason)
		refuse(w, http.StatusTooManyRequests, model.BudgetCode, reason)
		return false
	}
	return true
}

func (g *Server) chat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := g.admit(w, r)
	if !ok {
		return
	}
	part, err := stepPart(r.Header.Get(model.PartHeader))
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		refuse(w, http.StatusRequestEntityTooLarge, "invalid_request", err.Error())
		return
	}
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", "reading the request: "+err.Error())
		return
	}
	req, err := model.DecodeChatRequest(body)
	if err != nil {
		refuse(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Model != ModelName {
		refuse(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("the model is %q, not %q", ModelName, req.Model))
		return
	}

	ref := configfile.ModelRef(c.grant.Model)
	route, err := g.Steppers.Route(c.file, c.account, ref)
	if err != nil {
		c.logger.Error("gateway: no model adapter", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the run's model is not configured")
		return
	}
	// A run's steps are one conversation, whichever provider answers, and
	// one that carries on an earlier run's keeps its session; a
	// follow-up's is its mention's, which a retried run carries on. The
	// effort is the grant's, as the model is: a runner chooses neither.
	req.Model, req.Fallbacks, req.Session, req.Effort = ref.Model(), nil, cmp.Or(c.grant.Session, c.grant.RunID), model.Effort(c.grant.Effort)
	if id := c.grant.FollowupCommentID; id != 0 {
		req.Session = "followup-" + strconv.FormatInt(id, 10)
	}
	if part > 0 {
		// Each part of a split review is a conversation of its own.
		req.Session += "/" + strconv.Itoa(part)
	}
	call := adapter.Call{Route: route, Halve: true, Failed: func(err error, on, next adapter.Route) {
		if next.Ref != on.Ref {
			c.logger.Warn("gateway: step failed on the review model; trying the fallback", "fallback", next.Ref,
				"error", maskProvider(err.Error(), on.Provider))
			return
		}
		c.logger.Warn("gateway: step failed; trying again", "error", maskProvider(err.Error(), on.Provider))
	}}
	switch fb := configfile.ModelRef(c.grant.Fallback); {
	case fb == "":
	case fb.Provider() == ref.Provider():
		req.Fallbacks = []string{fb.Model()}
	default:
		// The review model's attempts spent, a fallback on another
		// provider gets the same step, unless the configuration no longer
		// has it, which the step then does without.
		if fallback, err := g.Steppers.Route(c.file, c.account, fb); err != nil {
			c.logger.Error("gateway: no adapter for the fallback", "fallback", fb, "error", err)
		} else {
			call.Fallback = &fallback
		}
	}
	if req.MaxTokens <= 0 || req.MaxTokens > MaxStepOutput {
		req.MaxTokens = MaxStepOutput
	}
	// The step is reserved before it runs; its actual spend replaces the
	// estimate once the provider answers.
	reserved := promptEstimate(body, req) + req.MaxTokens
	if !g.reserve(ctx, w, c, reserved) {
		return
	}
	start := time.Now()
	// The step, its retries and its fallback share one budget, inside
	// which the runner waits for the answer. With a fallback on another
	// provider, the review model's attempts get at most half of it, so a
	// model that hangs, or a long Retry-After, still leaves the fallback a
	// turn.
	sctx, cancel := context.WithTimeout(ctx, model.GatewayStepBudget)
	defer cancel()
	resp, attempts, served, err := call.Do(sctx, req)
	took := time.Since(start)
	g.Metrics.ModelCall(c.account.Key(), adapter.ServedRef(served.Ref, resp.Model), c.role(), adapter.Outcome(err), resp.Usage.Prompt(),
		resp.Usage.CacheRead, resp.Usage.Output, resp.CostUSD)
	if cerr := g.charge(ctx, c, reserved, resp, err == nil); cerr != nil {
		// A step that was answered is paid for either way; the run still
		// gets the answer.
		c.logger.Error("gateway: step not charged", "error", cerr)
	}
	// Recorded before the runner gets its answer, so the next step's delta
	// is taken against this one; the recorder bounds how long it waits. A
	// failed step's response names no model: it is recorded under the one
	// it last went to.
	req.Model = served.Ref.Model()
	adapter.Recorder{Store: g.Store, Metrics: g.Metrics}.Record(ctx, c.logger, store.ModelCall{
		AccountID: c.grant.AccountID, ReviewID: c.grant.ReviewID, RunnerRunID: c.grant.RunID, FollowupCommentID: c.grant.FollowupCommentID,
		Carries: c.grant.Continues, Kind: store.ModelCallAgentStep, Part: part, Duration: took,
	}, req, resp, err, adapter.Mask(c.file, served.Provider, c.token))
	if err != nil {
		// The provider's error goes to a pod that reads untrusted content;
		// it must not carry the key, or credentials in the provider's URL,
		// if the provider or the SDK echoed them.
		msg := maskProvider(err.Error(), served.Provider)
		c.logger.Warn("gateway: step failed", "error", msg)
		status, code := upstreamStatus(err)
		refuse(w, status, code, msg)
		return
	}
	c.logger.Debug("gateway: step", "model", resp.Model, "attempts", attempts, "input_tokens", resp.Usage.Prompt(),
		"output_tokens", resp.Usage.Output, "cost_usd", resp.CostUSD)
	out, err := model.EncodeChatResponse(c.grant.RunID, resp)
	if err != nil {
		refuse(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// conversation serves a review's run the conversation its token lets it
// carry on, as the runner that kept it encoded it. That is the text the
// provider was sent, unmasked, which a cache hit needs, so one that holds
// the provider's key, credentials in its URL, an egress credential or the
// run's token is not served, nor one kept under a model other than the
// run's. Not found refuses the run nothing: it starts afresh.
func (g *Server) conversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := g.admit(w, r)
	if !ok {
		return
	}
	if c.grant.Continues == "" {
		refuse(w, http.StatusNotFound, "not_found", "the run carries on no conversation")
		return
	}
	var text, kept string
	err := g.Store.WithAccount(ctx, c.grant.AccountID, func(tx pgx.Tx) error {
		var err error
		text, kept, err = store.Conversation(ctx, tx, c.grant.Continues)
		return err
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		refuse(w, http.StatusNotFound, "not_found", "the conversation is no longer kept")
		return
	case err != nil:
		c.logger.Error("gateway: conversation not read", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the conversation could not be read")
		return
	case kept != c.grant.Model:
		refuse(w, http.StatusNotFound, "not_found", "the conversation was kept under another model")
		return
	}
	provider, _ := c.file.Provider(c.account, configfile.ModelRef(c.grant.Model).Provider())
	if adapter.Mask(c.file, provider, c.token)(text) != text {
		c.logger.Warn("gateway: conversation not served: it holds a secret", "continues", review.ShortSHA(c.grant.Continues))
		refuse(w, http.StatusNotFound, "not_found", "the conversation is not served")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(text))
}

// upstreamStatus is the status and code a step its provider failed is
// refused with. They tell the runner's loop apart what model.Transient
// does: a provider's outage, which it may wait out and send the step again,
// from a provider's refusal of the request, which would fail the same way.
func upstreamStatus(err error) (int, string) {
	if model.Transient(err) {
		return http.StatusBadGateway, "upstream_error"
	}
	return http.StatusUnprocessableEntity, "upstream_refused"
}

// charge settles a step's reservation: an answered step's actual spend
// replaces it and is recorded against the run's review, or as a
// follow-up's, where the caps count it; a failed step is refunded. The two
// writes are independent, so a failed usage row still leaves the run's
// budget charged.
func (g *Server) charge(ctx context.Context, c runCall, reserved int64, resp model.StepResponse, answered bool) error {
	ctx, cancel := detach(ctx)
	defer cancel()
	if !answered {
		return g.Store.ChargeGatewayToken(ctx, c.token, -reserved)
	}
	grant := c.grant
	spent := resp.Usage.Prompt() + resp.Usage.Output
	budgetErr := g.Store.ChargeGatewayToken(ctx, c.token, spent-reserved)
	usageErr := g.Store.WithAccount(ctx, grant.AccountID, func(tx pgx.Tx) error {
		return store.InsertUsage(ctx, tx, store.Usage{
			AccountID: grant.AccountID, RepositoryID: grant.RepositoryID, ReviewID: c.usageReview(), Role: c.role(), Model: resp.Model,
			Upstream: resp.Upstream, Input: resp.Usage.Prompt(), Output: resp.Usage.Output, CostUSD: resp.CostUSD,
			ChatGPTPlan: resp.ChatGPTPlan, RunnerRunID: grant.RunID,
		})
	})
	return errors.Join(budgetErr, usageErr)
}

// maskProvider removes a provider's key, and any credentials in its base
// URL, from text bound for a runner.
func maskProvider(text string, p configfile.Provider) string {
	for _, s := range adapter.ProviderSecrets(p) {
		if s != "" {
			text = strings.ReplaceAll(text, s, "***")
		}
	}
	return text
}
