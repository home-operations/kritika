package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
)

// defaultSlots grants n slots to the default pass alone.
func defaultSlots(n int) map[string]int { return map[string]int{review.DefaultReviewer: n} }

func defaultKey(int) string { return review.DefaultReviewer }

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
		done <- scheduleParts(ctx, []int{0, 1, 2}, defaultKey, defaultSlots(1), ticks,
			func(_ context.Context, remaining, _ map[string]int) map[string]int {
				reports = append(reports, remaining[review.DefaultReviewer])
				return defaultSlots(int(grant.Load()))
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

// TestPartSlotsShrinkWhilePartsRun: a grant that drops leaves the running
// parts alone and starts no part until fewer run than it allows. Each report
// carries the parts that run.
func TestPartSlotsShrinkWhilePartsRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan int, 3)
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ticks := make(chan time.Time)
	var grant atomic.Int32
	grant.Store(2)
	active := make(chan int, 16)
	done := make(chan error, 1)
	go func() {
		done <- scheduleParts(ctx, []int{0, 1, 2}, defaultKey, defaultSlots(2), ticks,
			func(_ context.Context, _, running map[string]int) map[string]int {
				active <- running[review.DefaultReviewer]
				return defaultSlots(int(grant.Load()))
			}, func(ctx context.Context, i int) error {
				started <- i
				select {
				case <-release[i]:
				case <-ctx.Done():
				}
				return nil
			})
	}()
	reported := func(want int) {
		t.Helper()
		select {
		case got := <-active:
			if got != want {
				t.Fatalf("reported %d active parts, want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal("active parts not reported")
		}
	}
	begun := func(n int) []int {
		t.Helper()
		var got []int
		for range n {
			select {
			case i := <-started:
				got = append(got, i)
			case <-ctx.Done():
				t.Fatalf("parts did not start: %v", got)
			}
		}
		slices.Sort(got)
		return got
	}
	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal("capacity not polled")
		}
	}
	reported(0)
	if got := begun(2); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("started %v, want the first two parts", got)
	}
	grant.Store(1)
	close(release[0])
	reported(1)
	// A part started against the lower grant would show in the next report.
	tick()
	reported(1)
	close(release[1])
	reported(0)
	if got := begun(1); !slices.Equal(got, []int{2}) {
		t.Fatalf("started %v, want the last part", got)
	}
	close(release[2])
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("parts did not finish")
	}
}

// TestPartSlotsReportFails: a report that the gateway does not answer leaves
// the running parts alone and starts no part beyond the one slot of a pool,
// since the gateway may have taken it under a lower grant. The next report
// that is answered restores the grant. The reports are numbered in order.
func TestPartSlotsReportFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var failing atomic.Bool
	reports := make(chan runspec.PartsReport, 16)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report runspec.PartsReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || r.URL.Path != runspec.ReportPartsPath {
			t.Errorf("report to %s: %v", r.URL.Path, err)
		}
		// The answer is settled before the test sees the report, since the
		// test changes failing as soon as it does.
		refuse := failing.Load()
		reports <- report
		if refuse {
			http.Error(w, "refused", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(runspec.PartSlots{Slots: defaultSlots(2)})
	}))
	t.Cleanup(gw.Close)
	started := make(chan int, 3)
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- scheduleParts(ctx, []int{0, 1, 2}, defaultKey, defaultSlots(2), ticks,
			runnerPartSlots(&reporter{gatewayURL: gw.URL, token: "t"}, slog.New(slog.DiscardHandler)),
			func(ctx context.Context, i int) error {
				started <- i
				select {
				case <-release[i]:
				case <-ctx.Done():
				}
				return nil
			})
	}()
	var sequence int64
	reported := func(want int) {
		t.Helper()
		select {
		case got := <-reports:
			if got.Active[review.DefaultReviewer] != want || got.Sequence <= sequence {
				t.Fatalf("report %d after %d names %d active parts, want %d and a later report", got.Sequence, sequence,
					got.Active[review.DefaultReviewer], want)
			}
			sequence = got.Sequence
		case <-ctx.Done():
			t.Fatal("active parts not reported")
		}
	}
	begun := func(want ...int) {
		t.Helper()
		var got []int
		for range want {
			select {
			case i := <-started:
				got = append(got, i)
			case <-ctx.Done():
				t.Fatalf("parts did not start: %v", got)
			}
		}
		if slices.Sort(got); !slices.Equal(got, want) {
			t.Fatalf("started %v, want %v", got, want)
		}
	}
	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal("capacity not polled")
		}
	}
	reported(0)
	begun(0, 1)
	failing.Store(true)
	close(release[0])
	reported(1)
	// A part started after the report that failed would show in the next.
	tick()
	reported(1)
	failing.Store(false)
	tick()
	reported(1)
	begun(2)
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
}

// TestPartSlotsPerPool: each part runs in its pool's slots. A pool with one
// slot runs one part at a time while another's two run at once, and the
// remaining work is reported by pool.
func TestPartSlotsPerPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	key := func(i int) string {
		if i < 2 {
			return "lens"
		}
		return review.DefaultReviewer
	}
	started := make(chan int, 4)
	release := make([]chan struct{}, 4)
	for i := range release {
		release[i] = make(chan struct{})
	}
	var reports []map[string]int
	done := make(chan error, 1)
	go func() {
		done <- scheduleParts(ctx, []int{0, 1, 2, 3}, key, map[string]int{"lens": 1, review.DefaultReviewer: 2}, nil,
			func(_ context.Context, remaining, _ map[string]int) map[string]int {
				reports = append(reports, remaining)
				return map[string]int{"lens": 1, review.DefaultReviewer: 2}
			}, func(ctx context.Context, i int) error {
				started <- i
				select {
				case <-release[i]:
				case <-ctx.Done():
				}
				return nil
			})
	}()
	collect := func(n int) []int {
		t.Helper()
		var got []int
		for range n {
			select {
			case i := <-started:
				got = append(got, i)
			case <-ctx.Done():
				t.Fatalf("parts did not start: %v", got)
			}
		}
		slices.Sort(got)
		return got
	}
	// The lens's first part and both default parts start; the lens's second
	// waits for its one slot.
	if got := collect(3); !slices.Equal(got, []int{0, 2, 3}) {
		t.Fatalf("started %v, want the lens's first part and the default pass's two", got)
	}
	select {
	case got := <-started:
		t.Fatalf("part %d ran beyond its reviewer's slots", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release[2])
	select {
	case got := <-started:
		t.Fatalf("part %d started on a default slot", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release[0])
	if got := collect(1); !slices.Equal(got, []int{1}) {
		t.Fatalf("started %v, want the lens's second part", got)
	}
	close(release[1])
	close(release[3])
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("parts did not finish")
	}
	first, last := reports[0], reports[len(reports)-1]
	if first["lens"] != 2 || first[review.DefaultReviewer] != 2 || last["lens"] != 0 || last[review.DefaultReviewer] != 0 {
		t.Fatalf("remaining parts reported %v, want 2 and 2 first, 0 and 0 last", reports)
	}
}

// TestPartSlotsSharedPool: reviewers on one model share its pool, so their
// parts together never exceed its slots, whichever reviewer's are pending.
func TestPartSlotsSharedPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// Parts 0 and 1 are a lens's, 2 and 3 the default pass's, all on one model.
	shared := func(int) string { return "m/one" }
	started := make(chan int, 4)
	release := make([]chan struct{}, 4)
	for i := range release {
		release[i] = make(chan struct{})
	}
	done := make(chan error, 1)
	go func() {
		done <- scheduleParts(ctx, []int{0, 1, 2, 3}, shared, map[string]int{"m/one": 2}, nil, nil, func(ctx context.Context, i int) error {
			started <- i
			select {
			case <-release[i]:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	next := func() int {
		t.Helper()
		select {
		case i := <-started:
			return i
		case <-ctx.Done():
			t.Fatal("part did not start")
			return -1
		}
	}
	// Two parts start at once, in either order.
	if a, b := next(), next(); min(a, b) != 0 || max(a, b) != 1 {
		t.Fatalf("started %d and %d, want the first two parts", a, b)
	}
	select {
	case got := <-started:
		t.Fatalf("part %d ran beyond the pool's two slots", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release[0])
	if got := next(); got != 2 {
		t.Fatalf("started %d after a slot freed, want 2", got)
	}
	close(release[1])
	if got := next(); got != 3 {
		t.Fatalf("started %d after a slot freed, want 3", got)
	}
	close(release[2])
	close(release[3])
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("parts did not finish")
	}
}

// TestPoolKey: a part's pool is its reviewer's, the default pass's by its
// name whether the part names it or not, and a reviewer with no pool named
// is a pool of its own.
func TestPoolKey(t *testing.T) {
	p := reviewSpec()
	p.Agent.Pools = map[string]string{review.DefaultReviewer: "a/big", "security": "b/small"}
	for reviewer, want := range map[string]string{"": "a/big", review.DefaultReviewer: "a/big", "security": "b/small", "style": "style"} {
		if got := poolKey(p, reviewer); got != want {
			t.Errorf("poolKey(%q) = %q, want %q", reviewer, got, want)
		}
	}
	p.Agent = nil
	if got := poolKey(p, ""); got != review.DefaultReviewer {
		t.Errorf("poolKey with no limits = %q, want %q", got, review.DefaultReviewer)
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
				done <- scheduleParts(ctx, []int{0, 1, 2}, defaultKey, defaultSlots(2), nil, nil, func(ctx context.Context, i int) error {
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
