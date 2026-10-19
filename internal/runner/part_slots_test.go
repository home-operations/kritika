package runner

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestPartSlotsGrowWhilePartRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan int, 3)
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ticks := make(chan time.Time)
	var grant atomic.Int32
	grant.Store(1)
	var reports []int
	done := make(chan error, 1)
	go func() {
		done <- scheduleParts(ctx, []int{0, 1, 2}, 1, ticks, func(_ context.Context, remaining int) int {
			reports = append(reports, remaining)
			return int(grant.Load())
		}, func(ctx context.Context, i int) error {
			started <- i
			select {
			case <-release[i]:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	wait := func(want int) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("started %d, want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal("part did not start")
		}
	}
	wait(0)
	grant.Store(2)
	select {
	case ticks <- time.Now():
	case <-ctx.Done():
		t.Fatal("capacity not polled")
	}
	wait(1)
	select {
	case got := <-started:
		t.Fatalf("part %d exceeded the two granted slots", got)
	default:
	}
	close(release[0])
	wait(2)
	close(release[1])
	close(release[2])
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("parts did not finish")
	}
	if reports[0] != 3 || reports[len(reports)-1] != 0 || !slices.IsSortedFunc(reports, func(a, b int) int { return b - a }) {
		t.Fatalf("remaining parts were not monotonic: %v", reports)
	}
}

func TestPartSlotsStopAdmission(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "cancellation"
		if failure {
			name = "part failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan int, 3)
			fail := make(chan struct{})
			boom := errors.New("part failed")
			done := make(chan error, 1)
			go func() {
				done <- scheduleParts(ctx, []int{0, 1, 2}, 2, nil, nil, func(ctx context.Context, i int) error {
					started <- i
					if i == 0 && failure {
						select {
						case <-fail:
							return boom
						case <-ctx.Done():
						}
					}
					<-ctx.Done()
					return nil
				})
			}()
			for range 2 {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("initial parts did not start")
				}
			}
			if failure {
				close(fail)
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if failure && !errors.Is(err, boom) || !failure && err != nil {
					t.Fatalf("scheduler returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("active parts were not canceled")
			}
			if len(started) != 0 {
				t.Fatal("queued part started after cancellation")
			}
		})
	}
}
