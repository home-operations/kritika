package server

import (
	"context"
	"time"
)

// Lingering is a context that ends d after ctx does, with ctx's values: a
// listener keeps accepting connections while traffic moves off the pod.
func Lingering(ctx context.Context, d time.Duration) context.Context {
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	context.AfterFunc(ctx, func() { time.AfterFunc(d, cancel) })
	return lctx
}
