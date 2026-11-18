package store

import "testing"

// TestPartProgressInUse: the slots a runner's parts can be in are the most
// of the parts that run and the slots it read, within the parts that remain.
// Before it reports, they are every slot held.
func TestPartProgressInUse(t *testing.T) {
	parts := func(n int) map[string]int { return map[string]int{"m": n} }
	for _, tt := range []struct {
		name     string
		progress PartProgress
		want     int
	}{
		{"no report yet", PartProgress{}, 5},
		{"the runner read the lower grant, and as many parts still run", PartProgress{Remaining: parts(9), Active: parts(4), Seen: parts(3)}, 4},
		{"a part ended under the lower grant", PartProgress{Remaining: parts(8), Active: parts(3), Seen: parts(3)}, 3},
		{"the runner read the higher grant and can start within it", PartProgress{Remaining: parts(9), Active: parts(2), Seen: parts(4)}, 4},
		{"fewer parts remain than the grant read", PartProgress{Remaining: parts(2), Active: parts(2), Seen: parts(4)}, 2},
		{"every part is done", PartProgress{Remaining: parts(0), Active: parts(0), Seen: parts(4)}, 0},
		{"another model", PartProgress{Remaining: map[string]int{"other": 3}, Active: map[string]int{"other": 3}, Seen: map[string]int{"other": 3}}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.progress.InUse("m", 5); got != tt.want {
				t.Fatalf("InUse = %d, want %d", got, tt.want)
			}
		})
	}
}
