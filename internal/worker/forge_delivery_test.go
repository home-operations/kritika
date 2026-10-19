package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/forge"
)

type deliveryForge struct {
	forge.Client
	inline, status       chan struct{}
	inlineErr, statusErr error
	blockStatus          bool
}

func (f *deliveryForge) CreateReview(ctx context.Context, _, _ string, _ int, _ string, _ []forge.InlineComment) ([]int64, error) {
	close(f.inline)
	select {
	case <-f.status:
		return []int64{123}, f.inlineErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *deliveryForge) SetStatus(ctx context.Context, _, _, _ string, _ forge.StatusState, _ string) error {
	close(f.status)
	if f.blockStatus {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-f.inline:
		return f.statusErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestInlineAndStatusDrainOnCancel(t *testing.T) {
	f := &deliveryForge{inline: make(chan struct{}), status: make(chan struct{}), blockStatus: true}
	p := &publishPhase{pr: &pullRequest{repository: "owner/repo"}, client: f, logger: slog.Default()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _ = p.postInlineAndStatus(ctx, nil, 0)
		close(done)
	}()
	<-f.status
	<-f.inline
	select {
	case <-done:
		t.Fatal("returned before status delivery finished")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not drain on cancellation")
	}
}

func TestInlineAndStatusOverlap(t *testing.T) {
	failure := errors.New("forge unavailable")
	for _, tt := range []struct {
		name                 string
		inlineErr, statusErr error
	}{
		{name: "success"}, {name: "status failure", statusErr: failure},
		{name: "inline failure", inlineErr: failure}, {name: "both fail", inlineErr: failure, statusErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &deliveryForge{inline: make(chan struct{}), status: make(chan struct{}), inlineErr: tt.inlineErr, statusErr: tt.statusErr}
			p := &publishPhase{w: &Review{}, pr: &pullRequest{repository: "owner/repo"}, client: f, logger: slog.Default()}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ids, err := p.postInlineAndStatus(ctx, nil, 0)
			if !slices.Equal(ids, []int64{123}) || !errors.Is(err, tt.inlineErr) {
				t.Fatalf("delivery = %v, %v; want inline IDs and error %v", ids, err, tt.inlineErr)
			}
		})
	}
}
