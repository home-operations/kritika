package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/runspec"
)

// Local runs the runner in this process, reporting to the gateway that the job
// document names. It exists for tests and local development. It is not a
// supported deployment mode, because it gives the checkout the worker's
// credentials.
type Local struct{}

// Run implements Executor.
func (l *Local) Run(ctx context.Context, spec Spec) Result {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, spec.Deadline)
	defer cancel()
	// The spec goes through the same encoding and strict decoding as a
	// Job's mounted document, size limit included.
	job, err := specRoundTrip(spec.Job)
	if err == nil {
		err = runner.Run(ctx, job, spec.Secrets, logger)
	}
	logTail := tail(spec.Secrets.Mask(buf.String()), LogTailBytes)
	res := Result{JobName: "local", PodName: "local", StartedAt: started, LogTail: logTail}
	if err != nil {
		// A git or provider error can carry a credential, and the worker
		// stores and shows this one.
		res.Err = maskedError{msg: spec.Secrets.Mask(err.Error()), err: err}
		res.ExitCode = 1
		res.TerminationReason = "Error"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.DeadlineExceeded = true
			res.TerminationReason = "DeadlineExceeded"
		}
	}
	return res
}

// maskedError is an error whose message has the run's secrets masked. It
// still unwraps to the original, so errors.Is sees a deadline or a cancel.
type maskedError struct {
	msg string
	err error
}

func (e maskedError) Error() string { return e.msg }
func (e maskedError) Unwrap() error { return e.err }

func specRoundTrip(s runspec.Spec) (runspec.Spec, error) {
	b, err := runspec.Encode(s)
	if err != nil {
		return runspec.Spec{}, fmt.Errorf("executor: %w", err)
	}
	return runspec.Decode(b)
}

// LogTailBytes is how much of a runner's output is kept: enough to diagnose,
// small enough to store per run.
const LogTailBytes = 64 << 10

// tail is the last n bytes of s, less any leading bytes of a rune that the
// cut split. So the result stays valid UTF-8 for a text column.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
