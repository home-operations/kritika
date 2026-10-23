// Package ingest is the only surface a forge reaches. It looks the
// connection up by hook path, verifies the signature with that
// connection's secret, parses the payload into a forge-neutral event, and
// hands it to a Dispatcher, or, for a GitHub App just installed on an
// account no connection serves, to an Uninstaller. It never does work
// itself.
package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/metrics"
	"github.com/home-operations/kritika/internal/webhook"
)

// Request is a verified, parsed webhook with the configuration it applies to.
type Request struct {
	File    *configfile.File
	Account *configfile.Account
	Event   webhook.Event
}

// Outcome is what the dispatcher did with a request, for the response and
// the log line.
type Outcome struct {
	// Status is enqueued, recorded, skipped or ignored.
	Status string
	// Reason explains a skip or ignore, such as filter, disabled,
	// duplicate, not-default-branch, no-mention or action, and for an
	// installation event is its action.
	Reason string
	Job    string
}

// Outcome statuses.
const (
	Enqueued = "enqueued"
	// Recorded is an event applied as it arrived, with no job queued.
	Recorded = "recorded"
	Skipped  = "skipped"
	Ignored  = "ignored"
)

// Dispatcher turns a request into rows and jobs. The store-backed
// implementation is Service; tests use a fake.
type Dispatcher interface {
	Dispatch(ctx context.Context, req Request) (Outcome, error)
}

// DeliveryRecorder notes that a connection's webhook delivered a request
// kritika verified, or one with no signature, which a GitHub App with no
// webhook secret sends. The store-backed implementation is Service.
type DeliveryRecorder interface {
	RecordDelivery(ctx context.Context, connectionID string) error
	RecordUnsigned(ctx context.Context, connectionID string) error
}

// Authors reads who opened a pull request from the forge, for a webhook
// that does not name them: GitLab's names the author by id alone.
type Authors interface {
	PullRequestAuthor(ctx context.Context, in *configfile.Connection, repo string, number int) (login string, bot bool, err error)
}

// Uninstaller removes a GitHub App's installation from an account no
// connection serves, and audits it. The store-backed implementation is
// Service.
type Uninstaller interface {
	Uninstall(ctx context.Context, in *configfile.Connection, account string, installation int64) error
}

// dispatchTimeout bounds one dispatch, which does not end with its request.
const dispatchTimeout = 30 * time.Second

// uninstallBurst and uninstallEvery bound the uninstalls one replica
// makes. Each is a call with the App's own credential, which also mints
// the served accounts' tokens, and an audit row, so a stranger installing
// the App over and over must not spend either without limit. Past the
// bound an installation is ignored as any other webhook for an undeclared
// account is, and stays until an admin removes it or it is made again.
const (
	uninstallBurst = 5
	uninstallEvery = time.Minute
)

// installationFresh is how old an installation may be when its event
// arrives and still be removed. GitHub delivers it within seconds, but the
// App's owner can redeliver it for days, and one redelivered after its
// account was dropped from the configuration is the operator's.
const installationFresh = 10 * time.Minute

// Handler serves POST /hooks/{connection}.
type Handler struct {
	current *configfile.Current
	disp    Dispatcher
	logger  *slog.Logger
	// Metrics may be nil.
	Metrics *metrics.Metrics
	// Deliveries may be nil.
	Deliveries DeliveryRecorder
	// Authors may be nil, when no connection's webhooks leave a pull
	// request's author out.
	Authors Authors
	// Uninstaller may be nil, which leaves a GitHub App installed on an
	// account no connection serves for an admin to remove.
	Uninstaller Uninstaller

	uninstalls *rate.Limiter
}

// NewHandler builds the hook handler over the current configuration.
func NewHandler(current *configfile.Current, disp Dispatcher, logger *slog.Logger) *Handler {
	return &Handler{
		current: current, disp: disp, logger: logger,
		uninstalls: rate.NewLimiter(rate.Every(uninstallEvery), uninstallBurst),
	}
}

// ServeHTTP verifies, parses and dispatches. Status codes: 404 for an
// unknown connection, 401 for a missing or bad signature, 400 for an unparsable
// payload, 413 for an oversized one, 204 for a ping, 202 for anything
// accepted (enqueued, skipped or ignored: the forge only needs to know the
// delivery landed), 500 when the dispatcher failed and the forge should
// redeliver.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("connection")
	file := h.current.Get()
	in, ok := file.Connection(name)
	if !ok {
		// The name is the caller's, not ours: labelling by it would let any
		// request mint a new series.
		h.Metrics.Webhook("", "unknown_connection")
		http.Error(w, "unknown connection", http.StatusNotFound)
		return
	}
	logger := h.logger.With("connection", name)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhook.MaxBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.Metrics.Webhook(name, "too_large")
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		h.Metrics.Webhook(name, "unreadable")
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if err := webhook.Verify(in.Forge, in.WebhookSecretValue().Value(), r.Header, body); err != nil {
		logger.Warn("webhook rejected", "error", err, "remote", r.RemoteAddr)
		outcome := "unauthorized"
		// A delivery with no signature at all comes from an App with no
		// webhook secret, which the dashboard says to set. Only one the forge
		// sent says so: the hook path is public, and a bare POST from anyone
		// else must not raise that alarm.
		if errors.Is(err, webhook.ErrMissingSignature) && webhook.Delivered(in.Forge, r.Header) {
			outcome = "unsigned"
			if h.Deliveries != nil {
				if err := h.Deliveries.RecordUnsigned(r.Context(), in.ID()); err != nil {
					logger.Warn("unsigned webhook not recorded", "error", err)
				}
			}
		}
		h.Metrics.Webhook(name, outcome)
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}
	// Any verified delivery shows the forge's webhook is set up, whatever
	// becomes of the event.
	if h.Deliveries != nil {
		if err := h.Deliveries.RecordDelivery(r.Context(), in.ID()); err != nil {
			logger.Warn("webhook delivery not recorded", "error", err)
		}
	}
	ev, err := webhook.Parse(in.Forge, r.Header, body)
	if err != nil {
		logger.Warn("webhook unparsable", "error", err)
		h.Metrics.Webhook(name, "unparsable")
		http.Error(w, "unparsable payload", http.StatusBadRequest)
		return
	}
	logger = logger.With("delivery", ev.Delivery, "kind", ev.Kind, "action", ev.Action)

	var account *configfile.Account
	if ev.Account != "" && in.Serves(ev.Account) {
		account, _ = file.Account(in.Forge, ev.Account)
	}
	switch {
	case ev.Kind == webhook.KindPing:
		h.Metrics.Webhook(name, "ping")
		w.WriteHeader(http.StatusNoContent)
		return
	case ev.Kind == webhook.KindIgnored:
		logger.Debug("webhook ignored")
		h.Metrics.Webhook(name, Ignored)
		w.WriteHeader(http.StatusAccepted)
		return
	case account == nil && h.Uninstaller != nil && newInstallation(ev):
		h.Metrics.Webhook(name, h.uninstall(r.Context(), logger, in, ev))
		w.WriteHeader(http.StatusAccepted)
		return
	case account == nil:
		// A public App can be installed by anyone; only the declared
		// accounts are served. Accepted, so the forge does not retry.
		logger.Warn("webhook for an undeclared account ignored", "account", ev.Account)
		h.Metrics.Webhook(name, "undeclared_account")
		w.WriteHeader(http.StatusAccepted)
		return
	}

	logger = logger.With("account", account.Key())
	if ev.Repository != nil {
		logger = logger.With("repository", ev.Repository.FullName)
	}
	if n := pullNumber(ev); n != 0 {
		logger = logger.With("pr", n)
	}
	// The forge gives up on a delivery after ten seconds and does not send
	// it again, so a dispatch that outlasts the request still finishes: a
	// comment or thread event that rolled back with it would be lost.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), dispatchTimeout)
	defer cancel()
	// A pull request recorded with no author would lose the one it had, so
	// one the payload does not name is read from the forge, or the
	// delivery fails and polling brings the pull request in.
	if pr := ev.PullRequest; pr != nil && pr.Author == "" && ev.Repository != nil {
		if h.Authors == nil {
			logger.Error("webhook dispatch failed", "error", "the payload names no author and none can be read")
			h.Metrics.Webhook(name, "error")
			http.Error(w, "dispatch failed", http.StatusInternalServerError)
			return
		}
		pr.Author, pr.AuthorIsBot, err = h.Authors.PullRequestAuthor(ctx, in, ev.Repository.FullName, pr.Number)
		if err != nil || pr.Author == "" {
			logger.Error("webhook dispatch failed: the pull request's author could not be read", "error", err)
			h.Metrics.Webhook(name, "error")
			http.Error(w, "dispatch failed", http.StatusInternalServerError)
			return
		}
	}
	markBots(in, ev)
	out, err := h.disp.Dispatch(ctx, Request{File: file, Account: account, Event: ev})
	if err != nil {
		logger.Error("webhook dispatch failed", "error", err)
		h.Metrics.Webhook(name, "error")
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	logger.Info("webhook handled", "status", out.Status, "reason", out.Reason, "job", out.Job)
	h.Metrics.Webhook(name, out.Status)
	w.WriteHeader(http.StatusAccepted)
}

// newInstallation reports whether ev is an App's installation on an
// account as it is made.
func newInstallation(ev webhook.Event) bool {
	return ev.Kind == webhook.KindInstallation && ev.Action == actionCreated && ev.Installation.ID != 0 &&
		time.Since(ev.Installation.CreatedAt) < installationFresh
}

// uninstall removes the App from an account no connection serves as it is
// installed there, and returns the delivery's outcome. Only a new
// installation is removed: one made before its account was dropped from
// the configuration is the operator's, and stays.
func (h *Handler) uninstall(ctx context.Context, logger *slog.Logger, in *configfile.Connection, ev webhook.Event) string {
	logger = logger.With("account", ev.Account, "installation", ev.Installation.ID)
	if !h.uninstalls.Allow() {
		logger.Warn("App installed on an undeclared account left installed: too many uninstalls of late")
		return "uninstall_limited"
	}
	// Not ended with the request: a removal the forge made after the
	// request was cut would roll its audit row back.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchTimeout)
	defer cancel()
	if err := h.Uninstaller.Uninstall(ctx, in, ev.Account, ev.Installation.ID); err != nil {
		logger.Error("App installed on an undeclared account not uninstalled", "error", err)
		return "error"
	}
	logger.Warn("App uninstalled from an undeclared account")
	return "uninstalled"
}

// pullNumber is the pull request ev concerns, whether the event is the
// pull request's own, a comment on it or one of its review threads; 0
// when it concerns none.
func pullNumber(ev webhook.Event) int {
	switch {
	case ev.PullRequest != nil:
		return ev.PullRequest.Number
	case ev.Comment != nil:
		return ev.Comment.Number
	case ev.Thread != nil:
		return ev.Thread.Number
	}
	return 0
}

// markBots marks the pull request's and the comment's authors as bots when
// the connection lists them: Forgejo's payloads mark no account a bot, and
// Gitea's mark its bot users alone.
func markBots(in *configfile.Connection, ev webhook.Event) {
	if pr := ev.PullRequest; pr != nil && in.IsBot(pr.Author) {
		pr.AuthorIsBot = true
	}
	if c := ev.Comment; c != nil && in.IsBot(c.Author) {
		c.AuthorIsBot = true
	}
}
