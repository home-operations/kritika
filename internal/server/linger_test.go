package server

import (
	"context"
	"testing"
	"time"
)

// TestLingering: the context outlives its parent by the delay, so a
// listener keeps accepting connections while traffic moves off the pod.
func TestLingering(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	lctx := Lingering(ctx, 100*time.Millisecond)
	cancel()
	select {
	case <-lctx.Done():
		t.Fatal("ended with its parent")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-lctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("did not end after the delay")
	}
}
