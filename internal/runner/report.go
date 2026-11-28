package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/textcut"
	"github.com/home-operations/kritika/internal/tuning"
)

// reporter writes what a run found, by way of the gateway. It is the run's only
// way to do that.
type reporter struct {
	gatewayURL, token string
	// partsSequence numbers the reports of the run's parts. Only one
	// goroutine at a time makes them.
	partsSequence int64
}

func newReporter(p runspec.Spec, secrets runspec.Secrets) *reporter {
	return &reporter{gatewayURL: strings.TrimSuffix(p.GatewayURL, "/"), token: secrets.ReportToken}
}

// authorize makes sure that the gateway takes this run's reports.
func (r *reporter) authorize(ctx context.Context) error {
	return r.send(ctx, tuning.Get().RunSpec.ReportTimeout, http.MethodGet, runspec.ReportAuthorizePath, nil, nil)
}

// heartbeat is tried once. Another comes along soon, and one that retries
// could only overlap it.
func (r *reporter) heartbeat(ctx context.Context) error {
	_, err := r.attempt(ctx, tuning.Get().Runner.HeartbeatTimeout, http.MethodPost, runspec.ReportHeartbeatPath, nil, nil)
	return err
}

func (r *reporter) phase(ctx context.Context, phase string) error {
	return r.post(ctx, runspec.ReportPhasePath, runspec.PhaseReport{Phase: phase}, nil)
}

// failure accepts an already redacted error, cut to tuning's
// Runner.MaxFailureCause, and then, at a rune boundary, until its report as
// JSON encodes it fits Gateway.MaxSmallReport.
func (r *reporter) failure(ctx context.Context, cause string) error {
	body, err := failureReport(cause)
	if err != nil {
		return fmt.Errorf("runner: encode report %s: %w", runspec.ReportFailurePath, err)
	}
	return r.postEncoded(ctx, runspec.ReportFailurePath, body, nil)
}

// failureReport is the encoded failure report of cause, cut as failure cuts
// it.
func failureReport(cause string) ([]byte, error) {
	cut := textcut.Prefix(cause, int(tuning.Get().Runner.MaxFailureCause))
	limit := int(tuning.Get().Gateway.MaxSmallReport)
	for {
		body, err := json.Marshal(runspec.FailureReport{Cause: cut})
		over := len(body) - limit
		if err != nil || over <= 0 || cut == "" {
			return body, err
		}
		cut = textcut.Prefix(cut, textcut.EscapedRoom(len(cut), over))
	}
}

// context sends body, an encoded ContextReport.
func (r *reporter) context(ctx context.Context, body []byte) error {
	return r.postEncoded(ctx, runspec.ReportContextPath, body, nil)
}

func (r *reporter) similarExclude(ctx context.Context, paths []string) error {
	return r.post(ctx, runspec.ReportSimilarExcludePath, runspec.SimilarExcludeReport{Paths: paths}, nil)
}

func (r *reporter) agent(ctx context.Context, rec runspec.AgentRecord, phase string) error {
	return r.post(ctx, runspec.ReportAgentPath, runspec.AgentReport{Record: rec, Phase: phase}, nil)
}

// index uploads a run's chunks, encoded as they are sent. An upload that fails
// is sent again whole. One that the gateway would refuse as too large is not
// sent: see indexFits.
func (r *reporter) index(ctx context.Context, pack runspec.IndexPack, chunks []runspec.IndexChunk) error {
	if err := indexFits(ctx, pack, chunks); err != nil {
		return err
	}
	body := func() io.Reader {
		pr, pw := io.Pipe()
		go func() {
			_ = pw.CloseWithError(encodeIndex(pw, pack, chunks))
		}()
		return pr
	}
	return r.send(ctx, tuning.Get().RunSpec.IndexUploadTimeout, http.MethodPost, runspec.ReportIndexPath, body, nil)
}

// encodeIndex writes the index upload of pack and chunks to w: an
// IndexReport, then each chunk, a line each.
func encodeIndex(w io.Writer, pack runspec.IndexPack, chunks []runspec.IndexChunk) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	err := enc.Encode(runspec.IndexReport{Pack: pack, Chunks: len(chunks)})
	for i := 0; err == nil && i < len(chunks); i++ {
		err = enc.Encode(chunks[i])
	}
	return err
}

// indexFits says why the gateway would refuse the index upload of pack and
// chunks as too large, nil if it would not. Startup holds
// gateway.maxIndexLine and gateway.maxIndexBody to the indexer's limits, but
// only so far as source text grows in JSON: control bytes and invalid UTF-8
// grow more, and the changed paths of the opening line have no limit. The
// upload is measured, not kept, and the measure stops at the first limit it
// passes, or when ctx ends.
func indexFits(ctx context.Context, pack runspec.IndexPack, chunks []runspec.IndexChunk) error {
	t := tuning.Get().Gateway
	m := &lineMeter{ctx: ctx, maxLine: int64(t.MaxIndexLine), maxTotal: int64(t.MaxIndexBody)}
	if err := encodeIndex(m, pack, chunks); err != nil {
		return fmt.Errorf("runner: index upload: %w", err)
	}
	return nil
}

// lineMeter counts what is written to it, in all and in each line, newline
// included, and fails a write that takes either past its limit.
type lineMeter struct {
	ctx               context.Context
	maxLine, maxTotal int64
	total, line       int64
}

func (m *lineMeter) Write(p []byte) (int, error) {
	if err := m.ctx.Err(); err != nil {
		return 0, err
	}
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		n := i + 1
		if i < 0 {
			n = len(rest)
		}
		m.line += int64(n)
		if m.line > m.maxLine {
			return 0, fmt.Errorf("a line is over tuning.gateway.maxIndexLine, %d", m.maxLine)
		}
		if i >= 0 {
			m.line = 0
		}
		rest = rest[n:]
	}
	if m.total += int64(len(p)); m.total > m.maxTotal {
		return 0, fmt.Errorf("the upload is over tuning.gateway.maxIndexBody, %d", m.maxTotal)
	}
	return len(p), nil
}

func (r *reporter) checkpoints(ctx context.Context, input string) ([]runspec.PartCheckpoint, error) {
	var saved []runspec.PartCheckpoint
	path := runspec.ReportCheckpointsPath + "?" + url.Values{runspec.ReportCheckpointInput: {input}}.Encode()
	err := r.send(ctx, tuning.Get().RunSpec.ReportTimeout, http.MethodGet, path, nil, &saved)
	return saved, err
}

func (r *reporter) saveCheckpoint(ctx context.Context, input string, part int, payload json.RawMessage) error {
	return r.post(ctx, runspec.ReportCheckpointsPath, runspec.CheckpointReport{Input: input, Part: part, Payload: payload}, nil)
}

func (r *reporter) partsRemaining(ctx context.Context, remaining, active map[string]int) (map[string]int, error) {
	var slots runspec.PartSlots
	r.partsSequence++
	err := r.post(ctx, runspec.ReportPartsPath, runspec.PartsReport{Sequence: r.partsSequence, Remaining: remaining, Active: active}, &slots)
	return slots.Slots, err
}

func (r *reporter) post(ctx context.Context, path string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("runner: encode report %s: %w", path, err)
	}
	return r.postEncoded(ctx, path, body, out)
}

// postEncoded sends body, a report already encoded.
func (r *reporter) postEncoded(ctx context.Context, path string, body []byte, out any) error {
	return r.send(ctx, tuning.Get().RunSpec.ReportTimeout, http.MethodPost, path, func() io.Reader { return bytes.NewReader(body) }, out)
}

// errAnswerTooLarge is a gateway's answer to a report that is over
// Runner.MaxReportAnswer.
var errAnswerTooLarge = errors.New("the answer is over tuning.runner.maxReportAnswer")

// tooLargeError is a report that the gateway refused as too large, and the
// limit, in bytes, of the replica that refused it, 0 when the replica
// predates runspec.ReportLimitHeader.
type tooLargeError struct {
	limit int64
	err   error
}

func (e *tooLargeError) Error() string { return e.err.Error() }
func (e *tooLargeError) Unwrap() error { return e.err }

// send makes a request, again while its failure is worth another try: for as
// long as tuning's RunSpec.ReportRetryWindow when the gateway is out of reach
// or says the trouble is its own, waiting from Runner.ReportBackoff, doubled
// up to Runner.ReportMaxBackoff, between attempts. The context of the report
// ends it sooner. The window bounds the retries and not an attempt, whose
// timeout does that: no attempt starts after the window, nor a wait that
// would end past it. The first attempt that outlasted the window, as an
// upload can, starts the window over: it was no outage that ended it, or it
// would have failed sooner. A later one does not, so that a gateway that
// answers each attempt slowly with a failure is given up on.
func (r *reporter) send(ctx context.Context, timeout time.Duration, method, path string, body func() io.Reader, out any) error {
	window := tuning.Get().RunSpec.ReportRetryWindow
	backoff, maxBackoff := tuning.Get().Runner.ReportBackoff, tuning.Get().Runner.ReportMaxBackoff
	giveUp := time.Now().Add(window)
	wait := backoff
	restarted := false
	for {
		started := time.Now()
		again, err := r.attempt(ctx, timeout, method, path, body, out)
		if err == nil || !again {
			return err
		}
		now := time.Now()
		if !restarted && now.Sub(started) >= window {
			giveUp, wait, restarted = now.Add(window), backoff, true
		}
		if now.Add(wait).After(giveUp) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, maxBackoff)
	}
}

// attempt makes one request. It reports whether a failure is worth another try.
func (r *reporter) attempt(
	ctx context.Context, timeout time.Duration, method, path string, body func() io.Reader, out any,
) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = body()
	}
	req, err := http.NewRequestWithContext(ctx, method, r.gatewayURL+path, reader)
	if err != nil {
		return false, fmt.Errorf("runner: report %s: %w", path, err)
	}
	req.Header.Set("Authorization", tuning.BearerPrefix+r.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true, fmt.Errorf("runner: report %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	most := int64(tuning.Get().Runner.MaxReportAnswer)
	answer, err := io.ReadAll(io.LimitReader(resp.Body, most))
	if err != nil {
		return true, fmt.Errorf("runner: report %s: %w", path, err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		err := fmt.Errorf("runner: report %s: %s: %s", path, resp.Status, bytes.TrimSpace(answer))
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			limit, _ := strconv.ParseInt(resp.Header.Get(runspec.ReportLimitHeader), 10, 64)
			err = &tooLargeError{limit: max(limit, 0), err: err}
		}
		return reportRetryable(resp.StatusCode, answer), err
	}
	if int64(len(answer)) == most {
		if n, _ := io.ReadFull(resp.Body, make([]byte, 1)); n == 1 {
			return false, fmt.Errorf("runner: report %s: %w", path, errAnswerTooLarge)
		}
	}
	if out != nil {
		if err := json.Unmarshal(answer, out); err != nil {
			return false, fmt.Errorf("runner: report %s: %w", path, err)
		}
	}
	return false, nil
}

// reportRetryable reports whether a report that the gateway answered with
// status and answer is worth sending again. It is, when the gateway's own
// trouble stopped it, its body was cut short, or another upload of the run
// overtook it. A 404, or a 400 that names a field of the report as unknown,
// is a replica older than this run's image, which a rollout is about to
// replace: the older gateway decodes reports strictly, and has no route or
// field that the run's build added.
func reportRetryable(status int, answer []byte) bool {
	return status >= http.StatusInternalServerError || status == http.StatusNotFound || status == http.StatusConflict ||
		status == http.StatusRequestTimeout || status == http.StatusBadRequest && bytes.Contains(answer, []byte("json: unknown field "))
}
