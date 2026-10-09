package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/forge"
)

// fakeMarks records what marks react, unreact and find were asked.
type fakeMarks struct {
	reactErr error
	// on are the bot's reactions already there, by content, as find
	// answers them.
	on      map[string]int64
	reacted []string
	removed []int64
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
		find: func(ctx context.Context, content string) (int64, error) {
			f.ctxErr = ctx.Err()
			return f.on[content], nil
		},
		logger: slog.New(slog.DiscardHandler),
	}
}

func TestMarks(t *testing.T) {
	tests := []struct {
		name     string
		reactErr error
		on       map[string]int64
		outcome  outcome
		busy     bool
		reacted  []string
		removed  []int64
	}{
		{name: "answered: the eyes come off and the thumbs up goes on", outcome: answered,
			reacted: []string{forge.ReactionEyes, forge.ReactionDone}, removed: []int64{1}},
		{name: "answered after a failure: its mark comes off too", outcome: answered, on: map[string]int64{forge.ReactionFailed: 9},
			reacted: []string{forge.ReactionEyes, forge.ReactionDone}, removed: []int64{1, 9}},
		{name: "failed: the eyes come off and the confused face goes on", outcome: failed,
			reacted: []string{forge.ReactionEyes, forge.ReactionFailed}, removed: []int64{1}},
		{name: "failed after an answer: its thumbs up comes off too", outcome: failed, on: map[string]int64{forge.ReactionDone: 8},
			reacted: []string{forge.ReactionEyes, forge.ReactionFailed}, removed: []int64{1, 8}},
		{name: "unanswered: the eyes come off alone", on: map[string]int64{forge.ReactionDone: 8, forge.ReactionFailed: 9},
			reacted: []string{forge.ReactionEyes}, removed: []int64{1}},
		{name: "busy: the eyes stay on for the job still at work", outcome: answered, busy: true,
			reacted: []string{forge.ReactionEyes, forge.ReactionDone}},
		{name: "a refused reaction leaves nothing to end", reactErr: errors.New("Resource not accessible by integration"), outcome: answered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMarks{reactErr: tt.reactErr, on: tt.on}
			ctx, cancel := context.WithCancel(t.Context())
			end := f.marks().start(ctx)
			if !slices.Equal(f.reacted, tt.reacted[:min(len(tt.reacted), 1)]) || len(f.removed) != 0 {
				t.Fatalf("after start: reacted %v, removed %v", f.reacted, f.removed)
			}
			// The end lands once the job's ctx has ended.
			cancel()
			end(tt.outcome, tt.busy)
			if !slices.Equal(f.reacted, tt.reacted) || !slices.Equal(f.removed, tt.removed) {
				t.Fatalf("after end: reacted %v, removed %v; want %v and %v", f.reacted, f.removed, tt.reacted, tt.removed)
			}
			if tt.reactErr == nil && f.ctxErr != nil {
				t.Fatalf("the end was made on the ended ctx: %v", f.ctxErr)
			}
		})
	}
}
