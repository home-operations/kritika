package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/forge"
)

// fakeMarks records what marks react and unreact were asked.
type fakeMarks struct {
	reactErr error
	reacted  []string
	removed  []int64
	// ctxErr is the error of the ctx the last call was made on.
	ctxErr error
}

func (f *fakeMarks) marks() marks {
	return marks{
		react: func(ctx context.Context, content string) (int64, error) {
			f.ctxErr = ctx.Err()
			if f.reactErr != nil {
				return 0, f.reactErr
			}
			f.reacted = append(f.reacted, content)
			return int64(len(f.reacted)), nil
		},
		unreact: func(ctx context.Context, id int64) error {
			f.ctxErr = ctx.Err()
			f.removed = append(f.removed, id)
			return nil
		},
		logger: slog.New(slog.DiscardHandler),
	}
}

func TestMarks(t *testing.T) {
	tests := []struct {
		name           string
		reactErr       error
		answered, busy bool
		reacted        []string
		removed        []int64
	}{
		{name: "answered: the eyes come off and the thumbs up goes on", answered: true,
			reacted: []string{forge.ReactionEyes, forge.ReactionDone}, removed: []int64{1}},
		{name: "not answered: the eyes come off alone", reacted: []string{forge.ReactionEyes}, removed: []int64{1}},
		{name: "busy: the eyes stay on for the job still at work", answered: true, busy: true,
			reacted: []string{forge.ReactionEyes, forge.ReactionDone}},
		{name: "a refused reaction leaves nothing to end", reactErr: errors.New("Resource not accessible by integration"), answered: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMarks{reactErr: tt.reactErr}
			ctx, cancel := context.WithCancel(t.Context())
			end := f.marks().start(ctx)
			if !slices.Equal(f.reacted, tt.reacted[:min(len(tt.reacted), 1)]) || len(f.removed) != 0 {
				t.Fatalf("after start: reacted %v, removed %v", f.reacted, f.removed)
			}
			// The end lands once the job's ctx has ended.
			cancel()
			end(tt.answered, tt.busy)
			if !slices.Equal(f.reacted, tt.reacted) || !slices.Equal(f.removed, tt.removed) {
				t.Fatalf("after end: reacted %v, removed %v; want %v and %v", f.reacted, f.removed, tt.reacted, tt.removed)
			}
			if tt.reactErr == nil && f.ctxErr != nil {
				t.Fatalf("the end was made on the ended ctx: %v", f.ctxErr)
			}
		})
	}
}
