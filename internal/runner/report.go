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
)

// A report is sent again for as long as reportRetryWindow when the gateway is
// out of reach or says the trouble is its own. The context of the report ends
// it sooner. They are variables for the tests.
var (
	reportRetryWindow = runspec.ReportRetryWindow
	reportBackoff     = time.Second
	reportMaxBackoff  = 15 * time.Second
)

// heartbeatTimeout bounds one heartbeat, as runspec.ReportTimeout does a report.
const heartbeatTimeout = 10 * time.Second

// maxReportAnswer bounds what the gateway's answer is read to: the part
// checkpoints of a run, or an error.
const maxReportAnswer = 256 << 20

// reporter writes what a run found, by way of the gateway. It is the run's only
// way to do that.
type reporter struct {
	gatewayURL, token string
}

func newReporter(p runspec.Spec, secrets runspec.Secrets) *reporter {
	return &reporter{gatewayURL: strings.TrimSuffix(p.GatewayURL, "/"), token: secrets.ReportToken}
}

// authorize makes sure that the gateway takes this run's reports.
func (r *reporter) authorize(ctx context.Context) error {
	return r.send(ctx, runspec.ReportTimeout, http.MethodGet, runspec.ReportAuthorizePath, nil, nil)
}

// heartbeat is tried once. Another comes along soon, and one that retries
// could only overlap it.
func (r *reporter) heartbeat(ctx context.Context) error {
	_, err := r.attempt(ctx, heartbeatTimeout, http.MethodPost, runspec.ReportHeartbeatPath, nil, nil)
	return err
}

func (r *reporter) phase(ctx context.Context, phase string) error {
	return r.post(ctx, runspec.ReportPhasePath, runspec.PhaseReport{Phase: phase}, nil)
}

// failure accepts an already redacted error. The run's row keeps its first
// 2,000 characters, so a git error of any length still fits the report.
func (r *reporter) failure(ctx context.Context, cause string) error {
	return r.post(ctx, runspec.ReportFailurePath, runspec.FailureReport{Cause: textcut.Prefix(cause, 8<<10)}, nil)
}

func (r *reporter) context(ctx context.Context, pack runspec.ContextPack, phase string) error {
	return r.post(ctx, runspec.ReportContextPath, runspec.ContextReport{Pack: pack, Phase: phase}, nil)
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
	return r.send(ctx, runspec.IndexUploadTimeout, http.MethodPost, runspec.ReportIndexPath, body, nil)
}

func (r *reporter) checkpoints(ctx context.Context, input string) ([]runspec.PartCheckpoint, error) {
	var saved []runspec.PartCheckpoint
	path := runspec.ReportCheckpointsPath + "?" + url.Values{runspec.ReportCheckpointInput: {input}}.Encode()
	err := r.send(ctx, runspec.ReportTimeout, http.MethodGet, path, nil, &saved)
	return saved, err
}

func (r *reporter) saveCheckpoint(ctx context.Context, input string, part int, payload json.RawMessage) error {
	return r.post(ctx, runspec.ReportCheckpointsPath, runspec.CheckpointReport{Input: input, Part: part, Payload: payload}, nil)
}

func (r *reporter) partsRemaining(ctx context.Context, remaining int) (int, error) {
	var slots runspec.PartSlots
	err := r.post(ctx, runspec.ReportPartsPath, runspec.PartsReport{Remaining: remaining}, &slots)
	return slots.Slots, err
}

func (r *reporter) post(ctx context.Context, path string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("runner: encode report %s: %w", path, err)
	}
	return r.send(ctx, runspec.ReportTimeout, http.MethodPost, path, func() io.Reader { return bytes.NewReader(body) }, out)
}

// send makes a request, again while its failure is worth another try. The window
// bounds the retries and not an attempt, whose timeout does that. An attempt
// that outlasted the window, as an upload can, starts the window over: it was
// no outage that ended it, or it would have failed sooner.
func (r *reporter) send(ctx context.Context, timeout time.Duration, method, path string, body func() io.Reader, out any) error {
	giveUp := time.Now().Add(reportRetryWindow)
	wait := reportBackoff
	for {
		started := time.Now()
		again, err := r.attempt(ctx, timeout, method, path, body, out)
		if err == nil || !again {
			return err
		}
		now := time.Now()
		if now.Sub(started) >= reportRetryWindow {
			giveUp, wait = now.Add(reportRetryWindow), reportBackoff
		}
		if now.After(giveUp) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, reportMaxBackoff)
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
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true, fmt.Errorf("runner: report %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxReportAnswer))
	if err != nil {
		return true, fmt.Errorf("runner: report %s: %w", path, err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return reportRetryable(resp.StatusCode), fmt.Errorf("runner: report %s: %s: %s", path, resp.Status, bytes.TrimSpace(answer))
	}
	if out != nil {
		if err := json.Unmarshal(answer, out); err != nil {
			return false, fmt.Errorf("runner: report %s: %w", path, err)
		}
	}
	return false, nil
}

// reportRetryable reports whether a report that the gateway answered with
// status is worth sending again. It is, when the gateway's own trouble stopped
// it, its body was cut short, or another upload of the run overtook it. A 404 is
// a replica older than this run's image, which a rollout is about to replace.
func reportRetryable(status int) bool {
	return status >= http.StatusInternalServerError || status == http.StatusNotFound || status == http.StatusConflict ||
		status == http.StatusRequestTimeout
}
