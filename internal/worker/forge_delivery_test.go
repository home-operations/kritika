package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/home-operations/kritika/internal/forge"
)

// release, when set, holds SetStatus until it is closed, whatever its
// context says.
type deliveryForge struct {
	forge.Client
	inline, status, release chan struct{}
	inlineErr, statusErr    error
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
	if f.release != nil {
		<-f.release
		return f.statusErr
	}
	select {
	case <-f.inline:
		return f.statusErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The status request is joined before the publication lock is let go, even
// once the inline review has returned and the job has been canceled.
func TestInlineAndStatusWaitForStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &deliveryForge{inline: make(chan struct{}), status: make(chan struct{}), release: make(chan struct{})}
		p := &publishPhase{pr: &pullRequest{repository: "owner/repo"}, client: f, logger: slog.Default()}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			_, _ = p.postInlineAndStatus(ctx, nil, 0)
			close(done)
		}()
		<-f.status
		<-f.inline
		cancel()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("returned while the status request was in flight")
		default:
		}
		close(f.release)
		<-done
	})
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
			p := &publishPhase{w: &publicationWorker{}, pr: &pullRequest{repository: "owner/repo"}, client: f, logger: slog.Default()}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ids, err := p.postInlineAndStatus(ctx, nil, 0)
			if !slices.Equal(ids, []int64{123}) || !errors.Is(err, tt.inlineErr) {
				t.Fatalf("delivery = %v, %v; want inline IDs and error %v", ids, err, tt.inlineErr)
			}
		})
	}
}
