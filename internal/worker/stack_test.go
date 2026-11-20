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
	"github.com/home-operations/kritika/internal/review"
)

// stackForge answers StackBelow alone.
type stackForge struct {
	forge.Client
	below []forge.StackEntry
	err   error
	asked []string
	// stalled has the forge never answer.
	stalled bool
}

func (f *stackForge) StackBelow(ctx context.Context, owner, repo string, number int) ([]forge.StackEntry, error) {
	f.asked = append(f.asked, owner, repo)
	if f.stalled {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if number != 8 {
		return nil, nil
	}
	return f.below, f.err
}

func TestStackBelow(t *testing.T) {
	below := []forge.StackEntry{{Number: 5, Merged: true}, {Number: 6}, {Number: 7, Open: true}}
	for _, tt := range []struct {
		name   string
		client *stackForge
		number int
		want   []review.StackEntry
	}{
		{"the pull requests below, lowest first", &stackForge{below: below}, 8,
			[]review.StackEntry{{Number: 5, Merged: true}, {Number: 6}, {Number: 7, Open: true}}},
		{"in no stack", &stackForge{below: below}, 9, nil},
		{"a read that the forge failed", &stackForge{below: below, err: errors.New("github: 502")}, 8, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pr := &pullRequest{repository: "o/r", number: tt.number}
			got := stackBelow(t.Context(), tt.client, pr, slog.New(slog.DiscardHandler))
			if !slices.Equal(got, tt.want) || !slices.Equal(tt.client.asked, []string{"o", "r"}) {
				t.Fatalf("stackBelow = %+v, asked of %v; want %+v of o/r", got, tt.client.asked, tt.want)
			}
		})
	}
}

// TestStackBelowStalled: a forge that never answers costs the read's own
// time, not its caller's.
func TestStackBelowStalled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		started := time.Now()
		got := stackBelow(ctx, &stackForge{stalled: true}, &pullRequest{repository: "o/r", number: 8}, slog.New(slog.DiscardHandler))
		if got != nil || time.Since(started) != stackTimeout || ctx.Err() != nil {
			t.Fatalf("stackBelow = %+v after %s, caller's context: %v; want none after %s", got, time.Since(started), ctx.Err(), stackTimeout)
		}
	})
}
