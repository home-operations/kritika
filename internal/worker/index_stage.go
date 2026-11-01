package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/executor"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

func (w *Index) stage(
	ctx context.Context, job *river.Job[jobs.IndexArgs], account *configfile.Account, repository string,
	client forge.Client, runID string, spec runspec.Spec, logger *slog.Logger,
) (err error) {
	started := false
	defer func() {
		if err == nil || started {
			return
		}
		dctx, cancel := detach(ctx)
		defer cancel()
		err = errors.Join(err, w.Store.WithAccount(dctx, account.ID(), func(tx pgx.Tx) error {
			return store.FailRunnerRun(dctx, tx, spec.RunID, err.Error())
		}))
	}()
	_, name, _ := strings.Cut(repository, "/")
	token, err := client.GitToken(ctx, name)
	if err != nil {
		return err
	}
	reportToken, err := w.Store.MintIndexRunnerToken(ctx, account.ID(), spec.RunID, job.ID, job.Attempt)
	if err != nil {
		return err
	}
	deadline := w.Current.Get().RunnerDeadline()
	// The run's token admits its git fetch to the egress proxy and, with
	// no budget, nothing else. Minted last, so an error leaves none behind.
	gatewayToken, err := w.Store.MintGatewayToken(ctx, store.GatewayGrant{
		RunID: spec.RunID, AccountID: account.ID(), RepositoryID: job.Args.RepositoryID,
	}, time.Now().Add(deadline+w.GatewayTokenTTL))
	if err != nil {
		return err
	}
	defer w.revokeGatewayTokens(ctx, logger, spec.RunID)
	sup := runSupervision(w.Store, account.ID(), spec.RunID, "", "", "", w.superviseEvery, logger)
	started = true
	res, cause := supervise(ctx, sup, w.Executor, executor.Spec{
		Labels:      runnerLabels(account.Key(), repository, jobs.QueueIndex, 0),
		Annotations: runnerAnnotations(job.ID, spec.Head), Job: spec,
		Secrets:  runspec.Secrets{GitToken: token, GatewayToken: gatewayToken, ReportToken: reportToken},
		Deadline: deadline,
	})
	// Runner accounting survives River cancellation, while sealing staging
	// requires this attempt to still own the job.
	dctx, cancel := detach(ctx)
	defer cancel()
	if err := recordRun(dctx, w.Store, w.Metrics, account.Key(), account.ID(), spec.RunID, jobs.QueueIndex, res); err != nil {
		return err
	}
	if res.Err != nil {
		reason := res.Err.Error()
		if errors.Is(cause, errHeartbeatLost) {
			reason = "runner heartbeat lost"
		}
		logger.Warn("index runner failed", "error", reason, "job", res.JobName, "reason", res.TerminationReason)
		return fmt.Errorf("worker: index runner failed: %s", reason)
	}
	return w.Store.WithAccount(dctx, account.ID(), func(tx pgx.Tx) error {
		if err := store.LockIndexJob(dctx, tx, account.ID(), job.ID, job.Attempt); err != nil {
			return err
		}
		return store.ReadyIndexRun(dctx, tx, runID, spec.RunID)
	})
}
