package worker

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivershared/baseservice"
	"github.com/riverqueue/river/rivershared/riverpilot"

	"github.com/home-operations/kritika/internal/store"
)

// NewRiverDriver is River's pgx driver over pool, with a pilot that leaves a
// stuck job alone until the rescue horizon its attempt recorded
// (store.StartJob) has passed. River's rescuer cancels a job a cancel was
// asked of without calling its worker's Timeout, so riverTimeout cannot
// honour that horizon there.
func NewRiverDriver(pool *pgxpool.Pool) riverdriver.Driver[pgx.Tx] {
	return rescueDriver{riverpgxv5.New(pool)}
}

type rescueDriver struct{ *riverpgxv5.Driver }

func (rescueDriver) PluginInit(*baseservice.Archetype) {}

func (rescueDriver) PluginPilot() riverpilot.Pilot { return &rescuePilot{} }

type rescuePilot struct{ riverpilot.StandardPilot }

func (p *rescuePilot) JobRescueMany(
	ctx context.Context, exec riverdriver.Executor, params *riverdriver.JobRescueManyParams,
) (*struct{}, error) {
	rows, err := exec.JobGetByIDMany(ctx, &riverdriver.JobGetByIDManyParams{ID: params.ID, Schema: params.Schema})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	waiting := map[int64]bool{}
	for _, r := range rows {
		if after, ok := store.JobRescueAfter(r); ok && now.Sub(*r.AttemptedAt) < after {
			waiting[r.ID] = true
		}
	}
	kept := &riverdriver.JobRescueManyParams{Schema: params.Schema, StuckHorizon: params.StuckHorizon}
	for i, id := range params.ID {
		if waiting[id] {
			continue
		}
		kept.ID = append(kept.ID, id)
		kept.Error = append(kept.Error, params.Error[i])
		kept.FinalizedAt = append(kept.FinalizedAt, params.FinalizedAt[i])
		kept.ScheduledAt = append(kept.ScheduledAt, params.ScheduledAt[i])
		kept.State = append(kept.State, params.State[i])
	}
	if len(kept.ID) == 0 {
		return &struct{}{}, nil
	}
	return p.StandardPilot.JobRescueMany(ctx, exec, kept)
}
