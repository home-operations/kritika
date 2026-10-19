package worker

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/adapter"
	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

// structured routes a preparation call through the shared execution lifecycle.
func (p *publishPhase) structured(
	ref, fallback configfile.ModelRef, kind store.ModelCallKind, role, what string, halve bool,
) (*adapter.Execution, error) {
	call, err := p.w.Steppers.Execution(p.file, p.account, ref, fallback, halve, p.logger.With("call", what))
	if err != nil {
		return nil, err
	}
	call.Recorder = p.w.recorder()
	call.Record = store.ModelCall{AccountID: p.account.ID(), ReviewID: p.reviewID, Kind: kind}
	call.Usage = store.Usage{AccountID: p.account.ID(), RepositoryID: p.pr.repositoryID, ReviewID: p.reviewID,
		RunnerRunID: p.runID, Role: role}
	return call, nil
}

// pullBody reads the pull request's description.
func (p *publishPhase) pullBody(ctx context.Context) (string, error) {
	var body string
	err := p.w.Store.WithAccount(ctx, p.account.ID(), func(tx pgx.Tx) error {
		var err error
		body, err = store.PullRequestBody(ctx, tx, p.pr.id)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("worker: read pull request description: %w", err)
	}
	return body, nil
}
