package gateway

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

// Bounds on what a run reports. A record holds its context pack's two diffs
// at 4 MiB each and its conversation of up to maxBody, which JSON and base64
// grow. An index upload holds the indexer's 100,000 chunks of 6,000 bytes,
// which JSON grows too, and its opening line holds every changed path. A batch
// is written as soon as it holds indexBatchChunks chunks or indexBatchBytes, so
// an upload costs a batch and a line of memory however long it is. A part
// checkpoint holds one part's submitted answer, far below maxCheckpointBody,
// and one refused only costs a retry that part's run again.
const (
	maxReportBody     = 64 << 20
	maxCheckpointBody = 4 << 20
	maxSmallReport    = 64 << 10
	maxIndexBody      = 2 << 30
	maxIndexLine      = 32 << 20
	indexBatchChunks  = 500
	indexBatchBytes   = 8 << 20
)

// runReport is a runner's report, authenticated by its report token. That
// token is not the run token that admits the run to the proxy and its model: a
// command the run starts holds that one, so it must never authorize a report.
type runReport struct {
	token  string
	grant  store.RunnerGrant
	logger *slog.Logger
}

// report serves what a run reports about itself. Everything it writes goes to
// the run that the token names, as the application role under that run's
// account, and only to the columns and tables a runner reports. A request can
// name neither a run nor an account.
func (g *Server) report(w http.ResponseWriter, r *http.Request) {
	c := runReport{logger: g.Logger}
	c.token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// A token that is not a live run's is refused before its body is read, so
	// only a live run's report costs memory.
	if !g.runnerTx(r.Context(), w, c, func(_ context.Context, _ pgx.Tx, grant store.RunnerGrant) error {
		c.grant = grant
		return nil
	}) {
		return
	}
	c.logger = g.Logger.With("run", review.ShortSHA(c.grant.RunID))
	id := c.grant.RunID
	switch r.Method + " " + r.URL.Path {
	case http.MethodGet + " " + runspec.ReportAuthorizePath:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost + " " + runspec.ReportHeartbeatPath:
		g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error { return store.HeartbeatRunner(ctx, tx, id) })
	case http.MethodPost + " " + runspec.ReportPhasePath:
		if v, ok := decodeReport[runspec.PhaseReport](w, r, maxSmallReport); ok {
			g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error { return store.SetRunnerPhase(ctx, tx, id, v.Phase) })
		}
	case http.MethodPost + " " + runspec.ReportFailurePath:
		if v, ok := decodeReport[runspec.FailureReport](w, r, maxSmallReport); ok {
			g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error { return store.RecordRunnerFailure(ctx, tx, id, v.Cause) })
		}
	case http.MethodPost + " " + runspec.ReportContextPath:
		if v, ok := decodeReport[runspec.ContextReport](w, r, maxReportBody); ok {
			g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error {
				return store.WriteRunnerContext(ctx, tx, id, v.Pack, v.Phase)
			})
		}
	case http.MethodPost + " " + runspec.ReportAgentPath:
		if v, ok := decodeReport[runspec.AgentReport](w, r, maxReportBody); ok {
			g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error {
				return store.WriteRunnerAgent(ctx, tx, id, v.Record, v.Phase)
			})
		}
	case http.MethodPost + " " + runspec.ReportIndexPath:
		g.reportIndex(w, r, c)
	case http.MethodGet + " " + runspec.ReportCheckpointsPath:
		g.reportCheckpoints(w, r, c)
	case http.MethodPost + " " + runspec.ReportCheckpointsPath:
		if v, ok := decodeReport[runspec.CheckpointReport](w, r, maxCheckpointBody); ok {
			// The column is text, so nothing else makes sure of this, and a save
			// that holds nothing would break every read of the run's checkpoints.
			if !json.Valid(v.Payload) {
				refuse(w, http.StatusBadRequest, "invalid_request", "the checkpoint holds no JSON")
				return
			}
			g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error {
				return store.SavePartCheckpoint(ctx, tx, id, v.Input, v.Part, v.Payload)
			})
		}
	case http.MethodPost + " " + runspec.ReportPartsPath:
		g.reportParts(w, r, c)
	default:
		http.NotFound(w, r)
	}
}

// runnerTx runs fn in a transaction that c's token authorizes, or refuses the
// request and reports false.
func (g *Server) runnerTx(
	ctx context.Context, w http.ResponseWriter, c runReport, fn func(context.Context, pgx.Tx, store.RunnerGrant) error,
) bool {
	err := g.Store.WithRunner(ctx, c.token, func(tx pgx.Tx, grant store.RunnerGrant) error { return fn(ctx, tx, grant) })
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrRunnerToken):
		refuse(w, http.StatusUnauthorized, "invalid_token", "the report token is not valid")
	case errors.Is(err, store.ErrIndexStaging):
		refuse(w, http.StatusConflict, "conflict", err.Error())
	case refusedByDatabase(err):
		c.logger.Warn("gateway: report refused", "error", err)
		refuse(w, http.StatusBadRequest, "invalid_request", "the database refused the report")
	default:
		c.logger.Error("gateway: report not written", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the report could not be written")
	}
	return false
}

// refusedByDatabase reports whether err is the database refusing what a report
// holds: a bad value or a broken constraint, which fails the same way if sent
// again. Anything else is the database's own trouble.
func refusedByDatabase(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23"))
}

// runnerWrite is runnerTx for a write, which outlives the request: the body is
// here already, and a pod that is terminating records how its agent stopped and
// goes.
func (g *Server) runnerWrite(w http.ResponseWriter, r *http.Request, c runReport, fn func(context.Context, pgx.Tx) error) bool {
	ctx, cancel := detach(r.Context())
	defer cancel()
	return g.runnerTx(ctx, w, c, func(ctx context.Context, tx pgx.Tx, _ store.RunnerGrant) error { return fn(ctx, tx) })
}

// reportWrite writes a report and answers it.
func (g *Server) reportWrite(w http.ResponseWriter, r *http.Request, c runReport, fn func(context.Context, pgx.Tx) error) {
	if g.runnerWrite(w, r, c, fn) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// decodeReport reads a report's body, or refuses the request and reports
// false.
func decodeReport[T any](w http.ResponseWriter, r *http.Request, limit int64) (T, bool) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		refuseBody(w, err)
		return v, false
	}
	return v, true
}

// refuseBody refuses a request whose body could not be read. A body cut short
// or too slow is worth sending again. Any other fails the same way again.
func refuseBody(w http.ResponseWriter, err error) {
	_, tooLarge := errors.AsType[*http.MaxBytesError](err)
	switch {
	case tooLarge || errors.Is(err, bufio.ErrTooLong):
		refuse(w, http.StatusRequestEntityTooLarge, "invalid_request", err.Error())
	case errors.Is(err, io.ErrUnexpectedEOF) || os.IsTimeout(err):
		refuse(w, http.StatusRequestTimeout, "invalid_request", "reading the report: "+err.Error())
	default:
		refuse(w, http.StatusBadRequest, "invalid_request", "decoding the report: "+err.Error())
	}
}

// reportCheckpoints answers with what the run can reuse of the input named.
func (g *Server) reportCheckpoints(w http.ResponseWriter, r *http.Request, c runReport) {
	input := r.URL.Query().Get(runspec.ReportCheckpointInput)
	var saved []runspec.PartCheckpoint
	if !g.runnerTx(r.Context(), w, c, func(ctx context.Context, tx pgx.Tx, grant store.RunnerGrant) error {
		var err error
		saved, err = store.ReadPartCheckpoints(ctx, tx, grant.RunID, input)
		return err
	}) {
		return
	}
	writeReportJSON(w, saved, c.logger)
}

// reportParts takes the run's unfinished work and answers with the slots that
// the worker gave it.
func (g *Server) reportParts(w http.ResponseWriter, r *http.Request, c runReport) {
	v, ok := decodeReport[runspec.PartsReport](w, r, maxSmallReport)
	if !ok {
		return
	}
	var slots int
	if !g.runnerWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		slots, err = store.ReportRemainingParts(ctx, tx, c.grant.RunID, v.Remaining)
		return err
	}) {
		return
	}
	writeReportJSON(w, runspec.PartSlots{Slots: slots}, c.logger)
}

func writeReportJSON(w http.ResponseWriter, v any, logger *slog.Logger) {
	out, err := json.Marshal(v)
	if err != nil {
		logger.Error("gateway: report answer not encoded", "error", err)
		refuse(w, http.StatusInternalServerError, "server_error", "the answer could not be encoded")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// reportIndex takes a run's index: newline-delimited JSON, an IndexReport and
// then its chunks. Each batch is its own transaction and is read before that
// transaction starts, so an upload as slow as its deadline allows holds no
// pooled connection while it waits. The pack that seals the upload commits with
// the run's done phase, and nothing reads staging before it exists.
func (g *Server) reportIndex(w http.ResponseWriter, r *http.Request, c runReport) {
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(runspec.IndexUploadTimeout)); err != nil {
		c.logger.Warn("gateway: index upload keeps the listener's read deadline", "error", err)
	}
	body := http.MaxBytesReader(w, r.Body, maxIndexBody)
	lines := bufio.NewScanner(body)
	lines.Buffer(nil, maxIndexLine)
	if !lines.Scan() {
		refuseBody(w, cmp.Or(lines.Err(), io.ErrUnexpectedEOF))
		return
	}
	var open runspec.IndexReport
	if err := decodeLine(lines.Bytes(), &open); err != nil {
		refuseBody(w, err)
		return
	}
	id := c.grant.RunID
	var upload string
	var written bool
	if !g.runnerWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		upload, written, err = store.BeginRunnerIndex(ctx, tx, id)
		return err
	}) {
		return
	}
	if written {
		// The answer goes after the body, so a runner sending the upload again
		// reads it rather than a reset connection.
		_, _ = io.Copy(io.Discard, body)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	batch, size, count := make([]runspec.IndexChunk, 0, indexBatchChunks), 0, 0
	flush := func() bool {
		ok := g.runnerWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error { return store.StageRunnerChunks(ctx, tx, id, upload, batch) })
		batch, size = batch[:0], 0
		return ok
	}
	for lines.Scan() {
		var chunk runspec.IndexChunk
		if err := decodeLine(lines.Bytes(), &chunk); err != nil {
			refuseBody(w, err)
			return
		}
		batch, size, count = append(batch, chunk), size+len(lines.Bytes()), count+1
		if (len(batch) >= indexBatchChunks || size >= indexBatchBytes) && !flush() {
			return
		}
	}
	if err := lines.Err(); err != nil {
		refuseBody(w, err)
		return
	}
	if count != open.Chunks {
		refuse(w, http.StatusBadRequest, "invalid_request", "the upload holds fewer or more chunks than it said")
		return
	}
	if len(batch) > 0 && !flush() {
		return
	}
	g.reportWrite(w, r, c, func(ctx context.Context, tx pgx.Tx) error {
		return store.FinishRunnerIndex(ctx, tx, id, upload, open.Pack, count)
	})
}

func decodeLine(line []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
