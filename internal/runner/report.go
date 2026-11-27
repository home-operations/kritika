package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// Runner.MaxFailureCause.
func (r *reporter) failure(ctx context.Context, cause string) error {
	cut := textcut.Prefix(cause, int(tuning.Get().Runner.MaxFailureCause))
	return r.post(ctx, runspec.ReportFailurePath, runspec.FailureReport{Cause: cut}, nil)
}

func (r *reporter) context(ctx context.Context, pack runspec.ContextPack, phase string) error {
	return r.post(ctx, runspec.ReportContextPath, runspec.ContextReport{Pack: pack, Phase: phase}, nil)
}

func (r *reporter) similarExclude(ctx context.Context, paths []string) error {
	return r.post(ctx, runspec.ReportSimilarExcludePath, runspec.SimilarExcludeReport{Paths: paths}, nil)
}

func (r *reporter) agent(ctx context.Context, rec runspec.AgentRecord, phase string) error {
	return r.post(ctx, runspec.ReportAgentPath, runspec.AgentReport{Record: rec, Phase: phase}, nil)
}

// index uploads a run's chunks, encoded as they are sent. An upload that fails
// is sent again whole.
func (r *reporter) index(ctx context.Context, pack runspec.IndexPack, chunks []runspec.IndexChunk) error {
	body := func() io.Reader {
		pr, pw := io.Pipe()
		go func() {
			enc := json.NewEncoder(pw)
			enc.SetEscapeHTML(false)
			err := enc.Encode(runspec.IndexReport{Pack: pack, Chunks: len(chunks)})
			for i := 0; err == nil && i < len(chunks); i++ {
				err = enc.Encode(chunks[i])
			}
			_ = pw.CloseWithError(err)
		}()
		return pr
	}
	return r.send(ctx, tuning.Get().RunSpec.IndexUploadTimeout, http.MethodPost, runspec.ReportIndexPath, body, nil)
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
	return r.send(ctx, tuning.Get().RunSpec.ReportTimeout, http.MethodPost, path, func() io.Reader { return bytes.NewReader(body) }, out)
}

// send makes a request, again while its failure is worth another try: for as
// long as tuning's RunSpec.ReportRetryWindow when the gateway is out of reach
// or says the trouble is its own, waiting from Runner.ReportBackoff, doubled
// up to Runner.ReportMaxBackoff, between attempts. The context of the report
// ends it sooner. The window bounds the retries and not an attempt, whose
// timeout does that. An attempt that outlasted the window, as an upload can,
// starts the window over: it was no outage that ended it, or it would have
// failed sooner.
func (r *reporter) send(ctx context.Context, timeout time.Duration, method, path string, body func() io.Reader, out any) error {
	window, t := tuning.Get().RunSpec.ReportRetryWindow, tuning.Get().Runner
	backoff, maxBackoff := t.ReportBackoff, t.ReportMaxBackoff
	giveUp := time.Now().Add(window)
	wait := backoff
	for {
		started := time.Now()
		again, err := r.attempt(ctx, timeout, method, path, body, out)
		if err == nil || !again {
			return err
		}
		now := time.Now()
		if now.Sub(started) >= window {
			giveUp, wait = now.Add(window), backoff
		}
		if now.After(giveUp) {
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
	answer, err := io.ReadAll(io.LimitReader(resp.Body, int64(tuning.Get().Runner.MaxReportAnswer)))
	if err != nil {
		return true, fmt.Errorf("runner: report %s: %w", path, err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return reportRetryable(resp.StatusCode, answer), fmt.Errorf("runner: report %s: %s: %s", path, resp.Status, bytes.TrimSpace(answer))
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
